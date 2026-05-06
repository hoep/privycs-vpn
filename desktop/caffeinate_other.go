//go:build !darwin

package main

// Caffeinate is macOS-specific. On Linux + Windows the
// PreventDisplaySleep setting is silently ignored — the app and
// settings UI accept it but the start/stop calls become no-ops.
// (Equivalents on other platforms exist — `systemd-inhibit` on Linux,
// SetThreadExecutionState on Windows — but are not wired up yet
// since the user-pain reports are macOS-specific.)
func startCaffeinate() {}
func stopCaffeinate()  {}
