package main

import (
	"embed"
	"runtime"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/menu/keys"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

//go:embed all:frontend
var assets embed.FS

//go:embed build/appicon.png
var appIcon []byte

var version = "dev"

func main() {
	app := NewApp(version)

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
			wailsRuntime.WindowExecJS(app.ctx, "window.WailsInvoke('wails:openInspector')")
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
		OnStartup:  app.startup,
		OnDomReady: app.domReady,
		OnShutdown: app.shutdown,
		Linux: &linux.Options{
			Icon: appIcon,
		},
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
