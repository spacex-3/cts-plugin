package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
)

func parseProxyURLs(raw string) ([]string, error) {
	return parseProxyURLsWithScheme(raw, "http")
}

func parseProxyURLsWithScheme(raw, defaultScheme string) ([]string, error) {
	lines := expandProxyLines(raw)
	proxies := make([]string, 0, len(lines))
	for lineNumber, line := range lines {
		proxyURL, errParse := parseProxyURLWithScheme(line, defaultScheme)
		if errParse != nil {
			return nil, fmt.Errorf("proxy line %d: %w", lineNumber+1, errParse)
		}
		if proxyURL != "" {
			proxies = append(proxies, proxyURL)
		}
	}
	return proxies, nil
}

// parseProxyURLsTolerant keeps every entry it can parse and reports the ones it
// had to skip, so a single typo cannot take the whole probe round down.
func parseProxyURLsTolerant(raw, defaultScheme string) ([]string, []string) {
	lines := expandProxyLines(raw)
	proxies := make([]string, 0, len(lines))
	var issues []string
	for lineNumber, line := range lines {
		proxyURL, errParse := parseProxyURLWithScheme(line, defaultScheme)
		if errParse != nil {
			issues = append(issues, fmt.Sprintf("line %d: %v", lineNumber+1, errParse))
			continue
		}
		if proxyURL != "" {
			proxies = append(proxies, proxyURL)
		}
	}
	return proxies, issues
}

// expandProxyLines splits a raw proxy blob into one entry per line. A JSON array
// (the shape the config editor uses for the proxies field) is accepted both as the
// whole input and as a single line, so pasting the array form keeps working.
func expandProxyLines(raw string) []string {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	if items, ok := decodeProxyArray(raw); ok {
		return items
	}
	lines := strings.Split(raw, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if items, ok := decodeProxyArray(trimmed); ok {
			out = append(out, items...)
			continue
		}
		out = append(out, trimmed)
	}
	return out
}

func decodeProxyArray(raw string) ([]string, bool) {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) < 2 || trimmed[0] != '[' || trimmed[len(trimmed)-1] != ']' {
		return nil, false
	}
	var items []string
	if errUnmarshal := json.Unmarshal([]byte(trimmed), &items); errUnmarshal != nil {
		return nil, false
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if entry := strings.TrimSpace(item); entry != "" {
			out = append(out, entry)
		}
	}
	return out, true
}

func parseProxyURL(raw string) (string, error) {
	return parseProxyURLWithScheme(raw, "http")
}

func parseProxyURLWithScheme(raw, defaultScheme string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	if scheme, rest, ok := strings.Cut(trimmed, "://"); ok {
		// Some proxy providers document scheme://host:port:user:password even
		// though standard proxy URLs use scheme://user:password@host:port.
		if !strings.Contains(rest, "@") {
			if shorthand, errShorthand := buildProxyURL(scheme, rest); errShorthand == nil {
				return shorthand, nil
			}
		}
		setting, errParse := proxyutil.Parse(trimmed)
		if errParse != nil {
			return "", fmt.Errorf("proxy must use scheme://user:password@host:port or scheme://host:port:user:password: %w", errParse)
		}
		if setting.Mode == proxyutil.ModeDirect {
			return "direct", nil
		}
		if setting.Mode != proxyutil.ModeProxy || setting.URL == nil {
			return "", fmt.Errorf("unsupported proxy value %q", trimmed)
		}
		return setting.URL.String(), nil
	}
	return buildProxyURL(strings.ToLower(strings.TrimSpace(defaultScheme)), trimmed)
}

func buildProxyURL(scheme, raw string) (string, error) {
	host, port, user, password, errSplit := splitHostPortUserPassword(raw)
	if errSplit != nil {
		return "", errSplit
	}
	parsed := &url.URL{
		Scheme: strings.ToLower(strings.TrimSpace(scheme)),
		Host:   net.JoinHostPort(host, port),
	}
	switch {
	case password != "":
		parsed.User = url.UserPassword(user, password)
	case user != "":
		parsed.User = url.User(user)
	}
	setting, errParse := proxyutil.Parse(parsed.String())
	if errParse != nil {
		return "", errParse
	}
	if setting.Mode != proxyutil.ModeProxy || setting.URL == nil {
		return "", fmt.Errorf("unsupported proxy value %q", parsed.String())
	}
	return setting.URL.String(), nil
}

func splitHostPortUserPassword(raw string) (host, port, user, password string, err error) {
	trimmed := strings.TrimSpace(raw)
	if strings.HasPrefix(trimmed, "[") {
		end := strings.Index(trimmed, "]")
		if end <= 1 {
			return "", "", "", "", fmt.Errorf("invalid IPv6 proxy address %q", raw)
		}
		host = trimmed[1:end]
		if end+1 >= len(trimmed) || trimmed[end+1] != ':' {
			return "", "", "", "", fmt.Errorf("proxy must be [host]:port:user:password")
		}
		rest := trimmed[end+2:]
		parts := strings.Split(rest, ":")
		if len(parts) < 1 {
			return "", "", "", "", fmt.Errorf("proxy must be [host]:port:user:password")
		}
		port = strings.TrimSpace(parts[0])
		if len(parts) >= 2 {
			user = parts[1]
		}
		if len(parts) >= 3 {
			password = strings.Join(parts[2:], ":")
		}
		if host == "" || port == "" {
			return "", "", "", "", fmt.Errorf("proxy must be host:port:user:password")
		}
		return host, port, user, password, nil
	}

	// host:port, host:port:user and host:port:user:password (password may contain
	// colons) are all accepted; credentials stay optional.
	parts := strings.SplitN(trimmed, ":", 4)
	if len(parts) < 2 {
		return "", "", "", "", fmt.Errorf("proxy must be host:port:user:password or a proxy URL")
	}
	host = strings.TrimSpace(parts[0])
	port = strings.TrimSpace(parts[1])
	if len(parts) >= 3 {
		user = parts[2]
	}
	if len(parts) >= 4 {
		password = parts[3]
	}
	if host == "" || port == "" {
		return "", "", "", "", fmt.Errorf("proxy must be host:port:user:password")
	}
	return host, port, user, password, nil
}

var proxyCredentialsPattern = regexp.MustCompile(`(?i)([a-z][a-z0-9+.\-]*://)[^/@\s]+@`)

// sanitizeErrorText keeps proxy dial/TLS failures useful in logs without copying
// proxy credentials or unbounded upstream bodies into them.
func sanitizeErrorText(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	text = proxyCredentialsPattern.ReplaceAllString(text, "$1[redacted]@")
	return truncate(text, 400)
}

func redactProxyURL(proxyURL string) string {
	setting, errParse := proxyutil.Parse(strings.TrimSpace(proxyURL))
	if errParse != nil || setting.URL == nil {
		return "(invalid)"
	}
	return proxyutil.Redact(setting.URL.String())
}

func redactProxyUser(proxyURL string) string {
	setting, errParse := proxyutil.Parse(strings.TrimSpace(proxyURL))
	if errParse != nil || setting.URL == nil {
		return "(invalid)"
	}
	username := setting.URL.User.Username()
	if username == "" {
		return proxyutil.Redact(setting.URL.String())
	}
	return setting.URL.Scheme + "://" + username + "@[redacted]"
}
