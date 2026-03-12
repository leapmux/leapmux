package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/leapmux/leapmux/solo"
)

// App is the main application struct bound to the frontend via Wails.
type App struct {
	ctx     context.Context
	version string
	config  *DesktopConfig
	solo    *solo.Instance
}

// NewApp creates a new App instance.
func NewApp(version string) *App {
	return &App{version: version}
}

// startup is called when the app starts. The context is saved for runtime calls.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	cfg, err := LoadConfig()
	if err != nil {
		cfg = &DesktopConfig{}
	}
	a.config = cfg
}

// shutdown is called when the app is closing.
func (a *App) shutdown(_ context.Context) {
	a.stopSolo()
}

// GetConfig returns the saved desktop config to pre-fill the UI.
func (a *App) GetConfig() *DesktopConfig {
	return a.config
}

// GetVersion returns the app version string.
func (a *App) GetVersion() string {
	return a.version
}

// ConnectSolo starts the in-process Hub and Worker, waits for readiness,
// saves the config, and returns the local URL.
func (a *App) ConnectSolo() (string, error) {
	if err := a.startSolo(); err != nil {
		return "", err
	}

	if err := a.waitForSoloReady(a.ctx); err != nil {
		a.stopSolo()
		return "", fmt.Errorf("waiting for LeapMux to start: %w", err)
	}

	a.config = &DesktopConfig{Mode: "solo"}
	if err := SaveConfig(a.config); err != nil {
		fmt.Printf("warning: failed to save config: %v\n", err)
	}

	return "http://127.0.0.1:4327", nil
}

// ConnectDistributed probes the remote Hub URL and saves the config.
func (a *App) ConnectDistributed(hubURL string) (string, error) {
	hubURL = strings.TrimRight(strings.TrimSpace(hubURL), "/")
	if hubURL == "" {
		return "", fmt.Errorf("hub URL is required")
	}

	// Ensure the URL has a scheme.
	if !strings.HasPrefix(hubURL, "http://") && !strings.HasPrefix(hubURL, "https://") {
		hubURL = "https://" + hubURL
	}

	if err := probeHub(hubURL); err != nil {
		return "", fmt.Errorf("cannot reach Hub at %s: %w", hubURL, err)
	}

	a.config = &DesktopConfig{Mode: "distributed", HubURL: hubURL}
	if err := SaveConfig(a.config); err != nil {
		fmt.Printf("warning: failed to save config: %v\n", err)
	}

	return hubURL, nil
}
