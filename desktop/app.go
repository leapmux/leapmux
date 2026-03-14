package main

import (
	"context"
	"fmt"
	"runtime"
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
