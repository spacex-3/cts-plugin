package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestTrackedCookiesWhitelistAndExpiry(t *testing.T) {
	now := time.Date(2026, time.September, 21, 9, 0, 0, 0, time.UTC)
	jar := newCookieJar(300*time.Second, func() time.Time { return now })

	header := http.Header{}
	header.Add("Set-Cookie", "__cflb=abc; Path=/; Max-Age=60")
	header.Add("Set-Cookie", "__oailb=def; Path=/")
	header.Add("Set-Cookie", "session_token=super-secret; Path=/")
	header.Add("Set-Cookie", "oai-did=device; Path=/")

	captured := parseTrackedCookies(header, now)
	if len(captured) != 2 {
		t.Fatalf("captured = %#v, want only the two routing cookies", captured)
	}
	if updated := jar.observe("auth-1", "harvest", "request", captured); updated != 2 {
		t.Fatalf("stored = %d, want 2", updated)
	}
	if got := liveCookieNames(jar, "auth-1"); got != "__cflb,__oailb" {
		t.Fatalf("live = %q, want both cookies", got)
	}

	// The upstream Max-Age is honoured when it is shorter than the configured cap.
	now = now.Add(61 * time.Second)
	if got := liveCookieNames(jar, "auth-1"); got != "__oailb" {
		t.Fatalf("live after upstream expiry = %q, want only __oailb", got)
	}

	// The configured cookie TTL caps everything, even a long upstream Max-Age.
	now = now.Add(240 * time.Second)
	if got := jar.live("auth-1"); len(got) != 0 {
		t.Fatalf("live after the ttl cap = %#v, want none", got)
	}

	tombstone := http.Header{}
	tombstone.Add("Set-Cookie", "__cflb=; Path=/; Max-Age=0")
	if got := parseTrackedCookies(tombstone, now); len(got) != 0 {
		t.Fatalf("deletion tombstone captured as %#v, want none", got)
	}
}

func TestCookieJarKeepsAgeOfRepeatedValue(t *testing.T) {
	now := time.Date(2026, time.September, 21, 9, 0, 0, 0, time.UTC)
	jar := newCookieJar(time.Hour, func() time.Time { return now })
	cookie := []capturedCookie{{Name: "__cflb", Value: "abc"}}

	jar.observe("auth-1", "harvest", "request", cookie)
	now = now.Add(90 * time.Second)
	if updated := jar.observe("auth-1", "harvest", "request", cookie); updated != 0 {
		t.Fatalf("repeated value counted as %d updates, want 0", updated)
	}
	live := jar.live("auth-1")
	if len(live) != 1 || !live[0].CapturedAt.Equal(time.Date(2026, time.September, 21, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("age was extended by a repeated value: %#v", live)
	}

	replaced := []capturedCookie{{Name: "__cflb", Value: "xyz"}}
	if updated := jar.observe("auth-1", "probe", "proxy", replaced); updated != 1 {
		t.Fatalf("changed value counted as %d updates, want 1", updated)
	}
	live = jar.live("auth-1")
	if len(live) != 1 || live[0].Value != "xyz" || !live[0].CapturedAt.Equal(now) {
		t.Fatalf("changed value did not reset the age: %#v", live)
	}
}

func TestMergeCookieHeaderKeepsHostCookiesAndReplacesTrackedOnes(t *testing.T) {
	injected := []cookieEntry{{Name: "__cflb", Value: "new"}, {Name: "__oailb", Value: "oa"}}
	merged := mergeCookieHeader("__cflb=old; keep=1", injected)
	if merged != "keep=1; __cflb=new; __oailb=oa" {
		t.Fatalf("merged cookie header = %q", merged)
	}
	if got := mergeCookieHeader("", nil); got != "" {
		t.Fatalf("empty merge = %q, want empty", got)
	}
}

func TestApplyAfterAuthInjectsCookiesNextToState(t *testing.T) {
	now := time.Date(2026, time.September, 21, 9, 0, 0, 0, time.UTC)
	probe := false
	r := isolatedRuntime(t, pluginConfig{
		AuthIDs:           []string{"auth-1"},
		Models:            []string{"model-1"},
		Probe:             &probe,
		TargetStateLength: 3,
	})
	r.nowFunc = func() time.Time { return now }
	r.cache = newStateCache(time.Hour, 3, r.now)
	r.cookies = newCookieJar(time.Hour, r.now)

	header := http.Header{}
	header.Add("Set-Cookie", "__cflb=cf")
	header.Add("Set-Cookie", "__oailb=oa")
	if updated := r.observeCookies("auth-1", "harvest", "request", header); updated != 2 {
		t.Fatalf("stored cookies = %d, want 2", updated)
	}
	if !r.cache.putIfTarget("auth-1", "model-1", "abc", "harvest") {
		t.Fatal("failed to seed the cache")
	}
	withTestRuntime(t, r)

	req := pluginapi.RequestInterceptRequest{
		RequestID: "req-1",
		ToFormat:  "codex",
		Model:     "model-1",
		Headers:   http.Header{"Cookie": []string{"keep=1"}},
		Metadata:  map[string]any{cliproxyexecutor.SelectedAuthMetadataKey: "auth-1"},
	}
	resp := applyAfterAuth(req)
	if got := resp.Headers.Get(turnStateHeader); got != "abc" {
		t.Fatalf("injected state = %q, want abc", got)
	}
	if got := resp.Headers.Get("Cookie"); got != "keep=1; __cflb=cf; __oailb=oa" {
		t.Fatalf("injected cookie = %q", got)
	}

	stats := r.snapshotStatus().TicketStats[makeCacheKey("auth-1", "model-1")]
	if stats.Injections != 1 || stats.InjectionsWithCookie != 1 {
		t.Fatalf("counters = %+v, want one injection with cookies", stats)
	}
	record, ok := r.takeInjectedRequest("req-1")
	if !ok || record.Key != makeCacheKey("auth-1", "model-1") || !record.HadCookie {
		t.Fatalf("injection ledger = %#v ok=%t", record, ok)
	}
}

func TestApplyAfterAuthSkipsCookiesWhenDisabled(t *testing.T) {
	now := time.Date(2026, time.September, 21, 9, 0, 0, 0, time.UTC)
	probe, cookies := false, false
	r := isolatedRuntime(t, pluginConfig{
		AuthIDs:           []string{"auth-1"},
		Models:            []string{"model-1"},
		Probe:             &probe,
		InjectCookies:     &cookies,
		TargetStateLength: 3,
	})
	r.nowFunc = func() time.Time { return now }
	r.cache = newStateCache(time.Hour, 3, r.now)
	r.cookies = newCookieJar(time.Hour, r.now)

	header := http.Header{}
	header.Add("Set-Cookie", "__cflb=cf")
	r.observeCookies("auth-1", "harvest", "request", header)
	r.cache.putIfTarget("auth-1", "model-1", "abc", "harvest")
	withTestRuntime(t, r)

	resp := applyAfterAuth(pluginapi.RequestInterceptRequest{
		ToFormat: "codex",
		Model:    "model-1",
		Metadata: map[string]any{cliproxyexecutor.SelectedAuthMetadataKey: "auth-1"},
	})
	if got := resp.Headers.Get("Cookie"); got != "" {
		t.Fatalf("cookie injected while disabled: %q", got)
	}
}

func TestRejectedHarvestInvalidatesOnlyInjectedRequests(t *testing.T) {
	now := time.Date(2026, time.September, 21, 9, 0, 0, 0, time.UTC)
	probe := false
	r := isolatedRuntime(t, pluginConfig{
		AuthIDs:           []string{"auth-1"},
		Models:            []string{"model-1"},
		Probe:             &probe,
		TargetStateLength: 3,
	})
	r.nowFunc = func() time.Time { return now }
	r.cache = newStateCache(time.Hour, 3, r.now)
	r.observeState("auth-1", "model-1", "abc", "harvest")
	withTestRuntime(t, r)

	// A request that never carried our ticket says nothing about the cache.
	r.harvestHarvestedState("unknown-request", "auth-1", "model-1", "rejected-state")
	if _, ok := r.cache.lookup("auth-1", "model-1"); !ok {
		t.Fatal("a bare request's rejection must not retire the cached ticket")
	}

	applyAfterAuth(pluginapi.RequestInterceptRequest{
		RequestID: "req-1",
		ToFormat:  "codex",
		Model:     "model-1",
		Metadata:  map[string]any{cliproxyexecutor.SelectedAuthMetadataKey: "auth-1"},
	})
	r.harvestHarvestedState("req-1", "auth-1", "model-1", "rejected-state")

	if _, ok := r.cache.lookup("auth-1", "model-1"); ok {
		t.Fatal("the refused ticket should be dropped from the cache")
	}
	combo := r.snapshotStatus().Combos[makeCacheKey("auth-1", "model-1")]
	if combo.Invalidations != 1 || combo.LastInvalidatedBy != "state" {
		t.Fatalf("combo = %+v, want one state-driven invalidation", combo)
	}
}

func TestComboInvalidationBlamesTheOlderCookie(t *testing.T) {
	start := time.Date(2026, time.September, 21, 9, 0, 0, 0, time.UTC)
	now := start
	probe := false
	r := isolatedRuntime(t, pluginConfig{
		AuthIDs:           []string{"auth-1"},
		Models:            []string{"model-1"},
		Probe:             &probe,
		TargetStateLength: 3,
	})
	r.nowFunc = func() time.Time { return now }
	r.cache = newStateCache(time.Hour, 3, r.now)
	r.cookies = newCookieJar(time.Hour, r.now)

	header := http.Header{}
	header.Add("Set-Cookie", "__cflb=cf")
	r.observeCookies("auth-1", "harvest", "request", header)

	// The cookie is minutes old by the time a ticket is stored and injected.
	now = start.Add(120 * time.Second)
	r.observeState("auth-1", "model-1", "abc", "harvest")
	withTestRuntime(t, r)
	applyAfterAuth(pluginapi.RequestInterceptRequest{
		RequestID: "req-1",
		ToFormat:  "codex",
		Model:     "model-1",
		Metadata:  map[string]any{cliproxyexecutor.SelectedAuthMetadataKey: "auth-1"},
	})

	now = start.Add(150 * time.Second)
	r.harvestHarvestedState("req-1", "auth-1", "model-1", "refused-state")

	combo := r.snapshotStatus().Combos[makeCacheKey("auth-1", "model-1")]
	if combo.LastInvalidatedBy != "cookie" {
		t.Fatalf("invalidated by %q, want cookie (the cookie was older than the ticket)", combo.LastInvalidatedBy)
	}
	if combo.LastLifetime < 29 || combo.LastLifetime > 31 {
		t.Fatalf("combo lifetime = %.1fs, want about 30s", combo.LastLifetime)
	}
}

func TestCacheExpiryBooksTheComboLifetime(t *testing.T) {
	now := time.Date(2026, time.September, 21, 9, 0, 0, 0, time.UTC)
	probe := false
	// Expiry is enforced for cached tickets when their issuance time is checked,
	// which is the configuration where the TTL itself can end a combo.
	r := isolatedRuntime(t, pluginConfig{
		Models:      []string{"model-1"},
		Probe:       &probe,
		UseIssuedAt: true,
		TTLSeconds:  300,
	})
	r.nowFunc = func() time.Time { return now }
	if !r.observeState("auth-1", "model-1", syntheticState(now), "harvest") {
		t.Fatal("failed to seed the cache")
	}

	now = now.Add(310 * time.Second)
	if _, ok := r.cache.lookup("auth-1", "model-1"); ok {
		t.Fatal("the entry should be expired")
	}
	r.recordBareRequest("auth-1", "model-1")

	combo := r.snapshotStatus().Combos[makeCacheKey("auth-1", "model-1")]
	if combo.LastInvalidatedBy != "ttl" || combo.LastLifetime != 310 {
		t.Fatalf("combo = %+v, want a ttl-driven 310s lifetime", combo)
	}
	if stats := r.snapshotStatus().TicketStats[makeCacheKey("auth-1", "model-1")]; stats.Bare != 1 {
		t.Fatalf("bare counter = %d, want 1", stats.Bare)
	}
}

type cookieProbeTransport struct {
	calls        int
	cookieHeader string
	response     http.Header
	state        string
}

func (t *cookieProbeTransport) Do(_ context.Context, _ string, req *http.Request) (*http.Response, error) {
	t.calls++
	t.cookieHeader = req.Header.Get("Cookie")
	header := http.Header{"Content-Type": []string{"text/event-stream"}}
	for key, values := range t.response {
		for _, value := range values {
			header.Add(key, value)
		}
	}
	header.Set(turnStateHeader, t.state)
	return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader(""))}, nil
}

func TestProbeSendsLiveCookiesAndCapturesNewOnes(t *testing.T) {
	now := time.Date(2026, time.September, 21, 9, 0, 0, 0, time.UTC)
	probe, send := false, true
	r := isolatedRuntime(t, pluginConfig{
		Models:            []string{"model-1"},
		Probe:             &probe,
		ProbeSendCookies:  &send,
		TargetStateLength: 3,
	})
	r.nowFunc = func() time.Time { return now }
	r.cookies = newCookieJar(time.Hour, r.now)

	header := http.Header{}
	header.Add("Set-Cookie", "__cflb=cf")
	header.Add("Set-Cookie", "__oailb=oa")
	r.observeCookies("auth-1", "harvest", "request", header)

	transport := &cookieProbeTransport{
		state: "abc",
		response: http.Header{
			"Set-Cookie": []string{"__cflb=cf2; Path=/"},
		},
	}
	r.transport = transport
	target := probeTarget{AuthID: "auth-1", Model: "model-1", Token: "token", BaseURL: "https://example.test/backend-api/codex"}

	state, errProbe := r.probeOnce(context.Background(), "", target, r.configSnapshot())
	if errProbe != nil || state != "abc" {
		t.Fatalf("probe state = %q err = %v", state, errProbe)
	}
	if transport.cookieHeader != "__cflb=cf; __oailb=oa" {
		t.Fatalf("probe cookie header = %q, want the live pair", transport.cookieHeader)
	}
	live := r.cookies.live("auth-1")
	if len(live) != 2 || live[0].Value != "cf2" {
		t.Fatalf("probe response cookie was not captured: %#v", live)
	}
	if live[0].Source != "probe" || live[0].Route != "direct" {
		t.Fatalf("probe cookie provenance = %+v", live[0])
	}
}

// A probe is a cold request unless probe_send_cookies is turned on: it must not
// recycle a routing cookie captured on some other exit, and it must still capture
// the fresh pair the cold probe is handed.
func TestProbesGoOutColdByDefault(t *testing.T) {
	now := time.Date(2026, time.September, 21, 9, 0, 0, 0, time.UTC)
	probe := false
	r := isolatedRuntime(t, pluginConfig{
		Models:            []string{"model-1"},
		Probe:             &probe,
		TargetStateLength: 3,
	})
	if r.configSnapshot().probeSendCookiesEnabled() {
		t.Fatal("probe cookies should be off by default")
	}
	r.nowFunc = func() time.Time { return now }
	r.cookies = newCookieJar(time.Hour, r.now)
	header := http.Header{}
	header.Add("Set-Cookie", "__cflb=cf")
	header.Add("Set-Cookie", "__oailb=oa")
	r.observeCookies("auth-1", "harvest", "request", header)

	transport := &cookieProbeTransport{
		state:    "abc",
		response: http.Header{"Set-Cookie": []string{"__cflb=cf2; Path=/"}},
	}
	r.transport = transport
	target := probeTarget{AuthID: "auth-1", Model: "model-1", Token: "token", BaseURL: "https://example.test/backend-api/codex"}
	if state, errProbe := r.probeOnce(context.Background(), "", target, r.configSnapshot()); errProbe != nil || state != "abc" {
		t.Fatalf("probe state = %q err = %v", state, errProbe)
	}
	if transport.cookieHeader != "" {
		t.Fatalf("cold probe sent cookies: %q", transport.cookieHeader)
	}
	live := r.cookies.live("auth-1")
	if len(live) != 2 || live[0].Value != "cf2" || live[0].Source != "probe" {
		t.Fatalf("cold probe response cookies were not captured: %#v", live)
	}
	if r.probeSendsCookies(r.configSnapshot(), "auth-1") {
		t.Fatal("probeSendsCookies should stay false without probe_send_cookies")
	}
}

func TestProbeCanSkipCookies(t *testing.T) {
	now := time.Date(2026, time.September, 21, 9, 0, 0, 0, time.UTC)
	probe, send := false, false
	r := isolatedRuntime(t, pluginConfig{
		Models:            []string{"model-1"},
		Probe:             &probe,
		ProbeSendCookies:  &send,
		TargetStateLength: 3,
	})
	r.nowFunc = func() time.Time { return now }
	r.cookies = newCookieJar(time.Hour, r.now)
	header := http.Header{}
	header.Add("Set-Cookie", "__cflb=cf")
	r.observeCookies("auth-1", "harvest", "request", header)

	transport := &cookieProbeTransport{state: "abc"}
	r.transport = transport
	target := probeTarget{AuthID: "auth-1", Model: "model-1", Token: "token", BaseURL: "https://example.test/backend-api/codex"}
	if _, errProbe := r.probeOnce(context.Background(), "", target, r.configSnapshot()); errProbe != nil {
		t.Fatalf("probe failed: %v", errProbe)
	}
	if transport.cookieHeader != "" {
		t.Fatalf("probe sent cookies while disabled: %q", transport.cookieHeader)
	}
}

func TestStatusPageNeverRendersCookieValues(t *testing.T) {
	now := time.Date(2026, time.September, 21, 9, 0, 0, 0, time.UTC)
	probe := false
	r := isolatedRuntime(t, pluginConfig{
		AuthIDs:           []string{"auth-1"},
		Models:            []string{"model-1"},
		Probe:             &probe,
		TargetStateLength: 3,
	})
	r.nowFunc = func() time.Time { return now }
	r.cookies = newCookieJar(time.Hour, r.now)
	r.host = &targetListHost{files: []pluginapi.HostAuthFileEntry{{
		ID:        "auth-1",
		AuthIndex: "index-1",
		Name:      "account-1",
		Provider:  "codex",
	}}}
	header := http.Header{}
	header.Add("Set-Cookie", "__cflb=SECRETCOOKIEVALUE")
	header.Add("Set-Cookie", "__oailb=OTHERSECRET")
	r.observeCookies("auth-1", "harvest", "request", header)
	r.cache.putIfTarget("auth-1", "model-1", "abc", "harvest")

	withTestRuntime(t, r)
	view := buildStatusView()
	page := string(renderStatusPage(view, false))
	for _, secret := range []string{"SECRETCOOKIEVALUE", "OTHERSECRET"} {
		if strings.Contains(page, secret) {
			t.Fatalf("status page leaked %q", secret)
		}
	}
	account := view.Accounts[0]
	if account.CookieNames != "__cflb, __oailb" || account.CookieSource != "harvest" {
		t.Fatalf("account cookie summary = %+v", account)
	}
	if len(view.Cookies) != 2 || view.Cookies[0].Length != len("SECRETCOOKIEVALUE") {
		t.Fatalf("cookie list = %#v", view.Cookies)
	}
}

func liveCookieNames(jar *cookieJar, authID string) string {
	names := make([]string, 0, 2)
	for _, entry := range jar.live(authID) {
		names = append(names, entry.Name)
	}
	return strings.Join(names, ",")
}
