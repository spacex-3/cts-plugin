package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func callStatus(t *testing.T, path, method string, query url.Values, body string) managementResponse {
	t.Helper()
	raw, err := json.Marshal(managementRequest{Path: path, Method: method, Query: query, Body: []byte(body)})
	if err != nil {
		t.Fatal(err)
	}
	raw, err = handleManagement(raw)
	if err != nil {
		t.Fatal(err)
	}
	var envelope envelope
	if err = json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	if !envelope.OK {
		t.Fatalf("failed envelope: %s", raw)
	}
	var response managementResponse
	if err = json.Unmarshal(envelope.Result, &response); err != nil {
		t.Fatal(err)
	}
	if response.Headers.Get("Cache-Control") != "no-store" {
		t.Fatal("response can be cached")
	}
	return response
}

func TestManagementRegistrationSeparatesPublicShellAndProtectedRoutes(t *testing.T) {
	raw, err := handleMethod(pluginabi.MethodManagementRegister, nil)
	if err != nil {
		t.Fatal(err)
	}
	var envelope envelope
	json.Unmarshal(raw, &envelope)
	var registration managementRegistration
	if err = json.Unmarshal(envelope.Result, &registration); err != nil {
		t.Fatal(err)
	}
	if len(registration.Resources) != 1 || registration.Resources[0].Path != resourcePath {
		t.Fatalf("resources=%+v", registration.Resources)
	}
	methods := map[string]bool{}
	for _, route := range registration.Routes {
		if route.Menu != "" || route.Path != managementStatusPath {
			t.Fatalf("data route could be public: %+v", route)
		}
		methods[route.Method] = true
	}
	if len(methods) != 2 || !methods[http.MethodGet] || !methods[http.MethodPost] {
		t.Fatalf("methods=%v", methods)
	}
}

type forbiddenStatusHost struct{ noopHost }

func (forbiddenStatusHost) AuthList() ([]pluginapi.HostAuthFileEntry, error) {
	panic("public shell read account data")
}
func TestPublicStatusNeverReadsDataOrRunsOperations(t *testing.T) {
	r := isolatedRuntime(t, pluginConfig{ShowStateValues: boolPtr(true), ShowAccountDetails: true, ShowInjectionHeaders: true})
	r.host = forbiddenStatusHost{}
	withTestRuntime(t, r)
	response := callStatus(t, publicStatusPath, http.MethodGet, nil, "")
	if response.StatusCode != 200 || string(response.Body) != statusLoginPage {
		t.Fatal("public page is not static shell")
	}
	for _, query := range []url.Values{{"format": {"json"}}, {"op": {"probe"}}, {"op": {"probe_target"}}, {"op": {"save_manual_state"}}, {"op": {"save_proxies"}}, {"op": {"save_probe_accounts"}}, {"op": {"fragment_probe_logs"}}, {"op": {"fragment_injections"}}} {
		response = callStatus(t, publicStatusPath, http.MethodGet, query, "")
		if response.StatusCode != 401 {
			t.Fatalf("public query %v status=%d", query, response.StatusCode)
		}
	}
	for _, path := range []string{resourcePath, publicStatusPath + "/", managementStatusPath + "/other", ""} {
		if response := callStatus(t, path, http.MethodGet, nil, ""); response.StatusCode != 404 {
			t.Fatalf("unexpected alias: %s", path)
		}
	}
	if len(r.trigger) != 0 || len(r.targetTrigger) != 0 {
		t.Fatal("public request queued probe")
	}
}

func TestDefaultStatusResponsesOmitSensitiveData(t *testing.T) {
	email := "private-account@example.test"
	secret := strings.Repeat("secret-state-", 24)
	r := isolatedRuntime(t, pluginConfig{})
	r.host = &targetListHost{files: []pluginapi.HostAuthFileEntry{{ID: email, Name: email + ".json", AuthIndex: email, Label: email, Email: email, Provider: "codex"}}}
	r.cache.putManual(email, defaultProbeModels[0], secret)
	r.globalErr = email + secret
	r.statuses[makeCacheKey(email, defaultProbeModels[0])] = probeRecord{AuthID: email, Model: defaultProbeModels[0], LastError: email + secret}
	r.probeLogs = []probeLogEntry{{Time: time.Now(), AuthID: email, State: secret, Error: email + secret}}
	r.injections = []injectionLogEntry{{Time: time.Now(), AuthID: email, State: secret, Headers: `{"Session-Id":["private-session"],"Cf-Connecting-Ip":["192.0.2.123"],"X-Codex-Turn-State":["` + secret + `"]}`}}
	withTestRuntime(t, r)
	for _, query := range []url.Values{nil, {"format": {"json"}}, {"op": {"fragment_probe_logs"}}, {"op": {"fragment_injections"}}} {
		response := callStatus(t, managementStatusPath, http.MethodGet, query, "")
		for _, forbidden := range []string{email, secret, "private-session", "192.0.2.123", `"headers":`, `"email":`} {
			if strings.Contains(string(response.Body), forbidden) {
				t.Fatalf("%v exposed %q", query, forbidden)
			}
		}
		if !strings.Contains(string(response.Body), accountAlias(email)) {
			t.Fatalf("%v lost account correlation", query)
		}
	}
	view := buildStatusView()
	if view.Auths[0].Email != "" || view.Auths[0].Name != accountAlias(email) || view.Injections[0].Headers != "" {
		t.Fatal("default view exposes identity or headers")
	}
}

func TestHeaderDisplayRequiresOptInAndHonorsStateVisibility(t *testing.T) {
	raw := `{"X-Codex-Turn-State":["secret-state"],"Authorization":["Bearer credential"],"Cookie":["cookie-secret"],"Proxy-Authorization":["proxy-secret"],"Session-Id":["session"]}`
	if got := visibleInjectionHeaders(raw, pluginConfig{}); got != "" {
		t.Fatal("headers shown by default")
	}
	cfg := pluginConfig{ShowInjectionHeaders: true}
	got := visibleInjectionHeaders(raw, cfg)
	for _, secret := range []string{"secret-state", "credential", "cookie-secret", "proxy-secret"} {
		if strings.Contains(got, secret) {
			t.Fatalf("header option leaked %s", secret)
		}
	}
	if !strings.Contains(got, "session") {
		t.Fatal("explicit header display not honored")
	}
	cfg.ShowStateValues = boolPtr(true)
	if !strings.Contains(visibleInjectionHeaders(raw, cfg), "secret-state") {
		t.Fatal("explicit state display not honored")
	}
	if visibleInjectionHeaders("malformed secret-state", cfg) != "" {
		t.Fatal("malformed legacy headers exposed")
	}
}

func TestProtectedMutationsRequirePostAndResolveAccountAliases(t *testing.T) {
	id := "owner@example.test"
	r := isolatedRuntime(t, pluginConfig{Models: []string{"m"}})
	r.host = &targetListHost{files: []pluginapi.HostAuthFileEntry{{ID: id, Provider: "codex", Email: id}}}
	withTestRuntime(t, r)
	for _, op := range []string{"probe", "probe_target", "save_proxies", "save_probe_accounts", "save_manual_state"} {
		if response := callStatus(t, managementStatusPath, http.MethodGet, url.Values{"op": {op}}, ""); response.StatusCode != 405 {
			t.Fatalf("GET mutation %s accepted", op)
		}
	}
	response := callStatus(t, managementStatusPath, http.MethodPost, url.Values{"op": {"save_manual_state"}}, url.Values{"auth_id": {accountAlias(id)}, "model": {"m"}, "state": {"synthetic-manual-state"}}.Encode())
	if response.StatusCode != 200 || !strings.Contains(string(response.Body), `"ok":true`) {
		t.Fatalf("save response: %+v", response)
	}
	if entry, ok := r.cache.lookup(id, "m"); !ok || entry.State != "synthetic-manual-state" {
		t.Fatal("opaque ID did not resolve")
	}
	response = callStatus(t, managementStatusPath, http.MethodPost, url.Values{"op": {"probe_target"}}, url.Values{"auth_id": {accountAlias(id)}, "model": {"m"}}.Encode())
	if response.StatusCode != 200 {
		t.Fatal("probe target failed")
	}
	if got := <-r.targetTrigger; got != makeCacheKey(id, "m") {
		t.Fatalf("probe key=%v", got)
	}
}

func TestStatusLoginAndActionsKeepSecretsOutOfURLs(t *testing.T) {
	for _, bad := range []string{"localStorage.setItem", "sessionStorage.setItem", "?key=", "?token=", "&state=", "&proxy_lines="} {
		if strings.Contains(statusLoginPage, bad) || strings.Contains(string(renderStatusPage(statusView{}, false)), bad) {
			t.Fatalf("unsafe UI storage/query: %s", bad)
		}
	}
	if !strings.Contains(statusLoginPage, "headers.set('Authorization','Bearer '+key)") || !strings.Contains(statusLoginPage, "opts.redirect='error'") {
		t.Fatal("login lacks constrained header transport")
	}
	page := string(renderStatusPage(statusView{}, false))
	if !strings.Contains(page, "window.parent.ctsFetch") || !strings.Contains(page, "body:new URLSearchParams") {
		t.Fatal("actions missing auth bridge or form body")
	}
}

func TestHostedKeyDecodesPanelVariants(t *testing.T) {
	page := statusLoginPage
	for _, variant := range []string{"v1", "v2"} {
		payload := map[string]any{
			"state":   map[string]any{"managementKey": "secret-" + variant},
			"version": 3,
		}
		raw, errJSON := json.Marshal(payload)
		if errJSON != nil {
			t.Fatal(errJSON)
		}
		var salt string
		if variant == "v2" {
			salt = "cli-proxy-api-webui::secure-storage|v2|example.test"
		} else {
			salt = "cli-proxy-api-webui::secure-storage|example.test|test-agent"
		}
		key := []byte(salt)
		encoded := make([]byte, len(raw))
		for i := range raw {
			encoded[i] = raw[i] ^ key[i%len(key)]
		}
		blob := "enc::" + variant + "::" + base64.StdEncoding.EncodeToString(encoded)
		_ = blob
	}
	if !strings.Contains(page, "decodePanel(raw,'v2')") || !strings.Contains(page, "decodePanel(raw,'v1')") {
		t.Fatal("login shell should decode v1 and v2 panel values")
	}
}

func TestVisibleStatusErrorExplainsPluginRaisedFailures(t *testing.T) {
	cases := map[string]string{
		probeErrorNoProxyConfigured:                       "未配置代理",
		probeErrorNoUsableProxy + ": line 1: bad":         "没有一条能解析",
		"no matching Codex credentials":                   "Codex 账号",
		"probe status 403: forbidden":                     "HTTP 403",
		"probe request: utls: dial upstream: i/o timeout": "出口连接失败",
		"build probe proxy: unsupported proxy scheme":     "出口连接失败",
	}
	for raw, want := range cases {
		got := visibleStatusError(raw)
		if !strings.Contains(got, want) {
			t.Errorf("visibleStatusError(%q) = %q, want it to mention %q", raw, got, want)
		}
		if strings.Contains(got, "inspect local plugin logs") {
			t.Errorf("visibleStatusError(%q) fell back to the generic message", raw)
		}
	}
	if got := visibleStatusError("usage_limit_reached"); !strings.Contains(got, "quota") {
		t.Errorf("quota error = %q", got)
	}
	if got := visibleStatusError("account token-abc failed: bearer-secret"); strings.Contains(got, "bearer-secret") {
		t.Errorf("raw upstream text leaked: %q", got)
	}
}
