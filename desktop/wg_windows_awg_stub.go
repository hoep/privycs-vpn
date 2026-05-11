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
