package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"

	desktoppb "github.com/leapmux/leapmux/generated/proto/leapmux/desktop/v1"
	"github.com/leapmux/leapmux/solo"
	tunnelpkg "github.com/leapmux/leapmux/tunnel"
	"github.com/leapmux/leapmux/util/version"
)

// App is the desktop sidecar state managed over stdio RPC by the Tauri shell.
type App struct {
	ctx            context.Context
	cancel         context.CancelFunc
	shutdownOnce   sync.Once
	config         *DesktopConfig
	solo           *solo.Instance
	tunnels        *TunnelManager
	proxy          *HubProxy
	relay          *ChannelRelay
	hubURL         string
	binaryHash     string
	eventSinkMu    sync.RWMutex
	eventSink      func(*desktoppb.Event)
	prevLogHandler slog.Handler
}

const protocolVersion = "1"

func NewApp(binaryHash string) *App {
	ctx, cancel := context.WithCancel(context.Background())
	app := &App{
		ctx:        ctx,
		cancel:     cancel,
		tunnels:    NewTunnelManager(),
		binaryHash: binaryHash,
	}
	app.startup()
	return app
}

func (a *App) startup() {
	cfg, err := LoadConfig()
	if err != nil {
		cfg = &DesktopConfig{}
	}
	a.config = cfg
}

func (a *App) Shutdown() {
	a.shutdownOnce.Do(func() {
		a.closeChannelRelay()
		a.tunnels.CloseAll()
		a.stopSolo()
		a.cancel()
	})
}

func (a *App) shutdown() {
	a.Shutdown()
}

func (a *App) SetEventSink(sink func(*desktoppb.Event)) {
	a.eventSinkMu.Lock()
	defer a.eventSinkMu.Unlock()
	a.eventSink = sink
}

func (a *App) EmitEvent(event *desktoppb.Event) {
	a.eventSinkMu.RLock()
	sink := a.eventSink
	a.eventSinkMu.RUnlock()
	if sink != nil {
		sink(event)
	}
}

func (a *App) SidecarInfo() *desktoppb.SidecarInfo {
	mode := desktoppb.SidecarShellMode_SIDECAR_SHELL_MODE_LAUNCHER
	connected := a.proxy != nil
	if connected {
		switch a.config.Mode {
		case "distributed":
			mode = desktoppb.SidecarShellMode_SIDECAR_SHELL_MODE_DISTRIBUTED
		default:
			mode = desktoppb.SidecarShellMode_SIDECAR_SHELL_MODE_SOLO
		}
	}

	return &desktoppb.SidecarInfo{
		ProtocolVersion: protocolVersion,
		BinaryHash:      a.binaryHash,
		Pid:             int64(os.Getpid()),
		ShellMode:       mode,
		Connected:       connected,
		HubUrl:          a.hubURL,
	}
}

func (a *App) GetConfig() *DesktopConfig {
	return a.config
}

func (a *App) SetWindowSize(width, height int, maximized bool) error {
	if width > 0 {
		a.config.WindowWidth = width
	}
	if height > 0 {
		a.config.WindowHeight = height
	}
	a.config.WindowMaximized = maximized
	return SaveConfig(a.config)
}

func (a *App) GetVersion() string {
	return version.Value
}

type BuildInfo struct {
	Version    string `json:"version"`
	CommitHash string `json:"commit_hash"`
	CommitTime string `json:"commit_time"`
	BuildTime  string `json:"build_time"`
}

func (a *App) GetBuildInfo() BuildInfo {
	return BuildInfo{
		Version:    version.Value,
		CommitHash: version.CommitHash,
		CommitTime: version.CommitTime,
		BuildTime:  version.BuildTime,
	}
}

func (a *App) CheckFullDiskAccess() bool {
	return checkFullDiskAccess()
}

func (a *App) OpenFullDiskAccessSettings() {
	_ = openFullDiskAccessSettings()
}

func (a *App) Restart() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return exec.Command(exe).Start()
}

func (a *App) SwitchMode() error {
	a.closeChannelRelay()
	a.tunnels.CloseAll()
	a.stopSolo()
	a.proxy = nil
	a.hubURL = ""
	a.config.Mode = ""
	if err := SaveConfig(a.config); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}
	return nil
}

func (a *App) ConnectSolo() error {
	if err := a.startSolo(); err != nil {
		return err
	}

	socketPath := a.solo.Server().SocketPath()
	if err := a.waitForSoloReady(a.ctx, socketPath); err != nil {
		a.stopSolo()
		return fmt.Errorf("waiting for LeapMux to start: %w", err)
	}

	a.proxy = newUnixSocketProxy(socketPath)
	a.config.Mode = "solo"
	a.config.HubURL = ""
	if err := SaveConfig(a.config); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	a.hubURL = ""
	return nil
}

func (a *App) ConnectDistributed(hubURL string) error {
	hubURL = strings.TrimRight(strings.TrimSpace(hubURL), "/")
	if hubURL == "" {
		return fmt.Errorf("hub URL is required")
	}
	if !strings.HasPrefix(hubURL, "http://") && !strings.HasPrefix(hubURL, "https://") {
		hubURL = "https://" + hubURL
	}

	if err := probeHub(hubURL); err != nil {
		return fmt.Errorf("cannot reach Hub at %s: %w", hubURL, err)
	}

	a.proxy = newHTTPProxy(hubURL)
	a.hubURL = hubURL
	a.config.Mode = "distributed"
	a.config.HubURL = hubURL
	if err := SaveConfig(a.config); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	return nil
}

func (a *App) GetHubURL() string {
	return a.hubURL
}

func (a *App) CreateTunnel(config TunnelConfig) (*TunnelInfo, error) {
	if a.proxy == nil {
		return nil, fmt.Errorf("not connected")
	}

	config.HubURL = a.proxy.baseURL
	a.tunnels.SetChannelOptions(&tunnelpkg.OpenChannelOptions{
		HTTPClient:          a.proxy.client,
		WebSocketHTTPClient: a.proxy.wsClient,
	})
	return a.tunnels.CreateTunnel(a.ctx, config)
}

func (a *App) DeleteTunnel(tunnelID string) error {
	return a.tunnels.DeleteTunnel(tunnelID)
}

func (a *App) ListTunnels() []TunnelInfo {
	return a.tunnels.ListTunnels()
}
