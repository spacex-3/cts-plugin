package main

import (
	"os"
	"path/filepath"
	"strings"
)

func proxyLinesFile() (string, error) {
	dir, errDir := os.UserConfigDir()
	if errDir != nil {
		return "", errDir
	}
	return filepath.Join(dir, "codex-turn-state", "proxies.txt"), nil
}

func saveProxyLines(raw, scheme string) error {
	path, errPath := proxyLinesFile()
	if errPath != nil {
		return errPath
	}
	if errMkdir := os.MkdirAll(filepath.Dir(path), 0o700); errMkdir != nil {
		return errMkdir
	}
	content := "scheme: " + strings.ToLower(strings.TrimSpace(scheme)) + "\n" + strings.ReplaceAll(raw, "\r\n", "\n")
	return os.WriteFile(path, []byte(content), 0o600)
}

func loadPersistedProxyLines(cfg pluginConfig) pluginConfig {
	if strings.TrimSpace(cfg.Proxy) != "" || len(cfg.Proxies) > 0 {
		return cfg
	}
	path, errPath := proxyLinesFile()
	if errPath != nil {
		return cfg
	}
	data, errRead := os.ReadFile(path)
	if errRead != nil {
		return cfg
	}
	text := strings.TrimPrefix(string(data), "\ufeff")
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var rawLines []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if scheme := strings.TrimPrefix(line, "scheme:"); scheme != line {
			cfg.ProxyScheme = strings.TrimSpace(scheme)
			continue
		}
		rawLines = append(rawLines, line)
	}
	if len(rawLines) > 0 {
		cfg.Proxy = strings.Join(rawLines, "\n")
	}
	return cfg
}
