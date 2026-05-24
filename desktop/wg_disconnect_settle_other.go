//go:build !windows

package main

import "time"

// waitForVanillaWGServiceGone is a no-op on non-Windows platforms —
// Linux / macOS WireGuard tunnels do not have the Windows-specific
// SCM-service + wintun async-cleanup race that this function
// mitigates. The signature is preserved so call sites can be
// platform-agnostic.
func waitForVanillaWGServiceGone(ifaceName string, maxWait time.Duration) bool {
	_ = ifaceName
	_ = maxWait
	return true
}
