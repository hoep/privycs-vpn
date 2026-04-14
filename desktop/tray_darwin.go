//go:build darwin

package main

import "log"

// startTray is a no-op on macOS.
// systray conflicts with Wails on macOS because both register an Objective-C
// AppDelegate class, causing duplicate symbol linker errors.
// macOS uses the dock icon instead — the app remains accessible via the dock.
func (a *App) startTray() {
	log.Println("System tray not available on macOS (using dock icon)")
}
