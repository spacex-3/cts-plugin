package main

import (
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
)

func parseProxyURLs(raw string) ([]string, error) {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	lines := strings.Split(raw, "\n")
	proxies := make([]string, 0, len(lines))
	for lineNumber, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		proxyURL, errParse := parseProxyURL(line)
		if errParse != nil {
			return nil, fmt.Errorf("proxy line %d: %w", lineNumber+1, errParse)
		}
		if proxyURL != "" {
			proxies = append(proxies, proxyURL)
		}
	}
	return proxies, nil
}

func parseProxyURL(raw string) (string, error) {
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
	return buildProxyURL("http", trimmed)
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
	if user != "" || password != "" {
		parsed.User = url.UserPassword(user, password)
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
		if len(parts) < 3 {
			return "", "", "", "", fmt.Errorf("proxy must be host:port:user:password")
		}
		port = strings.TrimSpace(parts[0])
		user = parts[1]
		password = strings.Join(parts[2:], ":")
		if host == "" || port == "" {
			return "", "", "", "", fmt.Errorf("proxy must be host:port:user:password")
		}
		return host, port, user, password, nil
	}

	parts := strings.SplitN(trimmed, ":", 4)
	if len(parts) != 4 {
		return "", "", "", "", fmt.Errorf("proxy must be host:port:user:password or a proxy URL")
	}
	host = strings.TrimSpace(parts[0])
	port = strings.TrimSpace(parts[1])
	user = parts[2]
	password = parts[3]
	if host == "" || port == "" {
		return "", "", "", "", fmt.Errorf("proxy must be host:port:user:password")
	}
	return host, port, user, password, nil
}

func redactProxyURL(proxyURL string) string {
	setting, errParse := proxyutil.Parse(strings.TrimSpace(proxyURL))
	if errParse != nil || setting.URL == nil {
		return "(invalid)"
	}
	return proxyutil.Redact(setting.URL.String())
}
