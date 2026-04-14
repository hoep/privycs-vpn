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
