package main

import "testing"

func TestPluginConfigAllowsConfiguredTargets(t *testing.T) {
	cfg := normalizeConfig(pluginConfig{
		AuthIDs: []string{" auth-1 ", "AUTH-1"},
		Models:  []string{" gpt-5.6-sol "},
	})

	if !cfg.allows("AUTH-1", "GPT-5.6-SOL") {
		t.Fatal("configured auth and model should be allowed case-insensitively")
	}
	if cfg.allows("auth-2", "gpt-5.6-sol") {
		t.Fatal("unconfigured auth should not be allowed")
	}
	if cfg.allows("auth-1", "gpt-6-astra") {
		t.Fatal("unconfigured model should not be allowed")
	}
}

func TestPluginConfigDefaults(t *testing.T) {
	cfg := normalizeConfig(pluginConfig{})
	if got := cfg.interval().Seconds(); got != defaultIntervalSeconds {
		t.Fatalf("interval = %v, want %d", got, defaultIntervalSeconds)
	}
	if got := cfg.ttl().Seconds(); got != defaultTTLSeconds {
		t.Fatalf("ttl = %v, want %d", got, defaultTTLSeconds)
	}
	if got := cfg.targetLength(); got != defaultTargetStateLength {
		t.Fatalf("target length = %d, want %d", got, defaultTargetStateLength)
	}
	if !cfg.allows("any-auth", defaultProbeModels[0]) {
		t.Fatal("empty auth_ids should allow every auth for a default model")
	}
	if cfg.allows("any-auth", "unconfigured-model") {
		t.Fatal("models should remain scoped to the default model list")
	}
}

func TestProbeLogDefaultsAndClamp(t *testing.T) {
	cfg := normalizeConfig(pluginConfig{})
	if cfg.directProbeEnabled() {
		t.Fatal("direct probe should default to disabled")
	}
	if cfg.showStateValuesEnabled() {
		t.Fatal("state values should default to hidden")
	}
	if got := cfg.probeLogLimit(); got != defaultProbeLogLimit {
		t.Fatalf("probe log limit = %d, want %d", got, defaultProbeLogLimit)
	}
	cfg = normalizeConfig(pluginConfig{ProbeLogLimit: maxProbeLogLimit + 1})
	if got := cfg.probeLogLimit(); got != maxProbeLogLimit {
		t.Fatalf("clamped probe log limit = %d, want %d", got, maxProbeLogLimit)
	}
}
