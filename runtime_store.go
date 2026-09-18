package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type persistedRuntimeFile struct {
	Entries    []cacheEntry        `json:"entries,omitempty"`
	Logs       []probeLogEntry     `json:"logs,omitempty"`
	Injections []injectionLogEntry `json:"injections,omitempty"`
}

var runtimeConfigDir = os.UserConfigDir

func runtimeStateFile() (string, error) {
	dir, errDir := runtimeConfigDir()
	if errDir != nil {
		return "", errDir
	}
	return filepath.Join(dir, "codex-turn-state", "runtime.json"), nil
}

func (r *pluginRuntime) persistLocked() {
	if r == nil {
		return
	}
	path, errPath := runtimeStateFile()
	if errPath != nil {
		return
	}
	file := persistedRuntimeFile{
		Entries:    append([]cacheEntry(nil), r.cache.snapshot()...),
		Logs:       append([]probeLogEntry(nil), r.probeLogs...),
		Injections: append([]injectionLogEntry(nil), r.injections...),
	}
	data, errMarshal := json.Marshal(file)
	if errMarshal != nil {
		return
	}
	if errMkdir := os.MkdirAll(filepath.Dir(path), 0o700); errMkdir != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o600)
}

func loadPersistedRuntimeFile() persistedRuntimeFile {
	var file persistedRuntimeFile
	path, errPath := runtimeStateFile()
	if errPath != nil {
		return file
	}
	data, errRead := os.ReadFile(path)
	if errRead != nil {
		return file
	}
	_ = json.Unmarshal(data, &file)
	return file
}
