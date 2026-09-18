package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type sequenceProbeTransport struct {
	states  []string
	proxies []string
	calls   int
}

func (t *sequenceProbeTransport) Do(_ context.Context, proxyURL string, _ *http.Request) (*http.Response, error) {
	if t.calls >= len(t.states) {
		return nil, fmt.Errorf("unexpected probe call %d", t.calls+1)
	}
	state := t.states[t.calls]
	t.proxies = append(t.proxies, proxyURL)
	t.calls++
	body := fmt.Sprintf("data: {\"type\":\"codex.response.metadata\",\"headers\":{\"x-codex-turn-state\":%q}}\n\n", state)
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}, nil
}

func TestProbeTargetRetriesRejectedStateLength(t *testing.T) {
	probe := false
	transport := &sequenceProbeTransport{states: []string{"xx", "abc"}}
	testRuntime := newRuntime()
	testRuntime.config = normalizeConfig(pluginConfig{
		TargetStateLength: 3,
		MaxProbeAttempts:  2,
		Probe:             &probe,
	})
	testRuntime.cache = newStateCache(time.Hour, 3, time.Now)
	testRuntime.host = noopHost{}
	testRuntime.transport = transport

	target := probeTarget{
		AuthID:  "auth-1",
		Model:   defaultProbeModels[0],
		Token:   "token",
		BaseURL: "https://example.test/backend-api/codex",
	}
	testRuntime.probeTarget(context.Background(), "http://proxy.example:8080", target)

	if transport.calls != 2 {
		t.Fatalf("probe calls = %d, want 2", transport.calls)
	}
	entry, ok := testRuntime.cache.lookup(target.AuthID, target.Model)
	if !ok {
		t.Fatal("second target-length state should be cached")
	}
	if entry.State != "abc" {
		t.Fatalf("cached state = %q, want abc", entry.State)
	}
}

func TestBuildProbeBodyUsesSmallStreamingRequest(t *testing.T) {
	raw, errBuild := buildProbeBody("model-1", ".")
	if errBuild != nil {
		t.Fatal(errBuild)
	}
	text := string(raw)
	for _, want := range []string{`"model":"model-1"`, `"stream":true`, `"store":false`, `"parallel_tool_calls":true`, `"include":["reasoning.encrypted_content"]`} {
		if !strings.Contains(text, want) {
			t.Fatalf("probe body %s does not contain %s", text, want)
		}
	}
	if strings.Contains(text, `"max_output_tokens"`) {
		t.Fatalf("probe body contains unsupported max_output_tokens: %s", text)
	}
}

type chunkedProbeBody struct {
	chunks [][]byte
	reads  int
	closed bool
}

func (b *chunkedProbeBody) Read(dst []byte) (int, error) {
	if b.reads >= len(b.chunks) {
		return 0, io.EOF
	}
	chunk := b.chunks[b.reads]
	b.reads++
	return copy(dst, chunk), nil
}

func (b *chunkedProbeBody) Close() error {
	b.closed = true
	return nil
}

type singleResponseTransport struct {
	response *http.Response
}

func (t singleResponseTransport) Do(context.Context, string, *http.Request) (*http.Response, error) {
	return t.response, nil
}

func TestProbeOnceClosesAfterMetadataEvent(t *testing.T) {
	body := &chunkedProbeBody{chunks: [][]byte{
		[]byte("data: {\"type\":\"codex.response.metadata\",\"headers\":{\"x-codex-turn-state\":\"abc\"}}\n"),
		[]byte("\n"),
		[]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"unused\"}\n\n"),
	}}
	testRuntime := newRuntime()
	testRuntime.host = noopHost{}
	testRuntime.transport = singleResponseTransport{response: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       body,
	}}

	state, errProbe := testRuntime.probeOnce(context.Background(), "http://proxy.example:8080", probeTarget{
		AuthID:  "auth-1",
		Model:   "model-1",
		Token:   "token",
		BaseURL: "https://example.test/backend-api/codex",
	}, normalizeConfig(pluginConfig{}))
	if errProbe != nil {
		t.Fatal(errProbe)
	}
	if state != "abc" {
		t.Fatalf("state = %q, want abc", state)
	}
	if !body.closed {
		t.Fatal("probe response body was not closed")
	}
	if body.reads != 2 {
		t.Fatalf("body reads = %d, want 2 before trailing output", body.reads)
	}
}

type targetListHost struct {
	files []pluginapi.HostAuthFileEntry
	auths map[string]pluginapi.HostAuthGetResponse
	gets  []string
}

func (h *targetListHost) AuthList() ([]pluginapi.HostAuthFileEntry, error) {
	return append([]pluginapi.HostAuthFileEntry(nil), h.files...), nil
}

func (h *targetListHost) AuthGet(authIndex string) (pluginapi.HostAuthGetResponse, error) {
	h.gets = append(h.gets, authIndex)
	auth, ok := h.auths[authIndex]
	if !ok {
		return pluginapi.HostAuthGetResponse{}, fmt.Errorf("missing auth %s", authIndex)
	}
	return auth, nil
}

func (*targetListHost) Log(string, string, map[string]any) {}

func TestListProbeTargetsUsesRuntimeAuthIDAndIncludesUnavailable(t *testing.T) {
	probe := false
	host := &targetListHost{
		files: []pluginapi.HostAuthFileEntry{
			{ID: "auth-1", AuthIndex: "index-1", Provider: "codex", Email: "shared@example.test"},
			{ID: "auth-2", AuthIndex: "index-2", Provider: "codex", Email: "other@example.test", Unavailable: true},
		},
		auths: map[string]pluginapi.HostAuthGetResponse{
			"index-1": {JSON: json.RawMessage(`{"access_token":"token-1"}`)},
			"index-2": {JSON: json.RawMessage(`{"access_token":"token-2"}`)},
		},
	}
	testRuntime := newRuntime()
	testRuntime.config = normalizeConfig(pluginConfig{
		AuthIDs: []string{"auth-2"},
		Models:  []string{"model-1"},
		Probe:   &probe,
	})
	testRuntime.host = host

	targets, errList := testRuntime.listProbeTargets()
	if errList != nil {
		t.Fatal(errList)
	}
	if len(targets) != 1 {
		t.Fatalf("targets = %d, want 1", len(targets))
	}
	if targets[0].AuthID != "auth-2" || targets[0].Model != "model-1" {
		t.Fatalf("target = %#v, want auth-2/model-1", targets[0])
	}
	if len(host.gets) != 1 || host.gets[0] != "index-2" {
		t.Fatalf("auth gets = %v, want [index-2]", host.gets)
	}
}

func TestListProbeTargetsDoesNotTreatLabelsAsAuthIDs(t *testing.T) {
	probe := false
	host := &targetListHost{
		files: []pluginapi.HostAuthFileEntry{{
			ID:        "auth-1",
			AuthIndex: "index-1",
			Provider:  "codex",
			Email:     "alias@example.test",
		}},
		auths: map[string]pluginapi.HostAuthGetResponse{
			"index-1": {JSON: json.RawMessage(`{"access_token":"token-1"}`)},
		},
	}
	testRuntime := newRuntime()
	testRuntime.config = normalizeConfig(pluginConfig{
		AuthIDs: []string{"alias@example.test"},
		Models:  []string{"model-1"},
		Probe:   &probe,
	})
	testRuntime.host = host

	targets, errList := testRuntime.listProbeTargets()
	if errList != nil {
		t.Fatal(errList)
	}
	if len(targets) != 0 {
		t.Fatalf("targets = %#v, want none for a label-only match", targets)
	}
	if len(host.gets) != 0 {
		t.Fatalf("auth gets = %v, want none", host.gets)
	}
}

func TestDirectBaselineAndEveryProxyAttemptAreLogged(t *testing.T) {
	probe := false
	showState := true
	transport := &sequenceProbeTransport{states: []string{"direct-state", "xx", "abc"}}
	testRuntime := newRuntime()
	testRuntime.config = normalizeConfig(pluginConfig{
		TargetStateLength: 3,
		MaxProbeAttempts:  2,
		ProbeLogLimit:     10,
		Probe:             &probe,
		ShowStateValues:   &showState,
	})
	testRuntime.cache = newStateCache(time.Hour, 3, time.Now)
	testRuntime.host = noopHost{}
	testRuntime.transport = transport
	target := probeTarget{
		AuthID:  "auth-1",
		Model:   "model-1",
		Token:   "token",
		BaseURL: "https://example.test/backend-api/codex",
	}
	cfg := testRuntime.configSnapshot()
	testRuntime.probeDirectBaseline(context.Background(), target, cfg)
	testRuntime.probeTarget(context.Background(), "socks5://user:pass@proxy.example:1080", target)

	if got := transport.proxies; len(got) != 3 || got[0] != "" || got[1] == "" || got[2] == "" {
		t.Fatalf("probe routes = %#v, want direct then two proxy attempts", got)
	}
	snap := testRuntime.snapshotStatus()
	if len(snap.Logs) != 3 {
		t.Fatalf("probe logs = %d, want 3", len(snap.Logs))
	}
	if snap.Logs[0].Route != "direct" || snap.Logs[0].State != "direct-state" || snap.Logs[0].Cached {
		t.Fatalf("direct log = %#v", snap.Logs[0])
	}
	if snap.Logs[1].Route != "proxy" || snap.Logs[1].Attempt != 1 || snap.Logs[1].State != "xx" || snap.Logs[1].TargetMatch {
		t.Fatalf("first proxy log = %#v", snap.Logs[1])
	}
	if snap.Logs[2].Route != "proxy" || snap.Logs[2].Attempt != 2 || snap.Logs[2].State != "abc" || !snap.Logs[2].TargetMatch || !snap.Logs[2].Cached {
		t.Fatalf("second proxy log = %#v", snap.Logs[2])
	}
	entry, ok := testRuntime.cache.lookup(target.AuthID, target.Model)
	if !ok || entry.State != "abc" {
		t.Fatalf("cached entry = %#v, ok=%t", entry, ok)
	}
}

func TestProbeLogHidesStateAndHonorsLimit(t *testing.T) {
	probe := false
	testRuntime := newRuntime()
	testRuntime.config = normalizeConfig(pluginConfig{Probe: &probe, ProbeLogLimit: 2})
	target := probeTarget{AuthID: "auth-1", Model: "model-1"}
	for attempt, state := range []string{"one", "two", "three"} {
		testRuntime.recordProbeAttempt(target, "proxy", attempt+1, state, true, false, "")
	}
	snap := testRuntime.snapshotStatus()
	if len(snap.Logs) != 2 {
		t.Fatalf("probe logs = %d, want 2", len(snap.Logs))
	}
	for _, entry := range snap.Logs {
		if entry.State != "" {
			t.Fatalf("hidden state was retained: %#v", entry)
		}
	}
}

func TestProbeTargetOnceStopsAfterDirectMatch(t *testing.T) {
	probe := false
	transport := &sequenceProbeTransport{states: []string{"abc"}}
	testRuntime := newRuntime()
	testRuntime.config = normalizeConfig(pluginConfig{
		TargetStateLength: 3,
		DirectProbe:       boolPtr(true),
		Probe:             &probe,
	})
	testRuntime.cache = newStateCache(time.Hour, 3, time.Now)
	testRuntime.host = noopHost{}
	testRuntime.transport = transport
	target := probeTarget{AuthID: "auth-1", Model: "model-1", BaseURL: "https://example.test/backend-api/codex"}

	testRuntime.probeTargetOnce(context.Background(), target, testRuntime.configSnapshot(), []string{"http://proxy.example:8080"})

	if transport.calls != 1 {
		t.Fatalf("probe calls = %d, want only direct probe", transport.calls)
	}
	if transport.proxies[0] != "" {
		t.Fatalf("first probe should be direct, got %q", transport.proxies[0])
	}
	if _, ok := testRuntime.cache.lookup("auth-1", "model-1"); !ok {
		t.Fatal("direct 292 state should be cached")
	}
}

func TestProbeTargetOnceFallsBackToProxiesAfterDirectMismatch(t *testing.T) {
	probe := false
	transport := &sequenceProbeTransport{states: []string{"xx", "abc"}}
	testRuntime := newRuntime()
	testRuntime.config = normalizeConfig(pluginConfig{
		TargetStateLength: 3,
		DirectProbe:       boolPtr(true),
		MaxProbeAttempts:  2,
		Probe:             &probe,
	})
	testRuntime.cache = newStateCache(time.Hour, 3, time.Now)
	testRuntime.host = noopHost{}
	testRuntime.transport = transport
	target := probeTarget{AuthID: "auth-1", Model: "model-1", BaseURL: "https://example.test/backend-api/codex"}

	testRuntime.probeTargetOnce(context.Background(), target, testRuntime.configSnapshot(), []string{"http://proxy.example:8080"})

	if transport.calls != 2 {
		t.Fatalf("probe calls = %d, want direct then proxy fallback", transport.calls)
	}
	if transport.proxies[0] != "" || transport.proxies[1] == "" {
		t.Fatalf("probe order = %#v, want direct then proxy", transport.proxies)
	}
}
