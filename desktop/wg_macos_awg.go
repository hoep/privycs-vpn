//go:build darwin

package main

import "fmt"

// wgDarwinUpAwg / wgDarwinDownAwg — Stage 3 macOS in-process
// AmneziaWG entry points. Stubbed in Stage 2 (this commit) to keep
// the helper-side compile clean; Stage 3 implements the real
// amneziawg-go-driven device + tun creation, parallel to
// wg_macos.go's wgDarwinUp/Down for vanilla WG. See
// AMNEZIAWG_CLIENT_PLAN.md §3.2.C for the implementation outline.

func wgDarwinUpAwg(friendlyName, configContent string) (string, error) {
	return "", fmt.Errorf("AmneziaWG on macOS is not yet implemented — Stage 3 of AMNEZIAWG_CLIENT_PLAN.md")
}

func wgDarwinDownAwg(friendlyName string) error {
	return fmt.Errorf("AmneziaWG on macOS is not yet implemented — Stage 3 of AMNEZIAWG_CLIENT_PLAN.md")
}
