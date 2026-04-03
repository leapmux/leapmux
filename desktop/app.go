package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/leapmux/leapmux/util/version"
	"github.com/leapmux/leapmux/solo"
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

	// Inject JS handlers for external links, keyboard shortcuts (F12, Ctrl+Q),
	// using native postMessage since Wails runtime JS is unavailable after
	// the WebView navigates away from the wails:// launcher page.
	// The inspector message differs per platform:
	//   macOS:   "wails:openInspector"
	//   Linux:   "wails:showInspector"
	//   Windows: handled natively by WebView2 (F12 key), no message needed
	inspectorMsg := "wails:showInspector"
	if runtime.GOOS == "darwin" {
		inspectorMsg = "wails:openInspector"
	}

	wailsRuntime.WindowExecJS(a.ctx, fmt.Sprintf(`
(function() {
	// Build a postMessage helper that works whether or not the Wails
	// runtime JS has been loaded into this page. The WebKit user-content
	// manager ("external" handler) is attached to the webview, so
	// webkit.messageHandlers.external.postMessage is always available,
	// even on pages not served via the wails:// scheme.
	window.__lm_post = (function() {
		// Windows (WebView2)
		if (window.chrome && window.chrome.webview && window.chrome.webview.postMessage)
			return function(m) { window.chrome.webview.postMessage(m); };
		// macOS / Linux (WebKit)
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
			if (window.__lm_post) window.__lm_post('BO:' + href);
		}
	}, true);
	window.__lm_switchMode = function() {
		var bg = getComputedStyle(document.documentElement).getPropertyValue('--background').trim() || '#000';
		var overlay = document.createElement('div');
		overlay.style.cssText = 'position:fixed;inset:0;z-index:2147483647;background:' + bg + ';opacity:0;transition:opacity .3s ease';
		document.body.appendChild(overlay);
		requestAnimationFrame(function() {
			overlay.style.opacity = '1';
		});
		overlay.addEventListener('transitionend', function() {
			window.location.href = 'wails://wails/?action=switchMode';
		}, { once: true });
	};
	document.addEventListener('keydown', function(e) {
		if (e.key === 'F12') {
			if (window.__lm_post) window.__lm_post('%s');
		}
		if (e.key === 'q' && (e.ctrlKey || e.metaKey)) {
			e.preventDefault();
			if (window.__lm_post) window.__lm_post('Q');
		}
	}, true);
})();
`, inspectorMsg))

	// Inject minimal Wails method-call bridge for the navigated frontend
	// page. Wails' C-message protocol: __lm_post('C' + JSON) triggers a
	// Go method call, and the response arrives via window.wails.Callback.
	wailsRuntime.WindowExecJS(a.ctx, `
(function() {
	window.__lm_callbacks = {};
	window.wails = window.wails || {};
	window.wails.Callback = function(data) {
		var parsed = JSON.parse(data);
		var cb = window.__lm_callbacks[parsed.callbackid];
		if (cb) {
			delete window.__lm_callbacks[parsed.callbackid];
			if (parsed.error) {
				cb.reject(new Error(typeof parsed.error === 'string'
					? parsed.error : JSON.stringify(parsed.error)));
			} else {
				cb.resolve(parsed.result);
			}
		}
	};
	window.__lm_call = function(method, args) {
		return new Promise(function(resolve, reject) {
			var id = 'lm_' + (++window.__lm_callSeq);
			window.__lm_callbacks[id] = { resolve: resolve, reject: reject };
			if (window.__lm_post) {
				window.__lm_post('C' + JSON.stringify({
					name: method, args: args || [], callbackID: id
				}));
			} else {
				delete window.__lm_callbacks[id];
				reject(new Error('__lm_post not available'));
			}
		});
	};
	window.__lm_callSeq = 0;
})();
`)

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

	a.tunnels.CloseAll()
	a.stopSolo()
	a.connected = false
	a.config.Mode = ""
	if err := SaveConfig(a.config); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}
	return nil
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

	a.connected = true
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

	a.connected = true
	return hubURL, nil
}

// CreateTunnel creates a new TCP/IP tunnel to a worker.
func (a *App) CreateTunnel(config TunnelConfig) (*TunnelInfo, error) {
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
