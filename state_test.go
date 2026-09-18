package main

import (
	"testing"
	"time"
)

func TestStateCacheTargetAndExpiration(t *testing.T) {
	now := time.Date(2026, time.September, 17, 12, 0, 0, 0, time.UTC)
	cache := newStateCache(time.Hour, 3, func() time.Time { return now })

	if cache.putIfTarget("auth", "model", "xx", "probe") {
		t.Fatal("wrong-length state should be rejected")
	}
	if !cache.putIfTarget("auth", "model", "abc", "probe") {
		t.Fatal("target-length state should be accepted")
	}
	if _, ok := cache.lookup("auth", "model"); !ok {
		t.Fatal("fresh state should be found")
	}

	now = now.Add(time.Hour)
	if _, ok := cache.lookup("auth", "model"); ok {
		t.Fatal("state should expire at the TTL boundary")
	}
}

func TestStateCacheReconfigureDropsWrongLength(t *testing.T) {
	now := time.Date(2026, time.September, 17, 12, 0, 0, 0, time.UTC)
	cache := newStateCache(time.Hour, 3, func() time.Time { return now })
	if !cache.putIfTarget("auth", "model", "abc", "probe") {
		t.Fatal("expected initial state to be accepted")
	}
	cache.reconfigure(time.Hour, 4, func() time.Time { return now })
	if _, ok := cache.lookup("auth", "model"); ok {
		t.Fatal("state with the old target length should be removed")
	}
}

func TestStateCachePutManualAcceptsArbitraryLength(t *testing.T) {
	now := time.Date(2026, time.September, 18, 12, 0, 0, 0, time.UTC)
	cache := newStateCache(time.Hour, 292, func() time.Time { return now })
	entry, ok := cache.putManual("auth-1", "model-1", "manual-state-value")
	if !ok || entry.Length != len("manual-state-value") {
		t.Fatalf("manual entry = %#v, ok=%t", entry, ok)
	}
	got, okLookup := cache.lookup("auth-1", "model-1")
	if !okLookup || got.State != "manual-state-value" {
		t.Fatalf("manual lookup = %#v, ok=%t", got, okLookup)
	}
}
