package main

// macOS IPSec/IKEv2 helpers — split-tunnel route management and
// migration cleanup nudges for users coming from earlier Apple-Stack
// builds. The actual IPSec connect path lives in
// protocol_ipsec_macos_swanctl.go (Homebrew strongswan).
//
// History: pre-Phase-1, this file owned a full .mobileconfig +
// AppleScript-System-Events flow that drove macOS's built-in IKEv2
// stack. That entire path was deleted in v0.9.14.x once Sequoia's
// security boundary made it unworkable (Apple-DTS-Forum 663468:
// apps cannot programmatically control profile-installed VPNs). The
// only Apple-Stack remnants kept here are read-only detection
// (`isMacOSVPNConfigInstalled`, `scutilMatchesConnName`) used to
// nudge users who installed a profile under the old build to remove
// it from System Settings.

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// darwinUtunForIP returns the name of the utun interface that holds the given
// (inner/VPN) IP — used to identify the IKEv2 tunnel interface for traffic
// counting (NEVPNManager exposes no byte API). Pure Go (compiles everywhere;
// only meaningful on macOS where the IKEv2 tunnel is a utun).
func darwinUtunForIP(ip string) string {
	target := net.ParseIP(ip)
	if target == nil {
		return ""
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, ifc := range ifaces {
		if !strings.HasPrefix(ifc.Name, "utun") {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			var aip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				aip = v.IP
			case *net.IPAddr:
				aip = v.IP
			}
			if aip != nil && aip.Equal(target) {
				return ifc.Name
			}
		}
	}
	return ""
}

// darwinIfaceBytes returns rx/tx byte counters for an interface by parsing
// `netstat -ibn` (the <Link#…> row carries the totals). Returns (0,0) on any
// parse miss — never errors out the status path. macOS-only at runtime.
func darwinIfaceBytes(iface string) (rx, tx int64) {
	out, err := exec.Command("netstat", "-ibn").CombinedOutput()
	if err != nil {
		return 0, 0
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		// Link row: Name Mtu Network Ipkts Ierrs Ibytes Opkts Oerrs Obytes [Coll]
		// (no Address column) → Ibytes=f[5], Obytes=f[8].
		if len(f) < 9 || f[0] != iface || !strings.HasPrefix(f[2], "<Link") {
			continue
		}
		ib, _ := strconv.ParseInt(f[5], 10, 64)
		ob, _ := strconv.ParseInt(f[8], 10, 64)
		return ib, ob
	}
	return 0, 0
}

// isMacOSVPNConfigInstalled returns true when the macOS profile store
// shows our VPN service. Two detection sources, OR'd, because Apple
// has shuffled the relevant tooling layouts across recent releases
// and a single source has been brittle in practice:
//
//  1. `scutil --nc list` — historical canonical source. Output
//     format varies (quoted vs unquoted name, trailing [IPSec]
//     vs [IKEv2] tag, profile-bundle-installed entries that
//     show under the `Configuration Profile` type instead of
//     `IPSec` on Sonoma+).
//  2. `profiles -L` — lists every installed configuration profile
//     including those whose VPN payload didn't surface in scutil
//     for whatever per-version reason. Deprecated for MDM use but
//     still works for user-installed profiles on Sequoia.
//
// A miss in BOTH sources logs a one-shot diagnostic dump so we can
// spot future format drifts without users having to manually run
// the tools.
func isMacOSVPNConfigInstalled(connName string) bool {
	scutilOut, scutilErr := exec.Command("scutil", "--nc", "list").CombinedOutput()
	if scutilErr == nil && scutilMatchesConnName(string(scutilOut), connName) {
		return true
	}
	profOut, profErr := exec.Command("profiles", "-L").CombinedOutput()
	if profErr == nil && strings.Contains(string(profOut), connName) {
		return true
	}
	// Both detection sources failed. Dump them once so the user can
	// share the actual format with us — we'd rather see real output
	// than speculate. Goes through the regular log file the user
	// already tails.
	log.Printf("isMacOSVPNConfigInstalled(%q): both detection sources reported absent. "+
		"scutil err=%v output:\n%s\nprofiles -L err=%v output:\n%s",
		connName, scutilErr, strings.TrimSpace(string(scutilOut)),
		profErr, strings.TrimSpace(string(profOut)))
	return false
}

// scutilMatchesConnName checks scutil --nc list output for a VPN
// entry whose name matches connName. Accepts the quoted form
// ("Name") that pre-Sonoma scutil emits AND the unquoted form on
// recent System-Settings-installed profiles. Per-line walk so we
// don't false-positive on UUID hex strings that happen to spell
// short names.
func scutilMatchesConnName(output, connName string) bool {
	if strings.Contains(output, `"`+connName+`"`) {
		return true
	}
	for _, line := range strings.Split(output, "\n") {
		colon := strings.Index(line, ":")
		if colon < 0 {
			continue
		}
		tail := strings.TrimSpace(line[colon+1:])
		if br := strings.LastIndex(tail, " ["); br > 0 {
			tail = strings.TrimSpace(tail[:br])
		}
		tail = strings.Trim(tail, `"`)
		if tail == connName {
			return true
		}
	}
	return false
}

// openMacOSProfilesPane launches System Settings into the Profiles
// pane so the user can manually remove a stale leftover Privycs VPN
// profile inherited from a pre-Phase-1 build. Modern macOS does not
// allow programmatic removal of user-installed configuration profiles
// without MDM context — `profiles remove -p <id>` against a user-
// context profile is silently rejected on Sonoma+.
//
// Tries the Ventura+ Settings deeplink first, falls back to the
// legacy preference pane bundle. Either lands the user on the right
// place. Returns silently on failure — this is purely UX.
func openMacOSProfilesPane() {
	deeplinks := []string{
		"x-apple.systempreferences:com.apple.settings.PrivacySecurity.extension?Profiles",
		"/System/Library/PreferencePanes/Profiles.prefPane",
	}
	for _, link := range deeplinks {
		if err := exec.Command("open", link).Run(); err == nil {
			return
		}
	}
}

// macOSDeleteIPSecProfileHint is invoked when the user removes (or
// renames) an IPSec connection in Privycs. Three cleanup paths run
// best-effort:
//
//  1. Wipe the swanctl conf + PEMs via the privileged helper. Primary
//     cleanup since v0.9.14.x — swanctl-via-Homebrew is the only macOS
//     IPSec backend now.
//  2. Wipe leftover .mobileconfig + split-tunnel state from older
//     Apple-Stack builds. Files only exist if the user upgraded from
//     a pre-Phase-1 Privycs; harmless no-op otherwise.
//  3. If a stale Apple-Stack profile is still installed in System
//     Settings (left behind by an old Privycs build), nudge the user
//     to remove it manually — macOS does not allow programmatic
//     profile removal without MDM enrollment.
func macOSDeleteIPSecProfileHint(connName, reason string) {
	if runtime.GOOS != "darwin" {
		return
	}
	// (1) swanctl-managed cleanup via helper (primary path).
	if client := NewHelperClient(); client.IsHelperReachable() {
		if resp, err := client.SendCommand("ipsec_cleanup", map[string]string{
			"connection_name": connName,
		}); err == nil && resp.Success {
			log.Printf("IPSec: helper swanctl cleanup: %s", resp.Output)
		}
	}

	// (2) Legacy artifacts from old Apple-Stack builds.
	mcPath := filepath.Join(appDataDir(), connName+".mobileconfig")
	if err := os.Remove(mcPath); err == nil {
		log.Printf("IPSec: removed legacy cached %s", mcPath)
	}
	deleteMacOSSplitRouteState(connName)

	// (3) Stale Apple-Stack profile nudge — only fires for users who
	// upgraded with an installed-profile inherited from the old build.
	if !isMacOSVPNConfigInstalled(connName) {
		return
	}
	openMacOSProfilesPane()
	Notify(
		"Old VPN profile remains in System Settings",
		fmt.Sprintf("Privycs %s the %q connection. A leftover macOS VPN profile from an earlier build is still installed and is no longer used. Open System Settings → Privacy & Security → Profiles and remove %q manually.",
			reason, connName, connName),
		NotifyInfo,
	)
}

// ============================================================================
// macOS split-tunnel: post-Up CIDR bypass via route(8) through helper
// ============================================================================
//
// Apple's IKEv2 stack does not accept a client-side CIDR bypass list —
// IncludeAllNetworks toggles full vs server-traffic-selectors but the
// .sswan profile.SplitTunneling list has no native carrier. We
// recreate it at the route-table layer: capture the user's pre-VPN
// default gateway + interface, ask the privileged helper to install
// host-routes pinning each bypass CIDR to that gateway. The Apple
// stack's own routes (default/0.0.0.0/1, 128.0.0.0/1 inserted after
// scutil --nc start) overlap but are LESS specific than our /N
// bypasses, so longest-prefix-match keeps the bypass traffic on en0.
//
// Persistence under appDataDir/<connName>.split-routes.json so a
// crashed-mid-session Privycs can clean up the leftover routes on
// next launch.

type macosSplitRouteState struct {
	ConnName string   `json:"conn_name"`
	CIDRsV4  []string `json:"cidrs_ipv4,omitempty"`
	CIDRsV6  []string `json:"cidrs_ipv6,omitempty"`
}

// defaultRouteIPv4 returns the gateway and exit-interface of the
// current IPv4 default route. Empty strings on any parse failure or
// missing route. Mirrors `route -n get default` parsing in-process
// to avoid one more shell-out.
func defaultRouteIPv4() (gateway, iface string, err error) {
	out, runErr := exec.Command("/sbin/route", "-n", "get", "default").Output()
	if runErr != nil {
		return "", "", runErr
	}
	return parseDefaultRouteOutput(string(out))
}

// defaultRouteIPv6 returns the gateway and exit-interface of the
// current IPv6 default route. Empty strings on any parse failure or
// when there is no IPv6 default. Link-local IPv6 gateways carry the
// %iface suffix (e.g. fe80::1%en0) which the helper passes verbatim
// to `route add -inet6`.
func defaultRouteIPv6() (gateway, iface string, err error) {
	out, runErr := exec.Command("/sbin/route", "-n", "get", "-inet6", "default").Output()
	if runErr != nil {
		return "", "", runErr
	}
	return parseDefaultRouteOutput(string(out))
}

// parseDefaultRouteOutput extracts "gateway: <addr>" and
// "interface: <ifn>" lines from `route -n get default` output. The
// format is whitespace-aligned key:value pairs, one per line, and
// has been stable across macOS versions back to at least 10.10.
func parseDefaultRouteOutput(out string) (gateway, iface string, err error) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "gateway:") {
			gateway = strings.TrimSpace(strings.TrimPrefix(line, "gateway:"))
		} else if strings.HasPrefix(line, "interface:") {
			iface = strings.TrimSpace(strings.TrimPrefix(line, "interface:"))
		}
	}
	if gateway == "" || iface == "" {
		return gateway, iface, fmt.Errorf("default route has no gateway/interface fields")
	}
	return gateway, iface, nil
}

// installMacOSSplitTunnelRoutes drives the helper to add the bypass
// routes. Best-effort: a missing helper or a route(8) failure logs and
// notifies the user but does NOT tear down the tunnel — full-tunnel
// connectivity is preferable to "couldn't add bypass route, refuse to
// connect".
func (i *IPSecProtocol) installMacOSSplitTunnelRoutes(gw4, iface4, gw6 string) {
	if runtime.GOOS != "darwin" {
		return
	}
	if gw4 == "" {
		log.Printf("IPSec: split-tunnel skipped — no IPv4 default gateway captured pre-VPN")
		Notify(
			"Split-tunnel routes not applied",
			"Privycs could not capture the LAN default gateway before the tunnel came up. Bypass CIDRs will not be honored this session — disconnect and reconnect with the underlying network already up to retry.",
			NotifyInfo,
		)
		return
	}
	client := NewHelperClient()
	if !client.IsHelperReachable() {
		log.Printf("IPSec: split-tunnel skipped — privileged helper not reachable")
		Notify(
			"Split-tunnel routes not applied",
			"The privileged helper is not running. Install it from Settings → Privileged Helper to enable client-side CIDR bypass on macOS.",
			NotifyInfo,
		)
		return
	}

	cidrsV4, cidrsV6 := splitCIDRsByFamily(i.splitTunneling)
	resp, err := client.SendCommand("ipsec_split_routes_add", map[string]string{
		"gateway_ipv4": gw4,
		"gateway_ipv6": gw6,
		"iface":        iface4,
		"cidrs_ipv4":   strings.Join(cidrsV4, ","),
		"cidrs_ipv6":   strings.Join(cidrsV6, ","),
	})
	if err != nil || !resp.Success {
		log.Printf("IPSec: split-tunnel helper add failed: err=%v resp=%+v", err, resp)
		return
	}
	log.Printf("IPSec: split-tunnel %s", resp.Output)
	persistMacOSSplitRouteState(i.connName, cidrsV4, cidrsV6)
}

// installMacOSV6DefaultRoute asks the helper to add `default ::/0 via
// utun<N>` on macOS so charon-libipsec actually carries IPv6 traffic.
// User-reported 2026-05-29: even with v1.0.5.21's /128 → /64 vip
// rebind fixing source-selection, IPv6 still doesn't leave the tunnel
// — netstat -rn -f inet6 confirms no default v6 route via the utun.
// charon-libipsec on macOS does not install routing entries itself
// (it relies on the IKE peer's traffic-selectors being expressed via
// kernel SPD only). v1.0.5.27 fixes this by installing the v6 default
// route ourselves post-bypass.
//
// ORDERING — CRITICAL: this runs AFTER installMacOSSplitTunnelRoutes
// so any user-configured v6 bypass CIDRs are already in the routing
// table when ::/0 lands. BSD's longest-prefix-match then honors the
// bypass routes over the catch-all default. Reversing the order would
// route bypass-destined v6 traffic through the tunnel for the window
// between the two installs.
//
// Best-effort: a missing helper, an absent v6 vip, or a route(8)
// failure logs and falls through. The tunnel stays up; only IPv6
// connectivity is degraded — same as today's state, so no regression
// over the pre-v1.0.5.27 behaviour.
//
// The v6 vip is obtained via the protocol's own Status() — which on
// macOS pulls swanctl --list-sas and runs the same parser the helper
// uses. We pass the vip to the helper so it can find the right utun
// via helperFindUtunWithV6 (idempotent with the rebind path).
func (i *IPSecProtocol) installMacOSV6DefaultRoute() {
	if runtime.GOOS != "darwin" {
		return
	}
	status := i.Status()
	v6vip := strings.TrimSpace(parseFirstV6(status.LocalAddress))
	if v6vip == "" {
		log.Printf("IPSec v6-default: no v6 vip in Status — tunnel may be v4-only or charon hasn't surfaced the vip yet")
		return
	}
	client := NewHelperClient()
	if !client.IsHelperReachable() {
		log.Printf("IPSec v6-default: helper unreachable, skipping ::/0 install")
		return
	}
	resp, err := client.SendCommand("ipsec_install_macos_v6_default_route", map[string]string{
		"v6_vip": v6vip,
	})
	if err != nil || !resp.Success {
		log.Printf("IPSec v6-default: helper install failed (non-fatal): err=%v resp=%+v", err, resp)
		return
	}
	log.Printf("IPSec v6-default: %s", resp.Output)
}

// parseFirstV6 returns the first IPv6 literal found in a comma- or
// whitespace-separated address list. Returns "" when no v6 literal is
// present. Used to extract the v6 vip from Status().LocalAddress which
// may contain "10.100.114.3, fd45:43:45::3" for dual-stack tunnels.
func parseFirstV6(addrs string) string {
	for _, tok := range strings.FieldsFunc(addrs, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t'
	}) {
		tok = strings.TrimSpace(tok)
		if strings.Contains(tok, ":") {
			// strip an optional /<prefix> suffix
			if i := strings.IndexByte(tok, '/'); i > 0 {
				tok = tok[:i]
			}
			return tok
		}
	}
	return ""
}

// removeMacOSSplitTunnelRoutes loads the persisted state for connName
// and asks the helper to delete each route. Idempotent — missing
// state file or helper failure both no-op (the helper itself ignores
// "not in table" errors). Always wipes the state file last so we
// don't accumulate stale entries across sessions.
func (i *IPSecProtocol) removeMacOSSplitTunnelRoutes() {
	if runtime.GOOS != "darwin" {
		return
	}
	state, err := loadMacOSSplitRouteState(i.connName)
	if err != nil || state == nil {
		return
	}
	client := NewHelperClient()
	if client.IsHelperReachable() {
		resp, err := client.SendCommand("ipsec_split_routes_remove", map[string]string{
			"cidrs_ipv4": strings.Join(state.CIDRsV4, ","),
			"cidrs_ipv6": strings.Join(state.CIDRsV6, ","),
		})
		if err != nil || !resp.Success {
			log.Printf("IPSec: split-tunnel helper remove failed: err=%v resp=%+v", err, resp)
		} else {
			log.Printf("IPSec: split-tunnel %s", resp.Output)
		}
	}
	deleteMacOSSplitRouteState(i.connName)
}

// splitCIDRsByFamily separates a mixed v4/v6 CIDR list into two
// per-family slices. Heuristic: presence of ":" → IPv6, else IPv4.
func splitCIDRsByFamily(cidrs []string) (v4, v6 []string) {
	for _, c := range cidrs {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if strings.Contains(c, ":") {
			v6 = append(v6, c)
		} else {
			v4 = append(v4, c)
		}
	}
	return
}

// macosSplitRouteStatePath returns the per-connection JSON path. Same
// directory as the .sswan / .mobileconfig artifacts so a "scrub user
// state" UI flow can target a single dir.
func macosSplitRouteStatePath(connName string) string {
	return filepath.Join(appDataDir(), connName+".split-routes.json")
}

func persistMacOSSplitRouteState(connName string, v4, v6 []string) {
	state := macosSplitRouteState{ConnName: connName, CIDRsV4: v4, CIDRsV6: v6}
	data, err := json.Marshal(state)
	if err != nil {
		log.Printf("IPSec: split-tunnel state marshal failed: %v", err)
		return
	}
	if err := os.WriteFile(macosSplitRouteStatePath(connName), data, 0600); err != nil {
		log.Printf("IPSec: split-tunnel state write failed: %v", err)
	}
}

func loadMacOSSplitRouteState(connName string) (*macosSplitRouteState, error) {
	data, err := os.ReadFile(macosSplitRouteStatePath(connName))
	if err != nil {
		return nil, err
	}
	var state macosSplitRouteState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

func deleteMacOSSplitRouteState(connName string) {
	_ = os.Remove(macosSplitRouteStatePath(connName))
}

// CleanupMacOSSplitRouteOrphans scans the appDataDir for left-over
// per-connection split-route state files. Each one represents a
// connection that was active when Privycs last died (or rebooted). We
// ask the helper to remove the stale routes and wipe the state file.
// Called once at app start to keep the route table honest after
// unclean shutdowns. Also asks the helper to restore any orphan
// DNS-Override backups (separate state under /var/db/privycs-vpn,
// helper-only-readable, hence helper-driven).
func CleanupMacOSSplitRouteOrphans() {
	if runtime.GOOS != "darwin" {
		return
	}
	// Helper-driven DNS-orphan-cleanup first. /var/db/privycs-vpn is
	// root-only so the user-app can't enumerate it directly.
	if client := NewHelperClient(); client.IsHelperReachable() {
		if resp, err := client.SendCommand("macos_dns_override_clean", nil); err == nil && resp.Success {
			if !strings.Contains(resp.Output, "no backup directory") &&
				!strings.HasPrefix(resp.Output, "restored 0 ") {
				log.Printf("IPSec: %s", resp.Output)
			}
		}
	}

	entries, err := os.ReadDir(appDataDir())
	if err != nil {
		return
	}
	client := NewHelperClient()
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".split-routes.json") {
			continue
		}
		fullPath := filepath.Join(appDataDir(), name)
		data, err := os.ReadFile(fullPath)
		if err != nil {
			_ = os.Remove(fullPath)
			continue
		}
		var state macosSplitRouteState
		if err := json.Unmarshal(data, &state); err != nil {
			_ = os.Remove(fullPath)
			continue
		}
		// If the connection is still Connected in scutil, Privycs likely
		// just got restarted while the OS-level VPN survived. Leave the
		// state file alone — the next downMacOS cleans it up the normal
		// way.
		if isMacOSVPNConfigInstalled(state.ConnName) {
			out, _ := exec.Command("scutil", "--nc", "status", state.ConnName).CombinedOutput()
			if strings.Contains(string(out), "Connected") {
				continue
			}
		}
		log.Printf("IPSec: split-tunnel orphan cleanup for '%s' (%d v4 + %d v6 CIDRs)",
			state.ConnName, len(state.CIDRsV4), len(state.CIDRsV6))
		if client.IsHelperReachable() {
			client.SendCommand("ipsec_split_routes_remove", map[string]string{
				"cidrs_ipv4": strings.Join(state.CIDRsV4, ","),
				"cidrs_ipv6": strings.Join(state.CIDRsV6, ","),
			})
		}
		_ = os.Remove(fullPath)
	}
}
