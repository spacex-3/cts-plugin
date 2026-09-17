package main

import (
	"strings"
	"testing"
)

func TestExtractTurnStateFromSSEMetadata(t *testing.T) {
	raw := "event: response\n" +
		"data: {\"type\":\"codex.response.metadata\",\"headers\":{\"x-codex-turn-state\":\"target-state\"}}\n\n"
	if got := extractTurnStateFromChunk([]byte(raw)); got != "target-state" {
		t.Fatalf("turn state = %q, want target-state", got)
	}
}

func TestReadTurnStateFromSSEStopsAtMetadata(t *testing.T) {
	raw := "data: {\"type\":\"response.created\"}\n\n" +
		"data: {\"type\":\"codex.response.metadata\",\"headers\":{\"X-Codex-Turn-State\":\"ticket\"}}\n\n" +
		"data: {\"type\":\"response.completed\"}\n\n"
	got, errRead := readTurnStateFromSSE(strings.NewReader(raw))
	if errRead != nil {
		t.Fatal(errRead)
	}
	if got != "ticket" {
		t.Fatalf("turn state = %q, want ticket", got)
	}
}

func TestExtractTurnStateFromNestedPayload(t *testing.T) {
	raw := `{"payload":"{\"metadata\":{\"headers\":{\"x-codex-turn-state\":\"nested\"}}}"}`
	if got := extractTurnStateFromJSON([]byte(raw)); got != "nested" {
		t.Fatalf("turn state = %q, want nested", got)
	}
}
