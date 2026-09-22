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
	if !strings.Contains(hidden, "show_state_values") {
		t.Fatal("hidden status page should mention show_state_values")
	}
	base.ShowStateValues = true
	shown := string(renderStatusPage(base, false))
	if !strings.Contains(shown, "secret-state") {
		t.Fatal("enabled status page did not display the state value")
	}
}

func TestRenderStatusPageUsesMatchAndMismatchColors(t *testing.T) {
	page := string(renderStatusPage(statusView{

		Models: []string{"model-1"},
		Accounts: []statusAccount{{
			AuthID: "auth-1",
			Label:  "account-one",
			Models: []statusAccountModel{{
				Model:               "model-1",
				HasState:            true,
				Length:              292,
				RemainingTTLSeconds: 1800,
				Requests:            3,
				Successes:           2,
				AvgTTFTSeconds:      1.2,
			}},
		}},
		ProbeLogs: []statusProbeLog{{
			AuthID:      "auth-1",
			Model:       "model-1",
			Length:      292,
			TargetMatch: true,
		}, {
			AuthID:      "auth-1",
			Model:       "model-1",
			Length:      312,
			TargetMatch: false,
		}},
	}, false))
	if !strings.Contains(page, "probe-log-row match") {
		t.Fatal("status page should mark matching logs green")
	}
	if !strings.Contains(page, "probe-log-row mismatch") {
		t.Fatal("status page should mark nonmatching logs red")
	}
	if !strings.Contains(page, "平均首字") || !strings.Contains(page, "倒计时") {
		t.Fatal("status page should show window metrics and countdown")
	}
}

func TestAccountColorIsStableAndDistinct(t *testing.T) {
	first := accountColor("auth-1")
	second := accountColor("auth-2")
	if first != accountColor("auth-1") {
		t.Fatal("account color should be stable")
	}
	if first == second {
		t.Fatal("different accounts should get different colors")
	}
}

func TestRenderStatusPageProbeButtonDoesNotNavigate(t *testing.T) {
	page := string(renderStatusPage(statusView{}, false))
	if !strings.Contains(page, `type="button"`) || !strings.Contains(page, `fetch(location.pathname+'?op=probe'`) {
		t.Fatal("probe button should trigger a fetch instead of submitting a form")
	}
	if strings.Contains(page, `<form method="post"`) {
		t.Fatal("probe button should not submit a form")
	}
	if !strings.Contains(page, `id="refresh-now"`) {
		t.Fatal("status page should include an in-page refresh button")
	}
	if !strings.Contains(page, `id="manual-state"`) {
		t.Fatal("status page should include manual state input")
	}
}
