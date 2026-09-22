package main

import (
	"strconv"
	"strings"
	"time"
)

const (
	pluginName          = "codex-turn-state"
	resourcePath        = "/status"
	resourceContentType = "text/html; charset=utf-8"

	defaultIntervalSeconds         = 120
	defaultTargetStateLength       = 292
	defaultTTLSeconds              = 300
	defaultProbeLeadSeconds        = 180
	defaultCookieTTLSeconds        = 300
	defaultMaxProbeAttempts        = 3
	defaultAttemptsPerRoute        = 1
	maxAttemptsPerRoute            = 10
	defaultMaxOutputTokens         = 16
	defaultProbeLogLimit           = 200
	maxProbeLogLimit               = 1000
	defaultFailureReprobeThreshold = 3
	defaultProbePrompt             = "."
	defaultProbeSchedule           = "state_aware"

	// injectedRequestTTL bounds how long a production request is remembered so a
	// rejected upstream response can be tied back to the ticket we sent on it.
	injectedRequestTTL   = 20 * time.Minute
	injectedRequestLimit = 4096

	codexUserAgent  = "codex-tui/0.154.0 (Mac OS 26.5.2; arm64) iTerm.app/3.6.11 (codex-tui; 0.154.0)"
	codexOriginator = "codex-tui"
	codexDefaultURL = "https://chatgpt.com/backend-api/codex"

	turnStateHeader = "X-Codex-Turn-State"
)

var (
	pluginVersion      = "0.6.1"
	defaultProbeModels = []string{"gpt-5.6-sol", "gpt-6-astra"}

	// Fernet envelope block counts accepted as a full-strength turn state:
	// 10 blocks = 292 characters, 12 = 332 (Team/business). 11 blocks = 312 is
	// deliberately NOT accepted by default: it is what the plugin sees from
	// throttled or proxied exits, and mixing it into the cache would replace a
	// known-good 292 ticket. Add 11 to accepted_blocks only if 312 is confirmed
	// as a legitimate shape for your models.
	defaultAcceptedBlocks = []int{10, 12}
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
	AcceptedBlocks          []int    `yaml:"accepted_blocks"`
	TTLSeconds              int      `yaml:"ttl_seconds"`
	Inject                  *bool    `yaml:"inject"`
	Harvest                 *bool    `yaml:"harvest"`
	Probe                   *bool    `yaml:"probe"`
	DirectProbe             *bool    `yaml:"direct_probe"`
	InjectCookies           *bool    `yaml:"inject_cookies"`
	HarvestCookies          *bool    `yaml:"harvest_cookies"`
	ProbeSendCookies        *bool    `yaml:"probe_send_cookies"`
	CookieTTLSeconds        int      `yaml:"cookie_ttl_seconds"`
	InvalidateOnReject      *bool    `yaml:"invalidate_on_reject"`
	StateRefreshSeconds     int      `yaml:"state_refresh_seconds"`
	ShowAccountDetails      bool     `yaml:"show_account_details"`
	ShowInjectionHeaders    bool     `yaml:"show_injection_headers"`
	ShowStateValues         *bool    `yaml:"show_state_values"`
	ProbeSchedule           string   `yaml:"probe_schedule"`
	ProbeLeadSeconds        int      `yaml:"probe_lead_seconds"`
	ProbeWaitMilliseconds   int      `yaml:"probe_wait_milliseconds"`
	ProbeTimeoutSeconds     int      `yaml:"probe_timeout_seconds"`
	OnDemandCooldownSeconds int      `yaml:"on_demand_cooldown_seconds"`
	UseIssuedAt             bool     `yaml:"use_issued_at"`
	RequireCompleted        bool     `yaml:"require_completed"`
	ErrorAwareBackoff       bool     `yaml:"error_aware_backoff"`
	QuotaBackoffSeconds     int      `yaml:"quota_backoff_seconds"`
	RotateProxyStart        bool     `yaml:"rotate_proxy_start"`
	ProbeLogLimit           int      `yaml:"probe_log_limit"`
	MaxProbeAttempts        int      `yaml:"max_probe_attempts"`
	AttemptsPerRoute        int      `yaml:"attempts_per_route"`
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

// Direct probing is on by default: the direct egress is what production traffic
// uses when no host proxy is configured, and it is the egress that produced the
// accepted tickets in practice. Set direct_probe: false to keep probes on the
// proxy pool only.
func (c pluginConfig) directProbeEnabled() bool {
	return c.DirectProbe == nil || *c.DirectProbe
}

// Cookies carry the account-level routing credential the Codex backend hands out
// together with a turn state, so they are captured and injected by default.
func (c pluginConfig) injectCookiesEnabled() bool {
	return c.InjectCookies == nil || *c.InjectCookies
}

func (c pluginConfig) harvestCookiesEnabled() bool {
	return c.HarvestCookies == nil || *c.HarvestCookies
}

func (c pluginConfig) probeSendCookiesEnabled() bool {
	return c.ProbeSendCookies == nil || *c.ProbeSendCookies
}

func (c pluginConfig) invalidateOnRejectEnabled() bool {
	return c.InvalidateOnReject == nil || *c.InvalidateOnReject
}

func (c pluginConfig) cookieTTL() time.Duration {
	if c.CookieTTLSeconds <= 0 {
		return time.Duration(defaultCookieTTLSeconds) * time.Second
	}
	return time.Duration(c.CookieTTLSeconds) * time.Second
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

func defaultAcceptedBlocksList() []int {
	return append([]int(nil), defaultAcceptedBlocks...)
}

func (c pluginConfig) acceptedBlocks() []int {
	out := make([]int, 0, len(c.AcceptedBlocks))
	for _, block := range c.AcceptedBlocks {
		if block > 0 {
			out = append(out, block)
		}
	}
	if len(out) == 0 {
		return defaultAcceptedBlocksList()
	}
	return out
}

// blockStateLength is the base64 length a Fernet envelope with this many AES
// blocks ends up at: 57 bytes of version/timestamp/IV/HMAC plus 16 bytes per
// block, base64-encoded. 10 → 292, 11 → 312, 12 → 332.
func blockStateLength(blocks int) (int, bool) {
	if blocks <= 0 {
		return 0, false
	}
	raw := 57 + blocks*16
	return ((raw + 2) / 3) * 4, true
}

func stateBlocksLabel(blocks int) string {
	length, ok := blockStateLength(blocks)
	if !ok {
		return strconv.Itoa(blocks)
	}
	return strconv.Itoa(blocks) + "≈" + strconv.Itoa(length)
}

func describeAcceptedBlocks(blocks []int) string {
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		parts = append(parts, stateBlocksLabel(block))
	}
	return strings.Join(parts, " / ")
}

func (c pluginConfig) stateAccepted(state string) bool {
	blocks, okBlocks := parseStateBlocks(state)
	if okBlocks {
		return containsInt(c.acceptedBlocks(), blocks)
	}
	return c.targetLength() > 0 && len(strings.TrimSpace(state)) == c.targetLength()
}

func (c pluginConfig) maxAttempts() int {
	if c.MaxProbeAttempts <= 0 {
		return defaultMaxProbeAttempts
	}
	return c.MaxProbeAttempts
}

// attemptsPerRoute is how many tries a single egress (direct or one proxy) gets
// before the probe moves to the next one. The default of 1 keeps the historical
// behaviour of spending the whole max_probe_attempts budget on distinct egresses.
func (c pluginConfig) attemptsPerRoute() int {
	if c.AttemptsPerRoute <= 0 {
		return defaultAttemptsPerRoute
	}
	if c.AttemptsPerRoute > maxAttemptsPerRoute {
		return maxAttemptsPerRoute
	}
	return c.AttemptsPerRoute
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
	lines = append(lines, trimmedList(c.Proxies)...)
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

func (c pluginConfig) probeSchedule() string {
	switch strings.ToLower(strings.TrimSpace(c.ProbeSchedule)) {
	case "fixed":
		return "fixed"
	case "on_demand":
		return "on_demand"
	case "":
		return defaultProbeSchedule
	}
	return "state_aware"
}

// probeLead is how long before expiry a cached ticket starts counting as
// renewable. state_refresh_seconds takes precedence when set, so the refresh
// point can be expressed as "renew a ticket older than N seconds".
func (c pluginConfig) probeLead() time.Duration {
	if c.ProbeLeadSeconds > 0 {
		return time.Duration(c.ProbeLeadSeconds) * time.Second
	}
	if c.StateRefreshSeconds > 0 {
		lead := c.ttl() - time.Duration(c.StateRefreshSeconds)*time.Second
		if lead < 0 {
			return 0
		}
		return lead
	}
	return time.Duration(defaultProbeLeadSeconds) * time.Second
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
	cfg.AcceptedBlocks = append([]int(nil), cfg.AcceptedBlocks...)
	return cfg
}

func containsInt(values []int, target int) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
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
	cfg.Proxies = trimmedList(cfg.Proxies)
	cfg.ProxyScheme = strings.ToLower(strings.TrimSpace(cfg.ProxyScheme))
	cfg.ProbeSchedule = strings.ToLower(strings.TrimSpace(cfg.ProbeSchedule))
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
	if cfg.AttemptsPerRoute < 0 {
		cfg.AttemptsPerRoute = 0
	}
	if cfg.AttemptsPerRoute > maxAttemptsPerRoute {
		cfg.AttemptsPerRoute = maxAttemptsPerRoute
	}
	if cfg.ProbeLeadSeconds < 0 {
		cfg.ProbeLeadSeconds = 0
	}
	if cfg.CookieTTLSeconds < 0 {
		cfg.CookieTTLSeconds = 0
	}
	if cfg.StateRefreshSeconds < 0 {
		cfg.StateRefreshSeconds = 0
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

// trimmedList drops blank entries but keeps order and duplicates. A rotating
// proxy endpoint (same host, new exit IP per connection) may legitimately be
// listed several times to get several egress slots.
func trimmedList(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, raw := range values {
		if value := strings.TrimSpace(raw); value != "" {
			out = append(out, value)
		}
	}
	return out
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

// A negative wait means enqueue without waiting; zero selects the default.
func (c pluginConfig) probeWait() time.Duration {
	if c.ProbeWaitMilliseconds < 0 {
		return 0
	}
	if c.ProbeWaitMilliseconds == 0 {
		return 1500 * time.Millisecond
	}
	return time.Duration(c.ProbeWaitMilliseconds) * time.Millisecond
}

func (c pluginConfig) probeTimeout() time.Duration {
	if c.ProbeTimeoutSeconds <= 0 {
		return 60 * time.Second
	}
	return time.Duration(c.ProbeTimeoutSeconds) * time.Second
}

func (c pluginConfig) onDemandCooldown() time.Duration {
	if c.OnDemandCooldownSeconds <= 0 {
		return 600 * time.Second
	}
	return time.Duration(c.OnDemandCooldownSeconds) * time.Second
}

func (c pluginConfig) quotaBackoff() time.Duration {
	if c.QuotaBackoffSeconds <= 0 {
		return 900 * time.Second
	}
	return time.Duration(c.QuotaBackoffSeconds) * time.Second
}
