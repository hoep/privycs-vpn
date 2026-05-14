//go:build !windows

package main

import "fmt"

// Cross-platform stub of the Windows AWG entry points so the
// helper compiles on Linux/macOS. The helper-side branch gates
// `runtime.GOOS == "windows"` so these are never invoked on the
// wrong OS.

func wgWindowsUpAwg(ifaceName, configContent string) error {
	return fmt.Errorf("wgWindowsUpAwg invoked on non-windows build (should never happen)")
}

func wgWindowsDownAwg(ifaceName string) error {
	return fmt.Errorf("wgWindowsDownAwg invoked on non-windows build (should never happen)")
}

func wgWindowsStatusAwg(ifaceName string) (string, bool, error) {
	return "", false, fmt.Errorf("wgWindowsStatusAwg invoked on non-windows build (should never happen)")
}

// v0.9.15.30: per-tunnel Windows-Service AWG entry points. Stubs
// here so the helper compiles on Linux/macOS; live impl lives in
// awg_tunnel_service_windows.go.
func runAWGTunnelService(confPath, ifaceName string) {
	// no-op on non-Windows
}

func installAWGTunnelService(ifaceName, confPath string) error {
	return fmt.Errorf("installAWGTunnelService invoked on non-windows build (should never happen)")
}

func uninstallAWGTunnelService(ifaceName string) error {
	return fmt.Errorf("uninstallAWGTunnelService invoked on non-windows build (should never happen)")
}

func queryAWGTunnelService(ifaceName string) (string, bool, error) {
	return "", false, fmt.Errorf("queryAWGTunnelService invoked on non-windows build (should never happen)")
}

func sweepOrphanedAWGTunnelServices() {
	// no-op on non-Windows
}
