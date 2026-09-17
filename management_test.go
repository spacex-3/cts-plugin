package main

import (
	"strings"
	"testing"
	"time"
)

func TestRenderStatusPageShowsProbeStatesOnlyWhenEnabled(t *testing.T) {
	base := statusView{
		ProbeLogLimit: 10,
		ProbeLogs: []statusProbeLog{{
			Time:   time.Date(2026, time.September, 17, 12, 0, 0, 0, time.UTC).Format(time.RFC3339),
			AuthID: "auth-1",
			Model:  "model-1",
			Route:  "direct",
			State:  "secret-state",
			Length: 12,
		}},
	}
	hidden := string(renderStatusPage(base, false))
	if strings.Contains(hidden, "secret-state") {
		t.Fatal("hidden status page exposed a state value")
	}
	if !strings.Contains(hidden, "show_state_values: true") {
		t.Fatal("hidden status page should explain how to enable values")
	}
	base.ShowStateValues = true
	shown := string(renderStatusPage(base, false))
	if !strings.Contains(shown, "secret-state") {
		t.Fatal("enabled status page did not display the state value")
	}
}
