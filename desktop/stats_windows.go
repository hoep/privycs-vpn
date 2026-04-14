//go:build windows

package main

import (
	"fmt"
	"log"
	"strings"
)

// getWindowsTrafficStats retrieves network adapter traffic statistics on Windows.
// Uses .NET NetworkInterface which works for ALL adapter types: WireGuard (Wintun),
// OpenVPN (TAP/TUN), IPSec/IKEv2 (RAS WAN Miniport), and standard Ethernet.
// Does NOT require admin privileges.
func getWindowsTrafficStats(adapterName string) (rx, tx int64) {
	// Use .NET NetworkInterface — works for WireGuard, OpenVPN, IPSec, all types
	psCmd := fmt.Sprintf(
		`[System.Net.NetworkInformation.NetworkInterface]::GetAllNetworkInterfaces() | `+
			`Where-Object { ($_.OperationalStatus -eq 'Up') -and ($_.Name -eq '%s' -or $_.Name -like '*%s*' -or $_.Description -like '*%s*') } | `+
			`Select-Object -First 1 | `+
			`ForEach-Object { $s = $_.GetIPv4Statistics(); "$($s.BytesReceived) $($s.BytesSent)" }`,
		adapterName, adapterName, adapterName)

	out, err := execHidden("powershell", "-NoProfile", "-Command", psCmd).CombinedOutput()
	if err != nil {
		return 0, 0
	}

	result := strings.TrimSpace(string(out))
	if result == "" {
		return 0, 0
	}

	parts := strings.Fields(result)
	if len(parts) >= 2 {
		fmt.Sscan(parts[0], &rx)
		fmt.Sscan(parts[1], &tx)
	}

	if rx > 0 || tx > 0 {
		log.Printf("Traffic stats for %s: rx=%d tx=%d", adapterName, rx, tx)
	}
	return rx, tx
}

// getLinuxInterfaceStats is a no-op on Windows.
func getLinuxInterfaceStats(ifaceName string) (rx, tx int64) {
	return 0, 0
}
