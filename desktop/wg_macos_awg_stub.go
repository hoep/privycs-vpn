//go:build !darwin

package main

import "fmt"

// Cross-platform stub of the darwin AWG entry points so the
// helper compiles on Linux/Windows. The helper-side branch
// gates `runtime.GOOS == "darwin"` so these are never invoked
// on the wrong OS — the stubs exist purely to satisfy the
// compiler.

func wgDarwinUpAwg(friendlyName, configContent string) (string, error) {
	return "", fmt.Errorf("wgDarwinUpAwg invoked on non-darwin build (should never happen)")
}

func wgDarwinDownAwg(friendlyName string) error {
	return fmt.Errorf("wgDarwinDownAwg invoked on non-darwin build (should never happen)")
}
