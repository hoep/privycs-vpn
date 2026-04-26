//go:build windows

package main

import "log"

// cmdWlanSSID returns the currently-connected WLAN SSID via the
// privileged helper. The helper runs as LocalSystem; on most
// enterprise GPO configurations the Location-permission restriction
// is applied to user sessions only, so SYSTEM-context calls to
// WlanQueryInterface return the actual SSID where the user-mode app
// would have got an empty string.
//
// Reuses readWLANSSIDViaSyscall from network_monitor_windows.go - the
// same code, but executing inside the helper process which has
// different security context.
func (h *PrivilegedHelper) cmdWlanSSID(cmd HelperCommand) HelperResponse {
	log.Println("Helper: wlan_ssid query (SYSTEM context)")
	ssid, ok := readWLANSSIDViaSyscall()
	if !ok {
		return HelperResponse{
			Success: false,
			Error:   "WlanQueryInterface call failed in helper context",
		}
	}
	// ok=true, ssid="" is a definitive "no connected WLAN" answer
	// (e.g. Ethernet-only machine). Treat as success with empty
	// output so the caller does not retry.
	return HelperResponse{Success: true, Output: ssid}
}
