package main

import (
	"path/filepath"
	"testing"
	"time"
)

func TestPersistedRuntimeSurvivesReload(t *testing.T) {
	dir := t.TempDir()
	originalRuntimeConfigDir := runtimeConfigDir
	runtimeConfigDir = func() (string, error) { return dir, nil }
	defer func() { runtimeConfigDir = originalRuntimeConfigDir }()

	now := time.Date(2026, time.September, 18, 15, 0, 0, 0, time.UTC)
	first := newRuntime()
	first.nowFunc = func() time.Time { return now }
	first.config = normalizeConfig(pluginConfig{})
	first.cache = newStateCache(time.Hour, first.nowFunc)
	first.cache.putManual("auth-1", "model-1", "abc")
	first.mu.Lock()
	first.persistLocked()
	first.mu.Unlock()

	second := newRuntime()
	second.nowFunc = func() time.Time { return now }
	second.config = normalizeConfig(pluginConfig{})
	second.cache = newStateCache(time.Hour, second.nowFunc)
	second.mu.Lock()
	restorePersistedRuntimeLocked(second, second.config)
	second.mu.Unlock()

	entry, ok := second.cache.lookup("auth-1", "model-1")
	if !ok || entry.State != "abc" {
		t.Fatalf("restored entry = %#v, ok=%t", entry, ok)
	}
	_ = filepath.Join(dir, "codex-turn-state", "runtime.json")
}
