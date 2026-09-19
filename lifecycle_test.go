package main

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func syntheticState(at time.Time) string {
	raw := make([]byte, 57+160)
	raw[0] = 0x80
	binary.BigEndian.PutUint64(raw[1:9], uint64(at.Unix()))
	return base64.URLEncoding.EncodeToString(raw)
}

func isolatedRuntime(t *testing.T, cfg pluginConfig) *pluginRuntime {
	t.Helper()
	dir := t.TempDir()
	previous := runtimeConfigDir
	runtimeConfigDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { runtimeConfigDir = previous })
	r := newRuntime()
	r.config = normalizeConfig(cfg)
	r.host = noopHost{}
	r.cache = newStateCache(r.config.ttl(), r.config.targetLength(), r.now)
	r.cache.configureIssuedAt(cfg.UseIssuedAt)
	t.Cleanup(r.shutdown)
	return r
}

func TestFernetFreshnessAndValidation(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	fresh := syntheticState(now.Add(-50 * time.Minute))
	if len(fresh) != 292 {
		t.Fatal("fixture must match target length")
	}
	for _, token := range []string{fresh, strings.TrimRight(fresh, "=")} {
		issued, err := parseStateIssuedAt(token)
		if err != nil || !issued.Equal(now.Add(-50*time.Minute)) {
			t.Fatalf("issued=%v err=%v", issued, err)
		}
	}
	cache := newStateCache(time.Hour, 292, func() time.Time { return now })
	cache.configureIssuedAt(true)
	for name, token := range map[string]string{"old": syntheticState(now.Add(-time.Hour)), "future": syntheticState(now.Add(2 * time.Minute)), "invalid": strings.Repeat("x", 292), "version": base64.URLEncoding.EncodeToString(make([]byte, 217))} {
		if cache.putIfTarget("a", name, token, "probe") {
			t.Fatalf("accepted %s", name)
		}
	}
	if !cache.putIfTarget("a", "m", fresh, "probe") {
		t.Fatal("fresh state rejected")
	}
	entry, _ := cache.lookup("a", "m")
	if got := cache.remaining(entry); got != 10*time.Minute {
		t.Fatalf("remaining=%s", got)
	}
	now = now.Add(5 * time.Minute)
	cache.putIfTarget("a", "m", fresh, "probe")
	entry, _ = cache.lookup("a", "m")
	if cache.remaining(entry) != 5*time.Minute {
		t.Fatal("repeat observation rejuvenated state")
	}
	now = now.Add(5 * time.Minute)
	if _, ok := cache.lookup("a", "m"); ok {
		t.Fatal("expired state injectable in issued-at mode")
	}
}

func TestAcceptedBlocksCoversProAndTeam(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	pro := syntheticState(now)
	raw := make([]byte, 57+192)
	raw[0] = 0x80
	binary.BigEndian.PutUint64(raw[1:9], uint64(now.Unix()))
	teamState := base64.URLEncoding.EncodeToString(raw)

	cache := newStateCache(time.Hour, 292, func() time.Time { return now })
	if !cache.putIfTarget("a", "pro", pro, "probe") {
		t.Fatal("Pro 10-block state should be accepted by default")
	}
	if !cache.putIfTarget("a", "team", teamState, "probe") {
		t.Fatal("Team 12-block state should be accepted by default")
	}

	anomaly := make([]byte, 57+176)
	anomaly[0] = 0x80
	binary.BigEndian.PutUint64(anomaly[1:9], uint64(now.Unix()))
	anomalyState := base64.URLEncoding.EncodeToString(anomaly)
	if cache.putIfTarget("a", "bad", anomalyState, "probe") {
		t.Fatal("11-block anomaly should be rejected")
	}
}

func TestRestorePreservesAgeForProbeAndManual(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	for _, source := range []string{"probe", "manual"} {
		for _, issuedMode := range []bool{false, true} {
			r := isolatedRuntime(t, pluginConfig{UseIssuedAt: issuedMode})
			r.nowFunc = func() time.Time { return now }
			original := cacheEntry{AuthID: "a", Model: "m", State: syntheticState(now.Add(-55 * time.Minute)), StoredAt: now.Add(-50 * time.Minute), Source: source}
			r.cache.restore(original)
			r.mu.Lock()
			r.persistLocked()
			r.mu.Unlock()
			r.cache = newStateCache(time.Hour, 292, r.now)
			r.cache.configureIssuedAt(issuedMode)
			restorePersistedRuntimeLocked(r, r.config)
			got, ok := r.cache.lookup("a", "m")
			want := 10 * time.Minute
			if issuedMode {
				want = 5 * time.Minute
			}
			if !ok || !got.StoredAt.Equal(original.StoredAt) || r.cache.remaining(got) != want {
				t.Fatalf("%s issued=%t restored=%+v", source, issuedMode, got)
			}
		}
	}
}

func TestReadCompletedStateRejectsUnfinishedStreams(t *testing.T) {
	metadata := "data: {\"headers\":{\"x-codex-turn-state\":\"abc\"}}\n\n"
	complete := "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n"
	for name, body := range map[string]string{"metadata_only": metadata, "done": metadata + "data: [DONE]\n\n", "truncated": metadata + strings.TrimSpace(complete), "failed": metadata + "data: {\"type\":\"response.failed\"}\n\n", "incomplete": metadata + "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"incomplete\"}}\n\n", "missing_status": metadata + "data: {\"type\":\"response.completed\"}\n\n"} {
		if state, err := readCompletedState(strings.NewReader(body), "header"); err == nil || state != "" {
			t.Fatalf("%s accepted %q err=%v", name, state, err)
		}
	}
	if state, err := readCompletedState(strings.NewReader(metadata+complete), ""); err != nil || state != "abc" {
		t.Fatalf("complete=%q %v", state, err)
	}
	if state, err := readCompletedState(strings.NewReader(complete), "header"); err != nil || state != "header" {
		t.Fatalf("header=%q %v", state, err)
	}
}

func TestStrictProbeDoesNotTrustHeadersBeforeCompletion(t *testing.T) {
	r := isolatedRuntime(t, pluginConfig{RequireCompleted: true})
	body := &chunkedProbeBody{chunks: [][]byte{[]byte("data: {\"type\":\"response.failed\"}\n\n")}}
	r.transport = singleResponseTransport{&http.Response{StatusCode: 200, Header: http.Header{turnStateHeader: []string{"abc"}}, Body: body}}
	if state, err := r.probeOnce(context.Background(), "", probeTarget{BaseURL: "https://example.test"}, r.config); state != "" || err == nil {
		t.Fatalf("state=%q err=%v", state, err)
	}
	if !body.closed {
		t.Fatal("body not closed")
	}
}

func TestStrictHarvestWaitsForRequestCompletion(t *testing.T) {
	r := isolatedRuntime(t, pluginConfig{Models: []string{"m"}, TargetStateLength: 3, RequireCompleted: true})
	withTestRuntime(t, r)
	req := pluginapi.StreamChunkInterceptRequest{RequestID: "one", Model: "m", Metadata: map[string]any{cliproxyexecutor.SelectedAuthMetadataKey: "a"}, Body: []byte("data: {\"headers\":{\"x-codex-turn-state\":\"abc\"}}\n\n")}
	harvestFromStream(req)
	if _, ok := r.cache.lookup("a", "m"); ok {
		t.Fatal("candidate accepted before completion")
	}
	// Another request's completion cannot promote this candidate.
	req.RequestID = "two"
	req.Body = []byte("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
	harvestFromStream(req)
	if _, ok := r.cache.lookup("a", "m"); ok {
		t.Fatal("cross-request promotion")
	}
	req.RequestID = "one"
	raw := req.Body
	for _, chunk := range [][]byte{raw[:13], raw[13:]} {
		req.Body = chunk
		harvestFromStream(req)
	}
	if entry, ok := r.cache.lookup("a", "m"); !ok || entry.State != "abc" {
		t.Fatal("completed split stream not harvested")
	}
	for _, status := range []int{400, 500} {
		harvestFromResponse(pluginapi.ResponseInterceptRequest{RequestID: "bad", Model: "m", Metadata: req.Metadata, StatusCode: status, ResponseHeaders: http.Header{turnStateHeader: []string{"bad"}}, Body: []byte(`{"status":"completed"}`)})
	}
	harvestFromWebSocket(pluginapi.WebSocketResponseEvent{RequestID: "ws", AuthID: "a", Model: "m", Payload: []byte(`{"type":"response.failed","headers":{"x-codex-turn-state":"bad"}}`)})
	if entry, _ := r.cache.lookup("a", "m"); entry.State != "abc" {
		t.Fatal("failed response poisoned cache")
	}
	harvestFromResponse(pluginapi.ResponseInterceptRequest{RequestID: "good", Model: "m", Metadata: req.Metadata, StatusCode: 200, ResponseHeaders: http.Header{turnStateHeader: []string{"new"}}, Body: []byte(`{"status":"completed"}`)})
	if entry, _ := r.cache.lookup("a", "m"); entry.State != "new" {
		t.Fatal("successful response not harvested")
	}
}

type controlledTransport struct {
	calls   atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func (c *controlledTransport) Do(ctx context.Context, _ string, _ *http.Request) (*http.Response, error) {
	c.calls.Add(1)
	select {
	case c.entered <- struct{}{}:
	default:
	}
	select {
	case <-c.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &http.Response{StatusCode: 200, Header: http.Header{turnStateHeader: []string{"abc"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
}
func demandRuntime(t *testing.T, wait int) (*pluginRuntime, *controlledTransport) {
	r := isolatedRuntime(t, pluginConfig{Models: []string{"m"}, TargetStateLength: 3, ProbeSchedule: "on_demand", Proxy: "http://proxy.example:8080", ProbeWaitMilliseconds: wait, MaxProbeAttempts: 1})
	r.host = &targetListHost{files: []pluginapi.HostAuthFileEntry{{ID: "a", AuthIndex: "i", Provider: "codex"}}, auths: map[string]pluginapi.HostAuthGetResponse{"i": {JSON: json.RawMessage(`{"access_token":"synthetic"}`)}}}
	transport := &controlledTransport{entered: make(chan struct{}, 4), release: make(chan struct{})}
	r.transport = transport
	r.mu.Lock()
	r.startProbeLocked()
	r.mu.Unlock()
	return r, transport
}

func TestOnDemandIdleFreshScopeAndConcurrentRequests(t *testing.T) {
	r, transport := demandRuntime(t, 1000)
	withTestRuntime(t, r)
	select {
	case <-transport.entered:
		t.Fatal("idle startup probed")
	case <-time.After(30 * time.Millisecond):
	}
	req := pluginapi.RequestInterceptRequest{ToFormat: "codex", Model: "m", Metadata: map[string]any{cliproxyexecutor.SelectedAuthMetadataKey: "a"}}
	bad := req
	bad.Model = "other"
	applyAfterAuth(bad)
	if transport.calls.Load() != 0 {
		t.Fatal("unconfigured model probed")
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got := applyAfterAuth(req).Headers.Get(turnStateHeader); got != "abc" {
				t.Errorf("injection=%q", got)
			}
		}()
	}
	select {
	case <-transport.entered:
	case <-time.After(time.Second):
		t.Fatal("request did not trigger probe")
	}
	close(transport.release)
	wg.Wait()
	applyAfterAuth(req)
	if transport.calls.Load() != 1 {
		t.Fatalf("calls=%d", transport.calls.Load())
	}
	if !r.snapshotStatus().NextProbeAt.IsZero() {
		t.Fatal("on-demand has periodic countdown")
	}
}

func TestOnDemandWaitBudgetAndShutdown(t *testing.T) {
	r, transport := demandRuntime(t, 20)
	start := time.Now()
	r.ensureDemandProbe(makeCacheKey("a", "m"), r.config)
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("request blocked for %s", elapsed)
	}
	select {
	case <-transport.entered:
	case <-time.After(time.Second):
		t.Fatal("probe not started")
	}
	if _, ok := r.cache.lookup("a", "m"); ok {
		t.Fatal("blocked probe completed")
	}
	r.shutdown()
	r.mu.Lock()
	pending := len(r.demandPending)
	r.mu.Unlock()
	if pending != 0 {
		t.Fatal("shutdown did not release pending requests")
	}
}

func TestOnDemandNearExpiryAndProbeSelection(t *testing.T) {
	r, transport := demandRuntime(t, 1000)
	close(transport.release)
	now := time.Now()
	r.cache.restore(cacheEntry{AuthID: "a", Model: "m", State: "old", StoredAt: now.Add(-58 * time.Minute), Source: "probe"})
	cfg := r.config
	cfg.ProbeAuthIDs = []string{"other"}
	r.ensureDemandProbe(makeCacheKey("a", "m"), cfg)
	if transport.calls.Load() != 0 {
		t.Fatal("disabled probe account probed")
	}
	r.ensureDemandProbe(makeCacheKey("a", "m"), r.config)
	if transport.calls.Load() != 1 {
		t.Fatal("near-expiry state did not trigger")
	}
}

func TestFailureClassificationAndAccountBackoff(t *testing.T) {
	for _, status := range []int{429, 502, 503, 504} {
		if classifyFailure(status, "") != failureTransient {
			t.Fatalf("status %d", status)
		}
	}
	if classifyFailure(400, "server overloaded") != failureTransient {
		t.Fatal("overload")
	}
	for _, code := range []string{"usage_limit_reached", "insufficient_quota"} {
		if classifyFailure(429, code) != failureQuota {
			t.Fatal(code)
		}
	}
	if (pluginConfig{}).quotaBackoff() != 900*time.Second {
		t.Fatal("default backoff")
	}
	now := time.Now()
	r := isolatedRuntime(t, pluginConfig{Models: []string{"m", "other"}, ErrorAwareBackoff: true, QuotaBackoffSeconds: 30})
	r.nowFunc = func() time.Time { return now }
	record := pluginapi.UsageRecord{Provider: "codex", Generate: true, AuthID: "a", Model: "m", Failed: true, Failure: pluginapi.UsageFailure{StatusCode: 429, Body: "insufficient_quota"}}
	r.handleUsage(record)
	if len(r.targetTrigger) != 0 || !r.quotaBlocked("a", r.config) {
		t.Fatal("quota was retried")
	}
	if r.triggerTargetProbe(makeCacheKey("a", "other")) {
		t.Fatal("quota bypassed through another model")
	}
	now = now.Add(30 * time.Second)
	record.Failure = pluginapi.UsageFailure{StatusCode: 503}
	r.handleUsage(record)
	if len(r.targetTrigger) != 1 {
		t.Fatal("transient failure without cache did not queue early retry")
	}
}

func TestQuotaStopsProbeAttemptsAndRotatingProxyStart(t *testing.T) {
	r := isolatedRuntime(t, pluginConfig{TargetStateLength: 3, MaxProbeAttempts: 3, ErrorAwareBackoff: true})
	transport := &countFailureTransport{}
	r.transport = transport
	target := probeTarget{AuthID: "a", Model: "m", BaseURL: "https://example.test"}
	r.probeTargetWithProxies(context.Background(), []string{"http://one"}, target)
	if transport.calls != 1 {
		t.Fatalf("quota attempts=%d", transport.calls)
	}
	r.probeTargetWithProxies(context.Background(), []string{"http://one"}, target)
	if transport.calls != 1 {
		t.Fatal("quota retried during backoff")
	}
	r.config.ErrorAwareBackoff = false
	r.config.RotateProxyStart = true
	sequence := &sequenceProbeTransport{states: []string{"abc", "abc", "abc", "abc"}}
	r.transport = sequence
	for i := 0; i < 4; i++ {
		r.probeTargetWithProxies(context.Background(), []string{"http://one", "http://two", "http://three"}, target)
	}
	if strings.Join(sequence.proxies, ",") != "http://one,http://two,http://three,http://one" {
		t.Fatalf("proxies=%v", sequence.proxies)
	}
}

type countFailureTransport struct{ calls int }

func (c *countFailureTransport) Do(context.Context, string, *http.Request) (*http.Response, error) {
	c.calls++
	return &http.Response{StatusCode: 429, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"usage_limit_reached"}}`))}, nil
}

func TestLifecycleOptionsDefaultToLegacyBehavior(t *testing.T) {
	cfg := normalizeConfig(pluginConfig{})
	if cfg.probeSchedule() != "fixed" || cfg.UseIssuedAt || cfg.RequireCompleted || cfg.ErrorAwareBackoff || cfg.RotateProxyStart {
		t.Fatalf("legacy defaults changed: %+v", cfg)
	}
	if cfg.probeWait() != 1500*time.Millisecond || cfg.probeTimeout() != time.Minute {
		t.Fatal("unexpected demand defaults")
	}
	if (pluginConfig{ProbeWaitMilliseconds: -1}).probeWait() != 0 {
		t.Fatal("negative wait should be asynchronous")
	}
}

type probeTransportFunc func(context.Context, string, *http.Request) (*http.Response, error)

func (f probeTransportFunc) Do(ctx context.Context, proxy string, req *http.Request) (*http.Response, error) {
	return f(ctx, proxy, req)
}

func TestTransientProbeRetriesAndConcurrentQuotaStopsRetries(t *testing.T) {
	for _, quotaDuringRequest := range []bool{false, true} {
		r := isolatedRuntime(t, pluginConfig{TargetStateLength: 3, MaxProbeAttempts: 3, ErrorAwareBackoff: true})
		calls := 0
		r.transport = probeTransportFunc(func(context.Context, string, *http.Request) (*http.Response, error) {
			calls++
			if calls == 1 {
				if quotaDuringRequest {
					r.deferQuota("a", r.config)
				}
				return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("overloaded"))}, nil
			}
			return &http.Response{StatusCode: 200, Header: http.Header{turnStateHeader: []string{"abc"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
		})
		r.probeTargetWithProxies(context.Background(), []string{"http://proxy.example"}, probeTarget{AuthID: "a", Model: "m", BaseURL: "https://example.test"})
		want := 2
		if quotaDuringRequest {
			want = 1
		}
		if calls != want {
			t.Fatalf("concurrent quota=%t calls=%d want=%d", quotaDuringRequest, calls, want)
		}
	}
}

func TestRestoreDropsEntriesAlreadyPastStoredAtTTL(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	r := isolatedRuntime(t, pluginConfig{TargetStateLength: 3, TTLSeconds: 3600})
	r.nowFunc = func() time.Time { return now }
	r.cache.restore(cacheEntry{AuthID: "a", Model: "m", State: "abc", StoredAt: now.Add(-time.Hour), Source: "probe"})
	if _, ok := r.cache.lookup("a", "m"); ok {
		t.Fatal("expired persisted entry was revived")
	}
}
