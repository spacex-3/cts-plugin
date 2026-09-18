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

func TestParseProxyURLRejectsIncompleteValue(t *testing.T) {
	if _, errParse := parseProxyURL("proxy.example:8080"); errParse == nil {
		t.Fatal("expected incomplete proxy value to fail")
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

func TestParseProxyURLsReportsLineNumber(t *testing.T) {
	_, errParse := parseProxyURLs("proxy-one.example:8080:user:pass\ninvalid")
	if errParse == nil || !strings.Contains(errParse.Error(), "line 2") {
		t.Fatalf("error = %v, want line 2", errParse)
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
