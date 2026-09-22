package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestAuthCookieForwardState(t *testing.T) {
	cases := map[string]struct {
		payload string
		want    cookieForwardState
	}{
		"declared":            {`{"headers":{"Cookie":"$Cookie"}}`, cookieForwardOn},
		"declared lowercase":  {`{"headers":{"cookie":"$cookie"}}`, cookieForwardOn},
		"declared with space": {`{"headers":{"Cookie":" $Cookie "}}`, cookieForwardOn},
		"other headers only":  {`{"headers":{"X-Trace":"abc"}}`, cookieForwardMissing},
		"empty value":         {`{"headers":{"Cookie":""}}`, cookieForwardMissing},
		"literal value":       {`{"headers":{"Cookie":"__cflb=fixed"}}`, cookieForwardCustom},
		"no headers block":    {`{"access_token":"token"}`, cookieForwardMissing},
		"headers not an object": {
			`{"headers":["Cookie"]}`, cookieForwardMissing,
		},
	}
	for name, tc := range cases {
		if got := authCookieForwardState(json.RawMessage(tc.payload)); got != tc.want {
			t.Fatalf("%s: state = %q, want %q", name, got, tc.want)
		}
	}
	if got := authCookieForwardState(nil); got != cookieForwardUnknown {
		t.Fatalf("empty payload = %q, want unknown", got)
	}
	if got := authCookieForwardState(json.RawMessage(`not json`)); got != cookieForwardUnknown {
		t.Fatalf("invalid payload = %q, want unknown", got)
	}
}

func TestCookieForwardStateForReadsAuthJSON(t *testing.T) {
	host := &targetListHost{
		files: []pluginapi.HostAuthFileEntry{{ID: "auth-1", AuthIndex: "index-1", Provider: "codex"}},
		auths: map[string]pluginapi.HostAuthGetResponse{
			"index-1": {JSON: json.RawMessage(`{"headers":{"Cookie":"$Cookie"}}`)},
			"index-2": {JSON: json.RawMessage(`{"access_token":"token"}`)},
		},
	}
	r := newRuntime()
	r.host = host

	if got := r.cookieForwardStateFor("index-1"); got != cookieForwardOn {
		t.Fatalf("index-1 = %q, want on", got)
	}
	if got := r.cookieForwardStateFor("index-2"); got != cookieForwardMissing {
		t.Fatalf("index-2 = %q, want missing", got)
	}
	if got := r.cookieForwardStateFor("index-3"); got != cookieForwardUnknown {
		t.Fatalf("unresolved auth = %q, want unknown", got)
	}
	if got := r.cookieForwardStateFor(""); got != cookieForwardUnknown {
		t.Fatalf("empty auth index = %q, want unknown", got)
	}
}

func TestInjectedCookieHeaderMasksValues(t *testing.T) {
	if got := injectedCookieHeader(""); got != "" {
		t.Fatalf("empty names = %q, want empty", got)
	}
	if got := injectedCookieHeader("__cflb, __oailb"); got != "__cflb=***; __oailb=***" {
		t.Fatalf("masked header = %q", got)
	}
	if strings.Contains(injectedCookieHeader("__cflb"), "__cflb=") == false {
		t.Fatal("cookie header must name the cookie it masks")
	}
}

func TestSerializedRequestHeadersRecordsInjectedCookie(t *testing.T) {
	headers := http.Header{}
	headers.Set("X-Client-Request-Id", "req-1")

	serialized := serializedRequestHeaders(headers, "state-value", []string{"__cflb", "__oailb"})
	var decoded map[string][]string
	if err := json.Unmarshal([]byte(serialized), &decoded); err != nil {
		t.Fatalf("decode headers: %v", err)
	}
	if got := decoded["Cookie"]; len(got) != 1 || got[0] != "[redacted]" {
		t.Fatalf("cookie entry = %v, want [redacted]", got)
	}
	if got := decoded[turnStateHeader]; len(got) != 1 || got[0] != "state-value" {
		t.Fatalf("state entry = %v", got)
	}

	without := serializedRequestHeaders(headers, "state-value", nil)
	if strings.Contains(without, "Cookie") {
		t.Fatalf("cookie recorded without injection: %s", without)
	}
}

func TestCookieForwardBannerNamesAccountsThatCannotForward(t *testing.T) {
	view := statusView{
		InjectCookies: true,
		Auths: []statusAuth{
			{ID: "auth-1", Label: "account-1", CookieForward: cookieForwardMissing},
			{ID: "auth-2", Label: "account-2", CookieForward: cookieForwardOn},
			{ID: "auth-3", Label: "account-3", CookieForward: cookieForwardCustom},
		},
	}
	banner := writeCookieForwardBanner(view)
	if !strings.Contains(banner, "account-1") || !strings.Contains(banner, "account-3") {
		t.Fatalf("banner does not name blocked accounts: %s", banner)
	}
	if strings.Contains(banner, "account-2") {
		t.Fatalf("banner named a forwarding account: %s", banner)
	}
	if !strings.Contains(banner, "$Cookie") {
		t.Fatalf("banner does not explain the fix: %s", banner)
	}

	view.InjectCookies = false
	if got := writeCookieForwardBanner(view); got != "" {
		t.Fatalf("banner rendered while cookie injection is off: %s", got)
	}
	view.InjectCookies = true
	view.Auths = []statusAuth{{ID: "auth-2", CookieForward: cookieForwardOn}}
	if got := writeCookieForwardBanner(view); got != "" {
		t.Fatalf("banner rendered without blocked accounts: %s", got)
	}
}

func TestStatusViewReportsCookieForwardState(t *testing.T) {
	now := time.Date(2026, time.September, 22, 9, 0, 0, 0, time.UTC)
	probe := false
	r := isolatedRuntime(t, pluginConfig{
		AuthIDs:           []string{"auth-1"},
		Models:            []string{"model-1"},
		Probe:             &probe,
		TargetStateLength: 3,
	})
	r.nowFunc = func() time.Time { return now }
	r.host = &targetListHost{
		files: []pluginapi.HostAuthFileEntry{{
			ID:        "auth-1",
			AuthIndex: "index-1",
			Name:      "account-1",
			Provider:  "codex",
		}},
		auths: map[string]pluginapi.HostAuthGetResponse{
			"index-1": {JSON: json.RawMessage(`{"access_token":"token"}`)},
		},
	}
	withTestRuntime(t, r)

	view := buildStatusView()
	if len(view.Auths) != 1 || len(view.Accounts) != 1 {
		t.Fatalf("view = %+v", view)
	}
	if view.Auths[0].CookieForward != cookieForwardMissing {
		t.Fatalf("auth forward state = %q", view.Auths[0].CookieForward)
	}
	if view.Accounts[0].CookieForward != cookieForwardMissing {
		t.Fatalf("account forward state = %q", view.Accounts[0].CookieForward)
	}
	page := string(renderStatusPage(view, false))
	if !strings.Contains(page, "$Cookie") {
		t.Fatal("status page does not surface the cookie forwarding problem")
	}
}

func TestInjectionsFragmentRendersCookieColumn(t *testing.T) {
	view := statusView{
		InjectCookies: true,
		Injections: []statusInjection{{
			Time:             "2026-09-22T09:00:00Z",
			AuthID:           "auth-1",
			Model:            "model-1",
			State:            "state-value",
			Headers:          `{"Cookie":"[redacted]"}`,
			CookieNames:      "__cflb, __oailb",
			CookieAgeSeconds: 12,
			CookieHeader:     "__cflb=***; __oailb=***",
		}},
	}
	fragment := renderInjectionsFragment(view)
	if !strings.Contains(fragment, "Cookie 请求头") {
		t.Fatalf("cookie column missing: %s", fragment)
	}
	if !strings.Contains(fragment, "__cflb=***") {
		t.Fatalf("cookie column does not render the masked header: %s", fragment)
	}
	if strings.Contains(fragment, "[redacted]") == false {
		t.Fatal("request headers do not record the injected cookie")
	}
}
