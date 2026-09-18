package main

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestHandleUsageAggregatesWindowMetrics(t *testing.T) {
	now := time.Date(2026, time.September, 18, 8, 0, 0, 0, time.UTC)
	rt := newRuntime()
	rt.nowFunc = func() time.Time { return now }
	rt.config = normalizeConfig(pluginConfig{
		Models:            []string{"model-1"},
		TargetStateLength: 3,
		TTLSeconds:        3600,
	})
	rt.cache.reconfigure(rt.config.ttl(), rt.config.targetLength(), rt.nowFunc)
	if !rt.observeState("auth-1", "model-1", "abc", "probe") {
		t.Fatal("expected target state to be cached")
	}

	rt.handleUsage(pluginapi.UsageRecord{
		Provider:    "codex",
		Generate:    true,
		AuthID:      "auth-1",
		Model:       "model-1",
		RequestedAt: now.Add(time.Second),
		TTFT:        1200 * time.Millisecond,
		Detail: pluginapi.UsageDetail{
			InputTokens:  10,
			OutputTokens: 5,
			TotalTokens:  15,
		},
	})
	rt.handleUsage(pluginapi.UsageRecord{
		Provider:    "codex",
		Generate:    true,
		AuthID:      "auth-1",
		Model:       "model-1",
		RequestedAt: now.Add(2 * time.Second),
		TTFT:        800 * time.Millisecond,
		Detail: pluginapi.UsageDetail{
			InputTokens:  4,
			OutputTokens: 2,
			TotalTokens:  6,
		},
	})

	snap := rt.snapshotStatus()
	stats := snap.Windows[makeCacheKey("auth-1", "model-1")]
	if stats.Requests != 2 || stats.Successes != 2 || stats.Failures != 0 {
		t.Fatalf("requests/successes/failures = %d/%d/%d, want 2/2/0", stats.Requests, stats.Successes, stats.Failures)
	}
	if stats.TotalTokens != 21 || stats.InputTokens != 14 || stats.OutputTokens != 7 {
		t.Fatalf("unexpected token totals: %#v", stats)
	}
	if stats.TTFTSamples != 2 || stats.TTFTTotal != 2*time.Second {
		t.Fatalf("TTFT samples = %d, total = %s, want 2/2s", stats.TTFTSamples, stats.TTFTTotal)
	}
}

func TestHandleUsageQueuesTargetedReprobeAfterConsecutiveFailures(t *testing.T) {
	now := time.Date(2026, time.September, 18, 8, 0, 0, 0, time.UTC)
	rt := newRuntime()
	rt.nowFunc = func() time.Time { return now }
	probe := true
	rt.config = normalizeConfig(pluginConfig{
		Models:                  []string{"model-1"},
		TargetStateLength:       3,
		TTLSeconds:              3600,
		FailureReprobeThreshold: 3,
		Probe:                   &probe,
	})
	rt.cache.reconfigure(rt.config.ttl(), rt.config.targetLength(), rt.nowFunc)
	if !rt.observeState("auth-1", "model-1", "abc", "probe") {
		t.Fatal("expected target state to be cached")
	}

	for attempt := 1; attempt <= 3; attempt++ {
		rt.handleUsage(pluginapi.UsageRecord{
			Provider:    "codex",
			Generate:    true,
			AuthID:      "auth-1",
			Model:       "model-1",
			RequestedAt: now.Add(time.Duration(attempt) * time.Second),
			Failed:      true,
			Failure:     pluginapi.UsageFailure{StatusCode: 429, Body: "rate limited"},
		})
	}

	key := makeCacheKey("auth-1", "model-1")
	if len(rt.targetTrigger) != 1 {
		t.Fatalf("target trigger length = %d, want 1", len(rt.targetTrigger))
	}
	if got := <-rt.targetTrigger; got != key {
		t.Fatalf("trigger key = %#v, want %#v", got, key)
	}
	snap := rt.snapshotStatus()
	if !snap.Windows[key].ReprobeQueued {
		t.Fatal("failed window should mark reprobe queued")
	}
}

func TestObserveStateRefreshesStoredAtForDirectMatch(t *testing.T) {
	now := time.Date(2026, time.September, 18, 16, 0, 0, 0, time.UTC)
	rt := newRuntime()
	rt.nowFunc = func() time.Time { return now }
	rt.config = normalizeConfig(pluginConfig{TargetStateLength: 3, TTLSeconds: 3600})
	rt.cache = newStateCache(time.Hour, 3, rt.nowFunc)

	if !rt.observeState("auth-1", "model-1", "abc", "probe") {
		t.Fatal("expected initial state to be cached")
	}
	before, _ := rt.cache.lookup("auth-1", "model-1")

	now = now.Add(30 * time.Minute)
	if !rt.observeState("auth-1", "model-1", "abc", "direct") {
		t.Fatal("expected direct match to be cached")
	}
	after, _ := rt.cache.lookup("auth-1", "model-1")
	if after.StoredAt.Equal(before.StoredAt) {
		t.Fatalf("direct match should refresh StoredAt, before=%s after=%s", before.StoredAt, after.StoredAt)
	}
}
