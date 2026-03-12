package main

import (
	"embed"

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

	appMenu := menu.NewMenu()
	appMenu.Append(menu.AppMenu())
	appMenu.Append(menu.EditMenu())
	viewMenu := appMenu.AddSubmenu("View")
	viewMenu.AddText("Toggle Developer Tools", keys.Combo("i", keys.OptionOrAltKey, keys.CmdOrCtrlKey), func(_ *menu.CallbackData) {
		wailsRuntime.WindowExecJS(app.ctx, "window.WailsInvoke('wails:openInspector');window.WailsInvoke('wails:showInspector')")
	})
	appMenu.Append(menu.WindowMenu())

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
