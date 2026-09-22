package main

import (
	"encoding/json"
	"strings"
)

// CPA's Codex executor does not forward arbitrary client headers upstream: it
// copies a fixed whitelist (turn state, turn metadata, session/thread ids,
// user agent, originator, ...) from the client headers into the upstream
// request, and Cookie is not on that list. A Cookie header written by a plugin
// therefore never reaches OpenAI on its own.
//
// The supported way through is CPA's per-auth custom header feature: an auth
// file that declares
//
//	"headers": { "Cookie": "$Cookie" }
//
// makes the executor copy the request's Cookie header (which this plugin fills
// in) onto the upstream request. The status page checks every Codex account for
// that declaration so "the plugin looks like it does nothing" has a visible,
// checkable reason instead of a guess.
const (
	cookieForwardHeaderName  = "Cookie"
	cookieForwardHeaderValue = "$Cookie"
)

// cookieForwardState is the outcome of inspecting one auth file.
type cookieForwardState string

const (
	// cookieForwardOn: the account declares "Cookie": "$Cookie".
	cookieForwardOn cookieForwardState = "on"
	// cookieForwardMissing: nothing is declared, so injected cookies are dropped.
	cookieForwardMissing cookieForwardState = "missing"
	// cookieForwardCustom: the account sets Cookie to a literal value of its own;
	// the injected value is ignored, but the account author clearly meant this.
	cookieForwardCustom cookieForwardState = "custom"
	// cookieForwardUnknown: the auth payload could not be read.
	cookieForwardUnknown cookieForwardState = "unknown"
)

// authCookieForwardState reads the credential JSON the host returned and looks
// for the custom header declaration that lets cookies through.
func authCookieForwardState(raw json.RawMessage) cookieForwardState {
	if len(raw) == 0 {
		return cookieForwardUnknown
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return cookieForwardUnknown
	}
	headers, ok := payload["headers"].(map[string]any)
	if !ok {
		return cookieForwardMissing
	}
	for key, value := range headers {
		if !strings.EqualFold(strings.TrimSpace(key), cookieForwardHeaderName) {
			continue
		}
		text, _ := value.(string)
		text = strings.TrimSpace(text)
		if text == "" {
			return cookieForwardMissing
		}
		if strings.EqualFold(text, cookieForwardHeaderValue) {
			return cookieForwardOn
		}
		return cookieForwardCustom
	}
	return cookieForwardMissing
}

// cookieForwardStateFor inspects one account's credential JSON on demand. The
// host read is cheap and only happens while the status page is rendered.
func (r *pluginRuntime) cookieForwardStateFor(authIndex string) cookieForwardState {
	if r == nil || r.host == nil || strings.TrimSpace(authIndex) == "" {
		return cookieForwardUnknown
	}
	got, err := r.host.AuthGet(authIndex)
	if err != nil {
		return cookieForwardUnknown
	}
	return authCookieForwardState(got.JSON)
}

func (s cookieForwardState) label() string {
	switch s {
	case cookieForwardOn:
		return "已开启（Cookie: $Cookie）"
	case cookieForwardMissing:
		return "未开启"
	case cookieForwardCustom:
		return "自定义值"
	default:
		return "未知"
	}
}

// cookieForwardNeedsAttention is true for the states the operator has to fix
// before cookie injection can have any effect.
func (s cookieForwardState) cookieForwardNeedsAttention() bool {
	return s == cookieForwardMissing || s == cookieForwardCustom
}

// cookieForwardHint explains the one-key fix in the language of the page.
func cookieForwardHint() string {
	return `CPA 的 Codex 执行器只向上游转发固定白名单请求头，Cookie 不在其中。请在每个 Codex 账号 JSON 里加一行 <code>"headers": {"Cookie": "$Cookie"}</code>，插件注入的 Cookie 才会随请求发到上游。`
}
