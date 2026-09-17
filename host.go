package main

import (
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type hostAPI interface {
	AuthList() ([]pluginapi.HostAuthFileEntry, error)
	AuthGet(authIndex string) (pluginapi.HostAuthGetResponse, error)
	Log(level, message string, fields map[string]any)
}

type authListResponse struct {
	Files []pluginapi.HostAuthFileEntry `json:"files"`
}

type noopHost struct{}

func (noopHost) AuthList() ([]pluginapi.HostAuthFileEntry, error) {
	return nil, nil
}

func (noopHost) AuthGet(string) (pluginapi.HostAuthGetResponse, error) {
	return pluginapi.HostAuthGetResponse{}, nil
}

func (noopHost) Log(string, string, map[string]any) {}
