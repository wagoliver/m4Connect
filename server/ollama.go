package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const ollamaBase = "http://localhost:11434"

// ── Structs ───────────────────────────────────────────────────────────────────

type OllamaDetails struct {
	Format             string   `json:"format"`
	Family             string   `json:"family"`
	Families           []string `json:"families"`
	ParameterSize      string   `json:"parameter_size"`
	QuantizationLevel  string   `json:"quantization_level"`
}

type OllamaRunningModel struct {
	Name      string        `json:"name"`
	Model     string        `json:"model"`
	Size      int64         `json:"size"`
	SizeVRAM  int64         `json:"size_vram"`
	ExpiresAt time.Time     `json:"expires_at"`
	Details   OllamaDetails `json:"details"`
}

type OllamaInstalledModel struct {
	Name       string        `json:"name"`
	ModifiedAt time.Time     `json:"modified_at"`
	Size       int64         `json:"size"`
	Digest     string        `json:"digest"`
	Details    OllamaDetails `json:"details"`
}

type OllamaStatus struct {
	Online        bool                   `json:"online"`
	Version       string                 `json:"version"`
	PingMs        int64                  `json:"ping_ms"`
	RunningModels []OllamaRunningModel   `json:"running_models"`
	Models        []OllamaInstalledModel `json:"models"`
	MemUsedBytes  int64                  `json:"mem_used_bytes"`
}

// ── Client ────────────────────────────────────────────────────────────────────

var ollamaClient = &http.Client{Timeout: 3 * time.Second}

func ollamaGet(path string, out any) error {
	resp, err := ollamaClient.Get(ollamaBase + path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(out)
}

// ── CheckOllama ───────────────────────────────────────────────────────────────

func CheckOllama() OllamaStatus {
	start := time.Now()

	// Version check (also acts as health probe)
	var verResp struct {
		Version string `json:"version"`
	}
	if err := ollamaGet("/api/version", &verResp); err != nil {
		return OllamaStatus{Online: false}
	}
	pingMs := time.Since(start).Milliseconds()

	// Loaded models (/api/ps)
	var psResp struct {
		Models []OllamaRunningModel `json:"models"`
	}
	ollamaGet("/api/ps", &psResp)

	// Installed models (/api/tags)
	var tagsResp struct {
		Models []OllamaInstalledModel `json:"models"`
	}
	ollamaGet("/api/tags", &tagsResp)

	// Sum VRAM/RAM used by running models
	var memUsed int64
	for _, m := range psResp.Models {
		sz := m.SizeVRAM
		if sz == 0 {
			sz = m.Size
		}
		memUsed += sz
	}

	// Ensure slices are never nil in JSON
	running := psResp.Models
	if running == nil {
		running = []OllamaRunningModel{}
	}
	installed := tagsResp.Models
	if installed == nil {
		installed = []OllamaInstalledModel{}
	}

	return OllamaStatus{
		Online:        true,
		Version:       fmt.Sprintf("v%s", verResp.Version),
		PingMs:        pingMs,
		RunningModels: running,
		Models:        installed,
		MemUsedBytes:  memUsed,
	}
}
