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
	states []string
	calls  int
}

func (t *sequenceProbeTransport) Do(_ context.Context, _ string, _ *http.Request) (*http.Response, error) {
	if t.calls >= len(t.states) {
		return nil, fmt.Errorf("unexpected probe call %d", t.calls+1)
	}
	state := t.states[t.calls]
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
	raw, errBuild := buildProbeBody("model-1", ".", 7)
	if errBuild != nil {
		t.Fatal(errBuild)
	}
	text := string(raw)
	for _, want := range []string{`"model":"model-1"`, `"stream":true`, `"store":false`, `"max_output_tokens":7`} {
		if !strings.Contains(text, want) {
			t.Fatalf("probe body %s does not contain %s", text, want)
		}
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
