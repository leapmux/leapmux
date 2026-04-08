package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/leapmux/leapmux/solo"
	tunnelpkg "github.com/leapmux/leapmux/tunnel"
	"github.com/leapmux/leapmux/util/version"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// App is the main application struct bound to the frontend via Wails.
type App struct {
	ctx        context.Context
	config     *DesktopConfig
	solo       *solo.Instance
	logHandler *webviewHandler
	connected  bool // true after a successful Connect call
	tunnels    *TunnelManager
	proxy      *HubProxy
	relay      *ChannelRelay
	hubURL     string // the Hub URL (for distributed mode)
}

// NewApp creates a new App instance.
func NewApp() *App {
	return &App{
		tunnels: NewTunnelManager(),
	}
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

// domReady is called after the frontend DOM is loaded. Center the window here
// so it takes effect after the window is fully laid out.
func (a *App) domReady(_ context.Context) {
	wailsRuntime.WindowCenter(a.ctx)
	installTabKeyHandler()

	// Build a native message poster for opening the WebView inspector.
	// The Wails JS runtime does not expose an openInspector API; the
	// native WebView message handler must be used directly.
	// The inspector message name differs per platform:
	//   macOS:   "wails:openInspector"
	//   Linux:   "wails:showInspector"
	//   Windows: handled by WebView2 natively (F12 key), no message needed
	inspectorMsg := "wails:showInspector"
	if runtime.GOOS == "darwin" {
		inspectorMsg = "wails:openInspector"
	}

	// Inject minimal JS helpers for external links and keyboard shortcuts.
	// Since the SPA is served from wails://, Go bindings (window.go.main.App.*)
	// work natively. We only need link interception and dev tools shortcuts.
	wailsRuntime.WindowExecJS(a.ctx, fmt.Sprintf(`
(function() {
	// Build a postMessage helper for the native WebView message handler.
	var __post = (function() {
		if (window.chrome && window.chrome.webview && window.chrome.webview.postMessage)
			return function(m) { window.chrome.webview.postMessage(m); };
		if (window.webkit && window.webkit.messageHandlers &&
		    window.webkit.messageHandlers.external &&
		    window.webkit.messageHandlers.external.postMessage)
			return function(m) { window.webkit.messageHandlers.external.postMessage(m); };
		return null;
	})();

	// Intercept clicks on external links and open them in the default browser.
	document.addEventListener('click', function(e) {
		var el = e.target;
		while (el && el.tagName !== 'A') {
			el = el.parentElement;
		}
		if (!el) return;
		var href = el.getAttribute('href');
		if (href && /^https?:\/\//.test(href)) {
			e.preventDefault();
			e.stopPropagation();
			if (__post) __post('BO:' + href);
		}
	}, true);

	document.addEventListener('keydown', function(e) {
		if (e.key === 'F12') {
			if (__post) __post('%s');
		}
		if (e.key === 'F5') {
			e.preventDefault();
		}
		if ((e.key === 'r' || e.key === 'R') && (e.ctrlKey || e.metaKey)) {
			e.preventDefault();
		}
		if (e.key === 'q' && (e.ctrlKey || e.metaKey)) {
			e.preventDefault();
			if (__post) __post('Q');
		}
	}, true);
})();
`, inspectorMsg))

	// Flush buffered log records to the WebView console now that the
	// navigated page's DOM is ready and WindowExecJS calls will land.
	// On the initial launcher page load logHandler is nil (no-op).
	if a.logHandler != nil {
		a.logHandler.SetReady()
	}
}

// bringToFront shows and raises the window so it appears above other windows.
func (a *App) bringToFront() {
	if a.ctx == nil {
		return
	}
	wailsRuntime.WindowUnminimise(a.ctx)
	wailsRuntime.WindowShow(a.ctx)
	wailsRuntime.WindowSetAlwaysOnTop(a.ctx, true)
	wailsRuntime.WindowSetAlwaysOnTop(a.ctx, false)
}

// shutdown is called when the app is closing.
func (a *App) shutdown(_ context.Context) {
	// Save the current window size only if the app connected this session
	// (not if we're still on the picker UI with its smaller window).
	if a.connected {
		w, h := wailsRuntime.WindowGetSize(a.ctx)
		if w > 0 && h > 0 {
			a.config.WindowWidth = w
			a.config.WindowHeight = h
			_ = SaveConfig(a.config)
		}
	}
	a.closeChannelRelay()
	a.tunnels.CloseAll()
	a.stopSolo()
}

// GetConfig returns the saved desktop config to pre-fill the UI.
func (a *App) GetConfig() *DesktopConfig {
	return a.config
}

// GetVersion returns the app version string.
func (a *App) GetVersion() string {
	return version.Value
}

// BuildInfo holds version, commit hash, and build time for the frontend.
type BuildInfo struct {
	Version    string `json:"version"`
	CommitHash string `json:"commit_hash"`
	CommitTime string `json:"commit_time"`
	BuildTime  string `json:"build_time"`
}

// GetBuildInfo returns the full build information.
func (a *App) GetBuildInfo() BuildInfo {
	return BuildInfo{
		Version:    version.Value,
		CommitHash: version.CommitHash,
		CommitTime: version.CommitTime,
		BuildTime:  version.BuildTime,
	}
}

// CheckFullDiskAccess returns true if the app has Full Disk Access (macOS only).
// On other platforms it always returns true.
func (a *App) CheckFullDiskAccess() bool {
	return checkFullDiskAccess()
}

// OpenFullDiskAccessSettings opens the system settings pane for Full Disk Access (macOS only).
// On other platforms this is a no-op.
func (a *App) OpenFullDiskAccessSettings() {
	_ = openFullDiskAccessSettings()
}

// Restart re-launches the app and quits the current process.
func (a *App) Restart() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	_ = exec.Command(exe).Start()
	wailsRuntime.Quit(a.ctx)
}

// SwitchMode stops the current backend (if solo), saves the current window
// size, clears the saved mode from the config, and saves it. The frontend
// is responsible for navigating back to the launcher page and resizing the
// window.
func (a *App) SwitchMode() error {
	// Preserve window size so the next Connect restores it.
	if a.connected {
		w, h := wailsRuntime.WindowGetSize(a.ctx)
		if w > 0 && h > 0 {
			a.config.WindowWidth = w
			a.config.WindowHeight = h
		}
	}

	a.closeChannelRelay()
	a.tunnels.CloseAll()
	a.stopSolo()
	// Restore the original slog handler if we were forwarding to WebView.
	// stopSolo handles this for solo mode; this covers distributed mode.
	if a.logHandler != nil {
		slog.SetDefault(slog.New(a.logHandler.inner))
		a.logHandler = nil
	}
	a.connected = false
	a.proxy = nil
	a.hubURL = ""
	a.config.Mode = ""
	if err := SaveConfig(a.config); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}

	return nil
}

// ConnectSolo starts the in-process Hub and Worker with no TCP listener,
// creates a proxy via Unix socket, switches to the SPA, and returns.
func (a *App) ConnectSolo() error {
	if err := a.startSolo(); err != nil {
		return err
	}

	// Wait for Unix socket readiness.
	socketPath := a.solo.Server().SocketPath()
	if err := a.waitForSoloReady(a.ctx, socketPath); err != nil {
		a.stopSolo()
		return fmt.Errorf("waiting for LeapMux to start: %w", err)
	}

	// Create proxy via Unix socket.
	a.proxy = newUnixSocketProxy(socketPath)

	a.config.Mode = "solo"
	a.config.HubURL = ""
	if err := SaveConfig(a.config); err != nil {
		fmt.Printf("warning: failed to save config: %v\n", err)
	}

	a.connected = true
	return nil
}

// ConnectDistributed probes the remote Hub URL, creates an HTTP proxy,
// switches to the SPA, and returns.
func (a *App) ConnectDistributed(hubURL string) error {
	hubURL = strings.TrimRight(strings.TrimSpace(hubURL), "/")
	if hubURL == "" {
		return fmt.Errorf("hub URL is required")
	}

	// Ensure the URL has a scheme.
	if !strings.HasPrefix(hubURL, "http://") && !strings.HasPrefix(hubURL, "https://") {
		hubURL = "https://" + hubURL
	}

	if err := probeHub(hubURL); err != nil {
		return fmt.Errorf("cannot reach Hub at %s: %w", hubURL, err)
	}

	// Create proxy to remote Hub.
	a.proxy = newHTTPProxy(hubURL)
	a.hubURL = hubURL

	// Forward Go logs to the WebView console (Web Inspector / F12).
	a.logHandler = newWebviewHandler(
		slog.Default().Handler(),
		func(js string) { wailsRuntime.WindowExecJS(a.ctx, js) },
	)
	slog.SetDefault(slog.New(a.logHandler))
	a.logHandler.SetReady()

	a.config.Mode = "distributed"
	a.config.HubURL = hubURL
	if err := SaveConfig(a.config); err != nil {
		fmt.Printf("warning: failed to save config: %v\n", err)
	}

	a.connected = true
	return nil
}

// GetHubURL returns the Hub URL for the frontend. In solo mode this returns
// a dummy URL since routing happens through the proxy. In distributed mode
// it returns the actual Hub URL.
func (a *App) GetHubURL() string {
	return a.hubURL
}

// IsConnected returns whether the app has connected to a Hub.
func (a *App) IsConnected() bool {
	return a.connected
}

// CreateTunnel creates a new TCP/IP tunnel to a worker.
func (a *App) CreateTunnel(config TunnelConfig) (*TunnelInfo, error) {
	if a.proxy == nil {
		return nil, fmt.Errorf("not connected")
	}

	// Use the proxy's base URL and HTTP clients so that tunnels work in both
	// Solo mode (Unix socket) and Distributed mode (remote Hub).
	config.HubURL = a.proxy.baseURL
	a.tunnels.SetChannelOptions(&tunnelpkg.OpenChannelOptions{
		HTTPClient:          a.proxy.client,
		WebSocketHTTPClient: a.proxy.wsClient,
	})
	return a.tunnels.CreateTunnel(a.ctx, config)
}

// DeleteTunnel stops and removes a tunnel.
func (a *App) DeleteTunnel(tunnelID string) error {
	return a.tunnels.DeleteTunnel(tunnelID)
}

// ListTunnels returns info about all active tunnels.
func (a *App) ListTunnels() []TunnelInfo {
	return a.tunnels.ListTunnels()
}
