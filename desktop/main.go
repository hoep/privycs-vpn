package main

import (
	"embed"
	"os"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	// Check for --helper flag: run as privileged helper service (no UI).
	// On Windows, runHelperEntrypoint() integrates with the Service Control
	// Manager (required for `sc start` to not time out with Error 1053).
	// On Linux/macOS it just runs the listener directly.
	for _, arg := range os.Args[1:] {
		if arg == "--helper" {
			runHelperEntrypoint()
			return
		}
	}

	app := NewApp()

	err := wails.Run(&options.App{
		Title:     "Privycs VPN",
		Width:     420,
		Height:    945, // sized for pool indicator (icon + name + policy + member + countdown badge + at-line) + COD banner + connect button + protocol pills + traffic stats + pause + tray
		MinWidth:  380,
		MinHeight: 820,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 17, G: 24, B: 39, A: 255}, // gray-900
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		OnBeforeClose:    app.beforeClose,
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId:               "com.privycs.vpn-client",
			OnSecondInstanceLaunch: app.onSecondInstance,
		},
		Bind: []interface{}{
			app,
		},
		Windows: &windows.Options{
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
			Theme:                windows.Dark,
		},
		Mac: &mac.Options{
			TitleBar:             mac.TitleBarHiddenInset(),
			Appearance:           mac.NSAppearanceNameDarkAqua,
			WebviewIsTransparent: true,
			WindowIsTranslucent:  true,
			About: &mac.AboutInfo{
				Title:   "Privycs VPN",
				Message: "Enterprise VPN Client",
			},
		},
		Linux: &linux.Options{
			ProgramName: "Privycs VPN",
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}
