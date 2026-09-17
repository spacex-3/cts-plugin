package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
)

type managementRequest struct {
	Method         string
	Path           string
	Headers        http.Header
	Query          url.Values
	Body           []byte
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type managementResponse struct {
	StatusCode int         `json:"StatusCode"`
	Headers    http.Header `json:"Headers"`
	Body       []byte      `json:"Body"`
}

type managementRegistration struct {
	Resources []managementResource `json:"resources,omitempty"`
}

type managementResource struct {
	Path        string `json:"Path"`
	Menu        string `json:"Menu"`
	Description string `json:"Description"`
}

type statusView struct {
	Proxy             string           `json:"proxy"`
	AuthIDs           []string         `json:"auth_ids"`
	Models            []string         `json:"models"`
	IntervalSeconds   int              `json:"interval_seconds"`
	TargetStateLength int              `json:"target_state_length"`
	TTLSeconds        int              `json:"ttl_seconds"`
	Inject            bool             `json:"inject"`
	Harvest           bool             `json:"harvest"`
	Probe             bool             `json:"probe"`
	DirectProbe       bool             `json:"direct_probe"`
	ShowStateValues   bool             `json:"show_state_values"`
	ProbeLogLimit     int              `json:"probe_log_limit"`
	GlobalError       string           `json:"global_error,omitempty"`
	States            []statusState    `json:"states"`
	Probes            []statusProbe    `json:"probes"`
	ProbeLogs         []statusProbeLog `json:"probe_logs"`
	Auths             []statusAuth     `json:"auths"`
}

type statusState struct {
	AuthID       string `json:"auth_id"`
	Model        string `json:"model"`
	Length       int    `json:"length"`
	AgeSeconds   int    `json:"age_seconds"`
	RemainingTTL int    `json:"remaining_ttl_seconds"`
	Source       string `json:"source"`
	State        string `json:"state,omitempty"`
}

type statusProbe struct {
	AuthID     string `json:"auth_id"`
	Model      string `json:"model"`
	LastError  string `json:"last_error,omitempty"`
	LastLength int    `json:"last_length"`
	Accepted   bool   `json:"accepted"`
	Source     string `json:"source,omitempty"`
	AgeSeconds int    `json:"age_seconds"`
}

type statusProbeLog struct {
	Time        string `json:"time"`
	AgeSeconds  int    `json:"age_seconds"`
	AuthID      string `json:"auth_id"`
	Model       string `json:"model"`
	Route       string `json:"route"`
	Attempt     int    `json:"attempt"`
	State       string `json:"state,omitempty"`
	Length      int    `json:"length"`
	TargetMatch bool   `json:"target_match"`
	Cached      bool   `json:"cached"`
	Error       string `json:"error,omitempty"`
}

type statusAuth struct {
	ID          string `json:"id"`
	AuthIndex   string `json:"auth_index"`
	Name        string `json:"name"`
	Label       string `json:"label"`
	Email       string `json:"email,omitempty"`
	Disabled    bool   `json:"disabled"`
	Unavailable bool   `json:"unavailable"`
}

func handleManagement(raw []byte) ([]byte, error) {
	var req managementRequest
	if len(raw) > 0 {
		if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
			return nil, fmt.Errorf("decode management request: %w", errUnmarshal)
		}
	}
	op := strings.ToLower(strings.TrimSpace(req.Query.Get("op")))
	if op == "" && strings.EqualFold(req.Method, http.MethodPost) {
		op = "probe"
	}
	if op == "probe" {
		currentRuntime().triggerProbe()
	}
	view := buildStatusView()
	if strings.EqualFold(strings.TrimSpace(req.Query.Get("format")), "json") {
		body, errMarshal := json.MarshalIndent(view, "", "  ")
		if errMarshal != nil {
			return nil, errMarshal
		}
		return okEnvelope(managementResponse{
			StatusCode: http.StatusOK,
			Headers: http.Header{
				"Content-Type": []string{"application/json; charset=utf-8"},
			},
			Body: body,
		})
	}
	return okEnvelope(htmlResponse(http.StatusOK, renderStatusPage(view, op == "probe")))
}

func buildStatusView() statusView {
	snap := currentRuntime().snapshotStatus()
	cfg := snap.Config
	interval := cfg.IntervalSeconds
	if interval <= 0 {
		interval = defaultIntervalSeconds
	}
	ttlSeconds := cfg.TTLSeconds
	if ttlSeconds <= 0 {
		ttlSeconds = defaultTTLSeconds
	}
	target := cfg.TargetStateLength
	if target <= 0 {
		target = defaultTargetStateLength
	}
	view := statusView{
		Proxy:             redactedProxy(cfg.Proxy),
		AuthIDs:           cfg.authIDs(),
		Models:            cfg.models(),
		IntervalSeconds:   interval,
		TargetStateLength: target,
		TTLSeconds:        ttlSeconds,
		Inject:            cfg.injectEnabled(),
		Harvest:           cfg.harvestEnabled(),
		Probe:             cfg.probeEnabled(),
		DirectProbe:       cfg.directProbeEnabled(),
		ShowStateValues:   cfg.showStateValuesEnabled(),
		ProbeLogLimit:     cfg.probeLogLimit(),
		GlobalError:       snap.GlobalErr,
	}
	for _, entry := range snap.Entries {
		view.States = append(view.States, statusState{
			AuthID:       entry.AuthID,
			Model:        entry.Model,
			Length:       entry.Length,
			AgeSeconds:   durationSeconds(snap.Now.Sub(entry.StoredAt)),
			RemainingTTL: durationSeconds(currentRuntime().cache.remaining(entry)),
			Source:       entry.Source,
			State:        visibleState(entry.State, cfg.showStateValuesEnabled()),
		})
	}
	sort.Slice(view.States, func(i, j int) bool {
		if view.States[i].AuthID == view.States[j].AuthID {
			return view.States[i].Model < view.States[j].Model
		}
		return view.States[i].AuthID < view.States[j].AuthID
	})
	for _, record := range snap.Records {
		view.Probes = append(view.Probes, statusProbe{
			AuthID:     record.AuthID,
			Model:      record.Model,
			LastError:  record.LastError,
			LastLength: record.LastLength,
			Accepted:   record.LastAccepted,
			Source:     record.LastSource,
			AgeSeconds: durationSeconds(snap.Now.Sub(record.LastAttempt)),
		})
	}
	sort.Slice(view.Probes, func(i, j int) bool {
		if view.Probes[i].AuthID == view.Probes[j].AuthID {
			return view.Probes[i].Model < view.Probes[j].Model
		}
		return view.Probes[i].AuthID < view.Probes[j].AuthID
	})
	for i := len(snap.Logs) - 1; i >= 0; i-- {
		entry := snap.Logs[i]
		view.ProbeLogs = append(view.ProbeLogs, statusProbeLog{
			Time:        entry.Time.Format(time.RFC3339),
			AgeSeconds:  durationSeconds(snap.Now.Sub(entry.Time)),
			AuthID:      entry.AuthID,
			Model:       entry.Model,
			Route:       entry.Route,
			Attempt:     entry.Attempt,
			State:       visibleState(entry.State, cfg.showStateValuesEnabled()),
			Length:      entry.Length,
			TargetMatch: entry.TargetMatch,
			Cached:      entry.Cached,
			Error:       entry.Error,
		})
	}
	if files, errList := currentRuntime().host.AuthList(); errList != nil {
		if view.GlobalError == "" {
			view.GlobalError = errList.Error()
		}
	} else {
		for _, file := range files {
			if !isCodexAuth(file) {
				continue
			}
			view.Auths = append(view.Auths, statusAuth{
				ID:          file.ID,
				AuthIndex:   file.AuthIndex,
				Name:        file.Name,
				Label:       firstNonEmpty(file.Label, file.Email, file.Name),
				Email:       file.Email,
				Disabled:    file.Disabled,
				Unavailable: file.Unavailable,
			})
		}
		sort.Slice(view.Auths, func(i, j int) bool {
			return view.Auths[i].ID < view.Auths[j].ID
		})
	}
	return view
}

func renderStatusPage(view statusView, triggered bool) []byte {
	var out bytes.Buffer
	out.WriteString("<!DOCTYPE html><html><head><meta charset=\"utf-8\"><title>Codex Turn State</title>")
	out.WriteString("<style>body{font-family:-apple-system,BlinkMacSystemFont,\"Segoe UI\",sans-serif;margin:2rem;line-height:1.45;color:#1f2933}table{border-collapse:collapse;width:100%;margin:1rem 0}th,td{border:1px solid #d1d5db;padding:.45rem .6rem;text-align:left;vertical-align:top;word-break:break-word}th{background:#f3f4f6}code{background:#f3f4f6;padding:.1rem .3rem;border-radius:6px}.error{color:#b42318}.ok{color:#067647}form{margin:1rem 0 0}</style>")
	out.WriteString("</head><body><main>")
	out.WriteString("<h1>Codex Turn State</h1>")
	if triggered {
		out.WriteString("<p class=\"ok\">Probe cycle triggered.</p>")
	}
	if view.GlobalError != "" {
		out.WriteString("<p class=\"error\">")
		out.WriteString(html.EscapeString(view.GlobalError))
		out.WriteString("</p>")
	}
	out.WriteString("<h2>Config</h2><table><tbody>")
	writeRow(&out, "proxy", view.Proxy)
	writeRow(&out, "auth_ids", joinOrAll(view.AuthIDs))
	writeRow(&out, "models", strings.Join(view.Models, ", "))
	writeRow(&out, "interval_seconds", fmt.Sprintf("%d", view.IntervalSeconds))
	writeRow(&out, "target_state_length", fmt.Sprintf("%d", view.TargetStateLength))
	writeRow(&out, "ttl_seconds", fmt.Sprintf("%d", view.TTLSeconds))
	writeRow(&out, "inject", fmt.Sprintf("%t", view.Inject))
	writeRow(&out, "harvest", fmt.Sprintf("%t", view.Harvest))
	writeRow(&out, "probe", fmt.Sprintf("%t", view.Probe))
	writeRow(&out, "direct_probe", fmt.Sprintf("%t", view.DirectProbe))
	writeRow(&out, "show_state_values", fmt.Sprintf("%t", view.ShowStateValues))
	writeRow(&out, "probe_log_limit", fmt.Sprintf("%d", view.ProbeLogLimit))
	out.WriteString("</tbody></table>")
	out.WriteString("<form method=\"post\"><button type=\"submit\">Probe now</button></form>")

	out.WriteString("<h2>Cached States</h2>")
	if len(view.States) == 0 {
		out.WriteString("<p>No target-length states cached.</p>")
	} else {
		out.WriteString("<table><thead><tr><th>Auth</th><th>Model</th><th>Length</th><th>Age</th><th>TTL left</th><th>Source</th><th>State</th></tr></thead><tbody>")
		for _, entry := range view.States {
			out.WriteString("<tr>")
			writeCell(&out, entry.AuthID)
			writeCell(&out, entry.Model)
			writeCell(&out, fmt.Sprintf("%d", entry.Length))
			writeCell(&out, formatDuration(time.Duration(entry.AgeSeconds)*time.Second))
			writeCell(&out, formatDuration(time.Duration(entry.RemainingTTL)*time.Second))
			writeCell(&out, entry.Source)
			writeCell(&out, stateDisplay(entry.State, view.ShowStateValues))
			out.WriteString("</tr>")
		}
		out.WriteString("</tbody></table>")
	}

	out.WriteString("<h2>Recent Probes</h2>")
	if len(view.Probes) == 0 {
		out.WriteString("<p>No probe attempts yet.</p>")
	} else {
		out.WriteString("<table><thead><tr><th>Auth</th><th>Model</th><th>Length</th><th>Accepted</th><th>Source</th><th>Error</th></tr></thead><tbody>")
		for _, record := range view.Probes {
			out.WriteString("<tr>")
			writeCell(&out, record.AuthID)
			writeCell(&out, record.Model)
			writeCell(&out, fmt.Sprintf("%d", record.LastLength))
			writeCell(&out, fmt.Sprintf("%t", record.Accepted))
			writeCell(&out, record.Source)
			writeCell(&out, record.LastError)
			out.WriteString("</tr>")
		}
		out.WriteString("</tbody></table>")
	}

	out.WriteString("<h2>Probe Attempt Log</h2>")
	out.WriteString("<p><code>direct</code> is the optional no-proxy baseline; <code>proxy</code> contains every rotating-proxy attempt actually made. Direct baseline values are never cached.</p>")
	if !view.ShowStateValues {
		out.WriteString("<p>State values are hidden. Set <code>show_state_values: true</code> to display future values.</p>")
	}
	if len(view.ProbeLogs) == 0 {
		out.WriteString("<p>No probe log entries yet.</p>")
	} else {
		out.WriteString("<table><thead><tr><th>Time</th><th>Age</th><th>Auth</th><th>Model</th><th>Route</th><th>Attempt</th><th>Length</th><th>Target match</th><th>Cached</th><th>State</th><th>Error</th></tr></thead><tbody>")
		for _, entry := range view.ProbeLogs {
			out.WriteString("<tr>")
			writeCell(&out, entry.Time)
			writeCell(&out, formatDuration(time.Duration(entry.AgeSeconds)*time.Second))
			writeCell(&out, entry.AuthID)
			writeCell(&out, entry.Model)
			writeCell(&out, entry.Route)
			writeCell(&out, fmt.Sprintf("%d", entry.Attempt))
			writeCell(&out, fmt.Sprintf("%d", entry.Length))
			writeCell(&out, fmt.Sprintf("%t", entry.TargetMatch))
			writeCell(&out, fmt.Sprintf("%t", entry.Cached))
			writeCell(&out, stateDisplay(entry.State, view.ShowStateValues))
			writeCell(&out, entry.Error)
			out.WriteString("</tr>")
		}
		out.WriteString("</tbody></table>")
	}

	out.WriteString("<h2>Codex Auths</h2>")
	if len(view.Auths) == 0 {
		out.WriteString("<p>No Codex credentials visible through host.auth.list.</p>")
	} else {
		out.WriteString("<table><thead><tr><th>ID</th><th>Name</th><th>Label</th><th>Disabled</th><th>Unavailable</th></tr></thead><tbody>")
		for _, auth := range view.Auths {
			out.WriteString("<tr>")
			writeCell(&out, auth.ID)
			writeCell(&out, auth.Name)
			writeCell(&out, firstNonEmpty(auth.Label, auth.Email))
			writeCell(&out, fmt.Sprintf("%t", auth.Disabled))
			writeCell(&out, fmt.Sprintf("%t", auth.Unavailable))
			out.WriteString("</tr>")
		}
		out.WriteString("</tbody></table>")
	}
	out.WriteString("<p>JSON: <code>?format=json</code></p>")
	out.WriteString("</main></body></html>")
	return out.Bytes()
}

func visibleState(state string, show bool) string {
	if !show {
		return ""
	}
	return state
}

func stateDisplay(state string, show bool) string {
	if !show {
		return "(hidden)"
	}
	if state == "" {
		return "(none)"
	}
	return state
}

func writeRow(out *bytes.Buffer, key, value string) {
	out.WriteString("<tr><th>")
	out.WriteString(html.EscapeString(key))
	out.WriteString("</th><td><code>")
	out.WriteString(html.EscapeString(value))
	out.WriteString("</code></td></tr>")
}

func writeCell(out *bytes.Buffer, value string) {
	out.WriteString("<td>")
	out.WriteString(html.EscapeString(value))
	out.WriteString("</td>")
}

func htmlResponse(statusCode int, body []byte) managementResponse {
	return managementResponse{
		StatusCode: statusCode,
		Headers: http.Header{
			"Content-Type": []string{resourceContentType},
		},
		Body: body,
	}
}

func joinOrAll(values []string) string {
	if len(values) == 0 {
		return "(all Codex auths)"
	}
	return strings.Join(values, ", ")
}

func durationSeconds(d time.Duration) int {
	if d <= 0 {
		return 0
	}
	return int(d.Round(time.Second) / time.Second)
}

func redactedProxy(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, errParse := parseProxyURL(raw)
	if errParse != nil {
		return "(invalid)"
	}
	return proxyutil.Redact(parsed)
}
