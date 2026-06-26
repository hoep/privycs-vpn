package main

import (
	"embed"
	"os"
	"runtime"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/menu"
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

	// v0.9.15.30: per-tunnel AWG Windows service mode. Helper
	// invokes us as `privycs-vpn.exe --awg-tunnel <confPath> <iface>`
	// to host ONE AmneziaWG tunnel under SCM as the service
	// PrivycsAWGTunnel$<iface>. Process isolation per tunnel
	// (recommended embedding pattern from wireguard.com/embedding,
	// and what the upstream amneziawg-windows project does). On
	// Linux/macOS this is a no-op stub.
	if len(os.Args) >= 4 && os.Args[1] == "--awg-tunnel" {
		runAWGTunnelService(os.Args[2], os.Args[3])
		return
	}

	// v0.9.15.44: dump the amneziawg-windows ringlogger memory-
	// mapped log (their tunnel/service.go writes there after its
	// first InitGlobalLogger call — our file log loses visibility
	// past that point). Run as admin: PowerShell elevated +
	// `privycs-vpn.exe --dump-awg-log`. Output goes to stdout.
	if len(os.Args) >= 2 && os.Args[1] == "--dump-awg-log" {
		dumpAwgLog()
		return
	}

	app := NewApp()

	// macOS menu bar — required for the standard ⌘Q (Quit) keystroke
	// to actually fire. Wails v2 does not auto-wire the App-menu on
	// Mac when options.Menu is nil, which leaves the menu bar
	// effectively empty and ⌘Q with no handler. Symptom: user
	// reports "app lässt sich nicht beenden". The fix is to attach
	// the standard AppMenu (which contains Hide / Hide Others / Quit)
	// plus an EditMenu so Cmd-C / Cmd-V / Cmd-A work in the WebView.
	// On Linux/Windows we leave Menu nil — those platforms quit via
	// the title-bar close button or the system tray icon.
	var appMenu *menu.Menu
	if runtime.GOOS == "darwin" {
		appMenu = menu.NewMenuFromItems(
			menu.AppMenu(),
			menu.EditMenu(),
		)
	}

	err := wails.Run(&options.App{
		Title:     "Privycs VPN",
		Menu:      appMenu,
		Width:     490, // +70 over previous 420 - earlier dimensions felt cramped, especially with pool indicator + unreachable badges + truncated tunnel-names
		Height:    995, // +50 over previous 945 (v0.9.13.8) — extra breathing room around pool indicator + COD banner + connect button + protocol pills + traffic stats + pause + tray
		MinWidth:  450, // bumped together with default Width so the cramped state is no longer the floor
		MinHeight: 870, // +50 to keep proportional to default Height
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
		// Native file-drop: without this, macOS WKWebView (and the GTK/
		// WebView2 webviews) hand the HTML5 @drop handler an EMPTY
		// dataTransfer.files for OS file drags — so dropping a .zip / .conf
		// onto the pool / config drop-zones silently did nothing (no file,
		// no error, no backend call). EnableFileDrop makes Wails deliver the
		// dropped files' absolute paths via runtime.OnFileDrop, which the
		// frontend feeds into the path-based importers (CreatePoolFromPaths /
		// path config import).
		DragAndDrop: &options.DragAndDrop{
			EnableFileDrop: true,
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
