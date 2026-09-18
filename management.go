package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"hash/fnv"
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
	Proxy                   string           `json:"proxy"`
	Proxies                 []string         `json:"proxies"`
	AuthIDs                 []string         `json:"auth_ids"`
	Models                  []string         `json:"models"`
	IntervalSeconds         int              `json:"interval_seconds"`
	TargetStateLength       int              `json:"target_state_length"`
	TTLSeconds              int              `json:"ttl_seconds"`
	FailureReprobeThreshold int              `json:"failure_reprobe_threshold"`
	MaxProbeAttempts        int              `json:"max_probe_attempts"`
	Inject                  bool             `json:"inject"`
	Harvest                 bool             `json:"harvest"`
	Probe                   bool             `json:"probe"`
	DirectProbe             bool             `json:"direct_probe"`
	ShowStateValues         bool             `json:"show_state_values"`
	ProbeLogLimit           int              `json:"probe_log_limit"`
	GlobalError             string           `json:"global_error,omitempty"`
	Accounts                []statusAccount  `json:"accounts"`
	States                  []statusState    `json:"states"`
	Probes                  []statusProbe    `json:"probes"`
	ProbeLogs               []statusProbeLog `json:"probe_logs"`
	Auths                   []statusAuth     `json:"auths"`
}

type statusState struct {
	AuthID        string `json:"auth_id"`
	Model         string `json:"model"`
	Length        int    `json:"length"`
	AgeSeconds    int    `json:"age_seconds"`
	RemainingTTL  int    `json:"remaining_ttl_seconds"`
	ExpiresAtUnix int64  `json:"expires_at_unix,omitempty"`
	Source        string `json:"source"`
	State         string `json:"state,omitempty"`
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
	Proxy       string `json:"proxy,omitempty"`
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

type statusAccount struct {
	AuthID       string               `json:"auth_id"`
	Label        string               `json:"label"`
	Color        string               `json:"color"`
	ProbeEnabled bool                 `json:"probe_enabled"`
	Models       []statusAccountModel `json:"models"`
}

type statusAccountModel struct {
	Model               string  `json:"model"`
	HasState            bool    `json:"has_state"`
	Length              int     `json:"length"`
	Source              string  `json:"source,omitempty"`
	State               string  `json:"state,omitempty"`
	RemainingTTLSeconds int     `json:"remaining_ttl_seconds"`
	ExpiresAtUnix       int64   `json:"expires_at_unix,omitempty"`
	Requests            int64   `json:"requests"`
	Successes           int64   `json:"successes"`
	Failures            int64   `json:"failures"`
	ConsecutiveFailures int     `json:"consecutive_failures"`
	InputTokens         int64   `json:"input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	ReasoningTokens     int64   `json:"reasoning_tokens"`
	TotalTokens         int64   `json:"total_tokens"`
	AvgTTFTSeconds      float64 `json:"avg_ttft_seconds"`
	LastRequestAtUnix   int64   `json:"last_request_at_unix,omitempty"`
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
	if op == "save_proxies" {
		scheme := strings.TrimSpace(req.Query.Get("scheme"))
		lines := string(req.Body)
		if parsed := req.Query.Get("proxy_lines"); parsed != "" || strings.Contains(lines, "proxy_lines=") {
			if values, errValues := url.ParseQuery(lines); errValues == nil {
				lines = values.Get("proxy_lines")
			}
		}
		if errSave := currentRuntime().applyProxyLines(lines, scheme); errSave != nil {
			return nil, errSave
		}
		return okEnvelope(map[string]any{"ok": true, "saved": currentRuntime().configSnapshot().proxyLinesRaw()})
	}
	if op == "save_probe_accounts" {
		rawForm := string(req.Body)
		if req.Query.Get("auth_ids") == "" && rawForm != "" {
			if values, errValues := url.ParseQuery(rawForm); errValues == nil {
				rawForm = values.Get("auth_ids")
			}
		} else {
			rawForm = req.Query.Get("auth_ids")
		}
		authIDs := uniqueTrimmed(strings.Split(rawForm, ","))
		if errSave := currentRuntime().applyProbeAuthIDs(authIDs); errSave != nil {
			return nil, errSave
		}
		return okEnvelope(map[string]any{"ok": true})
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
	proxies := redactedProxyList(cfg)
	view := statusView{
		Proxy:                   strings.Join(proxies, "\n"),
		Proxies:                 proxies,
		AuthIDs:                 cfg.authIDs(),
		Models:                  cfg.models(),
		IntervalSeconds:         interval,
		TargetStateLength:       target,
		TTLSeconds:              ttlSeconds,
		FailureReprobeThreshold: cfg.failureReprobeThreshold(),
		MaxProbeAttempts:        cfg.maxAttempts(),
		Inject:                  cfg.injectEnabled(),
		Harvest:                 cfg.harvestEnabled(),
		Probe:                   cfg.probeEnabled(),
		DirectProbe:             cfg.directProbeEnabled(),
		ShowStateValues:         cfg.showStateValuesEnabled(),
		ProbeLogLimit:           cfg.probeLogLimit(),
		GlobalError:             snap.GlobalErr,
	}
	entryByKey := make(map[cacheKey]cacheEntry, len(snap.Entries))
	for _, entry := range snap.Entries {
		entryByKey[makeCacheKey(entry.AuthID, entry.Model)] = entry
		view.States = append(view.States, statusState{
			AuthID:        entry.AuthID,
			Model:         entry.Model,
			Length:        entry.Length,
			AgeSeconds:    durationSeconds(snap.Now.Sub(entry.StoredAt)),
			RemainingTTL:  durationSeconds(snap.TTL - snap.Now.Sub(entry.StoredAt)),
			ExpiresAtUnix: entry.StoredAt.Add(snap.TTL).Unix(),
			Source:        entry.Source,
			State:         visibleState(entry.State, cfg.showStateValuesEnabled()),
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
			Proxy:       entry.Proxy,
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
	view.Accounts = buildAccountCards(view.Auths, view.Models, cfg, snap, entryByKey)
	return view
}

func buildAccountCards(auths []statusAuth, models []string, cfg pluginConfig, snap runtimeSnapshot, entries map[cacheKey]cacheEntry) []statusAccount {
	allowed := cfg.authIDs()
	accounts := make([]statusAccount, 0, len(auths))
	for _, auth := range auths {
		if len(allowed) > 0 && !containsFold(allowed, auth.ID) {
			continue
		}
		account := statusAccount{
			AuthID:       auth.ID,
			Label:        firstNonEmpty(auth.Label, auth.Email, auth.Name, auth.ID),
			Color:        accountColor(auth.ID),
			ProbeEnabled: cfg.probeAuthEnabled(auth.ID),
		}
		for _, model := range models {
			key := makeCacheKey(auth.ID, model)
			entry, hasState := entries[key]
			stats := snap.Windows[key]
			modelCard := statusAccountModel{
				Model:               model,
				HasState:            hasState,
				Requests:            stats.Requests,
				Successes:           stats.Successes,
				Failures:            stats.Failures,
				ConsecutiveFailures: stats.ConsecutiveFailures,
				InputTokens:         stats.InputTokens,
				OutputTokens:        stats.OutputTokens,
				ReasoningTokens:     stats.ReasoningTokens,
				TotalTokens:         stats.TotalTokens,
			}
			if stats.TTFTSamples > 0 {
				modelCard.AvgTTFTSeconds = stats.TTFTTotal.Seconds() / float64(stats.TTFTSamples)
			}
			if hasState {
				modelCard.Length = entry.Length
				modelCard.Source = entry.Source
				modelCard.State = visibleState(entry.State, cfg.showStateValuesEnabled())
				modelCard.ExpiresAtUnix = entry.StoredAt.Add(snap.TTL).Unix()
				modelCard.RemainingTTLSeconds = durationSeconds(snap.TTL - snap.Now.Sub(entry.StoredAt))
			}
			account.Models = append(account.Models, modelCard)
		}
		accounts = append(accounts, account)
	}
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].AuthID < accounts[j].AuthID })
	return accounts
}

func renderStatusPage(view statusView, triggered bool) []byte {
	var out bytes.Buffer
	out.WriteString("<!DOCTYPE html><html lang=\"zh-CN\"><head><meta charset=\"utf-8\"><meta name=\"viewport\" content=\"width=device-width,initial-scale=1\"><title>Codex Turn State</title>")
	out.WriteString("<style>")
	out.WriteString(":root{--bg:#f6f7f9;--panel:#fff;--text:#111827;--muted:#4b5563;--line:#d7dbe1;--green:#0a7a40;--green-bg:#dff3e9;--red:#b42318;--red-bg:#fde2e2;--amber:#a36305;--amber-bg:#fef1d6;--blue:#2059d0;--blue-bg:#e6efff}")
	out.WriteString("*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--text);font:12.5px/1.5 -apple-system,BlinkMacSystemFont,\"Segoe UI\",\"PingFang SC\",\"Microsoft YaHei\",sans-serif}")
	out.WriteString("header{background:var(--panel);border-bottom:1px solid var(--line);padding:14px 20px;display:flex;align-items:center;justify-content:flex-start;gap:12px;flex-wrap:wrap}")
	out.WriteString("h1{font-size:17px;margin:0;display:flex;align-items:center;gap:8px}.dot{width:10px;height:10px;border-radius:50%;background:var(--green);display:inline-block}")
	out.WriteString("main{max-width:1280px;margin:0 auto;padding:16px 20px 36px}.section{margin:14px 0}.section-title{font-size:13px;font-weight:700;margin:0 0 8px;color:#323a46}")
	out.WriteString("button{background:var(--blue);color:#fff;border:0;border-radius:6px;padding:6px 12px;font-size:12.5px;cursor:pointer}button:hover{background:#1d4fd7}button:disabled{opacity:.55;cursor:wait}")
	out.WriteString("table{width:100%;border-collapse:collapse;background:var(--panel);border:1px solid var(--line)}th,td{border-bottom:1px solid var(--line);padding:6px 8px;text-align:left;vertical-align:top;word-break:break-word;color:#111827}th{background:#eef1f5;font-weight:700;font-size:12px;position:sticky;top:0;color:#111827}")
	out.WriteString("code,.mono{font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:11.5px}.pill{display:inline-flex;align-items:center;gap:4px;padding:1px 7px;border-radius:999px;font-size:11px;font-weight:700}")
	out.WriteString(".ok{color:var(--green);background:var(--green-bg)}.bad{color:var(--red);background:var(--red-bg)}.warn{color:var(--amber);background:var(--amber-bg)}.info{color:var(--blue);background:var(--blue-bg)}.muted{color:var(--muted)}")
	out.WriteString(".banner{border-left:4px solid var(--red);background:var(--red-bg);color:var(--red);padding:8px 12px;border-radius:4px;margin:12px 0}")
	out.WriteString(".toolbar{display:flex;align-items:center;gap:8px;margin:14px 0 2px}")
	out.WriteString(".proxy-editor{background:var(--panel);border:1px solid var(--line);border-radius:8px;padding:10px 12px;margin:8px 0}.proxy-editor summary{cursor:pointer;color:var(--blue);font-size:12.5px;font-weight:700}.proxy-editor textarea{display:block;width:100%;min-height:110px;margin:8px 0;padding:8px;font:12px/1.45 ui-monospace,SFMono-Regular,Menlo,monospace;border:1px solid var(--line);border-radius:6px;resize:vertical}.proxy-actions{display:flex;align-items:center;gap:8px;flex-wrap:wrap}.proxy-actions select{padding:5px 8px;border:1px solid var(--line);border-radius:6px}")
	out.WriteString(".chips{display:flex;flex-wrap:wrap;gap:6px;margin:10px 0}.chip{background:var(--panel);border:1px solid var(--line);border-radius:6px;padding:4px 8px;font-size:11.5px}")
	out.WriteString(".account-row{vertical-align:top}.account-name{font-weight:700;font-size:12.5px}.swatch{display:inline-block;width:10px;height:10px;border-radius:3px;margin-right:5px;background:var(--accent,#94a3b8)}")
	out.WriteString(".account-models{display:grid;grid-template-columns:repeat(auto-fit,minmax(260px,1fr));gap:6px}.account-model{background:#f8fafc;border:1px solid #edf0f3;border-radius:6px;padding:6px 7px}")
	out.WriteString(".model-block{padding:2px 0}.model-block:first-child{border-top:0}")
	out.WriteString(".model-row{display:flex;align-items:center;justify-content:space-between;gap:8px}.model-name{font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:11.5px;font-weight:700;color:#111827}")
	out.WriteString(".countdown{margin:4px 0 5px;color:#111827}.bar{height:5px;border-radius:999px;background:#e8ebef;overflow:hidden}.bar>i{display:block;height:100%;background:var(--green);transition:width 1s linear}")
	out.WriteString(".metrics{display:grid;grid-template-columns:repeat(3,1fr);gap:5px}.metric{background:#f8fafc;border:1px solid #edf0f3;border-radius:5px;padding:5px 7px}.metric-label{display:block;color:#4b5563;font-size:10.5px}.metric b{font-size:12.5px;font-weight:700;color:#111827}")
	out.WriteString("details.state{margin-top:7px}summary{cursor:pointer;color:var(--blue);font-size:11.5px}details.state code{display:block;max-height:150px;overflow:auto;white-space:pre-wrap;margin-top:5px}")
	out.WriteString("tr.match{background:var(--green-bg)}tr.mismatch{background:var(--red-bg)}.match-cell{color:var(--green);font-weight:700}.mismatch-cell{color:var(--red);font-weight:700}")
	out.WriteString("@media(max-width:640px){.grid{grid-template-columns:1fr}main,header{padding-left:10px;padding-right:10px}}")
	out.WriteString("</style></head><body>")

	out.WriteString("<header><h1><span class=\"dot\"></span>Codex Turn State</h1></header>")
	out.WriteString("<main>")
	out.WriteString("<div class=\"toolbar\"><button type=\"button\" id=\"probe-now\">立即探测</button><span id=\"probe-status\" class=\"muted\" aria-live=\"polite\"></span></div>")
	out.WriteString("<details class=\"proxy-editor\"><summary>代理池粘贴</summary><textarea id=\"proxy-lines\" rows=\"6\" spellcheck=\"false\" placeholder=\"每行一个：host:port:user:password\"></textarea><div class=\"proxy-actions\"><label>默认协议 <select id=\"proxy-scheme\">")
	out.WriteString("<option value=\"socks5\">socks5</option><option value=\"http\">http</option><option value=\"https\">https</option><option value=\"socks5h\">socks5h</option>")
	out.WriteString("</select></label><button type=\"button\" id=\"save-proxies\">保存代理池</button><span id=\"proxy-status\" class=\"muted\" aria-live=\"polite\"></span></div></details>")
	if triggered {
		out.WriteString("<div class=\"banner\" style=\"border-left-color:var(--green);background:var(--green-bg);color:var(--green)\">已触发一轮探测。</div>")
	}
	if view.GlobalError != "" {
		out.WriteString("<div class=\"banner\">")
		out.WriteString(html.EscapeString(view.GlobalError))
		out.WriteString("</div>")
	}

	out.WriteString("<div class=\"chips\">")
	out.WriteString(chipHTML("目标长度", fmt.Sprintf("%d", view.TargetStateLength)))
	out.WriteString(chipHTML("TTL", formatDuration(time.Duration(view.TTLSeconds)*time.Second)))
	out.WriteString(chipHTML("探测间隔", formatDuration(time.Duration(view.IntervalSeconds)*time.Second)))
	out.WriteString(chipHTML("失败重探阈值", fmt.Sprintf("%d", view.FailureReprobeThreshold)))
	out.WriteString(chipHTML("每轮尝试", fmt.Sprintf("%d", view.MaxProbeAttempts)))
	out.WriteString(chipHTML("代理数量", fmt.Sprintf("%d", len(view.Proxies))))
	out.WriteString(chipHTML("注入", boolLabel(view.Inject)))
	out.WriteString(chipHTML("采集", boolLabel(view.Harvest)))
	out.WriteString(chipHTML("探测", boolLabel(view.Probe)))
	out.WriteString(chipHTML("直连基线", boolLabel(view.DirectProbe)))
	out.WriteString(chipHTML("显示 state", boolLabel(view.ShowStateValues)))
	out.WriteString("</div>")
	if !view.ShowStateValues {
		out.WriteString("<p class=\"muted\">完整 state 默认隐藏；如需查看，请开启配置项 <code>show_state_values</code>。</p>")
	}

	out.WriteString("<div class=\"section\"><div class=\"section-title\"><span>账号状态</span><span style=\"margin-left:10px;font-weight:400\"><label style=\"margin-right:8px\"><input type=\"checkbox\" id=\"probe-select-all\" checked> 全选</label><button type=\"button\" id=\"save-probe-accounts\">保存探测账号</button><span id=\"probe-account-status\" class=\"muted\" style=\"margin-left:8px\"></span></span></div>")
	if len(view.Accounts) == 0 {
		out.WriteString("<p class=\"muted\">没有匹配的 Codex 账号。</p>")
	} else {
		out.WriteString("<table class=\"account-table\"><thead><tr><th style=\"width:1%\">探测</th><th>账号</th><th>模型状态</th><th>请求</th><th>成功</th><th>失败</th><th>Token</th><th>平均首字</th></tr></thead><tbody>")
		for _, account := range view.Accounts {
			writeAccountRow(&out, account, view.ShowStateValues)
		}
		out.WriteString("</tbody></table>")
	}
	out.WriteString("</div>")

	out.WriteString("<div class=\"section\"><div class=\"section-title\">最近探测</div>")
	if len(view.Probes) == 0 {
		out.WriteString("<p class=\"muted\">暂无探测记录。</p>")
	} else {
		out.WriteString("<table><thead><tr><th>账号</th><th>模型</th><th>长度</th><th>结果</th><th>来源</th><th>错误</th></tr></thead><tbody>")
		for _, record := range view.Probes {
			class := ""
			if record.Accepted {
				class = "match"
			} else if record.LastError != "" {
				class = "mismatch"
			}
			out.WriteString("<tr class=\"" + class + "\">")
			writeCell(&out, record.AuthID)
			writeCell(&out, record.Model)
			writeCell(&out, fmt.Sprintf("%d", record.LastLength))
			if record.Accepted {
				out.WriteString("<td class=\"match-cell\">命中 292</td>")
			} else if record.LastError != "" {
				out.WriteString("<td class=\"mismatch-cell\">未命中</td>")
			} else {
				out.WriteString("<td class=\"muted\">—</td>")
			}
			writeCell(&out, record.Source)
			writeCell(&out, record.LastError)
			out.WriteString("</tr>")
		}
		out.WriteString("</tbody></table>")
	}
	out.WriteString("</div>")

	out.WriteString("<div class=\"section\"><div class=\"section-title\">探测日志</div>")
	out.WriteString("<p class=\"muted\">绿色代表命中目标长度，红色代表未命中或失败；<code>direct</code> 为无代理基线，<code>proxy</code> 为实际代理轮询记录。</p>")
	if len(view.ProbeLogs) == 0 {
		out.WriteString("<p class=\"muted\">暂无日志。</p>")
	} else {
		out.WriteString("<table><thead><tr><th>时间</th><th>账号</th><th>模型</th><th>路由</th><th>代理</th><th>尝试</th><th>长度</th><th>匹配</th><th>缓存</th><th>State</th><th>错误</th></tr></thead><tbody>")
		for _, entry := range view.ProbeLogs {
			class := "mismatch"
			if entry.TargetMatch {
				class = "match"
			}
			out.WriteString("<tr class=\"" + class + "\">")
			writeCell(&out, entry.Time)
			writeCell(&out, entry.AuthID)
			writeCell(&out, entry.Model)
			writeCell(&out, entry.Route)
			writeCell(&out, entry.Proxy)
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
	out.WriteString("</div>")

	out.WriteString("<div class=\"section\"><div class=\"section-title\">Codex 凭据</div>")
	if len(view.Auths) == 0 {
		out.WriteString("<p class=\"muted\">未发现可见的 Codex 凭据。</p>")
	} else {
		out.WriteString("<table><thead><tr><th>ID</th><th>名称</th><th>标签</th><th>邮箱</th><th>禁用</th><th>不可用</th></tr></thead><tbody>")
		for _, auth := range view.Auths {
			out.WriteString("<tr>")
			writeCell(&out, auth.ID)
			writeCell(&out, auth.Name)
			writeCell(&out, auth.Label)
			writeCell(&out, auth.Email)
			writeCell(&out, boolLabel(auth.Disabled))
			writeCell(&out, boolLabel(auth.Unavailable))
			out.WriteString("</tr>")
		}
		out.WriteString("</tbody></table>")
	}
	out.WriteString("</div>")
	out.WriteString("<p class=\"muted\">JSON: <code>?format=json</code></p>")
	out.WriteString("<script>")
	out.WriteString("var probeBtn=document.getElementById('probe-now'),probeStatus=document.getElementById('probe-status');if(probeBtn){probeBtn.addEventListener('click',function(){probeBtn.disabled=true;probeStatus.textContent='正在触发探测...';fetch(location.pathname+'?op=probe',{method:'GET'}).then(function(){probeStatus.textContent='已触发一轮探测，结果稍后刷新可见。';setTimeout(function(){probeBtn.disabled=false;},600);}).catch(function(){probeStatus.textContent='触发失败，请重试。';probeBtn.disabled=false;});});}")
	out.WriteString("var saveBtn=document.getElementById('save-proxies'),proxyLines=document.getElementById('proxy-lines'),proxyScheme=document.getElementById('proxy-scheme'),proxyStatus=document.getElementById('proxy-status');if(saveBtn){saveBtn.addEventListener('click',function(){saveBtn.disabled=true;proxyStatus.textContent='正在保存...';fetch(location.pathname+'?op=save_proxies&scheme='+encodeURIComponent(proxyScheme.value),{method:'POST',headers:{'Content-Type':'application/x-www-form-urlencoded'},body:'proxy_lines='+encodeURIComponent(proxyLines.value)}).then(function(r){return r.json().catch(function(){return{};});}).then(function(d){if(d&&d.ok){proxyStatus.textContent='已保存，开始探测。';}else{proxyStatus.textContent='保存失败，请检查格式。';}}).catch(function(){proxyStatus.textContent='保存失败，请重试。';}).finally(function(){saveBtn.disabled=false;});});}")
	out.WriteString("var accountSaveBtn=document.getElementById('save-probe-accounts'),selectAll=document.getElementById('probe-select-all'),accountStatus=document.getElementById('probe-account-status');if(selectAll){selectAll.addEventListener('change',function(){document.querySelectorAll('.probe-account').forEach(function(el){el.checked=selectAll.checked;});});}if(accountSaveBtn){accountSaveBtn.addEventListener('click',function(){var ids=Array.from(document.querySelectorAll('.probe-account:checked')).map(function(el){return el.dataset.auth;});accountSaveBtn.disabled=true;if(accountStatus)accountStatus.textContent='正在保存...';fetch(location.pathname+'?op=save_probe_accounts',{method:'POST',headers:{'Content-Type':'application/x-www-form-urlencoded'},body:'auth_ids='+encodeURIComponent(ids.join(','))}).then(function(r){return r.json().catch(function(){return{};});}).then(function(d){if(accountStatus)accountStatus.textContent=d&&d.ok?'已保存，开始探测。':'保存失败。';}).catch(function(){if(accountStatus)accountStatus.textContent='保存失败，请重试。';}).finally(function(){accountSaveBtn.disabled=false;});});}")
	out.WriteString("function fmt(left){if(left<=0)return '已过期';var h=Math.floor(left/3600),m=Math.floor((left%3600)/60),s=left%60;return (h>0?h+':':'')+String(m).padStart(2,'0')+':'+String(s).padStart(2,'0');}")
	out.WriteString("function tick(){var now=Date.now();document.querySelectorAll('[data-countdown]').forEach(function(el){var ttl=Number(el.dataset.ttlSeconds||0);var left=Math.max(0,Math.ceil((Number(el.dataset.expiresAt)-now)/1000));el.textContent='倒计时 '+fmt(left);var bar=el.nextElementSibling.querySelector('i');if(bar){bar.style.width=(ttl>0?Math.min(100,left/ttl*100):0)+'%';}});}tick();setInterval(tick,1000);")
	out.WriteString("</script>")
	out.WriteString("</main></body></html>")
	return out.Bytes()
}

func writeModelBlock(out *bytes.Buffer, model statusAccountModel, showState bool) {
	out.WriteString("<div class=\"model-block\"><div class=\"model-row\"><span class=\"model-name\">")
	out.WriteString(html.EscapeString(model.Model))
	out.WriteString("</span>")
	if model.HasState {
		out.WriteString("<span class=\"pill ok\">292</span>")
	} else {
		out.WriteString("<span class=\"pill bad\">无 state</span>")
	}
	out.WriteString("</div>")
	if model.HasState {
		out.WriteString("<div class=\"countdown\" data-countdown data-expires-at=\"")
		out.WriteString(fmt.Sprintf("%d", model.ExpiresAtUnix))
		out.WriteString("\" data-ttl-seconds=\"")
		out.WriteString(fmt.Sprintf("%d", model.RemainingTTLSeconds))
		out.WriteString("\"></div><div class=\"bar\"><i></i></div>")
	} else {
		out.WriteString("<div class=\"countdown muted\">倒计时 —</div>")
	}
	out.WriteString("<div class=\"metrics\">")
	out.WriteString(metricHTML("请求", fmt.Sprintf("%d", model.Requests)))
	out.WriteString(metricHTML("成功", fmt.Sprintf("%d", model.Successes)))
	out.WriteString(metricHTML("失败", fmt.Sprintf("%d", model.Failures)))
	out.WriteString(metricHTML("Token", fmt.Sprintf("%d", model.TotalTokens)))
	out.WriteString(metricHTML("平均首字", avgTTFTText(model.AvgTTFTSeconds)))
	out.WriteString(metricHTML("连续失败", fmt.Sprintf("%d", model.ConsecutiveFailures)))
	out.WriteString("</div>")
	if showState && model.HasState && model.State != "" {
		out.WriteString("<details class=\"state\"><summary>查看 state</summary><code>")
		out.WriteString(html.EscapeString(model.State))
		out.WriteString("</code></details>")
	}
	out.WriteString("</div>")
}

func writeAccountRow(out *bytes.Buffer, account statusAccount, showState bool) {
	var requests, successes, failures, tokens int64
	ttftTotal := 0.0
	ttftSamples := 0
	out.WriteString("<tr class=\"account-row\">")
	out.WriteString("<td><input type=\"checkbox\" class=\"probe-account\" data-auth=\"")
	out.WriteString(html.EscapeString(account.AuthID))
	out.WriteString("\"")
	if account.ProbeEnabled {
		out.WriteString(" checked")
	}
	out.WriteString("></td>")
	out.WriteString("<td><span class=\"swatch\" style=\"--accent:")
	out.WriteString(account.Color)
	out.WriteString("\"></span><span class=\"account-name\">")
	out.WriteString(html.EscapeString(account.Label))
	out.WriteString("</span><div class=\"muted\" style=\"font-size:10.5px\">")
	out.WriteString(html.EscapeString(account.AuthID))
	out.WriteString("</div></td>")
	out.WriteString("<td><div class=\"account-models\">")
	for _, model := range account.Models {
		requests += model.Requests
		successes += model.Successes
		failures += model.Failures
		tokens += model.TotalTokens
		if model.AvgTTFTSeconds > 0 {
			ttftTotal += model.AvgTTFTSeconds
			ttftSamples++
		}
		writeCompactModelBlock(out, model, showState)
	}
	out.WriteString("</div></td>")
	out.WriteString("<td>")
	out.WriteString(fmt.Sprintf("%d", requests))
	out.WriteString("</td><td>")
	out.WriteString(fmt.Sprintf("%d", successes))
	out.WriteString("</td><td>")
	out.WriteString(fmt.Sprintf("%d", failures))
	out.WriteString("</td><td>")
	out.WriteString(fmt.Sprintf("%d", tokens))
	out.WriteString("</td><td>")
	if ttftSamples == 0 {
		out.WriteString("—")
	} else {
		out.WriteString(avgTTFTText(ttftTotal / float64(ttftSamples)))
	}
	out.WriteString("</td></tr>")
}

func writeCompactModelBlock(out *bytes.Buffer, model statusAccountModel, showState bool) {
	out.WriteString("<div class=\"account-model\"><div class=\"model-row\"><span class=\"model-name\">")
	out.WriteString(html.EscapeString(model.Model))
	out.WriteString("</span>")
	if model.HasState {
		out.WriteString("<span class=\"pill ok\">292</span>")
	} else {
		out.WriteString("<span class=\"pill bad\">无 state</span>")
	}
	out.WriteString("</div>")
	if model.HasState {
		out.WriteString("<div class=\"countdown\" data-countdown data-expires-at=\"")
		out.WriteString(fmt.Sprintf("%d", model.ExpiresAtUnix))
		out.WriteString("\" data-ttl-seconds=\"")
		out.WriteString(fmt.Sprintf("%d", model.RemainingTTLSeconds))
		out.WriteString("\"></div><div class=\"bar\"><i></i></div>")
	} else {
		out.WriteString("<div class=\"countdown muted\">倒计时 —</div>")
	}
	out.WriteString("<div class=\"metrics\">")
	out.WriteString(metricHTML("请求", fmt.Sprintf("%d", model.Requests)))
	out.WriteString(metricHTML("成功", fmt.Sprintf("%d", model.Successes)))
	out.WriteString(metricHTML("失败", fmt.Sprintf("%d", model.Failures)))
	out.WriteString(metricHTML("Token", fmt.Sprintf("%d", model.TotalTokens)))
	out.WriteString(metricHTML("平均首字", avgTTFTText(model.AvgTTFTSeconds)))
	out.WriteString(metricHTML("连续失败", fmt.Sprintf("%d", model.ConsecutiveFailures)))
	out.WriteString("</div>")
	if showState && model.HasState && model.State != "" {
		out.WriteString("<details class=\"state\"><summary>查看 state</summary><code>")
		out.WriteString(html.EscapeString(model.State))
		out.WriteString("</code></details>")
	}
	out.WriteString("</div>")
}

func chipHTML(label, value string) string {
	return "<span class=\"chip\">" + html.EscapeString(label) + ": <b>" + html.EscapeString(value) + "</b></span>"
}

func metricHTML(label, value string) string {
	return "<div class=\"metric\"><span class=\"metric-label\">" + html.EscapeString(label) + "</span><b>" + html.EscapeString(value) + "</b></div>"
}

func boolLabel(value bool) string {
	if value {
		return "开启"
	}
	return "关闭"
}

func avgTTFTText(seconds float64) string {
	if seconds <= 0 {
		return "—"
	}
	return fmt.Sprintf("%.2fs", seconds)
}

func accountColor(authID string) string {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(authID))
	hue := hash.Sum32() % 360
	return fmt.Sprintf("hsl(%d 65%% 45%%)", hue)
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

func redactedProxyList(cfg pluginConfig) []string {
	proxies, errParse := parseProxyURLsWithScheme(strings.Join(cfg.proxyLines(), "\n"), cfg.proxyScheme())
	if errParse != nil {
		return []string{"(invalid)"}
	}
	out := make([]string, 0, len(proxies))
	for _, proxyURL := range proxies {
		out = append(out, redactProxyURL(proxyURL))
	}
	return out
}
