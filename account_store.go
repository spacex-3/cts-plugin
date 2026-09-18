package main

import (
	"os"
	"path/filepath"
	"strings"
)

func probeAuthSelectionFile() (string, error) {
	dir, errDir := os.UserConfigDir()
	if errDir != nil {
		return "", errDir
	}
	return filepath.Join(dir, "codex-turn-state", "probe-auths.txt"), nil
}

func saveProbeAuthIDs(authIDs []string) error {
	path, errPath := probeAuthSelectionFile()
	if errPath != nil {
		return errPath
	}
	if errMkdir := os.MkdirAll(filepath.Dir(path), 0o700); errMkdir != nil {
		return errMkdir
	}
	lines := make([]string, 0, len(authIDs))
	for _, authID := range authIDs {
		authID = strings.TrimSpace(authID)
		if authID == "__none__" {
			continue
		}
		if authID != "" {
			lines = append(lines, authID)
		}
	}
	if len(lines) == 0 {
		lines = []string{"# none"}
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o600)
}

func loadPersistedProbeAuthIDs(cfg pluginConfig) pluginConfig {
	if len(cfg.ProbeAuthIDs) > 0 {
		return cfg
	}
	path, errPath := probeAuthSelectionFile()
	if errPath != nil {
		return cfg
	}
	data, errRead := os.ReadFile(path)
	if errRead != nil {
		return cfg
	}
	text := strings.TrimPrefix(string(data), "\ufeff")
	authIDs := make([]string, 0)
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "# none" {
			cfg.ProbeAuthIDs = []string{"__none__"}
			return cfg
		}
		if line != "" {
			authIDs = append(authIDs, line)
		}
	}
	cfg.ProbeAuthIDs = authIDs
	return cfg
}
