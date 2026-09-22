package main

import (
	"net/http"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestApplyAfterAuthInjectsOnlyConfiguredTarget(t *testing.T) {
	probe := false
	testRuntime := newRuntime()
	testRuntime.config = normalizeConfig(pluginConfig{
		AuthIDs: []string{"auth-1"},
		Models:  []string{"model-1"},
		Probe:   &probe,
	})
	testRuntime.cache = newStateCache(time.Hour, time.Now)
	if !testRuntime.cache.putState("auth-1", "model-1", "abc", "probe") {
		t.Fatal("failed to seed cache")
	}
	withTestRuntime(t, testRuntime)

	req := pluginapi.RequestInterceptRequest{
		ToFormat: "codex",
		Model:    "model-1",
		Headers:  http.Header{turnStateHeader: []string{"old"}},
		Metadata: map[string]any{cliproxyexecutor.SelectedAuthMetadataKey: "auth-1"},
	}
	resp := applyAfterAuth(req)
	if got := resp.Headers.Get(turnStateHeader); got != "abc" {
		t.Fatalf("injected state = %q, want abc", got)
	}

	req.Metadata[cliproxyexecutor.SelectedAuthMetadataKey] = "auth-2"
	resp = applyAfterAuth(req)
	if got := resp.Headers.Get(turnStateHeader); got != "" {
		t.Fatalf("unconfigured auth received state %q", got)
	}
}

func TestHarvestFromStreamRespectsConfiguredScope(t *testing.T) {
	probe := false
	testRuntime := newRuntime()
	testRuntime.config = normalizeConfig(pluginConfig{
		AuthIDs: []string{"auth-1"},
		Models:  []string{"model-1"},
		Probe:   &probe,
	})
	testRuntime.cache = newStateCache(time.Hour, time.Now)
	withTestRuntime(t, testRuntime)

	chunk := []byte(`data: {"headers":{"x-codex-turn-state":"abc"}}`)
	harvestFromStream(pluginapi.StreamChunkInterceptRequest{
		Model:    "model-1",
		Body:     chunk,
		Metadata: map[string]any{cliproxyexecutor.SelectedAuthMetadataKey: "auth-2"},
	})
	if _, ok := testRuntime.cache.lookup("auth-2", "model-1"); ok {
		t.Fatal("unconfigured auth should not be harvested")
	}

	harvestFromStream(pluginapi.StreamChunkInterceptRequest{
		Model:    "model-1",
		Body:     chunk,
		Metadata: map[string]any{cliproxyexecutor.SelectedAuthMetadataKey: "auth-1"},
	})
	if _, ok := testRuntime.cache.lookup("auth-1", "model-1"); !ok {
		t.Fatal("configured auth and model should be harvested")
	}
}

func withTestRuntime(t *testing.T, replacement *pluginRuntime) {
	t.Helper()
	previous := rt
	rt = replacement
	t.Cleanup(func() {
		replacement.shutdown()
		rt = previous
	})
}
