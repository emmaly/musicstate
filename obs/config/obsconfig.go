package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

type Config struct {
	Enabled      bool   `json:"server_enabled"`
	Port         int    `json:"server_port"`
	AuthRequired bool   `json:"auth_required"`
	Password     string `json:"server_password"`
}

func GetLocalConfig() (*Config, error) {
	configPath := getConfigPath()
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	return &config, nil
}

func getConfigPath() string {
	switch runtime.GOOS {
	case "windows":
		return filepath.Join(os.Getenv("APPDATA"), "obs-studio", "plugin_config", "obs-websocket", "config.json")
	case "darwin":
		return filepath.Join(os.Getenv("HOME"), "Library", "Application Support", "obs-studio", "plugin_config", "obs-websocket", "config.json")
	default: // linux and others
		return filepath.Join(os.Getenv("HOME"), ".config", "obs-studio", "plugin_config", "obs-websocket", "config.json")
	}
}
