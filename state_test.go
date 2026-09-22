package main

import (
	"testing"
	"time"
)

func TestStateCacheStoresEveryShape(t *testing.T) {
	now := time.Date(2026, time.September, 17, 12, 0, 0, 0, time.UTC)
	cache := newStateCache(time.Hour, func() time.Time { return now })

	// 0.6.3 admits every shape. Length and block count are shown on the status
	// page; refusing the shapes we did not expect is what kept the cache empty.
	for _, state := range []string{"xx", "abc", syntheticState(now)} {
		if !cache.putState("auth", "model", state, "probe") {
			t.Fatalf("state %q should be stored", state)
		}
		if entry, ok := cache.lookup("auth", "model"); !ok || entry.State != state {
			t.Fatalf("lookup after %q = %#v ok=%t", state, entry, ok)
		}
	}
	if cache.putState("auth", "model", "   ", "probe") {
		t.Fatal("a blank state is still refused")
	}

	// Lookup is deliberately not an expiry check: the reaper prunes by TTL.
	now = now.Add(2 * time.Hour)
	if _, ok := cache.lookup("auth", "model"); !ok {
		t.Fatal("lookup should not expire entries on its own")
	}
}

func TestStateCacheReconfigureReapsByTTLOnly(t *testing.T) {
	now := time.Date(2026, time.September, 17, 12, 0, 0, 0, time.UTC)
	cache := newStateCache(time.Hour, func() time.Time { return now })
	if !cache.putState("auth", "model", "abc", "probe") {
		t.Fatal("expected initial state to be stored")
	}
	cache.reconfigure(2*time.Hour, func() time.Time { return now })
	if _, ok := cache.lookup("auth", "model"); !ok {
		t.Fatal("reconfiguring the TTL must not drop a live ticket")
	}
	now = now.Add(90 * time.Minute)
	cache.reconfigure(20*time.Minute, func() time.Time { return now })
	if _, ok := cache.lookup("auth", "model"); ok {
		t.Fatal("reconfiguring to a shorter TTL should reap what is now expired")
	}
}

func TestStateCachePutManualAcceptsArbitraryLength(t *testing.T) {
	now := time.Date(2026, time.September, 18, 12, 0, 0, 0, time.UTC)
	cache := newStateCache(time.Hour, func() time.Time { return now })
	entry, ok := cache.putManual("auth-1", "model-1", "manual-state-value")
	if !ok || entry.Length != len("manual-state-value") {
		t.Fatalf("manual entry = %#v, ok=%t", entry, ok)
	}
	got, okLookup := cache.lookup("auth-1", "model-1")
	if !okLookup || got.State != "manual-state-value" {
		t.Fatalf("manual lookup = %#v, ok=%t", got, okLookup)
	}
}
