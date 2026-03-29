package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
)

const (
	configDir  = "/Library/Application Support/M4Server"
	configPath = configDir + "/config.json"
)

type Config struct {
	Token           string `json:"token"`
	PreferredSubnet string `json:"preferred_subnet"`
	PortalPort      int    `json:"portal_port"`
	HandshakePort   int    `json:"handshake_port"`
	MacSuffix       string `json:"mac_suffix"`
	ClientSuffix    string `json:"client_suffix"`
	LastIface       string `json:"last_iface,omitempty"`
}

func newToken() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func defaultConfig() Config {
	return Config{
		Token:           newToken(),
		PreferredSubnet: "10.10.10",
		PortalPort:      8080,
		HandshakePort:   54321,
		MacSuffix:       "1",
		ClientSuffix:    "2",
	}
}

func loadOrCreateConfig() (Config, error) {
	os.MkdirAll(filepath.Dir(configPath), 0755)

	data, err := os.ReadFile(configPath)
	if err != nil {
		cfg := defaultConfig()
		return cfg, saveConfig(cfg)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return defaultConfig(), nil
	}
	return cfg, nil
}

func saveConfig(cfg Config) error {
	data, _ := json.MarshalIndent(cfg, "", "  ")
	return os.WriteFile(configPath, data, 0600)
}
