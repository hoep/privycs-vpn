//go:build !windows

package main

// cmdWlanSSID is a Windows-only feature. On Linux / macOS we return
// "not supported" so the dispatch table can compile without
// duplicate-definition errors. SSID detection on those platforms
// uses the existing native paths in network_monitor_linux.go /
// network_monitor_darwin.go and does not currently need a helper-
// IPC path - neither NetworkManager (Linux) nor SCDynamicStore
// (macOS) gates SSID behind a comparable Location-permission GPO.
func (h *PrivilegedHelper) cmdWlanSSID(cmd HelperCommand) HelperResponse {
	return HelperResponse{
		Success: false,
		Error:   "wlan_ssid via helper is Windows-only",
	}
}
