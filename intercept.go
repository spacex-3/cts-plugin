package main

import (
	"encoding/json"
	"net/http"
	"strings"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func interceptBeforeAuth(raw []byte) ([]byte, error) {
	return passThroughRequest(raw)
}

func interceptAfterAuth(raw []byte) ([]byte, error) {
	var req pluginapi.RequestInterceptRequest
	if len(raw) > 0 {
		if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
			return nil, errUnmarshal
		}
	}
	return okEnvelope(applyAfterAuth(req))
}

func applyAfterAuth(req pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	rt := currentRuntime()
	if rt == nil {
		return pluginapi.RequestInterceptResponse{}
	}
	cfg := rt.configSnapshot()
	if !cfg.injectEnabled() {
		return pluginapi.RequestInterceptResponse{}
	}
	if !isCodexRequest(req.ToFormat) {
		return pluginapi.RequestInterceptResponse{}
	}
	authID := metadataString(req.Metadata, cliproxyexecutor.SelectedAuthMetadataKey)
	model := strings.TrimSpace(req.Model)
	if !cfg.allows(authID, model) {
		return pluginapi.RequestInterceptResponse{}
	}
	entry, ok := rt.cache.lookup(authID, model)
	if !ok || strings.TrimSpace(entry.State) == "" {
		return pluginapi.RequestInterceptResponse{}
	}
	rt.recordInjection(req, entry)
	headers := make(http.Header)
	headers.Set(turnStateHeader, entry.State)
	return pluginapi.RequestInterceptResponse{Headers: headers}
}

func interceptResponse(raw []byte) ([]byte, error) {
	var req pluginapi.ResponseInterceptRequest
	if len(raw) > 0 {
		if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
			return nil, errUnmarshal
		}
	}
	harvestFromResponse(req)
	return okEnvelope(pluginapi.ResponseInterceptResponse{})
}

func interceptStreamChunk(raw []byte) ([]byte, error) {
	var req pluginapi.StreamChunkInterceptRequest
	if len(raw) > 0 {
		if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
			return nil, errUnmarshal
		}
	}
	harvestFromStream(req)
	return okEnvelope(pluginapi.StreamChunkInterceptResponse{})
}

func observeWebSocket(raw []byte) ([]byte, error) {
	var event pluginapi.WebSocketResponseEvent
	if len(raw) > 0 {
		if errUnmarshal := json.Unmarshal(raw, &event); errUnmarshal != nil {
			return nil, errUnmarshal
		}
	}
	harvestFromWebSocket(event)
	return okEnvelope(map[string]any{})
}

func harvestFromResponse(req pluginapi.ResponseInterceptRequest) {
	rt := currentRuntime()
	if rt == nil {
		return
	}
	cfg := rt.configSnapshot()
	if !cfg.harvestEnabled() {
		return
	}
	authID, model := harvestIdentity(req.Metadata, "", req.Model)
	if !cfg.allows(authID, model) {
		return
	}
	state := headerTurnState(req.ResponseHeaders)
	if state == "" {
		state = extractTurnStateFromChunk(req.Body)
	}
	rt.observeState(authID, model, state, "harvest")
}

func harvestFromStream(req pluginapi.StreamChunkInterceptRequest) {
	rt := currentRuntime()
	if rt == nil {
		return
	}
	cfg := rt.configSnapshot()
	if !cfg.harvestEnabled() {
		return
	}
	authID, model := harvestIdentity(req.Metadata, "", req.Model)
	if !cfg.allows(authID, model) {
		return
	}
	state := extractTurnStateFromChunk(req.Body)
	if state == "" {
		state = headerTurnState(req.ResponseHeaders)
	}
	rt.observeState(authID, model, state, "harvest")
}

func harvestFromWebSocket(event pluginapi.WebSocketResponseEvent) {
	rt := currentRuntime()
	if rt == nil {
		return
	}
	cfg := rt.configSnapshot()
	if !cfg.harvestEnabled() {
		return
	}
	authID, model := harvestIdentity(event.Metadata, event.AuthID, event.Model)
	if !cfg.allows(authID, model) {
		return
	}
	state := extractTurnStateFromChunk(event.Payload)
	rt.observeState(authID, model, state, "harvest")
}

func harvestIdentity(meta map[string]any, authID, model string) (string, string) {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		authID = metadataString(meta, cliproxyexecutor.SelectedAuthMetadataKey)
	}
	model = strings.TrimSpace(model)
	return authID, model
}

func isCodexRequest(toFormat string) bool {
	return strings.EqualFold(strings.TrimSpace(toFormat), "codex")
}

func metadataString(meta map[string]any, key string) string {
	if meta == nil {
		return ""
	}
	value, ok := meta[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	default:
		return strings.TrimSpace(stringify(typed))
	}
}

func stringify(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []byte:
		return string(typed)
	default:
		raw, errMarshal := json.Marshal(typed)
		if errMarshal != nil {
			return ""
		}
		return strings.Trim(string(raw), `"`)
	}
}

func passThroughRequest(raw []byte) ([]byte, error) {
	var req pluginapi.RequestInterceptRequest
	if len(raw) > 0 {
		if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
			return nil, errUnmarshal
		}
	}
	return okEnvelope(pluginapi.RequestInterceptResponse{
		Headers: cloneHeader(req.Headers),
		Body:    append([]byte(nil), req.Body...),
	})
}
