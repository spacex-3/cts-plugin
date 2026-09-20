package main

import (
	"strings"
	"testing"
)

func TestParseProxyURLHostPortCredentials(t *testing.T) {
	got, errParse := parseProxyURL("proxy.example:8443:user:pass:word")
	if errParse != nil {
		t.Fatal(errParse)
	}
	want := "http://user:pass%3Aword@proxy.example:8443"
	if got != want {
		t.Fatalf("proxy URL = %q, want %q", got, want)
	}
}

func TestParseProxyURLIPv6(t *testing.T) {
	got, errParse := parseProxyURL("[2001:db8::1]:8080:user:password")
	if errParse != nil {
		t.Fatal(errParse)
	}
	want := "http://user:password@[2001:db8::1]:8080"
	if got != want {
		t.Fatalf("proxy URL = %q, want %q", got, want)
	}
}

func TestParseProxyURLAcceptsCredentialFreeValue(t *testing.T) {
	for raw, want := range map[string]string{
		"proxy.example:8080":              "http://proxy.example:8080",
		"proxy.example:8080:user":         "http://user@proxy.example:8080",
		"proxy.example:8080:user:pw:more": "http://user:pw%3Amore@proxy.example:8080",
	} {
		got, errParse := parseProxyURL(raw)
		if errParse != nil {
			t.Fatalf("parse %q: %v", raw, errParse)
		}
		if got != want {
			t.Fatalf("parse %q = %q, want %q", raw, got, want)
		}
	}
}

func TestParseProxyURLRejectsIncompleteValue(t *testing.T) {
	for _, raw := range []string{"proxy.example", ":8080", "proxy.example:"} {
		if _, errParse := parseProxyURL(raw); errParse == nil {
			t.Fatalf("expected %q to fail", raw)
		}
	}
}

func TestParseProxyURLRejectsIPv6WithoutPortSeparator(t *testing.T) {
	if _, errParse := parseProxyURL("[2001:db8::1]8080:user:password"); errParse == nil {
		t.Fatal("expected malformed IPv6 proxy value to fail")
	}
}

func TestRedactedProxyHidesCredentials(t *testing.T) {
	got := redactedProxy("proxy.example:8443:sensitive-user:sensitive-password")
	if strings.Contains(got, "sensitive-user") || strings.Contains(got, "sensitive-password") {
		t.Fatalf("redacted proxy leaked credentials: %q", got)
	}
	if got != "http://redacted@proxy.example:8443" {
		t.Fatalf("redacted proxy = %q", got)
	}
}

func TestParseProxyURLSchemePrefixedShorthand(t *testing.T) {
	got, errParse := parseProxyURL("socks5://us.proxy.example:10000:USER-zone-custom-region-US:p*a:ss")
	if errParse != nil {
		t.Fatal(errParse)
	}
	want := "socks5://USER-zone-custom-region-US:p%2Aa%3Ass@us.proxy.example:10000"
	if got != want {
		t.Fatalf("proxy URL = %q, want %q", got, want)
	}
}

func TestParseProxyURLStandardSocks5URL(t *testing.T) {
	got, errParse := parseProxyURL("socks5://user:p%40ss@proxy.example:1080")
	if errParse != nil {
		t.Fatal(errParse)
	}
	want := "socks5://user:p%40ss@proxy.example:1080"
	if got != want {
		t.Fatalf("proxy URL = %q, want %q", got, want)
	}
}

func TestParseProxyURLSchemeShorthandEscapesProviderCredentials(t *testing.T) {
	got, errParse := parseProxyURL("socks5://us.proxy.example:10000:USE&#x52;-zone-custom-region-US:p*ss")
	if errParse != nil {
		t.Fatal(errParse)
	}
	if !strings.HasPrefix(got, "socks5://") || !strings.HasSuffix(got, "@us.proxy.example:10000") {
		t.Fatalf("unexpected proxy URL %q", got)
	}
	if strings.Contains(got, "&#") || strings.Contains(got, "p*ss") {
		t.Fatalf("proxy credentials were not URL encoded: %q", got)
	}
}

func TestParseProxyURLsSplitsLines(t *testing.T) {
	got, errParse := parseProxyURLs("proxy-one.example:8080:user:pass\r\n\n socks5://user:pass@proxy-two.example:1080 ")
	if errParse != nil {
		t.Fatal(errParse)
	}
	if len(got) != 2 {
		t.Fatalf("proxy count = %d, want 2", len(got))
	}
	if got[0] != "http://user:pass@proxy-one.example:8080" || got[1] != "socks5://user:pass@proxy-two.example:1080" {
		t.Fatalf("proxies = %#v", got)
	}
}

func TestParseProxyURLsAcceptsJSONArrayInput(t *testing.T) {
	jsonArray := `["us.rrp.example:10000:user-a:pw","us2.rrp.example:10000:user-b:pw"]`
	want := []string{
		"http://user-a:pw@us.rrp.example:10000",
		"http://user-b:pw@us2.rrp.example:10000",
	}
	for name, raw := range map[string]string{
		"whole input":  jsonArray,
		"single line":  jsonArray,
		"with newline": jsonArray + "\n",
	} {
		got, errParse := parseProxyURLsWithScheme(raw, "http")
		if errParse != nil {
			t.Fatalf("%s: %v", name, errParse)
		}
		if len(got) != len(want) {
			t.Fatalf("%s: got %v, want %v", name, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%s: got %v, want %v", name, got, want)
			}
		}
	}

	empty, errEmpty := parseProxyURLsWithScheme("[]", "http")
	if errEmpty != nil || len(empty) != 0 {
		t.Fatalf("empty array = %v err=%v", empty, errEmpty)
	}

	// A JSON array inside the legacy single-line string field used to fail
	// with "proxy must be [host]:port:user:password" because of the leading '['.
	lines := pluginConfig{Proxy: jsonArray}.proxyLines()
	if _, errLegacy := parseProxyURLsWithScheme(strings.Join(lines, "\n"), "http"); errLegacy != nil {
		t.Fatalf("legacy proxy field with a JSON array: %v", errLegacy)
	}
}

func TestParseProxyURLsTolerantSkipsBadLines(t *testing.T) {
	raw := strings.Join([]string{
		"us.rrp.example:10000:user:pw",
		"[{\"not\":\"a proxy\"}]",
		"us2.rrp.example:10000:user:pw",
	}, "\n")
	proxies, issues := parseProxyURLsTolerant(raw, "http")
	if len(proxies) != 2 {
		t.Fatalf("proxies = %v, want 2 entries", proxies)
	}
	if len(issues) != 1 || !strings.Contains(issues[0], "line 2") {
		t.Fatalf("issues = %v, want one entry for line 2", issues)
	}

	// The strict variant still refuses the whole blob.
	if _, errStrict := parseProxyURLsWithScheme(raw, "http"); errStrict == nil {
		t.Fatal("expected the strict parser to reject the blob")
	}
}

func TestSanitizeErrorTextHidesProxyCredentials(t *testing.T) {
	got := sanitizeErrorText("utls: dial upstream: proxyconnect tcp: dial http://user:secret@proxy.example:8080: i/o timeout")
	if strings.Contains(got, "secret") || strings.Contains(got, "user:") {
		t.Fatalf("credentials leaked: %q", got)
	}
	if !strings.Contains(got, "i/o timeout") {
		t.Fatalf("reason was dropped: %q", got)
	}
}

func TestParseProxyURLsReportsLineNumber(t *testing.T) {
	_, errParse := parseProxyURLs("proxy-one.example:8080:user:pass\ninvalid")
	if errParse == nil || !strings.Contains(errParse.Error(), "line 2") {
		t.Fatalf("error = %v, want line 2", errParse)
	}
}

func TestRedactProxyUserShowsUsernameButHidesHostAndPassword(t *testing.T) {
	got := redactProxyUser("socks5://USER-zone-custom-region-US:p%40ss@us.rrp.bestgo.work:10000")
	if got != "socks5://USER-zone-custom-region-US@[redacted]" {
		t.Fatalf("redacted proxy user = %q", got)
	}
	if strings.Contains(got, "p%40ss") || strings.Contains(got, "us.rrp.bestgo.work") {
		t.Fatalf("redacted proxy leaked password or host: %q", got)
	}
}

func TestParseProxyURLsUsesDefaultScheme(t *testing.T) {
	got, errParse := parseProxyURLsWithScheme("us.rrp.bestgo.work:10000:user:pw\nproxy-two.example:1080:user:pw", "socks5")
	if errParse != nil {
		t.Fatal(errParse)
	}
	if len(got) != 2 || !strings.HasPrefix(got[0], "socks5://") || !strings.HasPrefix(got[1], "socks5://") {
		t.Fatalf("proxies = %#v, want socks5 URLs", got)
	}
}

func TestProxyLinesMergesLegacyStringAndArray(t *testing.T) {
	cfg := normalizeConfig(pluginConfig{
		Proxy:       "proxy-one.example:8080:user:pass",
		Proxies:     []string{"proxy-two.example:1080:user:pass", "proxy-three.example:1090:user:pass"},
		ProxyScheme: "socks5",
	})
	lines := cfg.proxyLines()
	if len(lines) != 3 || lines[0] != "proxy-one.example:8080:user:pass" {
		t.Fatalf("proxy lines = %#v", lines)
	}
	if errProxy := validateProxyConfig(cfg); errProxy != nil {
		t.Fatal(errProxy)
	}
}

// A rotating residential pool hands out a new exit IP per connection, so the
// same endpoint listed twice means two egress slots, not a typo.
func TestProxyLinesKeepRepeatedEgresses(t *testing.T) {
	rotating := "us.rrp.example:10000:user-zone:pw"
	cfg := normalizeConfig(pluginConfig{Proxies: []string{rotating, rotating, "us.rrp.example:10000:user-other:pw"}})
	lines := cfg.proxyLines()
	if len(lines) != 3 {
		t.Fatalf("proxy lines = %#v, want 3 entries", lines)
	}
	proxies, issues := parseProxyURLsTolerant(strings.Join(lines, "\n"), cfg.proxyScheme())
	if len(issues) != 0 || len(proxies) != 3 {
		t.Fatalf("proxies = %#v issues = %#v, want 3 usable entries", proxies, issues)
	}
	if proxies[0] != proxies[1] {
		t.Fatalf("proxies = %#v, want the repeated endpoint kept verbatim", proxies)
	}
}
