//go:build darwin && !cgo

package main

import "fmt"

// startPlatformWatcher stub for darwin builds without cgo — the
// real implementation in network_monitor_darwin.go uses cgo +
// SystemConfiguration framework callbacks. CI macOS runner ships
// cgo so the real implementation always lands in production
// builds; this stub keeps Linux-host cross-compile `go vet` clean.
//
// Returns an error rather than (no-op, nil) so a nocgo build that
// accidentally shipped would loudly fail at startup rather than
// silently lose network-change detection.
func startPlatformWatcher(callback func()) (stopFn func(), err error) {
	_ = callback
	return nil, fmt.Errorf("network_monitor: SystemConfiguration watcher requires cgo")
}

// getNetworkTypePlatform stub for darwin builds without cgo. Same
// rationale as startPlatformWatcher — real impl in
// network_monitor_darwin.go uses cgo + CoreWLAN. Returns "none" so
// the caller's path doesn't break on a degenerate build.
func getNetworkTypePlatform() string {
	return "none"
}

// getCurrentSSIDPlatform stub for darwin builds without cgo. Real
// impl uses CoreWLAN. Returns "" so the caller treats it as "no
// SSID known" (the same as a foreground-location-denied state on
// real macOS).
func getCurrentSSIDPlatform() string {
	return ""
}
