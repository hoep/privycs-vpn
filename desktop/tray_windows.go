//go:build windows

package main

import (
	_ "embed"
	"log"
	"time"

	"fyne.io/systray"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

//go:embed build/tray-icon.ico
var trayIconBytes []byte

func (a *App) startTray() {
	systray.Run(func() {
		a.onTrayReady()
	}, func() {})
}

func (a *App) onTrayReady() {
	systray.SetIcon(trayIconBytes)
	systray.SetTitle("Privycs VPN")
	systray.SetTooltip("Privycs VPN Client")

	// Double-click on tray icon shows the main window. fyne/systray
	// 1.12 only emits a single OnTapped callback per click; it has no
	// native WM_LBUTTONDBLCLK plumbing. So we implement classic
	// double-click detection ourselves: two clicks within 500ms count
	// as a double-click. The dispatch is on the systray message-loop
	// goroutine (single-threaded), so a plain int64 is safe without
	// further synchronisation.
	const dblClickWindowMs int64 = 500
	var lastClickMs int64
	systray.SetOnTapped(func() {
		now := time.Now().UnixMilli()
		if now-lastClickMs < dblClickWindowMs {
			lastClickMs = 0
			log.Println("Tray: double-click - showing window")
			if a.ctx != nil {
				wailsRuntime.WindowShow(a.ctx)
				// Two-step always-on-top toggle is the documented
				// Wails idiom to focus a window without keeping it
				// pinned above other windows afterwards.
				wailsRuntime.WindowSetAlwaysOnTop(a.ctx, true)
				wailsRuntime.WindowSetAlwaysOnTop(a.ctx, false)
			}
			return
		}
		lastClickMs = now
	})

	mShow := systray.AddMenuItem("Show Privycs VPN", "Show the main window")
	systray.AddSeparator()
	mConnect := systray.AddMenuItem("Connect", "Connect VPN")
	mDisconnect := systray.AddMenuItem("Disconnect", "Disconnect VPN")
	mDisconnect.Hide()
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit", "Quit Privycs VPN")

	// Periodically update menu items based on connection state.
	// Only update when state CHANGES to avoid interfering with menu display.
	// Frequent Hide/Show calls while the user opens the menu can cause
	// the menu to not appear (known systray library issue).
	ticker := time.NewTicker(2 * time.Second)
	lastConnected := false
	go func() {
		for {
			select {
			case <-ticker.C:
				a.mu.RLock()
				connected := a.connected
				a.mu.RUnlock()
				if connected != lastConnected {
					lastConnected = connected
					if connected {
						mConnect.Hide()
						mDisconnect.Show()
						systray.SetTooltip("Privycs VPN - Connected")
					} else {
						mConnect.Show()
						mDisconnect.Hide()
						systray.SetTooltip("Privycs VPN - Disconnected")
					}
				}
			case <-a.stopStats:
				ticker.Stop()
				return
			}
		}
	}()

	// Handle menu clicks
	go func() {
		for {
			select {
			case <-mShow.ClickedCh:
				log.Println("Tray: Show window")
				if a.ctx != nil {
					wailsRuntime.WindowShow(a.ctx)
					wailsRuntime.WindowSetAlwaysOnTop(a.ctx, true)
					wailsRuntime.WindowSetAlwaysOnTop(a.ctx, false)
				}
			case <-mConnect.ClickedCh:
				log.Println("Tray: Connect")
				go func() { a.Connect("") }()
			case <-mDisconnect.ClickedCh:
				log.Println("Tray: Disconnect")
				go func() { a.Disconnect() }()
			case <-mQuit.ClickedCh:
				log.Println("Tray: Quit requested")
				// Use mutex-protected disconnectInternal to avoid data races
				// and double-disconnect with concurrent UI disconnect
				a.mu.Lock()
				if a.connected {
					log.Println("Tray: Disconnecting tunnel...")
					a.disconnectInternal()
				}
				a.forceQuit = true
				a.mu.Unlock()
				systray.Quit()
				if a.ctx != nil {
					wailsRuntime.Quit(a.ctx)
				}
				return
			case <-a.stopStats:
				return
			}
		}
	}()
}
