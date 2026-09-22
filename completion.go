package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// Only a successful terminal event promotes a candidate. [DONE], EOF, failed
// and incomplete events do not establish successful Codex completion.
func completionEvent(raw []byte, allowResponse bool) (completed, failed bool) {
	if !gjson.ValidBytes(raw) {
		return false, false
	}
	event := gjson.ParseBytes(raw)
	kind := event.Get("type").String()
	status := event.Get("response.status").String()
	if kind == "response.completed" {
		return status == "completed", status != "completed"
	}
	if kind == "response.failed" || kind == "response.incomplete" || kind == "error" || event.Get("error").Exists() {
		return false, true
	}
	if allowResponse {
		return event.Get("status").String() == "completed", event.Get("status").String() == "failed" || event.Get("status").String() == "incomplete"
	}
	return false, false
}

func readCompletedState(r io.Reader, state string) (string, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	var data []string
	flush := func() (string, error, bool) {
		raw := []byte(strings.Join(data, "\n"))
		data = nil
		if candidate := extractTurnStateFromJSON(raw); candidate != "" {
			state = candidate
		}
		completed, failed := completionEvent(raw, false)
		if failed {
			return "", &probeFailure{body: string(raw)}, true
		}
		if completed {
			if state == "" {
				return "", fmt.Errorf("completed probe has no x-codex-turn-state"), true
			}
			return state, nil, true
		}
		return "", nil, false
	}
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
		if line == "" {
			if result, err, done := flush(); done {
				return result, err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	// A terminal event without its SSE delimiter is a truncated event.
	return "", fmt.Errorf("probe stream ended without response.completed")
}

type harvestKey struct {
	RequestID string
	Bucket    cacheKey
}
type harvestCandidate struct {
	state   string
	pending []byte
	updated time.Time
}

func (r *pluginRuntime) harvestCompleted(requestID, authID, model, state string, body []byte, allowResponse bool) {
	// No request ID means we cannot safely combine different requests' chunks.
	key := harvestKey{requestID, makeCacheKey(authID, model)}
	r.mu.Lock()
	now := r.now()
	for k, candidate := range r.harvestPending {
		if now.Sub(candidate.updated) > 2*time.Minute {
			delete(r.harvestPending, k)
		}
	}
	candidate := r.harvestPending[key]
	if candidate == nil {
		candidate = &harvestCandidate{}
	}
	if state != "" {
		candidate.state = state
	}
	candidate.updated = now
	var events [][]byte
	if gjson.ValidBytes(bytes.TrimSpace(body)) {
		events = append(events, bytes.TrimSpace(body))
	} else {
		candidate.pending = append(candidate.pending, body...)
		candidate.pending = bytes.ReplaceAll(candidate.pending, []byte("\r\n"), []byte("\n"))
		for {
			end := bytes.Index(candidate.pending, []byte("\n\n"))
			if end < 0 {
				break
			}
			var lines []string
			for _, line := range strings.Split(string(candidate.pending[:end]), "\n") {
				if strings.HasPrefix(line, "data:") {
					lines = append(lines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
				}
			}
			events = append(events, []byte(strings.Join(lines, "\n")))
			candidate.pending = candidate.pending[end+2:]
		}
	}
	completed, failed := false, false
	for _, event := range events {
		if value := extractTurnStateFromJSON(event); value != "" {
			candidate.state = value
		}
		success, failure := completionEvent(event, allowResponse)
		completed = completed || success
		failed = failed || failure
	}
	if completed || failed || len(candidate.pending) > 1024*1024 || requestID == "" {
		delete(r.harvestPending, key)
	} else if len(r.harvestPending) < 256 || r.harvestPending[key] != nil {
		r.harvestPending[key] = candidate
	}
	acceptedState := candidate.state
	r.mu.Unlock()
	if completed && !failed {
		// Only judge the ticket once the turn is known to have completed, so a
		// metadata frame from a still-healthy stream cannot retire a good ticket.
		r.harvestHarvestedState(requestID, authID, model, acceptedState)
		r.observeState(authID, model, acceptedState, "harvest")
	}
}
