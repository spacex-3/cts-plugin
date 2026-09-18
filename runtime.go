package main

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type pluginRuntime struct {
	mu            sync.Mutex
	config        pluginConfig
	cache         *stateCache
	statuses      map[cacheKey]probeRecord
	windows       map[cacheKey]windowStats
	probeLogs     []probeLogEntry
	globalErr     string
	host          hostAPI
	transport     probeTransport
	nowFunc       func() time.Time
	trigger       chan struct{}
	targetTrigger chan cacheKey
	cancel        context.CancelFunc
	wg            sync.WaitGroup
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
		config:        cfg,
		cache:         newStateCache(cfg.ttl(), cfg.targetLength(), time.Now),
		statuses:      make(map[cacheKey]probeRecord),
		windows:       make(map[cacheKey]windowStats),
		host:          liveHost{},
		transport:     utlsProbeTransport{},
		nowFunc:       time.Now,
		trigger:       make(chan struct{}, 1),
		targetTrigger: make(chan cacheKey, 64),
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
	if r.cache == nil {
		r.cache = newStateCache(cfg.ttl(), cfg.targetLength(), nowFunc)
	} else {
		r.cache.reconfigure(cfg.ttl(), cfg.targetLength(), nowFunc)
	}
	if r.statuses == nil {
		r.statuses = make(map[cacheKey]probeRecord)
	}
	if r.windows == nil {
		r.windows = make(map[cacheKey]windowStats)
	}
	if r.targetTrigger == nil {
		r.targetTrigger = make(chan cacheKey, 64)
	}
	if len(r.probeLogs) > cfg.probeLogLimit() {
		r.probeLogs = append([]probeLogEntry(nil), r.probeLogs[len(r.probeLogs)-cfg.probeLogLimit():]...)
	}
	if !cfg.showStateValuesEnabled() {
		for i := range r.probeLogs {
			r.probeLogs[i].State = ""
		}
	}
	r.globalErr = ""
	r.startProbeLocked()
	return nil
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
	refresh := strings.EqualFold(strings.TrimSpace(source), "probe")
	entry, accepted, reset := r.cache.storeTarget(authID, model, state, source, refresh)
	if accepted && reset {
		key := makeCacheKey(authID, model)
		r.mu.Lock()
		r.windows[key] = windowStats{WindowStartedAt: entry.StoredAt}
		r.mu.Unlock()
	}
	r.recordObservation(authID, model, state, accepted, source, "")
	return accepted
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
		if threshold > 0 && stats.ConsecutiveFailures >= threshold && !stats.ReprobeQueued {
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

	if queueProbe && !r.triggerTargetProbe(key) {
		r.mu.Lock()
		stats = r.windows[key]
		stats.ReprobeQueued = false
		r.windows[key] = stats
		r.mu.Unlock()
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

func (r *pluginRuntime) snapshotStatus() runtimeSnapshot {
	if r == nil {
		return runtimeSnapshot{}
	}
	r.mu.Lock()
	cfg := clonePluginConfig(r.config)
	globalErr := r.globalErr
	records := make([]probeRecord, 0, len(r.statuses))
	for _, record := range r.statuses {
		records = append(records, record)
	}
	windows := make(map[cacheKey]windowStats, len(r.windows))
	for key, stats := range r.windows {
		windows[key] = stats
	}
	logs := append([]probeLogEntry(nil), r.probeLogs...)
	r.mu.Unlock()
	entries := r.cache.snapshot()
	return runtimeSnapshot{
		Config:    cfg,
		GlobalErr: globalErr,
		Entries:   entries,
		Records:   records,
		Windows:   windows,
		Logs:      logs,
		Now:       r.now(),
		TTL:       cfg.ttl(),
		TargetLen: cfg.targetLength(),
	}
}

type runtimeSnapshot struct {
	Config    pluginConfig
	GlobalErr string
	Entries   []cacheEntry
	Records   []probeRecord
	Windows   map[cacheKey]windowStats
	Logs      []probeLogEntry
	Now       time.Time
	TTL       time.Duration
	TargetLen int
}
