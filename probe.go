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

func (rt *pluginRuntime) probeAll(ctx context.Context) {
	if rt == nil {
		return
	}
	cfg := rt.configSnapshot()
	if !cfg.probeEnabled() {
		return
	}
	if errCtx := ctx.Err(); errCtx != nil {
		return
	}
	targets, errTargets := rt.listProbeTargets()
	if errTargets != nil {
		rt.host.Log("warn", "codex-turn-state: list probe targets failed", map[string]any{"error": errTargets.Error()})
		rt.setGlobalProbeError(errTargets.Error())
		return
	}
	if len(targets) == 0 {
		rt.setGlobalProbeError("no matching Codex credentials")
		return
	}
	rt.setGlobalProbeError("")
	proxyURL, errProxy := parseProxyURL(cfg.Proxy)
	if errProxy != nil {
		rt.host.Log("warn", "codex-turn-state: invalid probe proxy", map[string]any{"error": errProxy.Error()})
		rt.setGlobalProbeError(errProxy.Error())
		return
	}
	if proxyURL == "" {
		rt.host.Log("info", "codex-turn-state: probe skipped because proxy is empty", nil)
		rt.setGlobalProbeError("proxy is required for probing")
		return
	}
	for _, target := range targets {
		if errCtx := ctx.Err(); errCtx != nil {
			return
		}
		if cfg.directProbeEnabled() {
			rt.probeDirectBaseline(ctx, target, cfg)
		}
		rt.probeTarget(ctx, proxyURL, target)
	}
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
	rt.recordProbeAttempt(target, "direct", 0, state, targetMatch, false, "")
	rt.host.Log("info", "codex-turn-state: direct baseline observed", map[string]any{
		"auth_id":      target.AuthID,
		"model":        target.Model,
		"length":       len(strings.TrimSpace(state)),
		"target_match": targetMatch,
	})
}

func (rt *pluginRuntime) probeTarget(ctx context.Context, proxyURL string, target probeTarget) {
	cfg := rt.configSnapshot()
	attempts := cfg.maxAttempts()
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if errCtx := ctx.Err(); errCtx != nil {
			return
		}
		state, errProbe := rt.probeOnce(ctx, proxyURL, target, cfg)
		if errProbe != nil {
			lastErr = errProbe
			rt.recordProbe(target, "", false, errProbe.Error())
			rt.recordProbeAttempt(target, "proxy", attempt, "", false, false, errProbe.Error())
			continue
		}
		accepted := rt.observeState(target.AuthID, target.Model, state, "probe")
		if accepted {
			rt.recordProbeAttempt(target, "proxy", attempt, state, true, true, "")
			rt.recordProbe(target, state, true, "")
			rt.host.Log("info", "codex-turn-state: captured target turn state", map[string]any{
				"auth_id": target.AuthID,
				"model":   target.Model,
				"length":  len(state),
			})
			return
		}
		lastErr = fmt.Errorf("turn state length %d does not match target %d", len(strings.TrimSpace(state)), cfg.targetLength())
		rt.recordProbeAttempt(target, "proxy", attempt, state, false, false, lastErr.Error())
		rt.recordProbe(target, state, false, lastErr.Error())
		rt.host.Log("info", "codex-turn-state: probe turn state rejected", map[string]any{
			"auth_id": target.AuthID,
			"model":   target.Model,
			"length":  len(strings.TrimSpace(state)),
			"target":  cfg.targetLength(),
			"attempt": attempt,
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
	body, errBody := buildProbeBody(target.Model, cfg.probePrompt(), cfg.maxOutputTokens())
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

func buildProbeBody(model, prompt string, maxOutputTokens int) ([]byte, error) {
	payload := map[string]any{
		"model":             model,
		"stream":            true,
		"store":             false,
		"instructions":      "",
		"max_output_tokens": maxOutputTokens,
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
