// privileged_helper_ipsec_routes.go — installs explicit per-CIDR
// routes on a Windows IKEv2 VPN connection, sidestepping the
// platform's first-traffic-selector-only limitation.

package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
)

// safeWindowsVPNName — what we accept as a VPN connection name from
// the App-side IPC. Tight allowlist: letters, digits, hyphen,
// underscore, dot, single-quote, space. Mirrors what the App writes
// at Add-VpnConnection time (the user's connection display name,
// possibly with spaces). Prevents shell-injection into the PS
// invocation.
var safeWindowsVPNName = regexp.MustCompile(`^[A-Za-z0-9_.\- ]+$`)

// safeCIDRRoute — a CIDR shape we will pass to Add-VpnConnectionRoute.
// Accepts IPv4 (a.b.c.d/N) and IPv6 (xxxx::yyyy/N). Tighter than the
// generic CIDR check elsewhere because this set goes straight into a
// PowerShell command line.
var safeCIDRRoute = regexp.MustCompile(`^[0-9A-Fa-f:.]+/[0-9]{1,3}$`)

// cmdIPSecInstallWindowsRoutes — applies Set-VpnConnection -SplitTunneling
// $true to the named VPN connection and adds each provided CIDR via
// Add-VpnConnectionRoute. Used by the IPSec connect path on Windows
// after a successful rasdial — fills in the bypass / IPv6 routes the
// platform discards because IKEv2 with MachineCertificate honours
// only the first traffic-selector returned by the server.
//
// Args:
//   - connection_name : the Windows VPN connection name (string,
//     matched against safeWindowsVPNName).
//   - cidrs           : newline-separated list of CIDRs (strings,
//     each matched against safeCIDRRoute). Empty / invalid entries
//     skipped with a logged warning; the operation continues with
//     whatever is valid (degraded > total failure).
//
// Behaviour notes:
//   - Idempotent. Add-VpnConnectionRoute can fail with "the route
//     already exists" if called twice for the same CIDR — we wrap
//     each call in try/catch and continue, so a reconnect against
//     an already-routed connection succeeds.
//   - One PowerShell invocation for the entire batch (single
//     subprocess spawn). For a 304-CIDR script the per-route
//     overhead is dominated by the inner cmdlet, not the outer
//     PowerShell startup — measured ~5-8 s on a typical Windows
//     11 install for 300 routes.
//   - Best-effort with non-fatal per-route failure: a single bad
//     CIDR does not abort the batch. The response Output enumerates
//     ok / fail counts so the caller can log a summary.
func (h *PrivilegedHelper) cmdIPSecInstallWindowsRoutes(cmd HelperCommand) HelperResponse {
	if runtime.GOOS != "windows" {
		return HelperResponse{Success: false, Error: "ipsec_install_windows_routes: windows-only"}
	}

	connName := strings.TrimSpace(cmd.Args["connection_name"])
	if connName == "" {
		return HelperResponse{Success: false, Error: "connection_name required"}
	}
	if !safeWindowsVPNName.MatchString(connName) {
		return HelperResponse{Success: false, Error: "connection_name has illegal characters"}
	}

	rawCidrs := cmd.Args["cidrs"]
	if rawCidrs == "" {
		return HelperResponse{Success: false, Error: "cidrs required"}
	}
	cidrs := strings.Split(rawCidrs, "\n")

	// Filter + validate before building the PS script.
	clean := make([]string, 0, len(cidrs))
	skipped := 0
	for _, c := range cidrs {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if !safeCIDRRoute.MatchString(c) {
			log.Printf("ipsec_install_windows_routes: skip invalid CIDR %q", c)
			skipped++
			continue
		}
		clean = append(clean, c)
	}
	if len(clean) == 0 {
		return HelperResponse{
			Success: false,
			Error:   fmt.Sprintf("no valid CIDRs in %d entries (%d skipped)", len(cidrs), skipped),
		}
	}

	// Build the PowerShell batch. Split-tunneling is enabled exactly
	// once at the top (Set-VpnConnection is idempotent and returns
	// quickly when SplitTunneling is already $true). Each route line
	// is wrapped in try/catch so an "already exists" failure on one
	// CIDR does not abort the batch. $ok / $fail counters at the
	// end produce a single-line summary that we propagate as Output.
	//
	// v1.0.5.18: write the script to a temp file and invoke via
	// `powershell -File <path>` rather than `-Command <inline>`.
	// User-reported on a 302-CIDR route batch: "The filename or
	// extension is too long" — Windows' CreateProcess command-line
	// length limit is ~8 KB effective, and 300 CIDRs of inline PS
	// renders to ~24 KB. The temp-file path bypasses that limit
	// because the script lives on disk, not in the process args.
	// Excluded networks (split-tunneling bypass subnets, from the .sswan).
	// On a full-tunnel IKEv2 SA (negotiated TS = 0.0.0.0/0) Windows installs a
	// default route THROUGH the VPN, so dropping these from the include set is
	// not enough — they need an explicit MORE-SPECIFIC route via the PHYSICAL
	// default gateway, which wins by longest-prefix over the VPN's 0.0.0.0/0.
	// This is the Windows equivalent of Android strongSwan setExcludedSubnets.
	var excl4, excl6 []string
	for _, c := range strings.Split(cmd.Args["excluded_cidrs"], "\n") {
		c = strings.TrimSpace(c)
		if c == "" || !safeCIDRRoute.MatchString(c) {
			continue
		}
		if strings.Contains(c, ":") {
			excl6 = append(excl6, c)
		} else {
			excl4 = append(excl4, c)
		}
	}

	var b strings.Builder
	b.WriteString("$ErrorActionPreference = 'Stop'\n")
	b.WriteString("$ok = 0\n$fail = 0\n$bok = 0\n$bfail = 0\n$v6ok = 0\n$v6fail = 0\n")
	// One-shot split-tunnel enable. Errors here are surfaced (it is
	// the gate that makes Add-VpnConnectionRoute have any effect).
	// All-User scope: the connection is created by the SYSTEM helper in the
	// machine-wide phonebook (visible/dialable by the logged-in user). The
	// previous -AllUserConnection:$false targeted a per-user entry that no
	// longer exists, so split-tunneling + routes silently missed.
	fmt.Fprintf(&b,
		"Set-VpnConnection -Name '%s' -SplitTunneling $true -AllUserConnection -PassThru -ErrorAction Stop | Out-Null\n",
		connName,
	)
	for _, c := range clean {
		fmt.Fprintf(&b,
			"try { Add-VpnConnectionRoute -ConnectionName '%s' -DestinationPrefix '%s' -AllUserConnection -PassThru -ErrorAction Stop | Out-Null; $ok++ } catch { $fail++ }\n",
			connName, c,
		)
	}

	// Bypass routes for the excluded subnets via the PHYSICAL default gateway
	// (the 0.0.0.0/0 / ::/0 route that is NOT on the VPN connection's adapter).
	if len(excl4) > 0 || len(excl6) > 0 {
		fmt.Fprintf(&b,
			"$pv4 = Get-NetRoute -DestinationPrefix '0.0.0.0/0' -ErrorAction SilentlyContinue | "+
				"Where-Object { $_.InterfaceAlias -ne '%s' -and $_.NextHop -ne '0.0.0.0' -and $_.NextHop -ne '::' } | "+
				"Sort-Object RouteMetric | Select-Object -First 1\n", connName)
		fmt.Fprintf(&b,
			"$pv6 = Get-NetRoute -DestinationPrefix '::/0' -ErrorAction SilentlyContinue | "+
				"Where-Object { $_.InterfaceAlias -ne '%s' -and $_.NextHop -ne '::' -and $_.NextHop -ne '0.0.0.0' } | "+
				"Sort-Object RouteMetric | Select-Object -First 1\n", connName)
		// helper that (re)creates a bypass route in the non-persistent
		// ActiveStore so it never outlives a reboot.
		b.WriteString("function Add-Bypass($pfx,$p){ if(-not $p){ $script:bfail++; return }; " +
			"try { Remove-NetRoute -DestinationPrefix $pfx -InterfaceIndex $p.ifIndex -Confirm:$false -ErrorAction SilentlyContinue } catch {}; " +
			"try { New-NetRoute -DestinationPrefix $pfx -InterfaceIndex $p.ifIndex -NextHop $p.NextHop -RouteMetric 1 -PolicyStore ActiveStore -ErrorAction Stop | Out-Null; $script:bok++ } catch { $script:bfail++ } }\n")
		for _, c := range excl4 {
			fmt.Fprintf(&b, "Add-Bypass '%s' $pv4\n", c)
		}
		for _, c := range excl6 {
			fmt.Fprintf(&b, "Add-Bypass '%s' $pv6\n", c)
		}
	}

	// IPv6 through the tunnel. If the IPSec adapter carries a non-link-local v6
	// (the gateway assigned a v6 VIP + NAT66s it — confirmed working on
	// iOS/Android), route global IPv6 (2000::/3) INTO the tunnel. Without this
	// the only ::/0 stays on the physical NIC, so v6 leaves via the physical
	// interface (no working global v6 there) and fails → "only IPv4". The
	// gateway's Windows routes-script is v4-oriented and never lays a working
	// v6 tunnel route, unlike the .sswan/strongSwan path iOS/Android use.
	fmt.Fprintf(&b,
		"$tv6 = Get-NetIPAddress -InterfaceAlias '%s' -AddressFamily IPv6 -ErrorAction SilentlyContinue | "+
			"Where-Object { $_.IPAddress -notlike 'fe80*' } | Select-Object -First 1\n", connName)
	// Use New-NetRoute on the tunnel adapter (by ifIndex) — the SAME mechanism
	// that works for the bypass routes. Add-VpnConnectionRoute didn't take for
	// v6. On-link (point-to-point tunnel, no -NextHop). Remove-first =
	// idempotent on reconnect. ActiveStore = reboot-safe.
	b.WriteString("if ($tv6) { foreach ($p in @('2000::/3','::/0')) { " +
		"try { Remove-NetRoute -DestinationPrefix $p -InterfaceIndex $tv6.ifIndex -Confirm:$false -ErrorAction SilentlyContinue } catch {}; " +
		"try { New-NetRoute -DestinationPrefix $p -InterfaceIndex $tv6.ifIndex -RouteMetric 1 -PolicyStore ActiveStore -ErrorAction Stop | Out-Null; $v6ok++ } catch { $v6fail++ } } }\n")

	b.WriteString("Write-Output \"routes-ok=$ok fail=$fail bypass-ok=$bok bypass-fail=$bfail v6tun-ok=$v6ok v6tun-fail=$v6fail\"\n")

	// Write to a temp .ps1 in the OS temp dir. UTF-8 with BOM so
	// PowerShell reliably picks up the encoding regardless of the
	// system's default ANSI codepage.
	scriptDir := os.TempDir()
	scriptFile, ferr := os.CreateTemp(scriptDir, "privycs-vpn-routes-*.ps1")
	if ferr != nil {
		return HelperResponse{
			Success: false,
			Error:   fmt.Sprintf("create temp script: %v", ferr),
		}
	}
	defer func() {
		_ = scriptFile.Close()
		_ = os.Remove(scriptFile.Name())
	}()
	// UTF-8 BOM
	if _, werr := scriptFile.Write([]byte{0xEF, 0xBB, 0xBF}); werr != nil {
		return HelperResponse{
			Success: false,
			Error:   fmt.Sprintf("write temp script BOM: %v", werr),
		}
	}
	if _, werr := scriptFile.WriteString(b.String()); werr != nil {
		return HelperResponse{
			Success: false,
			Error:   fmt.Sprintf("write temp script body: %v", werr),
		}
	}
	if cerr := scriptFile.Close(); cerr != nil {
		return HelperResponse{
			Success: false,
			Error:   fmt.Sprintf("close temp script: %v", cerr),
		}
	}

	out, err := exec.Command(
		"powershell.exe",
		"-NoProfile", "-NonInteractive",
		"-ExecutionPolicy", "Bypass",
		"-File", scriptFile.Name(),
	).CombinedOutput()
	if err != nil {
		return HelperResponse{
			Success: false,
			Error:   fmt.Sprintf("powershell batch failed: %s: %v", strings.TrimSpace(string(out)), err),
		}
	}
	summary := strings.TrimSpace(string(out))
	log.Printf("ipsec_install_windows_routes: connection=%s candidates=%d skipped=%d → %s",
		connName, len(clean), skipped, summary)
	return HelperResponse{
		Success: true,
		Output: fmt.Sprintf("installed routes for %s (%s, %d candidates, %d skipped pre-validation)",
			connName, summary, len(clean), skipped),
	}
}

// extractAddVpnConnectionRouteCIDRs scans a server-side PowerShell .cmd
// for "-DestinationPrefix '<cidr>'" / "-DestinationPrefix \"<cidr>\""
// directives and returns each unique CIDR. Order-preserving. Used by
// the App-side connect path: parse the stored WindowsRoutesScript
// (downloaded once at gateway import), pass the extracted CIDRs to
// cmdIPSecInstallWindowsRoutes via IPC. We deliberately do NOT
// execute the .cmd directly because the script also contains cert
// imports / Add-VpnConnection invocations that the App already
// handled separately — re-running them would create conflicts.
//
// Lives in the helper file because the cmdline-rule extraction is
// used by both sides (App fetches + parses; helper sanity-checks).
// On non-Windows builds the function is still compile-included (no
// build tag) so the App-side parser is platform-independent.
var addVpnRouteRegex = regexp.MustCompile(
	`(?i)-DestinationPrefix\s+["']([^"']+)["']`,
)

func extractAddVpnConnectionRouteCIDRs(script string) []string {
	matches := addVpnRouteRegex.FindAllStringSubmatch(script, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(matches))
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		c := strings.TrimSpace(m[1])
		if c == "" {
			continue
		}
		if _, dup := seen[c]; dup {
			continue
		}
		seen[c] = struct{}{}
		out = append(out, c)
	}
	return out
}

