//go:build windows

package main

import "fmt"

// wgWindowsUpAwg / wgWindowsDownAwg — Stage 4 Windows in-process
// AmneziaWG entry points. Stubbed in Stage 2 (this commit) so the
// helper-side compile is clean. Stage 4 will implement using
// amneziawg-go's Wintun-driven path, parallel to the macOS
// in-process pattern. No official AWG Windows tunnel-service
// exists today (AmneziaWG project ships their own client only),
// so we own the Wintun fd ourselves from inside the privileged
// helper process.

func wgWindowsUpAwg(ifaceName, configContent string) error {
	return fmt.Errorf("AmneziaWG on Windows is not yet implemented — Stage 4 of AMNEZIAWG_CLIENT_PLAN.md")
}

func wgWindowsDownAwg(ifaceName string) error {
	return fmt.Errorf("AmneziaWG on Windows is not yet implemented — Stage 4 of AMNEZIAWG_CLIENT_PLAN.md")
}
