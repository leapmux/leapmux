package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/leapmux/leapmux/solo"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
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

// domReady is called after the frontend DOM is loaded. Center the window here
// so it takes effect after the window is fully laid out.
func (a *App) domReady(_ context.Context) {
	wailsRuntime.WindowCenter(a.ctx)

	// Intercept clicks on external links and open them in the default browser
	// instead of navigating inside the WebView.
	//
	// Prevent WebKitGTK from intercepting Tab/Shift+Tab for native focus
	// traversal when the user is typing in the ProseMirror editor. Without
	// this, the keydown event never reaches ProseMirror's handleKeyDown on
	// Linux, so Shift+Tab (plan mode toggle) and Tab (heading conversion)
	// do not work.
	wailsRuntime.WindowExecJS(a.ctx, `
(function() {
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
			window.runtime.BrowserOpenURL(href);
		}
	}, true);
	document.addEventListener('keydown', function(e) {
		if (e.key === 'Tab') {
			var el = e.target;
			while (el) {
				if (el.classList && el.classList.contains('ProseMirror')) {
					e.preventDefault();
					return;
				}
				el = el.parentElement;
			}
		}
		if (e.key === 'F12') {
			window.WailsInvoke('wails:openInspector');
			window.WailsInvoke('wails:showInspector');
		}
		if (e.key === 'q' && (e.ctrlKey || e.metaKey)) {
			e.preventDefault();
			window.runtime.Quit();
		}
	}, true);
})();
`)
}

// shutdown is called when the app is closing.
func (a *App) shutdown(_ context.Context) {
	// Save the current window size if the user has connected at least once.
	if a.config != nil && a.config.Mode != "" {
		w, h := wailsRuntime.WindowGetSize(a.ctx)
		if w > 0 && h > 0 {
			a.config.WindowWidth = w
			a.config.WindowHeight = h
			_ = SaveConfig(a.config)
		}
	}
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
