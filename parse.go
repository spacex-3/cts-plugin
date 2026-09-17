package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
)

const turnStateHeaderKey = "x-codex-turn-state"

func headerTurnState(headers http.Header) string {
	if headers == nil {
		return ""
	}
	return strings.TrimSpace(headers.Get(turnStateHeader))
}

func extractTurnStateFromChunk(chunk []byte) string {
	chunk = bytes.TrimSpace(chunk)
	if len(chunk) == 0 {
		return ""
	}
	if state := extractTurnStateFromJSON(chunk); state != "" {
		return state
	}
	return extractTurnStateFromSSE(chunk)
}

func extractTurnStateFromSSE(raw []byte) string {
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var dataLines []string
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		switch {
		case strings.HasPrefix(line, "data:"):
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		case line == "":
			if state := extractTurnStateFromJSON([]byte(strings.Join(dataLines, "\n"))); state != "" {
				return state
			}
			dataLines = nil
		default:
			if state := extractTurnStateFromJSON([]byte(line)); state != "" {
				return state
			}
		}
	}
	if len(dataLines) > 0 {
		if state := extractTurnStateFromJSON([]byte(strings.Join(dataLines, "\n"))); state != "" {
			return state
		}
	}
	return ""
}

func readTurnStateFromSSE(r io.Reader) (string, error) {
	reader := bufio.NewReader(r)
	var dataLines []string
	for {
		line, errRead := reader.ReadString('\n')
		trimmed := strings.TrimRight(line, "\r\n")
		switch {
		case strings.HasPrefix(trimmed, "data:"):
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(trimmed, "data:")))
		case trimmed == "":
			if state := extractTurnStateFromJSON([]byte(strings.Join(dataLines, "\n"))); state != "" {
				return state, nil
			}
			dataLines = nil
		default:
			if !strings.HasPrefix(trimmed, "event:") && !strings.HasPrefix(trimmed, "id:") && !strings.HasPrefix(trimmed, "retry:") && trimmed != "" {
				if state := extractTurnStateFromJSON([]byte(trimmed)); state != "" {
					return state, nil
				}
			}
		}
		if errRead != nil {
			if len(dataLines) > 0 {
				if state := extractTurnStateFromJSON([]byte(strings.Join(dataLines, "\n"))); state != "" {
					return state, nil
				}
			}
			if errRead == io.EOF {
				return "", nil
			}
			return "", errRead
		}
	}
}

func extractTurnStateFromJSON(raw []byte) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("[DONE]")) {
		return ""
	}
	if bytes.HasPrefix(raw, []byte("data:")) {
		return extractTurnStateFromSSE(raw)
	}
	if !gjson.ValidBytes(raw) {
		return ""
	}
	parsed := gjson.ParseBytes(raw)
	if parsed.IsArray() {
		for _, item := range parsed.Array() {
			if state := turnStateFromGJSON(item); state != "" {
				return state
			}
		}
		return ""
	}
	return turnStateFromGJSON(parsed)
}

func turnStateFromGJSON(value gjson.Result) string {
	if !value.Exists() {
		return ""
	}
	if state := headerMapTurnState(value.Get("headers")); state != "" {
		return state
	}
	if state := headerMapTurnState(value.Get("metadata.headers")); state != "" {
		return state
	}
	if state := headerMapTurnState(value.Get("response.headers")); state != "" {
		return state
	}
	if state := strings.TrimSpace(value.Get("x-codex-turn-state").String()); state != "" {
		return state
	}
	if state := strings.TrimSpace(value.Get("X-Codex-Turn-State").String()); state != "" {
		return state
	}
	if payload := value.Get("payload"); payload.Exists() {
		switch {
		case payload.Type == gjson.String:
			if state := extractTurnStateFromJSON([]byte(payload.String())); state != "" {
				return state
			}
		default:
			if state := turnStateFromGJSON(payload); state != "" {
				return state
			}
		}
	}
	if data := value.Get("data"); data.Exists() {
		switch {
		case data.Type == gjson.String:
			if state := extractTurnStateFromJSON([]byte(data.String())); state != "" {
				return state
			}
		default:
			if state := turnStateFromGJSON(data); state != "" {
				return state
			}
		}
	}
	return ""
}

func headerMapTurnState(value gjson.Result) string {
	if !value.Exists() {
		return ""
	}
	if value.IsObject() {
		var found string
		value.ForEach(func(key, val gjson.Result) bool {
			if strings.EqualFold(key.String(), turnStateHeaderKey) {
				found = strings.TrimSpace(val.String())
				return false
			}
			return true
		})
		return found
	}
	if value.Type == gjson.JSON {
		var headers http.Header
		if errUnmarshal := json.Unmarshal([]byte(value.Raw), &headers); errUnmarshal == nil {
			return headerTurnState(headers)
		}
	}
	return ""
}
