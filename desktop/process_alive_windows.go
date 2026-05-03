//go:build windows

package main

// isProcessAlive is unused on Windows — the OpenVPN Status() path uses
// `tasklist` instead of a signal-0 liveness probe. Stub kept so the
// shared call site in protocol_openvpn.go compiles on every GOOS.
func isProcessAlive(pid int) bool {
	return false
}
