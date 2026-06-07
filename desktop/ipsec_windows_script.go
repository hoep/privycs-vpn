package main

import (
	"regexp"
	"strings"
)

// $VPNName = "..."  — the connection-name assignment in the gateway's
// Windows IPSec setup script. Only the assignment is rewritten; every later
// $VPNName reference follows automatically.
var vpnNameAssignRe = regexp.MustCompile(`(?m)^\s*\$VPNName\s*=\s*"[^"]*"`)

// rewriteWindowsSetupScriptAllUser adapts the gateway-generated IPSec Windows
// setup script (the polyglot .cmd/.ps1) so the privileged helper can run it as
// LocalSystem WITHOUT a UAC prompt while still producing a VPN connection the
// logged-in user can SEE and DIAL.
//
// Why this is needed: the server script is written for interactive double-
// click — it self-elevates via UAC and creates a PER-USER VPN connection plus
// patches the per-user RAS phonebook ($env:APPDATA\...\rasphone.pbk). Run
// verbatim by the SYSTEM helper, those per-user artefacts land in SYSTEM's own
// profile, invisible/undialable for the user — the reported "two connections,
// one (gw-ipsec-N) without bypass" bug: the app's own all-user create stays
// visible-but-bare while the script's fully-configured connection hides in the
// SYSTEM profile under the peer display name.
//
// All the script's MACHINE-WIDE steps (CA->Trusted Root, certutil PKCS#12,
// StrongCRLCheck / NegotiateDH2048_AES256 registry, NRPT DNS, IPv4 hosts-pin)
// are already correct under SYSTEM. Only the VPN connection + its phonebook
// patch must become ALL-USER (machine-wide phonebook in %ProgramData%, which
// every user can see and rasdial). Three minimal, well-anchored transforms —
// no edits to the ~250 Add-VpnConnectionRoute call sites:
//
//  1. Inject $PSDefaultParameterValues forcing -AllUserConnection on every VPN
//     cmdlet (Add/Set/Remove/Get-VpnConnection + Add-VpnConnectionRoute).
//     Explicit -AllUserConnection args on individual calls still win, so this
//     never conflicts.
//  2. Point the phonebook patch at the all-user phonebook (%ProgramData%
//     instead of the per-user %APPDATA%).
//  3. Rename the connection to the app's slot name (connName) so the connect
//     path (rasdial <connName>) finds it AND the app's own bare all-user
//     Add-VpnConnection (same name) is cleanly REPLACED by the script's
//     Remove+Add rather than duplicated.
func rewriteWindowsSetupScriptAllUser(script, connName string) string {
	preamble := "\n$PSDefaultParameterValues = @{" +
		"'Add-VpnConnection:AllUserConnection'=$true;" +
		"'Set-VpnConnection:AllUserConnection'=$true;" +
		"'Remove-VpnConnection:AllUserConnection'=$true;" +
		"'Get-VpnConnection:AllUserConnection'=$true;" +
		"'Add-VpnConnectionRoute:AllUserConnection'=$true}\n"

	// (1) Inject right after the polyglot batch header (the first "#>"), i.e.
	// at the very start of the PowerShell body. Fallback: prepend.
	if idx := strings.Index(script, "#>"); idx >= 0 {
		cut := idx + len("#>")
		script = script[:cut] + preamble + script[cut:]
	} else {
		script = preamble + script
	}

	// (2) All-user phonebook lives under %ProgramData%, not per-user %APPDATA%.
	script = strings.ReplaceAll(script,
		`$env:APPDATA\Microsoft\Network\Connections\Pbk`,
		`$env:ProgramData\Microsoft\Network\Connections\Pbk`)

	// (3) Align the VPN connection name with the app's slot. ReplaceAllLiteral
	// so the "$" in "$VPNName" is not treated as a regexp group reference.
	if connName != "" {
		repl := `$VPNName = "` + strings.ReplaceAll(connName, `"`, "") + `"`
		script = vpnNameAssignRe.ReplaceAllLiteralString(script, repl)
	}
	return script
}
