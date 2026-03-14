package main

import (
	"embed"
	"errors"
	"fmt"
	"os"
	"runtime"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/menu/keys"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

//go:embed all:frontend
var assets embed.FS

var version = "dev"

func main() {
	app := NewApp(version)

	release, err := acquireSingleInstance(app.bringToFront)
	if errors.Is(err, errAlreadyRunning) {
		return
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "single instance check: %v\n", err)
	}
	if release != nil {
		defer release()
	}

	// Build the application menu. On macOS the menu lives in the system
	// menu bar and costs no window space. On Windows and Linux the menu
	// bar consumes vertical space, so we omit it entirely and handle the
	// Dev Tools shortcut via JS instead (see domReady).
	var appMenu *menu.Menu
	if runtime.GOOS == "darwin" {
		appMenu = menu.NewMenu()
		appMenu.Append(menu.AppMenu())
		appMenu.Append(menu.EditMenu())
		viewMenu := appMenu.AddSubmenu("View")
		viewMenu.AddText("Toggle Developer Tools", keys.Key("f12"), func(_ *menu.CallbackData) {
			wailsRuntime.WindowExecJS(app.ctx, `
				(function() {
					if (window.webkit && window.webkit.messageHandlers && window.webkit.messageHandlers.external)
						window.webkit.messageHandlers.external.postMessage('wails:openInspector');
					else if (window.WailsInvoke)
						window.WailsInvoke('wails:openInspector');
				})();
			`)
		})
		appMenu.Append(menu.WindowMenu())
	}

	if err := wails.Run(&options.App{
		Title:     "LeapMux",
		Width:     900,
		Height:    680,
		MinWidth:  800,
		MinHeight: 600,
		Menu:      appMenu,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		// After connecting, the WebView navigates from the wails:// launcher
		// page to the real frontend (e.g. http://127.0.0.1:4327). Allow
		// that origin so JS→Go IPC (keyboard shortcuts, external links)
		// continues to work on the navigated page.
		BindingsAllowedOrigins: "http://*,https://*",
		OnStartup:              app.startup,
		OnDomReady:             app.domReady,
		OnShutdown:             app.shutdown,
		Mac: &mac.Options{
			Preferences: &mac.Preferences{
				FullscreenEnabled: mac.Enabled,
			},
		},
		Bind: []interface{}{
			app,
		},
	}); err != nil {
		panic(err)
	}
}
