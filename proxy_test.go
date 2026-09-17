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
