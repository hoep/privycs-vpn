//go:build !darwin || !cgo

package main

// RegisterMacOSPowerEvents is a no-op on non-darwin platforms AND on
// darwin builds without cgo (the macOS implementation in
// power_macos.go uses cgo + Objective-C with AppKit). The CI macOS
// runner has cgo so the real implementation always ships in
// production builds; this stub keeps `go vet` and Linux-host
// cross-compile checks honest. Linux ships systemd-suspend hooks
// that the OS dispatches to charon-systemd directly; Windows has
// its own WMI power events. Both can be plumbed in later if the
// need arises — for now Privycs's tunnel-health ICMP probe
// (tunnel_health_monitor.go) covers wake-recovery on those platforms
// at ~60 s latency, which has been acceptable in user reports.
func RegisterMacOSPowerEvents(willSleep, didWake func()) {
	_ = willSleep
	_ = didWake
}
