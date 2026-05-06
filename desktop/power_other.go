//go:build !darwin

package main

// RegisterMacOSPowerEvents is a no-op on non-darwin platforms. The
// macOS implementation in power_macos.go subscribes to NSWorkspace
// will-sleep / did-wake notifications. Linux ships systemd-suspend
// hooks that the OS dispatches to charon-systemd directly; Windows
// has its own WMI power events. Both can be plumbed in later if the
// need arises — for now Privycs's tunnel-health ICMP probe
// (tunnel_health_monitor.go) covers wake-recovery on those platforms
// at ~60 s latency, which has been acceptable in user reports.
func RegisterMacOSPowerEvents(willSleep, didWake func()) {
	_ = willSleep
	_ = didWake
}
