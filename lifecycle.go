package main

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type demandProbe struct {
	key  cacheKey
	done chan struct{}
}

// All network work stays on the lifecycle worker. Concurrent requests for the
// same bucket share a completion channel and only wait up to their own budget.
func (r *pluginRuntime) ensureDemandProbe(key cacheKey, cfg pluginConfig) {
	if cfg.probeSchedule() != "on_demand" || !cfg.probeEnabled() || !cfg.probeAuthEnabled(key.AuthID) || r.freshForProbe(key, cfg) || r.quotaBlocked(key.AuthID, cfg) || r.probeCooldownBlocked(key, cfg) {
		return
	}
	r.mu.Lock()
	if r.cancel == nil || r.config.probeSchedule() != "on_demand" {
		r.mu.Unlock()
		return
	}
	done, exists := r.demandPending[key]
	if !exists {
		done = make(chan struct{})
		r.demandPending[key] = done
		select {
		case r.demandTrigger <- demandProbe{key, done}:
		default:
			delete(r.demandPending, key)
			r.mu.Unlock()
			return
		}
	}
	r.mu.Unlock()
	if cfg.probeWait() == 0 {
		return
	}
	timer := time.NewTimer(cfg.probeWait())
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	}
}

func (r *pluginRuntime) finishDemand(job demandProbe) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.demandPending[job.key] == job.done {
		delete(r.demandPending, job.key)
		close(job.done)
	}
}

func (r *pluginRuntime) runDemandProbe(ctx context.Context, job demandProbe) {
	defer r.finishDemand(job)
	cfg := r.configSnapshot()
	if r.freshForProbe(job.key, cfg) {
		return
	}
	if r.probeCooldownBlocked(job.key, cfg) {
		return
	}
	probeCtx, cancel := context.WithTimeout(ctx, cfg.probeTimeout())
	defer cancel()
	r.probeKey(probeCtx, job.key)
}

func (r *pluginRuntime) freshForProbe(key cacheKey, cfg pluginConfig) bool {
	entry, ok := r.cache.lookup(key.AuthID, key.Model)
	return ok && !entry.freshnessTime().IsZero() && r.now().Before(entry.freshnessTime().Add(cfg.ttl()-cfg.probeLead()))
}

type failureKind int

const (
	failureOther failureKind = iota
	failureTransient
	failureQuota
	onDemandMissRounds = 3
)

func (r *pluginRuntime) probeCooldownBlocked(key cacheKey, cfg pluginConfig) bool {
	if cfg.probeSchedule() != "on_demand" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.now().Before(r.probeCooldownUntil[key])
}

func (r *pluginRuntime) recordProbeMissRound(key cacheKey, cfg pluginConfig) {
	if cfg.probeSchedule() != "on_demand" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	rounds := r.probeMissRounds[key] + 1
	if rounds >= onDemandMissRounds {
		r.probeMissRounds[key] = 0
		r.probeCooldownUntil[key] = r.now().Add(cfg.onDemandCooldown())
		return
	}
	r.probeMissRounds[key] = rounds
}

func (r *pluginRuntime) resetProbeMiss(key cacheKey) {
	r.mu.Lock()
	delete(r.probeMissRounds, key)
	delete(r.probeCooldownUntil, key)
	r.mu.Unlock()
}

func classifyFailure(status int, body string) failureKind {
	body = strings.ToLower(body)
	// Quota codes take precedence over the HTTP status (often also 429).
	if strings.Contains(body, "usage_limit_reached") || strings.Contains(body, "insufficient_quota") {
		return failureQuota
	}
	if status == 429 || status == 502 || status == 503 || status == 504 || strings.Contains(body, "overload") {
		return failureTransient
	}
	return failureOther
}

type probeFailure struct {
	status int
	body   string
}

func (e *probeFailure) Error() string { return fmt.Sprintf("probe status %d: %s", e.status, e.body) }

func (r *pluginRuntime) deferQuota(authID string, cfg pluginConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.quotaUntil[authID] = r.now().Add(cfg.quotaBackoff())
}

func (r *pluginRuntime) quotaBlocked(authID string, cfg pluginConfig) bool {
	if !cfg.ErrorAwareBackoff {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.now().Before(r.quotaUntil[authID])
}

func (r *pluginRuntime) stopForQuota(authID string, cfg pluginConfig, err error) bool {
	if cfg.ErrorAwareBackoff {
		if failure, ok := err.(*probeFailure); ok && classifyFailure(failure.status, failure.body) == failureQuota {
			r.deferQuota(authID, cfg)
			return true
		}
	}
	return false
}
