//go:build windows

package main

import (
	"fmt"
	"log"
	"strings"
)

// getWindowsTrafficStats retrieves network adapter traffic statistics on
// Windows. Two probe paths in order:
//
//  1. Get-NetAdapterStatistics — queries the native NDIS driver counters
//     via WMI. Works for DCO-offloaded adapters (ovpn-dco) which skip
//     the IP layer and therefore return zero on .NET's IPv4Statistics.
//     OpenVPN 2.6+ on Windows defaults to ovpn-dco, so this is the
//     common case. No admin privileges required.
//
//  2. .NET NetworkInterface IPv4Statistics — fallback for classic TAP
//     adapters on older OpenVPN / IPSec RAS / Wintun (which routes
//     traffic through the IP stack and populates IPv4Statistics).
//
// The caller passes a substring ("OpenVPN", "WireGuard", "Privycs") —
// both probes match any adapter with that substring in its name or
// description, so "OpenVPN" finds both "OpenVPN TAP-Windows6" and
// "OpenVPN Data Channel Offload" regardless of which driver is loaded.
func getWindowsTrafficStats(adapterName string) (rx, tx int64) {
	// Path 1: Get-NetAdapterStatistics — works for ovpn-dco and Wintun.
	// Filter by InterfaceDescription (more stable than Name which the
	// user can rename). Take Receive/Send Bytes across all OK-status
	// adapters matching the substring. Summing (Select-Object
	// -First 1 | Measure-Object | Sum) makes the query robust to the
	// case where multiple matching adapters exist (stale VPN adapters
	// left over from prior sessions).
	psNetAdapter := fmt.Sprintf(
		`$a = Get-NetAdapter -ErrorAction SilentlyContinue | `+
			`Where-Object { $_.Status -eq 'Up' -and ($_.Name -like '*%s*' -or $_.InterfaceDescription -like '*%s*') } | `+
			`Select-Object -First 1; `+
			`if ($a) { $s = $a | Get-NetAdapterStatistics; "$($s.ReceivedBytes) $($s.SentBytes)" } else { "0 0" }`,
		adapterName, adapterName)

	if out, err := execHidden("powershell", "-NoProfile", "-Command", psNetAdapter).CombinedOutput(); err == nil {
		result := strings.TrimSpace(string(out))
		parts := strings.Fields(result)
		if len(parts) >= 2 {
			fmt.Sscan(parts[0], &rx)
			fmt.Sscan(parts[1], &tx)
			if rx > 0 || tx > 0 {
				log.Printf("Traffic stats for %s (NetAdapter): rx=%d tx=%d", adapterName, rx, tx)
				return rx, tx
			}
		}
	}

	// Path 2: .NET NetworkInterface fallback. Useful when Get-NetAdapter
	// is unavailable (ancient Windows) or the adapter uses classic TAP
	// driver that routes through the IP stack.
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
		log.Printf("Traffic stats for %s (.NET): rx=%d tx=%d", adapterName, rx, tx)
	}
	return rx, tx
}

// getLinuxInterfaceStats is a no-op on Windows.
func getLinuxInterfaceStats(ifaceName string) (rx, tx int64) {
	return 0, 0
}
