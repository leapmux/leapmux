package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// DesktopConfig persists the user's last connection mode, hub URL, and window size.
type DesktopConfig struct {
	Mode         string `json:"mode"`                    // "solo" or "distributed"
	HubURL       string `json:"hub_url"`                 // Only for distributed
	WindowWidth  int    `json:"window_width,omitempty"`  // Saved window width
	WindowHeight int    `json:"window_height,omitempty"` // Saved window height
}

func configPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "leapmux", "desktop", "desktop.json"), nil
}

// LoadConfig reads the saved desktop config. Returns a zero-value config if the file does not exist.
func LoadConfig() (*DesktopConfig, error) {
	p, err := configPath()
	if err != nil {
		return &DesktopConfig{}, nil
	}

	data, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &DesktopConfig{}, nil
		}
		return nil, err
	}

	var cfg DesktopConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return &DesktopConfig{}, nil
	}
	return &cfg, nil
}

// SaveConfig writes the desktop config to disk.
func SaveConfig(cfg *DesktopConfig) error {
	p, err := configPath()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		return err
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o600)
}
