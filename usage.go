package main

import (
	"encoding/json"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func handleUsage(raw []byte) ([]byte, error) {
	var record pluginapi.UsageRecord
	if len(raw) > 0 {
		if errUnmarshal := json.Unmarshal(raw, &record); errUnmarshal != nil {
			return nil, fmt.Errorf("decode usage record: %w", errUnmarshal)
		}
	}
	currentRuntime().handleUsage(record)
	return okEnvelope(map[string]any{})
}
