package main

import (
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
)

func parseProxyURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	if strings.Contains(trimmed, "://") {
		setting, errParse := proxyutil.Parse(trimmed)
		if errParse != nil {
			return "", errParse
		}
		if setting.Mode == proxyutil.ModeDirect {
			return "direct", nil
		}
		if setting.Mode != proxyutil.ModeProxy || setting.URL == nil {
			return "", fmt.Errorf("unsupported proxy value %q", trimmed)
		}
		return setting.URL.String(), nil
	}

	host, port, user, password, errSplit := splitHostPortUserPassword(trimmed)
	if errSplit != nil {
		return "", errSplit
	}
	parsed := &url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort(host, port),
	}
	if user != "" || password != "" {
		parsed.User = url.UserPassword(user, password)
	}
	if _, errParse := proxyutil.Parse(parsed.String()); errParse != nil {
		return "", errParse
	}
	return parsed.String(), nil
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
