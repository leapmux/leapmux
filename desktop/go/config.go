package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// Window display modes persisted in DesktopConfig.WindowMode. The states are
// mutually exclusive; WindowWidth/Height always hold the last *windowed*
// geometry so exiting maximized/fullscreen returns to a sensible size.
const (
	WindowModeNormal     = "normal"
	WindowModeMaximized  = "maximized"
	WindowModeFullscreen = "fullscreen"
)

// DesktopConfig persists the user's last connection mode, hub URL, window size,
// and a cache of the Desktop window-behaviour preferences.
//
// The behaviour fields are a ONE-WAY cache of an account setting, not a setting
// of their own: the webview pushes the resolved values down, and the shell
// reads them at launch because it must decide tray creation and the initial
// window state before the webview -- and therefore before any preference -- can
// be read. Nothing here is ever read back into a preference. See
// SetDesktopBehaviorRequest in proto/leapmux/desktop/v1/frame.proto for why
// start_on_login is not among them, and for the per-OS-user limit.
//
// An absent behaviour field means the built-in default, which is why every one
// of them is `omitempty` and why the zero value of each is the default: tray
// off, close to the tray, minimize to the taskbar, start with a window.
type DesktopConfig struct {
	Mode           string `json:"mode"`                       // "solo" or "distributed"
	HubURL         string `json:"hub_url"`                    // Only for distributed
	WindowWidth    int    `json:"window_width,omitempty"`     // Saved windowed width
	WindowHeight   int    `json:"window_height,omitempty"`    // Saved windowed height
	WindowMode     string `json:"window_mode,omitempty"`      // "normal" | "maximized" | "fullscreen"
	TrayEnabled    bool   `json:"tray_enabled,omitempty"`     // Show a tray / menu-bar icon
	TrayOnClose    string `json:"tray_on_close,omitempty"`    // "tray" | "quit"
	TrayOnMinimize string `json:"tray_on_minimize,omitempty"` // "tray" | "taskbar"
	StartMinimized string `json:"start_minimized,omitempty"`  // "window" | "minimized"
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
