package main

import (
	"errors"
	"fmt"
	"io/fs"
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

func main() {
	app := NewApp()

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

	// Serve the SPA frontend from the embedded assets. The SPA handles
	// both the launcher view (mode selection) and the main app.
	spaFS, _ := fs.Sub(spaAssets, "spa")

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
			wailsRuntime.WindowExecJS(app.ctx, `if (window.runtime) window.runtime.openInspector();`)
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
			Handler: newSPAHandler(spaFS),
		},
		OnStartup:  app.startup,
		OnDomReady: app.domReady,
		OnShutdown: app.shutdown,
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
