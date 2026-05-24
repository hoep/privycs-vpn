package main

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// IPv6 leak-prevention helper actions. Used by App.connectActiveTarget
// after a successful Up() when (a) the tunnel is IPv4-only AND
// (b) the OS has live IPv6 connectivity. Always-on (no user setting):
// leaving v6 leakable through a v4-only tunnel is a critical security
// bug, not a user preference. Background: a v4-only tunnel installs
// only IPv4 routes; the OS continues to use its native v6 default
// route for AAAA-resolved destinations, which means traffic the user
// expects to be inside the VPN actually exits the device's physical
// interface in the clear. Server-side enforcement is not possible —
// the leak happens client-side before any packet reaches the gateway.
// We block all v6 outbound at the OS firewall level for the duration
// of the v4-only tunnel session, and clear the rules on disconnect.
//
// All three platforms use the most surgical idiom available:
//   - Linux: ip6tables custom chain + jumps. Survives `iptables -F`
//     of the user's other chains. Cleared by deleting the chain.
//   - macOS: pf anchor "privycs-v6-killswitch". pfctl -E enables pf
//     (idempotent); the anchor is loaded with one block rule + a
//     loopback exception. Cleared by flushing the anchor.
//   - Windows: Windows Firewall named rules group. Cleared by
//     removing all rules with the matching DisplayGroup.
//
// Loopback (::1) is always exempted so localhost services that use
// IPv6 sockets don't break.

// cmdIPv6Block installs the OS firewall rules to block all IPv6
// outbound traffic except on loopback. Idempotent — safe to call
// multiple times; the unblock path tears everything down even if
// called on a never-blocked system.
func (h *PrivilegedHelper) cmdIPv6Block(cmd HelperCommand) HelperResponse {
	switch runtime.GOOS {
	case "linux":
		return ipv6BlockLinux()
	case "darwin":
		return ipv6BlockMacOS()
	case "windows":
		return ipv6BlockWindows()
	default:
		return HelperResponse{Success: false, Error: "unsupported OS for ipv6 block"}
	}
}

func (h *PrivilegedHelper) cmdIPv6Unblock(cmd HelperCommand) HelperResponse {
	switch runtime.GOOS {
	case "linux":
		return ipv6UnblockLinux()
	case "darwin":
		return ipv6UnblockMacOS()
	case "windows":
		return ipv6UnblockWindows()
	default:
		return HelperResponse{Success: true, Output: "no-op (unsupported OS)"}
	}
}

// ============================================================================
// Linux — ip6tables custom chain
// ============================================================================

const ipv6LinuxChain = "PRIVYCS_V6_KS"

func ipv6BlockLinux() HelperResponse {
	// Idempotent: tear down any existing chain first, then rebuild.
	ipv6UnblockLinux()
	cmds := [][]string{
		{"ip6tables", "-N", ipv6LinuxChain},
		// Loopback exempt — localhost services using ::1 must still work.
		{"ip6tables", "-A", ipv6LinuxChain, "-o", "lo", "-j", "ACCEPT"},
		// Drop everything else outbound.
		{"ip6tables", "-A", ipv6LinuxChain, "-j", "DROP"},
		// Hook into OUTPUT.
		{"ip6tables", "-I", "OUTPUT", "-j", ipv6LinuxChain},
	}
	for _, args := range cmds {
		out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
		if err != nil {
			// Best-effort cleanup if mid-build something fails so we
			// don't leave half-built rules.
			ipv6UnblockLinux()
			return HelperResponse{
				Success: false,
				Error:   fmt.Sprintf("ip6tables %v: %s: %v", args[1:], strings.TrimSpace(string(out)), err),
			}
		}
	}
	return HelperResponse{Success: true, Output: "ipv6 blocked (Linux ip6tables PRIVYCS_V6_KS chain)"}
}

func ipv6UnblockLinux() HelperResponse {
	// Best-effort: ignore errors at every step (chain may not exist).
	exec.Command("ip6tables", "-D", "OUTPUT", "-j", ipv6LinuxChain).Run()
	exec.Command("ip6tables", "-F", ipv6LinuxChain).Run()
	exec.Command("ip6tables", "-X", ipv6LinuxChain).Run()
	return HelperResponse{Success: true, Output: "ipv6 unblocked (Linux)"}
}

// ============================================================================
// macOS — pfctl anchor
// ============================================================================

const ipv6MacOSAnchor = "privycs-v6-killswitch"

func ipv6BlockMacOS() HelperResponse {
	// Ensure pf is enabled. -E is idempotent; if already enabled it
	// just acks. We don't disable on unblock — pf may be enabled by
	// other tools (Little Snitch, manual config) and we don't want
	// to fight them.
	exec.Command("pfctl", "-E").Run()

	// Anchor rules: pass on lo0 (loopback exempt), block everything
	// else outbound v6.
	rules := `
pass quick out on lo0 inet6 all
block quick out inet6 all
`
	cmd := exec.Command("pfctl", "-a", ipv6MacOSAnchor, "-f", "-")
	cmd.Stdin = strings.NewReader(rules)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return HelperResponse{
			Success: false,
			Error:   fmt.Sprintf("pfctl -f anchor %s: %s: %v", ipv6MacOSAnchor, strings.TrimSpace(string(out)), err),
		}
	}
	return HelperResponse{Success: true, Output: "ipv6 blocked (macOS pf anchor " + ipv6MacOSAnchor + ")"}
}

func ipv6UnblockMacOS() HelperResponse {
	// Flush all rules in our anchor. -F all clears rules + nat + state.
	exec.Command("pfctl", "-a", ipv6MacOSAnchor, "-F", "all").Run()
	return HelperResponse{Success: true, Output: "ipv6 unblocked (macOS)"}
}

// ============================================================================
// Windows — netsh advfirewall named rule group
// ============================================================================

const ipv6WindowsGroup = "Privycs IPv6 Killswitch"

// v1.0.5.13: rule names simplified to single-token ASCII identifiers.
// User-reported on a German-locale Windows: the previous names
// containing parens and spaces ("Privycs IPv6 Killswitch (Allow
// Loopback)") combined with "remoteip=::1/128" + "protocol=any"
// caused netsh advfirewall's argument parser to bail out with
// "Eine angegebene IP-Adresse oder ein angegebenes Adressschlüsselwort
// ist ungültig". Some Windows builds appear to do a secondary
// re-tokenisation of the name value that mis-parses parens, and
// the resulting failure surfaces against the next positional token
// (the IP). The fix is purely syntactic — no functional change.
const (
	ipv6WindowsAllowLoopbackName = "Privycs-V6-AllowLoopback"
	ipv6WindowsBlockOutboundName = "Privycs-V6-BlockOutbound"
)

func ipv6BlockWindows() HelperResponse {
	// Idempotent: clear first.
	ipv6UnblockWindows()
	// v1.0.5.14: switched from netsh advfirewall to PowerShell's
	// New-NetFirewallRule cmdlet. The v1.0.5.13 simplification of
	// the rule name + IP parameters did not solve the user-reported
	// "netsh add allow-loopback ... Eine angegebene IP-Adresse oder
	// ein angegebenes Adressschlüsselwort ist ungültig" — the
	// genuine cause is that the legacy netsh advfirewall parser on
	// some Windows builds rejects bare IPv6 literals (::1, ::/0)
	// at the `remoteip=` keyword, regardless of subnet-prefix
	// presence, rule-name spacing, or other parameter quirks.
	// PowerShell's New-NetFirewallRule uses the modern Defender-
	// Firewall COM API which handles IPv6 addresses natively.
	//
	// PowerShell is available on every Windows 10+ install
	// (Windows PowerShell 5.1 is part of the OS).
	allowScript := `New-NetFirewallRule ` +
		`-DisplayName '` + ipv6WindowsAllowLoopbackName + `' ` +
		`-Description 'Allow IPv6 loopback while v4-only tunnel is active' ` +
		`-Direction Outbound -Action Allow ` +
		`-RemoteAddress '::1' ` +
		`-Profile Any -ErrorAction Stop | Out-Null`
	allowOut, allowErr := exec.Command(
		"powershell.exe", "-NoProfile", "-NonInteractive",
		"-ExecutionPolicy", "Bypass", "-Command", allowScript,
	).CombinedOutput()
	if allowErr != nil {
		return HelperResponse{
			Success: false,
			Error:   fmt.Sprintf("New-NetFirewallRule allow-loopback: %s: %v", strings.TrimSpace(string(allowOut)), allowErr),
		}
	}
	// Block everything else outbound to v6.
	blockScript := `New-NetFirewallRule ` +
		`-DisplayName '` + ipv6WindowsBlockOutboundName + `' ` +
		`-Description 'Block all IPv6 outbound while v4-only tunnel is active' ` +
		`-Direction Outbound -Action Block ` +
		`-RemoteAddress '::/0' ` +
		`-Profile Any -ErrorAction Stop | Out-Null`
	blockOut, blockErr := exec.Command(
		"powershell.exe", "-NoProfile", "-NonInteractive",
		"-ExecutionPolicy", "Bypass", "-Command", blockScript,
	).CombinedOutput()
	if blockErr != nil {
		// Roll back the allow rule so we don't leave a dangling allow.
		ipv6UnblockWindows()
		return HelperResponse{
			Success: false,
			Error:   fmt.Sprintf("New-NetFirewallRule block: %s: %v", strings.TrimSpace(string(blockOut)), blockErr),
		}
	}
	return HelperResponse{Success: true, Output: "ipv6 blocked (Windows Firewall '" + ipv6WindowsGroup + "')"}
}

func ipv6UnblockWindows() HelperResponse {
	// v1.0.5.14: PowerShell Remove-NetFirewallRule for the
	// v1.0.5.14+ rules. Legacy netsh cleanup retained for any
	// pre-existing rules from older installs (some of which
	// may have managed to half-create rules via the netsh path).
	// Remove-NetFirewallRule with -ErrorAction SilentlyContinue
	// no-ops cleanly when the rule does not exist.
	removeScript := `Remove-NetFirewallRule ` +
		`-DisplayName '` + ipv6WindowsAllowLoopbackName + `','` + ipv6WindowsBlockOutboundName + `' ` +
		`-ErrorAction SilentlyContinue | Out-Null`
	exec.Command(
		"powershell.exe", "-NoProfile", "-NonInteractive",
		"-ExecutionPolicy", "Bypass", "-Command", removeScript,
	).Run()
	// Legacy-name cleanup (pre-v1.0.5.13 + v1.0.5.13-only
	// netsh-created rules). Idempotent + silent on miss.
	exec.Command(
		"netsh", "advfirewall", "firewall", "delete", "rule",
		"name=Privycs IPv6 Killswitch (Allow Loopback)",
	).Run()
	exec.Command(
		"netsh", "advfirewall", "firewall", "delete", "rule",
		"name=Privycs IPv6 Killswitch (Block Outbound)",
	).Run()
	exec.Command(
		"netsh", "advfirewall", "firewall", "delete", "rule",
		"name="+ipv6WindowsAllowLoopbackName,
	).Run()
	exec.Command(
		"netsh", "advfirewall", "firewall", "delete", "rule",
		"name="+ipv6WindowsBlockOutboundName,
	).Run()
	return HelperResponse{Success: true, Output: "ipv6 unblocked (Windows)"}
}
