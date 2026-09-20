package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type pluginRuntime struct {
	mu                 sync.Mutex
	config             pluginConfig
	cache              *stateCache
	statuses           map[cacheKey]probeRecord
	windows            map[cacheKey]windowStats
	ticketStats        map[cacheKey]ticketCounters
	probeLogs          []probeLogEntry
	injections         []injectionLogEntry
	globalErr          string
	host               hostAPI
	transport          probeTransport
	nowFunc            func() time.Time
	trigger            chan struct{}
	targetTrigger      chan cacheKey
	nextProbeAt        time.Time
	demandTrigger      chan demandProbe
	demandPending      map[cacheKey]chan struct{}
	quotaUntil         map[string]time.Time
	probeMissRounds    map[cacheKey]int
	probeCooldownUntil map[cacheKey]time.Time
	poolCursor         uint64
	harvestPending     map[harvestKey]*harvestCandidate
	cancel             context.CancelFunc
	wg                 sync.WaitGroup
}

type probeLogEntry struct {
	Time        time.Time
	AuthID      string
	Model       string
	Route       string
	Proxy       string
	Attempt     int
	State       string
	Length      int
	TargetMatch bool
	Cached      bool
	Error       string
}

type injectionLogEntry struct {
	Time            time.Time
	RequestID       string
	TraceID         string
	SourceFormat    string
	ToFormat        string
	Endpoint        string
	RequestedModel  string
	Stream          bool
	ReasoningEffort string
	SessionID       string
	AuthID          string
	Model           string
	Length          int
	Source          string
	State           string
	Headers         string
	UsageAttached   bool
	InputTokens     int64
	OutputTokens    int64
	ReasoningTokens int64
	TotalTokens     int64
	TTFT            time.Duration
	Latency         time.Duration
	Failed          bool
}

// ticketCounters answers, per auth+model, the questions the state value itself
// cannot: whether production requests actually carried a ticket, and whether the
// upstream handed back the ticket it was given or minted a different one.
type ticketCounters struct {
	AuthID           string
	Model            string
	Injections       int64
	LastInjectedAt   time.Time
	LastInjectedFrom string
	Bare             int64
	LastBareAt       time.Time
	Echoes           int64
	Changes          int64
	LastTurnoverAt   time.Time
}

type windowStats struct {
	WindowStartedAt     time.Time
	Requests            int64
	Successes           int64
	Failures            int64
	ConsecutiveFailures int
	InputTokens         int64
	OutputTokens        int64
	ReasoningTokens     int64
	TotalTokens         int64
	TTFTTotal           time.Duration
	TTFTSamples         int64
	LastRequestAt       time.Time
	LastFailure         string
	ReprobeQueued       bool
}

var rt = newRuntime()

func newRuntime() *pluginRuntime {
	cfg := normalizeConfig(pluginConfig{})
	return &pluginRuntime{
		config:             cfg,
		cache:              newStateCache(cfg.ttl(), cfg.targetLength(), time.Now),
		statuses:           make(map[cacheKey]probeRecord),
		windows:            make(map[cacheKey]windowStats),
		ticketStats:        make(map[cacheKey]ticketCounters),
		injections:         make([]injectionLogEntry, 0),
		host:               liveHost{},
		transport:          utlsProbeTransport{},
		nowFunc:            time.Now,
		trigger:            make(chan struct{}, 1),
		targetTrigger:      make(chan cacheKey, 64),
		demandTrigger:      make(chan demandProbe, 64),
		demandPending:      make(map[cacheKey]chan struct{}),
		quotaUntil:         make(map[string]time.Time),
		probeMissRounds:    make(map[cacheKey]int),
		probeCooldownUntil: make(map[cacheKey]time.Time),
		harvestPending:     make(map[harvestKey]*harvestCandidate),
	}
}

func currentRuntime() *pluginRuntime {
	return rt
}

func (r *pluginRuntime) now() time.Time {
	if r == nil || r.nowFunc == nil {
		return time.Now()
	}
	return r.nowFunc()
}

func (r *pluginRuntime) configSnapshot() pluginConfig {
	if r == nil {
		return normalizeConfig(pluginConfig{})
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return clonePluginConfig(r.config)
}

func (r *pluginRuntime) applyConfig(cfg pluginConfig) error {
	cfg = loadPersistedProxyLines(cfg)
	cfg = loadPersistedProbeAuthIDs(cfg)
	cfg = normalizeConfig(cfg)
	if errProxy := validateProxyConfig(cfg); errProxy != nil {
		return errProxy
	}
	r.mu.Lock()
	r.stopProbeLocked()
	r.mu.Unlock()
	r.wg.Wait()

	r.mu.Lock()
	defer r.mu.Unlock()
	nowFunc := r.nowFunc
	if nowFunc == nil {
		nowFunc = time.Now
	}
	r.config = clonePluginConfig(cfg)
	r.harvestPending = make(map[harvestKey]*harvestCandidate)
	if r.cache == nil {
		r.cache = newStateCache(cfg.ttl(), cfg.targetLength(), nowFunc)
	}
	r.cache.configureAcceptedBlocks(cfg.acceptedBlocks())
	r.cache.reconfigure(cfg.ttl(), cfg.targetLength(), nowFunc)
	r.cache.configureIssuedAt(cfg.UseIssuedAt)
	if r.statuses == nil {
		r.statuses = make(map[cacheKey]probeRecord)
	}
	if r.windows == nil {
		r.windows = make(map[cacheKey]windowStats)
	}
	if r.targetTrigger == nil {
		r.targetTrigger = make(chan cacheKey, 64)
	}
	if r.injections == nil {
		r.injections = make([]injectionLogEntry, 0)
	}
	if len(r.probeLogs) > cfg.probeLogLimit() {
		r.probeLogs = append([]probeLogEntry(nil), r.probeLogs[len(r.probeLogs)-cfg.probeLogLimit():]...)
	}
	if !cfg.showStateValuesEnabled() {
		for i := range r.probeLogs {
			r.probeLogs[i].State = ""
		}
	}
	restorePersistedRuntimeLocked(r, cfg)
	r.globalErr = ""
	r.startProbeLocked()
	return nil
}

func restorePersistedRuntimeLocked(r *pluginRuntime, cfg pluginConfig) {
	if len(r.probeLogs) > 0 || len(r.cache.snapshot()) > 0 {
		return
	}
	file := loadPersistedRuntimeFile()
	if len(file.Entries) > 0 {
		for _, entry := range file.Entries {
			r.cache.restore(entry)
		}
	}
	limit := cfg.probeLogLimit()
	if len(file.Logs) > limit {
		file.Logs = file.Logs[len(file.Logs)-limit:]
	}
	if len(file.Injections) > limit {
		file.Injections = file.Injections[len(file.Injections)-limit:]
	}
	r.probeLogs = append(r.probeLogs, file.Logs...)
	r.injections = append(r.injections, file.Injections...)
	if r.ticketStats == nil {
		r.ticketStats = make(map[cacheKey]ticketCounters)
	}
	for _, counters := range file.Counters {
		key := makeCacheKey(counters.AuthID, counters.Model)
		if key.AuthID == "" || key.Model == "" {
			continue
		}
		r.ticketStats[key] = counters
	}
}

func (r *pluginRuntime) applyProbeAuthIDs(authIDs []string) error {
	authIDs = uniqueTrimmed(authIDs)
	if len(authIDs) == 0 {
		authIDs = []string{"__none__"}
	}
	r.mu.Lock()
	cfg := clonePluginConfig(r.config)
	cfg.ProbeAuthIDs = authIDs
	r.config = normalizeConfig(cfg)
	r.mu.Unlock()
	if errSave := saveProbeAuthIDs(strings.Split(strings.Join(authIDs, "\n"), "\n")); errSave != nil {
		r.host.Log("warn", "codex-turn-state: failed to persist probe auth selection", map[string]any{"error": errSave.Error()})
		return errSave
	}
	r.triggerProbe()
	return nil
}

func (r *pluginRuntime) applyProxyLines(raw, scheme string) error {
	raw = strings.ReplaceAll(strings.TrimSpace(raw), "\r\n", "\n")
	if raw == "" {
		return errors.New("至少需要一条代理")
	}
	scheme = strings.ToLower(strings.TrimSpace(scheme))
	if scheme == "" {
		scheme = r.configSnapshot().proxyScheme()
	}
	proxies, errParse := parseProxyURLsWithScheme(raw, scheme)
	if errParse != nil {
		return errParse
	}
	if len(proxies) == 0 {
		return errors.New("至少需要一条代理")
	}
	r.mu.Lock()
	cfg := clonePluginConfig(r.config)
	cfg.Proxy = raw
	cfg.Proxies = nil
	cfg.ProxyScheme = scheme
	r.config = normalizeConfig(cfg)
	r.mu.Unlock()
	if errSave := saveProxyLines(raw, scheme); errSave != nil {
		r.host.Log("warn", "codex-turn-state: failed to persist proxy lines", map[string]any{"error": errSave.Error()})
		return errSave
	}
	r.triggerProbe()
	return nil
}

func (r *pluginRuntime) applyManualState(authID, model, state string) error {
	authID = strings.TrimSpace(authID)
	model = strings.TrimSpace(model)
	state = strings.TrimSpace(state)
	if authID == "" || model == "" || state == "" {
		return errors.New("账号、模型和 state 都不能为空")
	}
	if !r.configSnapshot().allows(authID, model) {
		return errors.New("账号或模型不在当前配置范围内")
	}
	entry, ok := r.cache.putManual(authID, model, state)
	if !ok {
		return errors.New("手动 state 写入失败")
	}
	r.mu.Lock()
	r.windows[makeCacheKey(authID, model)] = windowStats{WindowStartedAt: entry.StoredAt}
	r.persistLocked()
	r.mu.Unlock()
	r.recordObservation(authID, model, state, true, "manual", "")
	return nil
}

func (r *pluginRuntime) shutdown() {
	r.mu.Lock()
	r.stopProbeLocked()
	r.mu.Unlock()
	r.wg.Wait()
}

func (r *pluginRuntime) startProbeLocked() {
	if !r.config.probeEnabled() {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.runProbeLoop(ctx)
	}()
}

func (r *pluginRuntime) stopProbeLocked() {
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
}

func (r *pluginRuntime) observeState(authID, model, state, source string) bool {
	if r == nil {
		return false
	}
	state = strings.TrimSpace(state)
	if state == "" {
		return false
	}
	refresh := strings.EqualFold(strings.TrimSpace(source), "probe") || strings.EqualFold(strings.TrimSpace(source), "direct")
	prev, hadPrev := r.cache.lookup(authID, model)
	entry, accepted, reset := r.cache.storeTarget(authID, model, state, source, refresh)
	if accepted && reset {
		key := makeCacheKey(authID, model)
		r.mu.Lock()
		r.windows[key] = windowStats{WindowStartedAt: entry.StoredAt}
		r.mu.Unlock()
	}
	if accepted && !refresh {
		r.recordTicketTurnover(authID, model, hadPrev && strings.TrimSpace(prev.State) == state)
	}
	r.recordObservation(authID, model, state, accepted, source, "")
	return accepted
}

// recordTicketTurnover notes whether a harvested response carried the ticket the
// cache already held (an echo) or a different one (a change). Probed and direct
// tickets are intentionally excluded: those are the requests asking for a new
// ticket, so they would only ever report changes.
func (r *pluginRuntime) recordTicketTurnover(authID, model string, same bool) {
	if r == nil {
		return
	}
	key := makeCacheKey(authID, model)
	if key.AuthID == "" || key.Model == "" {
		return
	}
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	stats := r.ticketStats[key]
	stats.AuthID = key.AuthID
	stats.Model = key.Model
	if same {
		stats.Echoes++
	} else {
		stats.Changes++
	}
	stats.LastTurnoverAt = now
	r.ticketStats[key] = stats
	r.persistLocked()
}

func (r *pluginRuntime) recordProbe(target probeTarget, state string, accepted bool, errText string) {
	r.recordObservation(target.AuthID, target.Model, state, accepted, "probe", errText)
}

func (r *pluginRuntime) recordObservation(authID, model, state string, accepted bool, source, errText string) {
	if r == nil {
		return
	}
	key := makeCacheKey(authID, model)
	if key.AuthID == "" || key.Model == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	record := r.statuses[key]
	record.AuthID = key.AuthID
	record.Model = key.Model
	record.LastAttempt = r.now()
	record.LastError = errText
	record.LastLength = len(strings.TrimSpace(state))
	record.LastAccepted = accepted
	if source != "" {
		record.LastSource = source
	}
	r.statuses[key] = record
	r.persistLocked()
}

func (r *pluginRuntime) recordInjection(req pluginapi.RequestInterceptRequest, entry cacheEntry) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	limit := r.config.probeLogLimit()
	r.injections = append(r.injections, injectionLogEntry{
		Time:            r.now(),
		RequestID:       strings.TrimSpace(req.RequestID),
		TraceID:         strings.TrimSpace(req.TraceID),
		SourceFormat:    strings.TrimSpace(req.SourceFormat),
		ToFormat:        strings.TrimSpace(req.ToFormat),
		Endpoint:        metadataString(req.Metadata, cliproxyexecutor.RequestPathMetadataKey),
		RequestedModel:  strings.TrimSpace(req.RequestedModel),
		Stream:          req.Stream,
		ReasoningEffort: metadataString(req.Metadata, cliproxyexecutor.ReasoningEffortMetadataKey),
		SessionID:       firstNonEmpty(metadataString(req.Metadata, cliproxyexecutor.CanonicalSessionIDMetadataKey), metadataString(req.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey)),
		AuthID:          strings.TrimSpace(entry.AuthID),
		Model:           strings.TrimSpace(entry.Model),
		Length:          entry.Length,
		Source:          entry.Source,
		State:           entry.State,
		Headers:         serializedRequestHeaders(req.Headers, entry.State),
	})
	if len(r.injections) > limit {
		r.injections = append([]injectionLogEntry(nil), r.injections[len(r.injections)-limit:]...)
	}
	key := makeCacheKey(entry.AuthID, entry.Model)
	if key.AuthID != "" && key.Model != "" {
		stats := r.ticketStats[key]
		stats.AuthID = key.AuthID
		stats.Model = key.Model
		stats.Injections++
		stats.LastInjectedAt = r.now()
		stats.LastInjectedFrom = entry.Source
		r.ticketStats[key] = stats
	}
	r.persistLocked()
}

// recordBareRequest notes a production request that passed every gate but had no
// usable cached state, so it went upstream without the header.
func (r *pluginRuntime) recordBareRequest(authID, model string) {
	if r == nil {
		return
	}
	key := makeCacheKey(authID, model)
	if key.AuthID == "" || key.Model == "" {
		return
	}
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	stats := r.ticketStats[key]
	stats.AuthID = key.AuthID
	stats.Model = key.Model
	stats.Bare++
	stats.LastBareAt = now
	r.ticketStats[key] = stats
	r.persistLocked()
}

func serializedRequestHeaders(headers http.Header, state string) string {
	out := make(http.Header)
	if strings.TrimSpace(state) != "" {
		out.Set(turnStateHeader, strings.TrimSpace(state))
	}
	if headers == nil {
		raw, _ := json.Marshal(out)
		return string(raw)
	}
	for key, values := range headers {
		if strings.EqualFold(key, "Authorization") || strings.EqualFold(key, "Cookie") {
			out[key] = []string{"[redacted]"}
			continue
		}
		out[key] = append([]string(nil), values...)
	}
	raw, errMarshal := json.Marshal(out)
	if errMarshal != nil {
		return ""
	}
	return string(raw)
}

func (r *pluginRuntime) recordProbeAttempt(target probeTarget, route string, attempt int, state string, targetMatch, cached bool, errText string) {
	r.recordProbeAttemptWithProxy(target, route, "", attempt, state, targetMatch, cached, errText)
}

func (r *pluginRuntime) recordProbeAttemptWithProxy(target probeTarget, route, proxy string, attempt int, state string, targetMatch, cached bool, errText string) {
	if r == nil {
		return
	}
	state = strings.TrimSpace(state)
	entry := probeLogEntry{
		Time:        r.now(),
		AuthID:      strings.TrimSpace(target.AuthID),
		Model:       strings.TrimSpace(target.Model),
		Route:       strings.TrimSpace(route),
		Proxy:       strings.TrimSpace(proxy),
		Attempt:     attempt,
		Length:      len(state),
		TargetMatch: targetMatch,
		Cached:      cached,
		Error:       strings.TrimSpace(errText),
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.config.showStateValuesEnabled() {
		entry.State = state
	}
	r.probeLogs = append(r.probeLogs, entry)
	limit := r.config.probeLogLimit()
	if len(r.probeLogs) > limit {
		r.probeLogs = append([]probeLogEntry(nil), r.probeLogs[len(r.probeLogs)-limit:]...)
	}
	r.persistLocked()
}

func (r *pluginRuntime) handleUsage(record pluginapi.UsageRecord) {
	if r == nil || !strings.EqualFold(strings.TrimSpace(record.Provider), "codex") || !record.Generate {
		return
	}
	cfg := r.configSnapshot()
	key := makeCacheKey(record.AuthID, record.Model)
	if !cfg.allows(key.AuthID, key.Model) {
		return
	}
	failureHandled := false
	if cfg.ErrorAwareBackoff && record.Failed {
		switch classifyFailure(record.Failure.StatusCode, record.Failure.Body) {
		case failureQuota:
			r.deferQuota(key.AuthID, cfg)
			failureHandled = true
		case failureTransient:
			r.triggerTargetProbe(key)
			failureHandled = true
		}
	}
	entry, ok := r.cache.lookup(key.AuthID, key.Model)
	if !ok {
		return
	}
	requestedAt := record.RequestedAt
	if requestedAt.IsZero() {
		requestedAt = r.now()
	}
	if requestedAt.Before(entry.StoredAt) {
		return
	}

	queueProbe := false
	r.mu.Lock()
	stats := r.windows[key]
	if stats.WindowStartedAt.IsZero() || !stats.WindowStartedAt.Equal(entry.StoredAt) {
		stats = windowStats{WindowStartedAt: entry.StoredAt}
	}
	stats.Requests++
	stats.LastRequestAt = requestedAt
	stats.InputTokens += record.Detail.InputTokens
	stats.OutputTokens += record.Detail.OutputTokens
	stats.ReasoningTokens += record.Detail.ReasoningTokens
	totalTokens := record.Detail.TotalTokens
	if totalTokens == 0 {
		totalTokens = record.Detail.InputTokens + record.Detail.OutputTokens
	}
	stats.TotalTokens += totalTokens
	if record.TTFT > 0 {
		stats.TTFTTotal += record.TTFT
		stats.TTFTSamples++
	}
	if record.Failed {
		stats.Failures++
		stats.ConsecutiveFailures++
		stats.LastFailure = firstNonEmpty(record.Failure.Body, httpStatusText(record.Failure.StatusCode))
		threshold := cfg.failureReprobeThreshold()
		if !failureHandled && threshold > 0 && stats.ConsecutiveFailures >= threshold && !stats.ReprobeQueued {
			stats.ReprobeQueued = true
			queueProbe = true
		}
	} else {
		stats.Successes++
		stats.ConsecutiveFailures = 0
		stats.LastFailure = ""
	}
	r.windows[key] = stats
	r.mu.Unlock()
	r.attachUsageToInjection(record)

	if queueProbe && !r.triggerTargetProbe(key) {
		r.mu.Lock()
		stats = r.windows[key]
		stats.ReprobeQueued = false
		r.windows[key] = stats
		r.mu.Unlock()
	}
}

func (r *pluginRuntime) attachUsageToInjection(record pluginapi.UsageRecord) {
	if r == nil {
		return
	}
	requestedAt := record.RequestedAt
	if requestedAt.IsZero() {
		requestedAt = r.now()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.injections) - 1; i >= 0; i-- {
		entry := &r.injections[i]
		if entry.UsageAttached || entry.AuthID != record.AuthID || entry.Model != record.Model {
			continue
		}
		if record.SessionID != "" && entry.SessionID != "" && record.SessionID != entry.SessionID {
			continue
		}
		delta := requestedAt.Sub(entry.Time)
		if delta < -2*time.Minute || delta > 10*time.Minute {
			continue
		}
		entry.UsageAttached = true
		entry.InputTokens = record.Detail.InputTokens
		entry.OutputTokens = record.Detail.OutputTokens
		entry.ReasoningTokens = record.Detail.ReasoningTokens
		totalTokens := record.Detail.TotalTokens
		if totalTokens == 0 {
			totalTokens = record.Detail.InputTokens + record.Detail.OutputTokens
		}
		entry.TotalTokens = totalTokens
		entry.TTFT = record.TTFT
		entry.Latency = record.Latency
		entry.Failed = record.Failed
		return
	}
}

func httpStatusText(statusCode int) string {
	if statusCode <= 0 {
		return "request failed"
	}
	return "HTTP " + strconv.Itoa(statusCode)
}

func (r *pluginRuntime) setGlobalProbeError(errText string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.globalErr = errText
}

func (r *pluginRuntime) setNextProbeAt(at time.Time) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.nextProbeAt = at
	r.mu.Unlock()
}

func (r *pluginRuntime) snapshotStatus() runtimeSnapshot {
	if r == nil {
		return runtimeSnapshot{}
	}
	r.mu.Lock()
	cfg := clonePluginConfig(r.config)
	globalErr := r.globalErr
	nextProbeAt := r.nextProbeAt
	records := make([]probeRecord, 0, len(r.statuses))
	for _, record := range r.statuses {
		records = append(records, record)
	}
	windows := make(map[cacheKey]windowStats, len(r.windows))
	for key, stats := range r.windows {
		windows[key] = stats
	}
	ticketStats := make(map[cacheKey]ticketCounters, len(r.ticketStats))
	for key, stats := range r.ticketStats {
		ticketStats[key] = stats
	}
	logs := append([]probeLogEntry(nil), r.probeLogs...)
	injections := append([]injectionLogEntry(nil), r.injections...)
	r.mu.Unlock()
	entries := r.cache.snapshot()
	expiredEntries := r.cache.expiredSnapshot()
	return runtimeSnapshot{
		Config:         cfg,
		GlobalErr:      globalErr,
		Entries:        entries,
		ExpiredEntries: expiredEntries,
		Records:        records,
		Windows:        windows,
		TicketStats:    ticketStats,
		Logs:           logs,
		Injections:     injections,
		Now:            r.now(),
		TTL:            cfg.ttl(),
		TargetLen:      cfg.targetLength(),
		NextProbeAt:    nextProbeAt,
	}
}

type runtimeSnapshot struct {
	Config         pluginConfig
	GlobalErr      string
	Entries        []cacheEntry
	ExpiredEntries []cacheEntry
	Records        []probeRecord
	Windows        map[cacheKey]windowStats
	TicketStats    map[cacheKey]ticketCounters
	Logs           []probeLogEntry
	Injections     []injectionLogEntry
	Now            time.Time
	TTL            time.Duration
	TargetLen      int
	NextProbeAt    time.Time
}
