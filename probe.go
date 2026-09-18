package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

type probeTransport interface {
	Do(ctx context.Context, proxyURL string, req *http.Request) (*http.Response, error)
}

type utlsProbeTransport struct{}

func (utlsProbeTransport) Do(_ context.Context, proxyURL string, req *http.Request) (*http.Response, error) {
	client, errClient := newUTLSHTTPClient(strings.TrimSpace(proxyURL))
	if errClient != nil {
		return nil, errClient
	}
	return client.Do(req)
}

type probeTarget struct {
	AuthID    string
	AuthIndex string
	Name      string
	Label     string
	Model     string
	Token     string
	AccountID string
	BaseURL   string
}

func (rt *pluginRuntime) runProbeLoop(ctx context.Context) {
	interval := rt.configSnapshot().interval()
	if interval <= 0 {
		interval = time.Duration(defaultIntervalSeconds) * time.Second
	}
	rt.probeAll(ctx)
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-rt.trigger:
			rt.probeAll(ctx)
		case key := <-rt.targetTrigger:
			rt.probeKey(ctx, key)
		case <-timer.C:
			rt.probeAll(ctx)
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(interval)
	}
}

func (rt *pluginRuntime) triggerProbe() {
	if rt == nil {
		return
	}
	select {
	case rt.trigger <- struct{}{}:
	default:
	}
}

func (rt *pluginRuntime) triggerTargetProbe(key cacheKey) bool {
	if rt == nil || key.AuthID == "" || key.Model == "" || !rt.configSnapshot().probeEnabled() {
		return false
	}
	select {
	case rt.targetTrigger <- key:
		return true
	default:
		return false
	}
}

func (rt *pluginRuntime) probeAll(ctx context.Context) {
	if rt == nil {
		return
	}
	cfg := rt.configSnapshot()
	if !cfg.probeEnabled() || ctx.Err() != nil {
		return
	}
	targets, proxies, ok := rt.prepareProbe(cfg)
	if !ok {
		return
	}
	for _, target := range targets {
		if ctx.Err() != nil {
			return
		}
		if cfg.directProbeEnabled() {
			rt.probeDirectBaseline(ctx, target, cfg)
		}
		rt.probeTargetWithProxies(ctx, proxies, target)
	}
}

func (rt *pluginRuntime) probeKey(ctx context.Context, key cacheKey) {
	cfg := rt.configSnapshot()
	if !cfg.probeEnabled() || ctx.Err() != nil {
		return
	}
	targets, proxies, ok := rt.prepareProbe(cfg)
	if ok {
		for _, target := range targets {
			if makeCacheKey(target.AuthID, target.Model) != key {
				continue
			}
			if cfg.directProbeEnabled() {
				rt.probeDirectBaseline(ctx, target, cfg)
			}
			rt.probeTargetWithProxies(ctx, proxies, target)
			break
		}
	}
	rt.mu.Lock()
	stats := rt.windows[key]
	if stats.ReprobeQueued {
		stats.ReprobeQueued = false
		rt.windows[key] = stats
	}
	rt.mu.Unlock()
}

func (rt *pluginRuntime) prepareProbe(cfg pluginConfig) ([]probeTarget, []string, bool) {
	targets, errTargets := rt.listProbeTargets()
	if errTargets != nil {
		rt.host.Log("warn", "codex-turn-state: list probe targets failed", map[string]any{"error": errTargets.Error()})
		rt.setGlobalProbeError(errTargets.Error())
		return nil, nil, false
	}
	if len(targets) == 0 {
		rt.setGlobalProbeError("no matching Codex credentials")
		return nil, nil, false
	}
	proxies, errProxy := parseProxyURLsWithScheme(strings.Join(cfg.proxyLines(), "\n"), cfg.proxyScheme())
	if errProxy != nil {
		rt.host.Log("warn", "codex-turn-state: invalid probe proxy", map[string]any{"error": errProxy.Error()})
		rt.setGlobalProbeError(errProxy.Error())
		return nil, nil, false
	}
	if len(proxies) == 0 {
		rt.host.Log("info", "codex-turn-state: probe skipped because proxy is empty", nil)
		rt.setGlobalProbeError("at least one proxy is required for probing")
		return nil, nil, false
	}
	rt.setGlobalProbeError("")
	return targets, proxies, true
}

func (rt *pluginRuntime) probeDirectBaseline(ctx context.Context, target probeTarget, cfg pluginConfig) {
	state, errProbe := rt.probeOnce(ctx, "", target, cfg)
	if errProbe != nil {
		rt.recordProbeAttempt(target, "direct", 0, "", false, false, errProbe.Error())
		rt.host.Log("info", "codex-turn-state: direct baseline probe failed", map[string]any{
			"auth_id": target.AuthID,
			"model":   target.Model,
			"error":   errProbe.Error(),
		})
		return
	}
	targetMatch := len(strings.TrimSpace(state)) == cfg.targetLength()
	errText := ""
	if !targetMatch {
		errText = fmt.Sprintf("turn state length %d does not match target %d", len(strings.TrimSpace(state)), cfg.targetLength())
	}
	rt.recordProbeAttempt(target, "direct", 0, state, targetMatch, false, errText)
}

func (rt *pluginRuntime) probeTarget(ctx context.Context, proxyURL string, target probeTarget) {
	rt.probeTargetWithProxies(ctx, []string{proxyURL}, target)
}

func (rt *pluginRuntime) probeTargetWithProxies(ctx context.Context, proxies []string, target probeTarget) {
	cfg := rt.configSnapshot()
	attempts := cfg.maxAttempts()
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if ctx.Err() != nil {
			return
		}
		proxyIndex := (attempt - 1) % len(proxies)
		proxyURL := proxies[proxyIndex]
		proxyLabel := redactProxyURL(proxyURL)
		state, errProbe := rt.probeOnce(ctx, proxyURL, target, cfg)
		if errProbe != nil {
			lastErr = errProbe
			rt.recordProbe(target, "", false, errProbe.Error())
			rt.recordProbeAttemptWithProxy(target, "proxy", proxyLabel, attempt, "", false, false, errProbe.Error())
			continue
		}
		accepted := rt.observeState(target.AuthID, target.Model, state, "probe")
		if accepted {
			rt.recordProbeAttemptWithProxy(target, "proxy", proxyLabel, attempt, state, true, true, "")
			rt.recordProbe(target, state, true, "")
			rt.host.Log("info", "codex-turn-state: captured target turn state", map[string]any{
				"auth_id":     target.AuthID,
				"model":       target.Model,
				"length":      len(state),
				"proxy_index": proxyIndex + 1,
			})
			return
		}
		lastErr = fmt.Errorf("turn state length %d does not match target %d", len(strings.TrimSpace(state)), cfg.targetLength())
		rt.recordProbeAttemptWithProxy(target, "proxy", proxyLabel, attempt, state, false, false, lastErr.Error())
		rt.recordProbe(target, state, false, lastErr.Error())
		rt.host.Log("info", "codex-turn-state: probe turn state rejected", map[string]any{
			"auth_id":     target.AuthID,
			"model":       target.Model,
			"length":      len(strings.TrimSpace(state)),
			"target":      cfg.targetLength(),
			"attempt":     attempt,
			"proxy_index": proxyIndex + 1,
		})
	}
	if lastErr != nil {
		rt.host.Log("warn", "codex-turn-state: probe failed", map[string]any{
			"auth_id": target.AuthID,
			"model":   target.Model,
			"error":   lastErr.Error(),
		})
	}
}

func (rt *pluginRuntime) probeOnce(ctx context.Context, proxyURL string, target probeTarget, cfg pluginConfig) (string, error) {
	body, errBody := buildProbeBody(target.Model, cfg.probePrompt())
	if errBody != nil {
		return "", errBody
	}
	requestURL := strings.TrimSuffix(strings.TrimSpace(target.BaseURL), "/") + "/responses"
	req, errRequest := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(body))
	if errRequest != nil {
		return "", fmt.Errorf("build probe request: %w", errRequest)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+target.Token)
	if target.AccountID != "" {
		req.Header.Set("Chatgpt-Account-Id", target.AccountID)
	}
	req.Header.Set("User-Agent", codexUserAgent)
	req.Header.Set("Originator", codexOriginator)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Connection", "Keep-Alive")

	resp, errDo := rt.transport.Do(ctx, proxyURL, req)
	if errDo != nil {
		return "", fmt.Errorf("probe request: %w", errDo)
	}
	if resp == nil || resp.Body == nil {
		return "", fmt.Errorf("probe request returned an empty response")
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			rt.host.Log("debug", "codex-turn-state: close probe body failed", map[string]any{"error": errClose.Error()})
		}
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		limited, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("probe status %d: %s", resp.StatusCode, summarizeProbeError(limited))
	}
	if state := headerTurnState(resp.Header); state != "" {
		return state, nil
	}
	state, errRead := readTurnStateFromSSE(resp.Body)
	if errRead != nil && state == "" {
		return "", fmt.Errorf("read probe stream: %w", errRead)
	}
	if strings.TrimSpace(state) == "" {
		return "", fmt.Errorf("probe stream ended without x-codex-turn-state")
	}
	return state, nil
}

func (rt *pluginRuntime) listProbeTargets() ([]probeTarget, error) {
	cfg := rt.configSnapshot()
	files, errList := rt.host.AuthList()
	if errList != nil {
		return nil, errList
	}
	authIDs := cfg.authIDs()
	models := cfg.models()
	var targets []probeTarget
	for _, file := range files {
		if !isCodexAuth(file) || file.Disabled {
			continue
		}
		authID := strings.TrimSpace(file.ID)
		if authID == "" || strings.TrimSpace(file.AuthIndex) == "" {
			continue
		}
		if len(authIDs) > 0 && !containsFold(authIDs, authID) {
			continue
		}
		if !cfg.probeAuthEnabled(authID) {
			continue
		}
		got, errGet := rt.host.AuthGet(file.AuthIndex)
		if errGet != nil {
			rt.host.Log("warn", "codex-turn-state: auth get failed", map[string]any{
				"auth_id":    file.ID,
				"auth_index": file.AuthIndex,
				"error":      errGet.Error(),
			})
			continue
		}
		token, accountID, baseURL := credentialFromAuthJSON(got.JSON)
		baseURL = firstNonEmpty(baseURL, file.BaseURL)
		if token == "" {
			rt.host.Log("warn", "codex-turn-state: missing access_token", map[string]any{"auth_id": file.ID})
			continue
		}
		if baseURL == "" {
			baseURL = codexDefaultURL
		}
		for _, model := range models {
			targets = append(targets, probeTarget{
				AuthID:    authID,
				AuthIndex: file.AuthIndex,
				Name:      file.Name,
				Label:     firstNonEmpty(file.Label, file.Email, file.Name, authID),
				Model:     model,
				Token:     token,
				AccountID: accountID,
				BaseURL:   baseURL,
			})
		}
	}
	return targets, nil
}

func isCodexAuth(file pluginapi.HostAuthFileEntry) bool {
	if strings.EqualFold(strings.TrimSpace(file.Provider), "codex") {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(file.Type), "codex") {
		return true
	}
	return false
}

func credentialFromAuthJSON(raw json.RawMessage) (token, accountID, baseURL string) {
	if len(raw) == 0 {
		return "", "", ""
	}
	parsed := gjson.ParseBytes(raw)
	token = firstNonEmpty(
		parsed.Get("access_token").String(),
		parsed.Get("token_data.access_token").String(),
		parsed.Get("api_key").String(),
	)
	accountID = firstNonEmpty(
		parsed.Get("account_id").String(),
		parsed.Get("token_data.account_id").String(),
	)
	baseURL = firstNonEmpty(
		parsed.Get("base_url").String(),
		parsed.Get("attributes.base_url").String(),
	)
	return strings.TrimSpace(token), strings.TrimSpace(accountID), strings.TrimSpace(baseURL)
}

func buildProbeBody(model, prompt string) ([]byte, error) {
	payload := map[string]any{
		"model":               model,
		"stream":              true,
		"store":               false,
		"instructions":        "",
		"parallel_tool_calls": true,
		"include":             []string{"reasoning.encrypted_content"},
		"input": []map[string]any{{
			"type": "message",
			"role": "user",
			"content": []map[string]any{{
				"type": "input_text",
				"text": prompt,
			}},
		}},
	}
	raw, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return nil, fmt.Errorf("marshal probe body: %w", errMarshal)
	}
	return raw, nil
}

func summarizeProbeError(raw []byte) string {
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return "empty body"
	}
	if gjson.Valid(text) {
		parsed := gjson.Parse(text)
		for _, path := range []string{"error.message", "message", "error", "detail"} {
			if value := strings.TrimSpace(parsed.Get(path).String()); value != "" {
				return truncate(value, 240)
			}
		}
	}
	return truncate(text, 240)
}

func truncate(text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	return text[:limit] + "..."
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
