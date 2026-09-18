package main

import (
	"strings"
	"time"
)

const (
	pluginName          = "codex-turn-state"
	resourcePath        = "/status"
	resourceContentType = "text/html; charset=utf-8"

	defaultIntervalSeconds         = 300
	defaultTargetStateLength       = 292
	defaultTTLSeconds              = 3600
	defaultMaxProbeAttempts        = 3
	defaultMaxOutputTokens         = 16
	defaultProbeLogLimit           = 200
	maxProbeLogLimit               = 1000
	defaultFailureReprobeThreshold = 3
	defaultProbePrompt             = "."

	codexUserAgent  = "codex-tui/0.154.0 (Mac OS 26.5.2; arm64) iTerm.app/3.6.11 (codex-tui; 0.154.0)"
	codexOriginator = "codex-tui"
	codexDefaultURL = "https://chatgpt.com/backend-api/codex"

	turnStateHeader = "X-Codex-Turn-State"
)

var (
	pluginVersion      = "0.3.4"
	defaultProbeModels = []string{"gpt-5.6-sol", "gpt-6-astra"}
)

type pluginConfig struct {
	Proxy                   string   `yaml:"proxy"`
	Proxies                 []string `yaml:"proxies"`
	ProxyScheme             string   `yaml:"proxy_scheme"`
	AuthIDs                 []string `yaml:"auth_ids"`
	ProbeAuthIDs            []string `yaml:"probe_auth_ids"`
	Models                  []string `yaml:"models"`
	IntervalSeconds         int      `yaml:"interval_seconds"`
	TargetStateLength       int      `yaml:"target_state_length"`
	TTLSeconds              int      `yaml:"ttl_seconds"`
	Inject                  *bool    `yaml:"inject"`
	Harvest                 *bool    `yaml:"harvest"`
	Probe                   *bool    `yaml:"probe"`
	DirectProbe             *bool    `yaml:"direct_probe"`
	ShowStateValues         *bool    `yaml:"show_state_values"`
	ProbeLogLimit           int      `yaml:"probe_log_limit"`
	MaxProbeAttempts        int      `yaml:"max_probe_attempts"`
	FailureReprobeThreshold int      `yaml:"failure_reprobe_threshold"`
	MaxOutputTokens         int      `yaml:"max_output_tokens"`
	Prompt                  string   `yaml:"prompt"`
}

func (c pluginConfig) injectEnabled() bool {
	return c.Inject == nil || *c.Inject
}

func (c pluginConfig) harvestEnabled() bool {
	return c.Harvest == nil || *c.Harvest
}

func (c pluginConfig) probeEnabled() bool {
	return c.Probe == nil || *c.Probe
}

func (c pluginConfig) directProbeEnabled() bool {
	return c.DirectProbe != nil && *c.DirectProbe
}

func (c pluginConfig) showStateValuesEnabled() bool {
	return c.ShowStateValues != nil && *c.ShowStateValues
}

func (c pluginConfig) probeLogLimit() int {
	if c.ProbeLogLimit <= 0 {
		return defaultProbeLogLimit
	}
	if c.ProbeLogLimit > maxProbeLogLimit {
		return maxProbeLogLimit
	}
	return c.ProbeLogLimit
}

func (c pluginConfig) interval() time.Duration {
	if c.IntervalSeconds <= 0 {
		return time.Duration(defaultIntervalSeconds) * time.Second
	}
	return time.Duration(c.IntervalSeconds) * time.Second
}

func (c pluginConfig) ttl() time.Duration {
	if c.TTLSeconds <= 0 {
		return time.Duration(defaultTTLSeconds) * time.Second
	}
	return time.Duration(c.TTLSeconds) * time.Second
}

func (c pluginConfig) targetLength() int {
	if c.TargetStateLength <= 0 {
		return defaultTargetStateLength
	}
	return c.TargetStateLength
}

func (c pluginConfig) maxAttempts() int {
	if c.MaxProbeAttempts <= 0 {
		return defaultMaxProbeAttempts
	}
	return c.MaxProbeAttempts
}

func (c pluginConfig) proxyScheme() string {
	scheme := strings.ToLower(strings.TrimSpace(c.ProxyScheme))
	switch scheme {
	case "http", "https", "socks5", "socks5h":
		return scheme
	default:
		return "http"
	}
}

func (c pluginConfig) proxyLines() []string {
	lines := make([]string, 0, len(c.Proxies)+1)
	if strings.TrimSpace(c.Proxy) != "" {
		for _, line := range strings.Split(strings.ReplaceAll(c.Proxy, "\r\n", "\n"), "\n") {
			if strings.TrimSpace(line) != "" {
				lines = append(lines, strings.TrimSpace(line))
			}
		}
	}
	lines = append(lines, uniqueTrimmed(c.Proxies)...)
	return lines
}

func (c pluginConfig) proxyLinesRaw() string {
	return strings.Join(c.proxyLines(), "\n")
}

func (c pluginConfig) failureReprobeThreshold() int {
	if c.FailureReprobeThreshold < 0 {
		return 0
	}
	if c.FailureReprobeThreshold == 0 {
		return defaultFailureReprobeThreshold
	}
	return c.FailureReprobeThreshold
}

func (c pluginConfig) maxOutputTokens() int {
	if c.MaxOutputTokens <= 0 {
		return defaultMaxOutputTokens
	}
	return c.MaxOutputTokens
}

func (c pluginConfig) probePrompt() string {
	if strings.TrimSpace(c.Prompt) == "" {
		return defaultProbePrompt
	}
	return c.Prompt
}

func (c pluginConfig) models() []string {
	out := uniqueTrimmed(c.Models)
	if len(out) == 0 {
		return append([]string(nil), defaultProbeModels...)
	}
	return out
}

func (c pluginConfig) authIDs() []string {
	return uniqueTrimmed(c.AuthIDs)
}

func (c pluginConfig) probeAuthIDs() []string {
	return uniqueTrimmed(c.ProbeAuthIDs)
}

func (c pluginConfig) probeAuthEnabled(authID string) bool {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return false
	}
	probeAuthIDs := c.probeAuthIDs()
	if len(probeAuthIDs) == 0 {
		return true
	}
	return containsFold(probeAuthIDs, authID)
}

func (c pluginConfig) allows(authID, model string) bool {
	authID = strings.TrimSpace(authID)
	model = strings.TrimSpace(model)
	if authID == "" || model == "" {
		return false
	}
	authIDs := c.authIDs()
	if len(authIDs) > 0 && !containsFold(authIDs, authID) {
		return false
	}
	return containsFold(c.models(), model)
}

func clonePluginConfig(cfg pluginConfig) pluginConfig {
	cfg.AuthIDs = append([]string(nil), cfg.AuthIDs...)
	cfg.ProbeAuthIDs = append([]string(nil), cfg.ProbeAuthIDs...)
	cfg.Models = append([]string(nil), cfg.Models...)
	cfg.Proxies = append([]string(nil), cfg.Proxies...)
	return cfg
}

func containsFold(values []string, target string) bool {
	target = strings.TrimSpace(target)
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), target) {
			return true
		}
	}
	return false
}

func normalizeConfig(cfg pluginConfig) pluginConfig {
	cfg.Proxy = strings.TrimSpace(cfg.Proxy)
	cfg.Proxies = uniqueTrimmed(cfg.Proxies)
	cfg.ProxyScheme = strings.ToLower(strings.TrimSpace(cfg.ProxyScheme))
	cfg.AuthIDs = uniqueTrimmed(cfg.AuthIDs)
	cfg.ProbeAuthIDs = uniqueTrimmed(cfg.ProbeAuthIDs)
	cfg.Models = uniqueTrimmed(cfg.Models)
	if cfg.IntervalSeconds < 0 {
		cfg.IntervalSeconds = 0
	}
	if cfg.TargetStateLength < 0 {
		cfg.TargetStateLength = 0
	}
	if cfg.TTLSeconds < 0 {
		cfg.TTLSeconds = 0
	}
	if cfg.ProbeLogLimit < 0 {
		cfg.ProbeLogLimit = 0
	}
	if cfg.ProbeLogLimit > maxProbeLogLimit {
		cfg.ProbeLogLimit = maxProbeLogLimit
	}
	if cfg.MaxProbeAttempts < 0 {
		cfg.MaxProbeAttempts = 0
	}
	if cfg.FailureReprobeThreshold < 0 {
		cfg.FailureReprobeThreshold = -1
	}
	if cfg.MaxOutputTokens < 0 {
		cfg.MaxOutputTokens = 0
	}
	cfg.Prompt = strings.TrimSpace(cfg.Prompt)
	return cfg
}

func uniqueTrimmed(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	return out
}

func boolPtr(v bool) *bool {
	return &v
}

func formatDuration(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	d = d.Round(time.Second)
	return d.String()
}

func validateProxyConfig(cfg pluginConfig) error {
	_, errParse := parseProxyURLsWithScheme(strings.Join(cfg.proxyLines(), "\n"), cfg.proxyScheme())
	return errParse
}
