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

// Global probe errors the status page maps back to readable advice. The prefixes
// are stable so status_security.go can classify them without echoing raw text.
const (
	probeErrorNoProxyConfigured = "proxy config missing: no proxy entry and direct_probe is off"
	probeErrorNoUsableProxy     = "proxy config invalid: no proxy entry could be parsed"
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
	if rt.configSnapshot().probeSchedule() == "on_demand" {
		rt.setNextProbeAt(time.Time{})
		defer func() {
			for {
				select {
				case job := <-rt.demandTrigger:
					rt.finishDemand(job)
				default:
					return
				}
			}
		}()
		for {
			select {
			case <-ctx.Done():
				return
			case job := <-rt.demandTrigger:
				rt.runDemandProbe(ctx, job)
			case <-rt.trigger:
				rt.probeAll(ctx, false)
			case key := <-rt.targetTrigger:
				rt.probeKey(ctx, key)
			}
		}
	}
	interval := rt.configSnapshot().interval()
	if interval <= 0 {
		interval = time.Duration(defaultIntervalSeconds) * time.Second
	}
	rt.setNextProbeAt(rt.now().Add(interval))
	if len(rt.cache.snapshot()) == 0 {
		rt.probeAll(ctx, false)
		rt.setNextProbeAt(rt.now().Add(interval))
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-rt.trigger:
			rt.probeAll(ctx, false)
			rt.setNextProbeAt(rt.now().Add(interval))
		case key := <-rt.targetTrigger:
			rt.probeKey(ctx, key)
		case <-timer.C:
			rt.probeAll(ctx, true)
			rt.setNextProbeAt(rt.now().Add(interval))
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
	cfg := rt.configSnapshot()
	if rt == nil || key.AuthID == "" || key.Model == "" || !cfg.probeEnabled() || rt.quotaBlocked(key.AuthID, cfg) || rt.probeCooldownBlocked(key, cfg) {
		return false
	}
	select {
	case rt.targetTrigger <- key:
		return true
	default:
		return false
	}
}

func (rt *pluginRuntime) probeAll(ctx context.Context, skipFresh bool) {
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
		if skipFresh && rt.shouldSkipFreshProbe(target, cfg) {
			continue
		}
		rt.probeTargetOnce(ctx, target, cfg, proxies)
	}
}

func (rt *pluginRuntime) shouldSkipFreshProbe(target probeTarget, cfg pluginConfig) bool {
	if cfg.probeSchedule() != "state_aware" {
		return false
	}
	return rt.freshForProbe(makeCacheKey(target.AuthID, target.Model), cfg)
}

func (rt *pluginRuntime) probeKey(ctx context.Context, key cacheKey) {
	cfg := rt.configSnapshot()
	if !cfg.probeEnabled() || ctx.Err() != nil || rt.quotaBlocked(key.AuthID, cfg) {
		return
	}
	targets, proxies, ok := rt.prepareProbe(cfg)
	if ok {
		for _, target := range targets {
			if makeCacheKey(target.AuthID, target.Model) != key {
				continue
			}
			rt.probeTargetOnce(ctx, target, cfg, proxies)
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
	proxies, issues := parseProxyURLsTolerant(strings.Join(cfg.proxyLines(), "\n"), cfg.proxyScheme())
	for _, issue := range issues {
		rt.host.Log("warn", "codex-turn-state: skipped unparsable proxy entry", map[string]any{"detail": issue})
	}
	if len(proxies) == 0 && !cfg.directProbeEnabled() {
		if len(issues) > 0 {
			rt.host.Log("warn", "codex-turn-state: probe skipped because no proxy entry could be parsed", map[string]any{"skipped": len(issues)})
			rt.setGlobalProbeError(probeErrorNoUsableProxy + ": " + strings.Join(issues, "; "))
			return nil, nil, false
		}
		rt.host.Log("info", "codex-turn-state: probe skipped because proxy is empty and direct_probe is off", nil)
		rt.setGlobalProbeError(probeErrorNoProxyConfigured)
		return nil, nil, false
	}
	if len(proxies) == 0 {
		rt.host.Log("info", "codex-turn-state: probing direct only (no proxy configured)", map[string]any{"targets": len(targets)})
	}
	rt.setGlobalProbeError("")
	return targets, proxies, true
}

func (rt *pluginRuntime) probeDirectBaseline(ctx context.Context, target probeTarget, cfg pluginConfig) {
	state, errProbe := rt.probeOnce(ctx, "", target, cfg)
	if errProbe != nil {
		rt.recordProbeAttempt(target, "direct", 0, "", false, false, errProbe.Error())
		rt.host.Log("info", "codex-turn-state: direct baseline probe failed", map[string]any{
			"auth_id":      target.AuthID,
			"model":        target.Model,
			"cookies_sent": rt.probeSendsCookies(cfg, target.AuthID),
			"error":        errProbe.Error(),
		})
		return
	}
	// Every state the upstream returns is admitted. Length and block count are
	// recorded on the status page, never used to refuse a ticket: the shape of a
	// state says which turn it came from (reasoning content or not), not whether
	// it is usable, and refusing a shape we did not expect throws away a ticket
	// that works.
	rt.recordProbeAttempt(target, "direct", 0, state, true, false, "")
	rt.observeState(target.AuthID, target.Model, state, "direct")
}

func (rt *pluginRuntime) probeTargetOnce(ctx context.Context, target probeTarget, cfg pluginConfig, proxies []string) {
	if rt.probeCooldownBlocked(makeCacheKey(target.AuthID, target.Model), cfg) {
		return
	}
	if rt.quotaBlocked(target.AuthID, cfg) {
		return
	}
	if cfg.directProbeEnabled() {
		for attempt := 1; attempt <= cfg.attemptsPerRoute(); attempt++ {
			if ctx.Err() != nil {
				return
			}
			state, errProbe := rt.probeOnce(ctx, "", target, cfg)
			if errProbe != nil {
				rt.recordProbeAttempt(target, "direct", attempt, "", false, false, errProbe.Error())
				rt.host.Log("info", "codex-turn-state: direct probe attempt failed", map[string]any{
					"auth_id":      target.AuthID,
					"model":        target.Model,
					"route":        "direct",
					"attempt":      attempt,
					"cookies_sent": rt.probeSendsCookies(cfg, target.AuthID),
					"error":        sanitizeErrorText(errProbe.Error()),
				})
				if rt.stopForQuota(target.AuthID, cfg, errProbe) {
					return
				}
				continue
			}
			rt.recordProbeAttempt(target, "direct", attempt, state, true, false, "")
			if rt.observeState(target.AuthID, target.Model, state, "direct") {
				rt.resetProbeMiss(makeCacheKey(target.AuthID, target.Model))
				return
			}
		}
	}
	rt.probeTargetWithProxies(ctx, proxies, target)
}

func (rt *pluginRuntime) probeTarget(ctx context.Context, proxyURL string, target probeTarget) {
	rt.probeTargetWithProxies(ctx, []string{proxyURL}, target)
}

func (rt *pluginRuntime) probeTargetWithProxies(ctx context.Context, proxies []string, target probeTarget) {
	cfg := rt.configSnapshot()
	if len(proxies) == 0 || rt.quotaBlocked(target.AuthID, cfg) {
		return
	}
	start := 0
	if cfg.RotateProxyStart {
		rt.mu.Lock()
		start = int(rt.poolCursor % uint64(len(proxies)))
		rt.poolCursor++
		rt.mu.Unlock()
	}
	attempts := cfg.maxAttempts()
	perRoute := cfg.attemptsPerRoute()
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if ctx.Err() != nil || rt.quotaBlocked(target.AuthID, cfg) {
			return
		}
		proxyIndex := (start + (attempt-1)/perRoute) % len(proxies)
		proxyURL := proxies[proxyIndex]
		proxyLabel := redactProxyUser(proxyURL)
		state, errProbe := rt.probeOnce(ctx, proxyURL, target, cfg)
		if errProbe != nil {
			lastErr = errProbe
			rt.recordProbe(target, "", false, errProbe.Error())
			rt.recordProbeAttemptWithProxy(target, "proxy", proxyLabel, attempt, "", false, false, errProbe.Error())
			rt.host.Log("info", "codex-turn-state: probe attempt failed", map[string]any{
				"auth_id":     target.AuthID,
				"model":       target.Model,
				"route":       "proxy",
				"proxy":       proxyLabel,
				"proxy_index": proxyIndex + 1,
				"attempt":     attempt,
				"error":       sanitizeErrorText(errProbe.Error()),
			})
			if rt.stopForQuota(target.AuthID, cfg, errProbe) {
				return
			}
			continue
		}
		accepted := rt.observeState(target.AuthID, target.Model, state, "probe")
		if accepted {
			rt.recordProbeAttemptWithProxy(target, "proxy", proxyLabel, attempt, state, true, true, "")
			rt.recordProbe(target, state, true, "")
			rt.resetProbeMiss(makeCacheKey(target.AuthID, target.Model))
			rt.host.Log("info", "codex-turn-state: captured target turn state", map[string]any{
				"auth_id":      target.AuthID,
				"model":        target.Model,
				"length":       len(state),
				"proxy_index":  proxyIndex + 1,
				"cookies_sent": rt.probeSendsCookies(cfg, target.AuthID),
			})
			return
		}
		lastErr = fmt.Errorf("probe returned no usable turn state (length %d, blocks %s)", len(strings.TrimSpace(state)), stateBlocksText(state))
		rt.recordProbeAttemptWithProxy(target, "proxy", proxyLabel, attempt, state, false, false, lastErr.Error())
		rt.recordProbe(target, state, false, lastErr.Error())
		rt.host.Log("info", "codex-turn-state: probe state not stored", map[string]any{
			"auth_id":      target.AuthID,
			"model":        target.Model,
			"length":       len(strings.TrimSpace(state)),
			"blocks":       stateBlocksText(state),
			"attempt":      attempt,
			"proxy_index":  proxyIndex + 1,
			"cookies_sent": rt.probeSendsCookies(cfg, target.AuthID),
		})
	}
	if lastErr != nil {
		rt.recordProbeMissRound(makeCacheKey(target.AuthID, target.Model), cfg)
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
	// A probe is a cold request by default: it mints a fresh ticket (and a fresh
	// cookie pair, captured below) instead of recycling the routing cookie of a
	// previous probe from a different egress. See probeSendCookiesEnabled.
	if rt.probeSendsCookies(cfg, target.AuthID) {
		req.Header.Set("Cookie", mergeCookieHeader("", rt.cookies.live(target.AuthID)))
	}

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
	// A probe is also a chance to refresh the routing cookies: a successful probe
	// hands out the ticket and its cookie pair together.
	rt.observeCookies(target.AuthID, "probe", probeRouteLabel(proxyURL), resp.Header)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		limited, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", &probeFailure{status: resp.StatusCode, body: string(limited)}
	}
	if cfg.RequireCompleted {
		return readCompletedState(resp.Body, headerTurnState(resp.Header))
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

func probeRouteLabel(proxyURL string) string {
	if strings.TrimSpace(proxyURL) == "" {
		return "direct"
	}
	return redactProxyUser(proxyURL)
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
