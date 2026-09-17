package main

import (
	"context"
	"strings"
	"sync"
	"time"
)

type pluginRuntime struct {
	mu        sync.Mutex
	config    pluginConfig
	cache     *stateCache
	statuses  map[cacheKey]probeRecord
	probeLogs []probeLogEntry
	globalErr string
	host      hostAPI
	transport probeTransport
	nowFunc   func() time.Time
	trigger   chan struct{}
	cancel    context.CancelFunc
	wg        sync.WaitGroup
}

type probeLogEntry struct {
	Time        time.Time
	AuthID      string
	Model       string
	Route       string
	Attempt     int
	State       string
	Length      int
	TargetMatch bool
	Cached      bool
	Error       string
}

var rt = newRuntime()

func newRuntime() *pluginRuntime {
	cfg := normalizeConfig(pluginConfig{})
	return &pluginRuntime{
		config:    cfg,
		cache:     newStateCache(cfg.ttl(), cfg.targetLength(), time.Now),
		statuses:  make(map[cacheKey]probeRecord),
		host:      liveHost{},
		transport: utlsProbeTransport{},
		nowFunc:   time.Now,
		trigger:   make(chan struct{}, 1),
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
	cfg = normalizeConfig(cfg)
	if errProxy := validateProxyConfig(cfg.Proxy); errProxy != nil {
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
	accepted := r.cache.putIfTarget(authID, model, state, source)
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
	if r == nil {
		return
	}
	state = strings.TrimSpace(state)
	entry := probeLogEntry{
		Time:        r.now(),
		AuthID:      strings.TrimSpace(target.AuthID),
		Model:       strings.TrimSpace(target.Model),
		Route:       strings.TrimSpace(route),
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
	cfg := r.config
	globalErr := r.globalErr
	records := make([]probeRecord, 0, len(r.statuses))
	for _, record := range r.statuses {
		records = append(records, record)
	}
	logs := append([]probeLogEntry(nil), r.probeLogs...)
	r.mu.Unlock()
	entries := r.cache.snapshot()
	return runtimeSnapshot{
		Config:    cfg,
		GlobalErr: globalErr,
		Entries:   entries,
		Records:   records,
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
	Logs      []probeLogEntry
	Now       time.Time
	TTL       time.Duration
	TargetLen int
}
