package main

import (
	"context"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestTicketCountersTrackInjectionsBareAndTurnover(t *testing.T) {
	now := time.Date(2026, time.September, 20, 9, 0, 0, 0, time.UTC)
	r := isolatedRuntime(t, pluginConfig{TargetStateLength: 3})
	r.nowFunc = func() time.Time { return now }

	// Probe and direct writes are the requests that ask for a fresh ticket, so they
	// must not move the turnover counters.
	r.observeState("auth-1", "model-1", "abc", "probe")
	r.observeState("auth-1", "model-1", "abc", "direct")
	// Two harvested responses hand the same ticket back, then two carry a
	// different one.
	r.observeState("auth-1", "model-1", "abc", "harvest")
	r.observeState("auth-1", "model-1", "abc", "harvest")
	r.observeState("auth-1", "model-1", "xyz", "harvest")
	r.observeState("auth-1", "model-1", "abc", "harvest")

	entry, ok := r.cache.lookup("auth-1", "model-1")
	if !ok {
		t.Fatal("expected a cached entry")
	}
	r.recordInjection(pluginapi.RequestInterceptRequest{}, entry)
	r.recordBareRequest("auth-1", "model-1")

	stats := r.snapshotStatus().TicketStats[makeCacheKey("auth-1", "model-1")]
	if stats.Echoes != 2 || stats.Changes != 2 {
		t.Fatalf("echoes/changes = %d/%d, want 2/2", stats.Echoes, stats.Changes)
	}
	if stats.Injections != 1 || stats.LastInjectedFrom != "harvest" {
		t.Fatalf("injections = %d from %q, want 1 from harvest", stats.Injections, stats.LastInjectedFrom)
	}
	if stats.Bare != 1 || stats.LastBareAt.IsZero() {
		t.Fatalf("bare = %d at %v, want 1 with a timestamp", stats.Bare, stats.LastBareAt)
	}
}

func TestTicketCountersPersistAcrossReload(t *testing.T) {
	dir := t.TempDir()
	previous := runtimeConfigDir
	runtimeConfigDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { runtimeConfigDir = previous })

	cfg := normalizeConfig(pluginConfig{TargetStateLength: 3})
	first := newRuntime()
	first.config = cfg
	first.cache = newStateCache(time.Hour, 3, time.Now)
	first.observeState("auth-1", "model-1", "abc", "harvest")
	first.observeState("auth-1", "model-1", "abc", "harvest")
	first.recordBareRequest("auth-1", "model-1")

	second := newRuntime()
	second.config = cfg
	second.cache = newStateCache(time.Hour, 3, time.Now)
	second.mu.Lock()
	restorePersistedRuntimeLocked(second, second.config)
	second.mu.Unlock()

	stats := second.ticketStats[makeCacheKey("auth-1", "model-1")]
	if stats.Echoes != 1 || stats.Changes != 1 || stats.Bare != 1 {
		t.Fatalf("restored counters = echoes %d, changes %d, bare %d; want 1/1/1", stats.Echoes, stats.Changes, stats.Bare)
	}
}

func TestProbeAttemptsPerRouteRepeatsEachEgress(t *testing.T) {
	probe := false
	transport := &sequenceProbeTransport{states: []string{"xx", "xx", "abc"}}
	r := isolatedRuntime(t, pluginConfig{
		TargetStateLength: 3,
		MaxProbeAttempts:  3,
		AttemptsPerRoute:  2,
		Probe:             &probe,
	})
	r.transport = transport
	target := probeTarget{
		AuthID:  "auth-1",
		Model:   defaultProbeModels[0],
		Token:   "token",
		BaseURL: "https://example.test/backend-api/codex",
	}

	r.probeTargetWithProxies(context.Background(), []string{"http://p1.example:8080", "http://p2.example:8080"}, target)

	want := []string{"http://p1.example:8080", "http://p1.example:8080", "http://p2.example:8080"}
	if len(transport.proxies) != len(want) {
		t.Fatalf("probe proxies = %#v, want %#v", transport.proxies, want)
	}
	for i := range want {
		if transport.proxies[i] != want[i] {
			t.Fatalf("probe proxies = %#v, want %#v", transport.proxies, want)
		}
	}
	if _, ok := r.cache.lookup("auth-1", defaultProbeModels[0]); !ok {
		t.Fatal("accepted state from the second egress should be cached")
	}
}

func TestDirectProbeRetriesWithinOneEgress(t *testing.T) {
	probe := false
	transport := &sequenceProbeTransport{states: []string{"xx", "abc"}}
	r := isolatedRuntime(t, pluginConfig{
		TargetStateLength: 3,
		DirectProbe:       boolPtr(true),
		AttemptsPerRoute:  2,
		Probe:             &probe,
	})
	r.transport = transport
	target := probeTarget{
		AuthID:  "auth-1",
		Model:   defaultProbeModels[0],
		Token:   "token",
		BaseURL: "https://example.test/backend-api/codex",
	}

	r.probeTargetOnce(context.Background(), target, r.configSnapshot(), []string{"http://p1.example:8080"})

	if transport.calls != 2 {
		t.Fatalf("probe calls = %d, want 2 direct attempts", transport.calls)
	}
	for i, proxyURL := range transport.proxies {
		if proxyURL != "" {
			t.Fatalf("direct attempt %d used proxy %q", i+1, proxyURL)
		}
	}
	if _, ok := r.cache.lookup("auth-1", defaultProbeModels[0]); !ok {
		t.Fatal("the second direct attempt should be cached")
	}
}

func TestAttemptsPerRouteDefaultsAndClamps(t *testing.T) {
	cases := []struct {
		in   int
		want int
	}{
		{0, defaultAttemptsPerRoute},
		{-3, defaultAttemptsPerRoute},
		{2, 2},
		{maxAttemptsPerRoute + 5, maxAttemptsPerRoute},
	}
	for _, testCase := range cases {
		cfg := pluginConfig{AttemptsPerRoute: testCase.in}
		if got := cfg.attemptsPerRoute(); got != testCase.want {
			t.Fatalf("attemptsPerRoute(%d) = %d, want %d", testCase.in, got, testCase.want)
		}
	}
}
