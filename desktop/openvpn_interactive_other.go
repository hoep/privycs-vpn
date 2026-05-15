//go:build !windows

package main

import "errors"

// Non-Windows stub for the OpenVPN Interactive Service client.
// privileged_helper.go is cross-platform (runtime GOOS check, not a
// build tag) so it must be able to reference this symbol on every
// platform; the real implementation lives in
// openvpn_interactive_windows.go. This stub is never reached at
// runtime — the only caller is gated behind `runtime.GOOS ==
// "windows"`.

var errInteractiveServiceUnsupported = errors.New("openvpn interactive service: windows-only")

func startOpenVPNViaInteractiveService(configDir string, args []string) (int, error) {
	return 0, errInteractiveServiceUnsupported
}
