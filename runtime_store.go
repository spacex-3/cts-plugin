package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
)

type persistedRuntimeFile struct {
	Entries        []cacheEntry        `json:"entries,omitempty"`
	Logs           []probeLogEntry     `json:"logs,omitempty"`
	Injections     []injectionLogEntry `json:"injections,omitempty"`
	Counters       []ticketCounters    `json:"counters,omitempty"`
	Cookies        []cookieEntry       `json:"cookies,omitempty"`
	CookieCounters []cookieCounters    `json:"cookie_counters,omitempty"`
	Combos         []comboStats        `json:"combos,omitempty"`
}

// sortedCookieCounters and sortedCombos keep the persisted file stable so
// repeated writes and diffs stay comparable.
func sortedCookieCounters(stats map[string]cookieCounters) []cookieCounters {
	out := make([]cookieCounters, 0, len(stats))
	for _, counters := range stats {
		out = append(out, counters)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AuthID < out[j].AuthID })
	return out
}

func sortedCombos(stats map[cacheKey]comboStats) []comboStats {
	out := make([]comboStats, 0, len(stats))
	for _, counters := range stats {
		out = append(out, counters)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].AuthID == out[j].AuthID {
			return out[i].Model < out[j].Model
		}
		return out[i].AuthID < out[j].AuthID
	})
	return out
}

// sortedTicketCounters keeps the persisted file stable so repeated writes and
// diffs stay comparable.
func sortedTicketCounters(stats map[cacheKey]ticketCounters) []ticketCounters {
	out := make([]ticketCounters, 0, len(stats))
	for _, counters := range stats {
		out = append(out, counters)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].AuthID == out[j].AuthID {
			return out[i].Model < out[j].Model
		}
		return out[i].AuthID < out[j].AuthID
	})
	return out
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
		Entries:        append([]cacheEntry(nil), r.cache.snapshot()...),
		Logs:           append([]probeLogEntry(nil), r.probeLogs...),
		Injections:     append([]injectionLogEntry(nil), r.injections...),
		Counters:       sortedTicketCounters(r.ticketStats),
		Cookies:        r.cookies.snapshot(),
		CookieCounters: sortedCookieCounters(r.cookieStats),
		Combos:         sortedCombos(r.combos),
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
