package main

// macOS IPSec/IKEv2 configuration: translate a strongSwan .sswan profile
// into an Apple Configuration Profile (.mobileconfig) and hand it to
// macOS for user-approved install. Apple's built-in IKEv2 stack
// (NEVPNProtocolIKEv2 driven by scutil --nc) takes over from there.
//
// What this does NOT cover (parity gap with Linux/Windows that the user
// should be aware of):
//   - RFC 8784 PPK: Apple's IKE stack does not implement the PPK_IDENTITY
//     payload, so pq_safe-mixed authentication is silently downgraded to
//     plain certificate auth. The .sswan ppk_id / ppk_psk fields are
//     ignored on macOS today. The fix would be an embedded libcharon
//     (see android/vendor/strongswan path) or a Homebrew swanctl
//     hand-off — both are out of scope for this Phase-1 change.
//   - First-time install requires a System Settings click-through. We
//     cannot bypass that on user-space macOS without an MDM enrollment.
//     The Up() path will fail with "no such config" until the user
//     completes the install dialog; the next Connect tap then succeeds.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"text/template"

	"github.com/google/uuid"
)

// macosMobileConfigTemplate is a minimal Apple .mobileconfig that wraps
// one PKCS#12 payload (the client cert + private key, with chain) and
// one IKEv2 VPN payload referencing it. Mirrors the iOS template the
// gateway emits for `HandleDownloadIPSecIOSProfile` but trimmed to the
// fields actually present in a .sswan profile (no separate CA payload,
// no DoT, no OnDemand — those need server-side data we don't have on
// the client).
const macosMobileConfigTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>PayloadContent</key>
	<array>
		<dict>
			<key>PayloadType</key>
			<string>com.apple.security.pkcs12</string>
			<key>PayloadVersion</key>
			<integer>1</integer>
			<key>PayloadIdentifier</key>
			<string>com.privycs.vpn.cert.{{.CertUUID}}</string>
			<key>PayloadUUID</key>
			<string>{{.CertUUID}}</string>
			<key>PayloadDisplayName</key>
			<string>{{.ConnName}} Certificate</string>
			<key>PayloadContent</key>
			<data>{{.PKCS12Base64}}</data>
			<key>Password</key>
			<string>{{.PKCS12Password}}</string>
		</dict>
		<dict>
			<key>PayloadType</key>
			<string>com.apple.vpn.managed</string>
			<key>PayloadVersion</key>
			<integer>1</integer>
			<key>PayloadIdentifier</key>
			<string>com.privycs.vpn.ikev2.{{.VPNUUID}}</string>
			<key>PayloadUUID</key>
			<string>{{.VPNUUID}}</string>
			<key>PayloadDisplayName</key>
			<string>{{.ConnName}}</string>
			<key>UserDefinedName</key>
			<string>{{.ConnName}}</string>
			<key>VPNType</key>
			<string>IKEv2</string>
			<key>IKEv2</key>
			<dict>
				<key>RemoteAddress</key>
				<string>{{.RemoteAddress}}</string>
				<key>RemoteIdentifier</key>
				<string>{{.RemoteID}}</string>
				<key>LocalIdentifier</key>
				<string>{{.LocalID}}</string>
				<key>AuthenticationMethod</key>
				<string>Certificate</string>
				<key>PayloadCertificateUUID</key>
				<string>{{.CertUUID}}</string>
				<key>EnablePFS</key>
				<true/>
				<key>IncludeAllNetworks</key>
				<{{if .IncludeAllNetworks}}true{{else}}false{{end}}/>
				<key>ExcludeLocalNetworks</key>
				<true/>{{if .HasMTU}}
				<key>TunnelMTU</key>
				<integer>{{.MTU}}</integer>{{end}}
			</dict>{{if .HasDNS}}
			<key>DNS</key>
			<dict>
				<key>ServerAddresses</key>
				<array>{{range .DNSServers}}
					<string>{{.}}</string>{{end}}
				</array>
			</dict>{{end}}
		</dict>
	</array>
	<key>PayloadDisplayName</key>
	<string>{{.ConnName}}</string>
	<key>PayloadDescription</key>
	<string>Privycs IKEv2 VPN profile</string>
	<key>PayloadIdentifier</key>
	<string>com.privycs.vpn.profile.{{.ProfileUUID}}</string>
	<key>PayloadUUID</key>
	<string>{{.ProfileUUID}}</string>
	<key>PayloadType</key>
	<string>Configuration</string>
	<key>PayloadVersion</key>
	<integer>1</integer>
	<key>PayloadOrganization</key>
	<string>Privycs</string>
	<key>PayloadRemovalDisallowed</key>
	<false/>
</dict>
</plist>`

type macosMobileConfigData struct {
	ConnName       string
	RemoteAddress  string
	RemoteID       string
	LocalID        string
	PKCS12Base64   string
	PKCS12Password string
	HasDNS         bool
	DNSServers     []string
	HasMTU         bool
	MTU            int
	// IncludeAllNetworks=true forces every packet through the tunnel
	// (full-tunnel). We set it to false when the .sswan profile carries
	// a non-empty split-tunneling list to defer routing to whatever the
	// server pushes via IKE traffic-selectors. CIDR-level client-side
	// bypass is NOT honored by Apple's IKE stack — that would need a
	// separate post-Up route-manipulation pass via the helper. Until
	// then, the split-tunneling field flips full-vs-server-decides only.
	IncludeAllNetworks bool
	ProfileUUID        string
	VPNUUID            string
	CertUUID           string
}

// configureMacOSFromSSwan generates a .mobileconfig from the parsed
// .sswan profile and hands it to macOS for user-approved install.
// Idempotent: if scutil already lists a VPN with this name, the install
// step is skipped (re-import does not re-prompt the user).
func (i *IPSecProtocol) configureMacOSFromSSwan(profile *sswanProfile) error {
	password := profile.Local.P12Password
	if password == "" {
		// Privycs-default mirrors the Windows fallback in
		// configureWindowsFromSSwan: when the gateway omits the export
		// password the bundle was sealed with the literal string
		// "privycs". Aligns the two desktop platforms.
		password = "privycs"
	}

	// DNS resolution priority matches the Linux/Windows IPSec paths:
	// Settings-driven DnsOverride (populated via SetDnsOverride before
	// Configure runs — see App.applyDnsOverride wrapping in app.go) wins
	// over whatever the .sswan profile carried. Empty override falls
	// back to the .sswan list, which itself may be empty for full
	// "let-server-push-DNS" behaviour.
	dnsServers := profile.DNSServers
	if len(i.dnsOverride) > 0 {
		log.Printf("DNS override (IPSec/macOS): applied %s", strings.Join(i.dnsOverride, ","))
		dnsServers = i.dnsOverride
	}

	data := macosMobileConfigData{
		ConnName:       i.connName,
		RemoteAddress:  profile.Remote.Addr,
		RemoteID:       profile.Remote.ID,
		LocalID:        profile.Local.ID,
		PKCS12Base64:   profile.Local.P12,
		PKCS12Password: password,
		HasDNS:         len(dnsServers) > 0,
		DNSServers:     dnsServers,
		HasMTU:         profile.MTU > 0,
		MTU:            profile.MTU,
		// Empty split-tunneling list -> force full tunnel (the common
		// case). Non-empty -> defer routing to server-pushed traffic
		// selectors. CIDR-level bypass is documented as a separate
		// follow-up (helper-driven post-Up route manipulation).
		IncludeAllNetworks: len(profile.SplitTunneling) == 0,
		// Stable per-connection UUIDs so re-running configure does not
		// produce a "different profile, please replace" prompt — macOS
		// matches by PayloadUUID. profile.UUID is the .sswan-side UUID
		// and is stable across re-imports.
		ProfileUUID: stableUUID("profile:" + profile.UUID),
		VPNUUID:     stableUUID("vpn:" + profile.UUID),
		CertUUID:    stableUUID("cert:" + profile.UUID),
	}

	tmpl, err := template.New("mobileconfig").Parse(macosMobileConfigTemplate)
	if err != nil {
		return fmt.Errorf("parse mobileconfig template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("render mobileconfig: %w", err)
	}

	mcPath := filepath.Join(appDataDir(), i.connName+".mobileconfig")
	if err := os.WriteFile(mcPath, buf.Bytes(), 0600); err != nil {
		return fmt.Errorf("write mobileconfig: %w", err)
	}

	if isMacOSVPNConfigInstalled(i.connName) {
		log.Printf("IPSec: macOS VPN config '%s' already installed, skipping open() prompt", i.connName)
		i.configured = true
		return nil
	}

	// `open` hands the .mobileconfig to System Settings → Privacy &
	// Security → Profiles. The user clicks Install + enters the admin
	// password to actually finalise it. We cannot block on that here —
	// macOS gives no programmatic completion signal — so we return
	// success and let Up() report "scutil: no such service" until the
	// install completes.
	log.Printf("IPSec: opening macOS profile install dialog for %s -> %s", i.connName, profile.Remote.Addr)
	if err := exec.Command("open", mcPath).Run(); err != nil {
		return fmt.Errorf("open mobileconfig install dialog: %w", err)
	}
	i.configured = true
	return nil
}

// isMacOSVPNConfigInstalled returns true when `scutil --nc list` shows
// a VPN service whose name matches connName. Quote-handling mirrors
// scutil's output format (it surrounds names with double quotes).
func isMacOSVPNConfigInstalled(connName string) bool {
	out, err := exec.Command("scutil", "--nc", "list").CombinedOutput()
	if err != nil {
		return false
	}
	needle := `"` + connName + `"`
	return strings.Contains(string(out), needle)
}

// configureMacOSFromMobileConfig accepts a pre-built Apple Configuration
// Profile (.mobileconfig) and hands it to System Settings without going
// through .sswan translation. We extract the connection name from the
// IKEv2 payload's UserDefinedName key so subsequent scutil --nc start
// matches.
//
// Plist parsing is intentionally regex-based to avoid pulling in a
// full plist library for a single key extraction. The format is
// stable XML and the regex tolerates whitespace + indentation
// variations.
func (i *IPSecProtocol) configureMacOSFromMobileConfig(cfg []byte) error {
	name := extractMobileConfigUserDefinedName(string(cfg))
	if name == "" {
		// Fall back to PayloadDisplayName at the root level if the
		// IKEv2 sub-dict didn't carry UserDefinedName. Still no match
		// → reject; we cannot drive scutil without a name.
		name = extractMobileConfigPayloadDisplayName(string(cfg))
	}
	if name == "" {
		return fmt.Errorf("mobileconfig is missing UserDefinedName / PayloadDisplayName — cannot drive scutil")
	}
	i.connName = name
	i.serverAddr = extractMobileConfigRemoteAddress(string(cfg))
	log.Printf("IPSec: importing pre-built .mobileconfig as connection '%s' -> %s", i.connName, i.serverAddr)

	mcPath := filepath.Join(appDataDir(), i.connName+".mobileconfig")
	if err := os.WriteFile(mcPath, cfg, 0600); err != nil {
		return fmt.Errorf("write mobileconfig: %w", err)
	}

	if isMacOSVPNConfigInstalled(i.connName) {
		log.Printf("IPSec: macOS VPN config '%s' already installed, skipping open() prompt", i.connName)
		i.configured = true
		return nil
	}

	if err := exec.Command("open", mcPath).Run(); err != nil {
		return fmt.Errorf("open mobileconfig install dialog: %w", err)
	}
	i.configured = true
	return nil
}

var (
	reMobileUserDefinedName    = regexp.MustCompile(`<key>\s*UserDefinedName\s*</key>\s*<string>([^<]+)</string>`)
	reMobilePayloadDisplayName = regexp.MustCompile(`<key>\s*PayloadDisplayName\s*</key>\s*<string>([^<]+)</string>`)
	reMobileRemoteAddress      = regexp.MustCompile(`<key>\s*RemoteAddress\s*</key>\s*<string>([^<]+)</string>`)
)

// openMacOSProfilesPane launches the user's System Settings (or System
// Preferences on older macOS) into the Profiles pane so they can
// manually remove a leftover Privycs VPN profile. Modern macOS does
// not allow programmatic removal of user-installed configuration
// profiles without MDM context — calling `profiles remove -p <id>`
// against a user-context profile is silently rejected on Sonoma+.
//
// We try the Ventura+ Settings deeplink first, fall back to the
// legacy preference pane bundle. Either one opens the right place;
// the Settings app handles the URL gracefully on any reasonably
// recent macOS. Returns silently on failure — this is purely UX.
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
// renames) an IPSec connection in Privycs. Two cleanup paths run
// best-effort: (1) wipe the per-connection .mobileconfig cache and
// nudge the user toward System Settings → Profiles to remove the
// Apple-Stack profile (macOS does not allow programmatic profile
// removal without MDM); (2) ask the helper to drop the swanctl conf
// + PEMs in case this connection was the PPK-via-swanctl variant.
// Both paths are no-ops if the corresponding artifacts don't exist,
// so we always run both — the connection's history (Apple-Stack vs
// swanctl) isn't reliably tracked across delete-time.
func macOSDeleteIPSecProfileHint(connName, reason string) {
	if runtime.GOOS != "darwin" {
		return
	}
	// (1a) Wipe cached .mobileconfig.
	mcPath := filepath.Join(appDataDir(), connName+".mobileconfig")
	if err := os.Remove(mcPath); err == nil {
		log.Printf("IPSec: removed cached %s", mcPath)
	}

	// (1b) Wipe split-tunnel state file (if a CIDR-bypass run was
	// active), in case Apple-Stack-with-split-tunnel was the variant.
	deleteMacOSSplitRouteState(connName)

	// (2) swanctl-managed cleanup via helper. Best-effort — if no
	// helper, no swanctl conf, no swanctl install, all paths return
	// silently and the connection-delete still completes.
	if client := NewHelperClient(); client.IsHelperReachable() {
		if resp, err := client.SendCommand("ipsec_cleanup", map[string]string{
			"connection_name": connName,
		}); err == nil && resp.Success {
			log.Printf("IPSec: helper swanctl cleanup: %s", resp.Output)
		}
	}

	if !isMacOSVPNConfigInstalled(connName) {
		// No Apple-Stack profile to clean up — nothing for the user to do.
		return
	}

	openMacOSProfilesPane()
	Notify(
		"VPN profile remains in System Settings",
		fmt.Sprintf("Privycs %s the %q connection but cannot remove the macOS VPN profile. Open System Settings → Privacy & Security → Profiles and remove %q manually.",
			reason, connName, connName),
		NotifyInfo,
	)
}

func extractMobileConfigUserDefinedName(content string) string {
	m := reMobileUserDefinedName.FindStringSubmatch(content)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m[1])
}

func extractMobileConfigPayloadDisplayName(content string) string {
	m := reMobilePayloadDisplayName.FindStringSubmatch(content)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m[1])
}

func extractMobileConfigRemoteAddress(content string) string {
	m := reMobileRemoteAddress.FindStringSubmatch(content)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m[1])
}

// stableUUID derives a deterministic UUIDv5 from the given seed. macOS
// uses PayloadUUID to identify a profile across re-installs; using a
// content-derived UUID keeps "edit and re-import" flowing through the
// "Replace existing profile" path instead of accumulating duplicates.
func stableUUID(seed string) string {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("privycs-vpn:"+seed)).String()
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
