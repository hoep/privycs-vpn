//go:build !windows

package main

// cmdIPSecInstallWindowsProfile is a no-op on non-Windows platforms.
// The full-RAS-VPN-setup-script execution is Windows-specific (it
// drives the Microsoft Windows RAS / rasphone.pbk + LocalMachine
// cert store). macOS and Linux use their native IPSec stacks
// (Apple .mobileconfig integration on macOS, swanctl on Linux),
// which the App-side import path handles directly.
//
// Returning Success=false (rather than panicking) so a misrouted
// IPC from a buggy client surfaces as a structured error instead
// of crashing the helper.
func (h *PrivilegedHelper) cmdIPSecInstallWindowsProfile(cmd HelperCommand) HelperResponse {
	_ = cmd
	return HelperResponse{
		Success: false,
		Error:   "ipsec_install_windows_profile: Windows-only command issued on non-Windows helper",
	}
}
