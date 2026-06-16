package main

import (
	"context"
	"crypto/sha256"
	encoding_base64 "encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// IPSecProtocol implements VPNProtocol for IKEv2/IPSec connections
type IPSecProtocol struct {
	// tunnelName is the per-profile slot name set by the app layer
	// via SetTunnelName before each Configure (see setTunnelName +
	// tunnelNameForSlot in app.go). Drives the OS-level connName so
	// two IPSec profiles never collide on the Windows phonebook key.
	// Mirrors the pattern WireGuard and OpenVPN already use.
	tunnelName  string
	connName    string
	connectedAt time.Time
	serverAddr  string
	localAddr   string
	configured  bool // true after first successful Configure()
	// windowsConnectedTrustUntil — Windows-only fast-path for the
	// post-rasdial status reporting. rasdial blocks until the IKE
	// SA is fully established, so when upWindows returns nil we
	// already know the tunnel is up — but the existing Status()
	// query spawns a `Get-VpnConnection` PowerShell which costs
	// 200-500 ms per invocation. The app.go connect-path then
	// polls Status() every 250 ms until Connected=true, which
	// can take 1-5 s of pure user-visible "connecting…" latency
	// even though rasdial already proved the tunnel is up. v1.0.5.16
	// optimisation: upWindows sets this timestamp to now+5 s; while
	// the current time is before this deadline, Status() short-
	// circuits with Connected=true and skips the PS spawn. After
	// the deadline, Status() falls back to the live PS query
	// (needed so a tunnel dropping silently is detected by the
	// status emitter / tunnel-health monitor).
	windowsConnectedTrustUntil time.Time
	// splitTunneling holds the .sswan-defined CIDR bypass list. Only
	// the macOS path consumes it (post-Up route-table manipulation
	// via the privileged helper); Linux/Windows do split-tunneling
	// inside the protocol layer (swanctl traffic-selectors / RAS
	// per-route). Mixed v4 + v6, parser sorts at use-site.
	splitTunneling []string
	// usingSwanctl is true on macOS when the .sswan profile carried
	// RFC 8784 PPK material AND Homebrew strongswan is installed; the
	// Configure dispatcher routes through swanctl instead of Apple's
	// IKE stack so the PPK actually negotiates. Determines which
	// upMacOS/downMacOS sub-path runs.
	usingSwanctl bool
	// User-configured DNS-server override from Settings. Populated
	// at Configure() time via SetDnsOverride. Forwarded to the
	// privileged helper on Up() so /etc/resolv.conf is rewritten
	// for the duration of the tunnel; restored on Down().
	// Linux-only; macOS uses .mobileconfig (DNS embedded there) and
	// Windows IPSec uses rasdial which doesn't accept DNS override
	// directly.
	dnsOverride []string
	// v1.0.5.24: macOS swanctl byte-counter sticky-max. parseSwanctlBytes
	// can transiently return 0 during the brief gap between the
	// per-CHILD-SA --ike-filtered query and the all-SA fallback query
	// (lines 236-248 below), and after a rekey the SA database may
	// report a freshly-zeroed counter for a poll tick or two before
	// the new CHILD SA's ESP counters tick up. Either case made the
	// user-visible counter jump backwards to 0 mid-session. Sticky-max
	// holds the highest values observed for this Status()-handler
	// lifetime and never reports lower. Reset to 0 on Up() so a fresh
	// connect starts clean. macOS-only field (Linux/Windows have their
	// own kernel-driven counters that don't have this gap).
	stickyMaxBytesRx int64
	stickyMaxBytesTx int64
	// macOS IPSec traffic: NEVPNManager exposes no inner-IP, so we identify
	// the tunnel utun by diffing the utun set captured just before connect vs
	// once connected (the new one is the tunnel). macosTunIface is then read
	// for rx/tx via netstat. macOS-only.
	macosPreUtuns  map[string]bool
	macosTunIface  string
}

// SetDnsOverride records the user's manual DNS server list (from
// Settings.DNSOverride) so Up()/Down() can pass it through to the
// privileged helper. Empty list = no override (current behaviour).
//
// Called by App.applyDnsOverride() right after Configure() returns.
// Pure setter, no side effects.
func (i *IPSecProtocol) SetDnsOverride(servers []string) {
	i.dnsOverride = servers
}

// SetTunnelName implements the tunnelNamer interface (see setTunnelName
// in app.go). Called by every Configure call-site with the per-profile
// slot name (tunnelNameForSlot(cfg.ID, conn.Name)). The name drives
// the OS-level connection identifier — Windows phonebook key, Linux
// swanctl conn ID, and the on-disk .sswan/.p12 file basenames — so
// two IPSec profiles can coexist without colliding. Empty name is a
// no-op; Configure falls back to a content hash in that case.
func (i *IPSecProtocol) SetTunnelName(name string) {
	if name == "" {
		return
	}
	i.tunnelName = name
}

// ConnectionName returns the Windows phonebook entry name / Linux
// swanctl conn id / macOS .mobileconfig PayloadDisplayName that this
// handler is currently bound to. Set by configureFromStruct from the
// imported config's cfg.ConnectionName; matches the value passed to
// Add-VpnConnection on Windows. Exposed for app-side post-Up route
// installation (see app.go installWindowsIPSecRoutes — uses this name
// as the -ConnectionName arg to Add-VpnConnectionRoute).
func (i *IPSecProtocol) ConnectionName() string {
	return i.connName
}

// IPSecConfig holds IPSec-specific configuration
type IPSecConfig struct {
	ConnectionName string `json:"connection_name"`
	RemoteAddress  string `json:"remote_address"`
	RemoteID       string `json:"remote_id"`
	LocalID        string `json:"local_id"`
	IKEProposals   string `json:"ike_proposals"`
	ESPProposals   string `json:"esp_proposals"`
	CACertPEM      string `json:"ca_cert_pem"`
	ClientCertPEM  string `json:"client_cert_pem"`
	ClientKeyPEM   string `json:"client_key_pem"`
	// BypassNetworks are the .sswan split-tunneling CIDRs that must NOT go
	// through the tunnel (e.g. site-to-site 10.x). On Linux they are carved
	// out of remote_ts so strongSwan's trap policy never captures them.
	BypassNetworks []string `json:"bypass_networks"`
}

// NewIPSecProtocol creates a new IPSec protocol handler
func NewIPSecProtocol() *IPSecProtocol {
	return &IPSecProtocol{
		connName: "privycs-vpn",
	}
}

func (i *IPSecProtocol) Name() string { return "ipsec" }

func (i *IPSecProtocol) IsAvailable() bool {
	switch runtime.GOOS {
	case "linux":
		_, err := exec.LookPath("swanctl")
		return err == nil
	case "darwin":
		return true // macOS has built-in IKEv2
	case "windows":
		return true // Windows has built-in IKEv2
	default:
		return false
	}
}

func (i *IPSecProtocol) Up(ctx context.Context) error {
	if !i.IsAvailable() {
		return fmt.Errorf("IPSec not available on this system")
	}

	// v1.0.5.24: reset macOS sticky-max counter so a fresh session
	// starts at 0 instead of inheriting the previous session's peak.
	// No-op fields on Linux/Windows (they don't consume them).
	i.stickyMaxBytesRx = 0
	i.stickyMaxBytesTx = 0

	switch runtime.GOOS {
	case "linux":
		return i.upLinux(ctx)
	case "darwin":
		return i.upMacOS(ctx)
	case "windows":
		return i.upWindows(ctx)
	default:
		return fmt.Errorf("unsupported platform for IPSec")
	}
}

func (i *IPSecProtocol) Down(ctx context.Context) error {
	switch runtime.GOOS {
	case "linux":
		return i.downLinux(ctx)
	case "darwin":
		return i.downMacOS(ctx)
	case "windows":
		return i.downWindows(ctx)
	default:
		return nil
	}
}

func (i *IPSecProtocol) Status() ProtocolStatus {
	status := ProtocolStatus{
		Protocol:      "ipsec",
		ServerAddress: i.serverAddr,
		LocalAddress:  i.localAddr,
	}

	switch runtime.GOOS {
	case "linux":
		// Query via helper — avoids sudo prompt every 2 seconds.
		client := NewHelperClient()
		if !client.IsHelperReachable() {
			return status
		}
		resp, err := client.SendCommand("status", map[string]string{
			"protocol":  "ipsec",
			"interface": i.connName,
		})
		if err == nil && resp.Success && strings.Contains(resp.Output, "ESTABLISHED") {
			status.Connected = true
			status.ConnectedAt = i.connectedAt.Format(time.RFC3339)
			status.BytesRx, status.BytesTx = parseSwanctlBytes(resp.Output)
			if vip := parseSwanctlVirtualIP(resp.Output); vip != "" {
				i.localAddr = vip
				status.LocalAddress = vip
			}
		}
	case "darwin":
		// macOS NEVPNManager: connected-state from NEVPNConnection.status.
		// Apple's IKEv2 Personal-VPN exposes no byte API, so we read the
		// tunnel utun's kernel counters: macosStatusNEVPN gives the inner VPN
		// IP, we find the utun holding it, and read its rx/tx via netstat.
		if connected, vip := macosStatusNEVPN(i); connected {
			status.Connected = true
			status.ConnectedAt = i.connectedAt.Format(time.RFC3339)
			if vip != "" {
				i.localAddr = vip
				status.LocalAddress = vip
			}
			// Traffic: read the tunnel utun identified at connect (upMacOS
			// snapshot-diff) — macosStatusNEVPN gives no inner IP. Sticky-max
			// so a netstat blip never makes the counter jump backwards.
			if i.macosTunIface != "" {
				rx, tx := darwinIfaceBytes(i.macosTunIface)
				if rx > i.stickyMaxBytesRx {
					i.stickyMaxBytesRx = rx
				}
				if tx > i.stickyMaxBytesTx {
					i.stickyMaxBytesTx = tx
				}
				status.BytesRx = i.stickyMaxBytesRx
				status.BytesTx = i.stickyMaxBytesTx
			}
		}
	case "windows":
		// v1.0.5.16 fast-path: trust the windowsConnectedTrustUntil
		// timestamp set in upWindows for ~5 s after a successful
		// rasdial. Saves the 200-500 ms PowerShell spawn per
		// Status() invocation during the post-Up poll loop in
		// app.go (which polls every 250 ms until Connected=true).
		// User-visible: rasdial returns within ~1 s; without this
		// the App still shows "Connecting…" for 1-5 s while the
		// poll loop accumulates PS spawn overhead.
		if !i.windowsConnectedTrustUntil.IsZero() &&
			time.Now().Before(i.windowsConnectedTrustUntil) {
			status.Connected = true
			status.ConnectedAt = i.connectedAt.Format(time.RFC3339)
			// Skip the PS query + traffic-stats lookup. The status
			// emitter (called every few seconds by the App) will
			// catch up with real-state data after the trust window
			// expires — bytes counter starts populating then.
			return status
		}
		// Look up both per-user AND machine-wide VPN connections — the helper
		// creates with -AllUserConnection, direct-fallback creates per-user.
		psCmd := fmt.Sprintf(
			`$v = Get-VpnConnection -Name '%s' -AllUserConnection -ErrorAction SilentlyContinue; `+
				`if (-not $v) { $v = Get-VpnConnection -Name '%s' -ErrorAction SilentlyContinue }; `+
				`if ($v) { $v.ConnectionStatus } else { 'NotFound' }`,
			escapePowerShellString(i.connName), escapePowerShellString(i.connName))
		out, err := execHidden("powershell", "-NoProfile", "-Command", psCmd).CombinedOutput()
		if err == nil && strings.Contains(string(out), "Connected") {
			status.Connected = true
			status.ConnectedAt = i.connectedAt.Format(time.RFC3339)
			// Windows IPSec/IKEv2 via Add-VpnConnection / rasdial
			// creates an adapter whose alias usually matches the
			// connection name, but on some Win11 builds the adapter
			// shows up as "WAN Miniport (IKEv2)" or has a sanitized
			// name. Try several patterns to be robust.
			status.BytesRx, status.BytesTx = getWindowsTrafficStats(
				i.connName,     // primary - matches default adapter alias
				"IKEv2",        // RAS miniport label
				"WAN Miniport", // generic RAS catch-all
			)
		}
	}

	// No timestamp fallback — only trust actual OS service/connection check.

	return status
}

func (i *IPSecProtocol) Configure(cfg []byte) error {
	// Try parsing as JSON (structured IPSec config)
	var ipsecCfg IPSecConfig
	if err := json.Unmarshal(cfg, &ipsecCfg); err == nil && ipsecCfg.RemoteAddress != "" {
		return i.configureFromStruct(&ipsecCfg)
	}

	// Try parsing as strongSwan .sswan profile
	content := string(cfg)
	if strings.Contains(content, "\"remote\"") || strings.Contains(content, "remote_addrs") {
		return i.configureFromSSwan(cfg)
	}

	// Apple Configuration Profile (.mobileconfig) direct-import is
	// no longer supported on the swanctl path. The previous flow
	// installed the profile in System Settings and drove connect via
	// AppleScript — both of which Sequoia broke (security boundary
	// for app-controlled profile-installed VPNs). Users with only a
	// .mobileconfig should pull the .sswan equivalent from their
	// gateway, or a future App-Store-flavor build (NEVPNManager-cgo)
	// will accept .mobileconfig directly via the in-app NE API.
	if runtime.GOOS == "darwin" && isMobileConfigPlist(content) {
		return fmt.Errorf("macOS .mobileconfig direct-import is no longer supported on this build flavor — request a .sswan profile from your gateway instead")
	}

	return fmt.Errorf("unrecognized IPSec config format")
}

// isMobileConfigPlist returns true when content looks like an Apple
// Configuration Profile (.mobileconfig). We test for the plist DOCTYPE
// AND a Configuration PayloadType to avoid matching unrelated XML
// plists (e.g. launchd plists, app preferences).
func isMobileConfigPlist(content string) bool {
	return strings.Contains(content, "<!DOCTYPE plist") &&
		strings.Contains(content, "<string>Configuration</string>")
}

func (i *IPSecProtocol) configureFromStruct(cfg *IPSecConfig) error {
	if cfg.ConnectionName != "" {
		i.connName = cfg.ConnectionName
	}
	i.serverAddr = cfg.RemoteAddress

	switch runtime.GOOS {
	case "linux":
		return i.configureLinux(cfg)
	case "darwin":
		log.Println("macOS IPSec: use .mobileconfig for full integration")
		return nil
	case "windows":
		return i.configureWindows(cfg)
	default:
		return fmt.Errorf("unsupported platform for IPSec configuration")
	}
}

// sswanProfile represents a strongSwan .sswan profile (Android format)
type sswanProfile struct {
	UUID   string `json:"uuid"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	Remote struct {
		Addr string `json:"addr"`
		ID   string `json:"id"`
	} `json:"remote"`
	Local struct {
		ID          string `json:"id"`
		P12         string `json:"p12"`
		P12Password string `json:"p12-password"`
	} `json:"local"`
	// Optional in the .sswan JSON. Mirrors the Android client's
	// SswanConfig.mtu field. Server emits omitempty so a value of 0
	// means "use Apple/strongSwan auto-detection". Currently only the
	// macOS path consumes this (TunnelMTU in the generated
	// .mobileconfig); Linux/Windows fall through to swanctl/rasdial
	// auto-detection.
	MTU            int      `json:"mtu,omitempty"`
	SplitTunneling []string `json:"split-tunneling"`
	DNSServers     []string `json:"dns-servers"`
	// RFC 8784 Postquantum Preshared Key. Both fields populated only
	// when the source server interface is pq_safe. Carried verbatim
	// to libcharon (Android via vendored strongswan patch, macOS-Pro
	// via Homebrew swanctl when installed). Apple's built-in IKE
	// stack ignores them — pq_safe-secured connections downgrade to
	// cert-only auth on macOS unless Homebrew strongswan is present.
	PPKID  string `json:"ppk_id,omitempty"`
	PPKPSK string `json:"ppk_psk,omitempty"`
	// MacOSSignedProfile is a base64-encoded, S/MIME-signed Apple
	// .mobileconfig built server-side and embedded inline in the
	// .sswan. When populated AND the local platform is macOS, the
	// configureMacOSFromSSwan path writes the signed bytes directly
	// instead of generating an unsigned variant — System Settings
	// then shows "Verified" on install. Empty for .sswan files that
	// originate from a non-Privycs strongSwan server (no signing
	// cert) or older Privycs gateways (pre-v0.8.1.179): client falls
	// back to local generation in that case.
	MacOSSignedProfile string `json:"macos_signed_profile,omitempty"`
}

func (i *IPSecProtocol) configureFromSSwan(cfg []byte) error {
	var profile sswanProfile
	if err := json.Unmarshal(cfg, &profile); err != nil {
		return fmt.Errorf("failed to parse .sswan profile: %w", err)
	}

	if profile.Remote.Addr == "" {
		return fmt.Errorf("invalid .sswan profile: missing remote address")
	}

	// Capture the previously-configured state BEFORE updating the
	// singleton — the OS-specific configure paths need it for a
	// correct cache check (only skip if we were already configured
	// for THIS exact profile, not just any IPSec profile).
	oldConnName, oldServerAddr, oldConfigured := i.connName, i.serverAddr, i.configured

	// Derive a unique-per-profile name. See deriveIPSecConnName for
	// the lookup order. profile.Name is intentionally NOT used: the
	// gateway emits the same name for every profile of a given user,
	// and on Windows that collides at the phonebook key — the bug
	// behind the v0.9.14-and-earlier "second profile doesn't connect"
	// report.
	i.connName = deriveIPSecConnName(i.tunnelName, &profile)
	i.serverAddr = profile.Remote.Addr
	i.splitTunneling = profile.SplitTunneling

	log.Printf("Parsed .sswan profile: %s -> %s (tunnelName=%q, sswan name=%q)",
		i.connName, i.serverAddr, i.tunnelName, profile.Name)

	// Save the raw .sswan for reference (encrypted-at-rest v1.0.0)
	sswanPath := filepath.Join(appDataDir(), i.connName+".sswan")
	EncryptedWriteFile(sswanPath, cfg, 0600)

	// Extract and save PKCS#12 bundle if present (encrypted-at-rest v1.0.0)
	if profile.Local.P12 != "" {
		p12Path := filepath.Join(appDataDir(), i.connName+".p12")
		p12Data, err := base64Decode(profile.Local.P12)
		if err != nil {
			return fmt.Errorf("failed to decode PKCS#12 from .sswan: %w", err)
		}
		if err := EncryptedWriteFile(p12Path, p12Data, 0600); err != nil {
			return fmt.Errorf("failed to write PKCS#12: %w", err)
		}
		log.Printf("PKCS#12 bundle saved to %s (%d bytes)", p12Path, len(p12Data))
	}

	switch runtime.GOOS {
	case "windows":
		return i.configureWindowsFromSSwan(&profile, oldConnName, oldServerAddr, oldConfigured)
	case "linux":
		return i.configureLinuxFromSSwan(&profile)
	case "darwin":
		// macOS: NEVPNManager (Apple built-in IKEv2). The app creates
		// the VPN config in-process and therefore owns it, so it can
		// drive connect/disconnect programmatically — unlike the dead
		// .mobileconfig+AppleScript path (DTS-Forum 663468) and without
		// the Homebrew-strongSwan dependency the old swanctl path needed.
		// .sswan cert + identifiers map straight onto NEVPNProtocolIKEv2
		// (same mapping proven in the iOS port). Requires the Personal
		// VPN entitlement (build/darwin/entitlements.plist).
		i.usingSwanctl = false
		return macosConfigureNEVPN(i, &profile)
	default:
		log.Printf("Platform %s: .sswan saved, manual configuration required", runtime.GOOS)
		return nil
	}
}

func (i *IPSecProtocol) configureWindowsFromSSwan(profile *sswanProfile, oldConnName, oldServerAddr string, oldConfigured bool) error {
	// No cache-skip on Windows.
	//
	// Background: prior builds skipped this function when the same
	// profile was already configured AND its phonebook entry still
	// existed — a roughly-2-second saving on a same-profile Connect.
	// The skip was unsafe though: Windows' LocalMachine\My cert store
	// is GLOBAL state that any sibling IPSec profile mutates between
	// our sessions. After configuring profile A, the singleton's
	// cache flag for profile B is still "fresh-from-last-session" —
	// the user clicks Connect on B, we skip the helper, the cert
	// sweep (v1.0.2) never runs, Windows picks A's cert and the
	// gateway rejects the IKE_AUTH with 13801. Confirmed in the user
	// log on 2026-05-23 21:15:08 ("already configured (cached),
	// skipping" → rasdial 13801).
	//
	// The helper IPC is fast enough (~1-2 s including Import-Pfx and
	// Add-VpnConnection), and rerunning is idempotent
	// (Remove-VpnConnection before Add). Correctness over micro-perf:
	// always run the helper.
	//
	// oldConnName / oldServerAddr / oldConfigured are kept on the
	// function signature in case a future per-profile cert-store
	// inspection wants to bring the cache back smarter.
	_, _, _ = oldConnName, oldServerAddr, oldConfigured

	// One-time legacy cleanup — pre-fix builds always used the shared
	// name "privycs-vpn" for every IPSec profile and left a single
	// phonebook entry behind. With per-profile names that legacy
	// entry is dead weight; silently drop it on each reconfigure so
	// it doesn't shadow the new entries. Best-effort, silent fail.
	if i.connName != "privycs-vpn" {
		execHidden("powershell", "-NoProfile", "-Command",
			"Remove-VpnConnection -Name 'privycs-vpn' -Force -AllUserConnection -ErrorAction SilentlyContinue; "+
				"Remove-VpnConnection -Name 'privycs-vpn' -Force -ErrorAction SilentlyContinue").Run()
	}

	// Proactively remove any stale user-scope VPN entry with the same name.
	// The helper runs as SYSTEM and cannot reach a user's HKCU phonebook, so
	// without this step an older user-scope entry (created by the UAC-fallback
	// path in v0.9.0.12/13) can coexist with the new AllUser entry. When
	// rasdial picks the user-scope one first and its cert reference is stale,
	// IKE auth fails. We run as the user here so HKCU is accessible.
	removeUserScope := execHidden("powershell", "-NoProfile", "-Command",
		fmt.Sprintf(`Remove-VpnConnection -Name '%s' -Force -ErrorAction SilentlyContinue`,
			escapePowerShellString(i.connName)))
	removeUserScope.Run() // best-effort, ignore errors

	p12Password := profile.Local.P12Password
	if p12Password == "" {
		// Empty p12-password field means the server used the default export password.
		p12Password = "privycs"
	}

	log.Printf("IPSec: creating Windows VPN connection '%s' -> %s", i.connName, profile.Remote.Addr)

	// Cert FriendlyName uses the slot-stable connName (e.g.
	// "gw-ipsec-51") so the entry is identifiable in Windows
	// Certificate Manager AND correlates 1-to-1 with the slot identifier
	// the Privycs log emits. Re-using the user-facing profile.Name
	// would be ambiguous when two profiles happen to share a display
	// label or when the .sswan was generated without a name field.
	friendlyLabel := i.connName

	client := NewHelperClient()
	if client.IsHelperReachable() {
		// Helper-based path: no UAC prompt, cert goes to LocalMachine\My.
		resp, err := client.SendCommand("ipsec_configure", map[string]string{
			"conn_name":      i.connName,
			"server_address": profile.Remote.Addr,
			"p12_base64":     profile.Local.P12,
			"p12_password":   p12Password,
			"friendly_label": friendlyLabel,
		})
		if err != nil {
			return fmt.Errorf("helper ipsec_configure failed: %w", err)
		}
		if !resp.Success {
			return fmt.Errorf("ipsec configure via helper: %s", resp.Error)
		}
		i.configured = true
		log.Printf("Windows IKEv2 VPN connection created via helper: %s -> %s", i.connName, profile.Remote.Addr)
		// v1.0.5: surface the helper's PowerShell Write-Host diagnostics
		// (ipsec-helper: lines) in the client log even on success — they
		// carry the cert-store sweep counts + thumbprints the helper saw,
		// which is the only way to debug a still-failing rasdial 13801
		// from outside the SYSTEM process.
		if s := strings.TrimSpace(resp.Output); s != "" {
			for _, ln := range strings.Split(s, "\n") {
				ln = strings.TrimRight(ln, "\r")
				if ln != "" {
					log.Printf("helper: %s", ln)
				}
			}
		}
		return nil
	}

	// Fallback: UAC-elevated setup (one-time admin prompt).
	// PowerShell's Import-PfxCertificate reads the file via -FilePath
	// directly from disk — it cannot decrypt our encrypted-at-rest
	// blob. So write a PLAIN temp file just for this PS invocation,
	// delete it immediately after. The persistent encrypted copy in
	// appDataDir() stays untouched.
	var p12Path string
	if profile.Local.P12 != "" {
		data, derr := base64Decode(profile.Local.P12)
		if derr != nil {
			return fmt.Errorf("decode p12 for UAC fallback: %w", derr)
		}
		tmp, terr := os.CreateTemp("", "privycs-ipsec-*.p12")
		if terr != nil {
			return fmt.Errorf("create temp p12: %w", terr)
		}
		tmp.Chmod(0600)
		if _, err := tmp.Write(data); err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
			return fmt.Errorf("write temp p12: %w", err)
		}
		tmp.Close()
		p12Path = tmp.Name()
		defer os.Remove(p12Path) // best-effort cleanup
	} else {
		// caller relies on the previously-stored .p12 — fall back to
		// the persistent path (won't help if encrypted, but matches
		// pre-v1.0 behaviour for users without P12 inline data).
		p12Path = filepath.Join(appDataDir(), i.connName+".p12")
	}

	// See privileged_helper.go cmdIPSecConfigureWindows for the multi-
	// profile cert-pick fix rationale: Windows MachineCertificate auth
	// picks the first matching cert in the store, so when two Privycs
	// IPSec profiles share an issuing CA the 2nd dial sends the 1st
	// profile's cert + IDi → 13801. Sweep the store for prior Privycs
	// IPSec certs, tag the new one by FriendlyName, then re-create the
	// phonebook entry. The FriendlyName uses the slot-stable connName
	// so the cert entry correlates 1-to-1 with the slot ID in the
	// Privycs log.
	// v1.0.4: same fix as cmdIPSecConfigureWindows in privileged_helper.
	// See that function's comment for the array-comparison root cause.
	psScript := fmt.Sprintf(`
$ErrorActionPreference = 'Stop'
$p12Password = ConvertTo-SecureString -String '%s' -AsPlainText -Force
$friendly = 'Privycs IPSec - %s'
$store = 'Cert:\LocalMachine\My'
try {
    $imported = @(Import-PfxCertificate -FilePath '%s' -CertStoreLocation $store -Password $p12Password -ErrorAction Stop)
} catch {
    $store = 'Cert:\CurrentUser\My'
    $imported = @(Import-PfxCertificate -FilePath '%s' -CertStoreLocation $store -Password $p12Password)
}
$myThumbs = @($imported | ForEach-Object { $_.Thumbprint })
$leaf = $imported | Where-Object { $_.Issuer -ne $_.Subject } | Select-Object -First 1
if (-not $leaf) { $leaf = $imported[0] }
$myIssuer = $leaf.Issuer
@(Get-ChildItem $store | Where-Object {
    ($_.Thumbprint -notin $myThumbs) -and (
        ($_.FriendlyName -like 'Privycs IPSec - *') -or ($_.Issuer -eq $myIssuer)
    )
}) | Remove-Item -Force -ErrorAction SilentlyContinue
$leaf.FriendlyName = $friendly
Remove-VpnConnection -Name '%s' -Force -ErrorAction SilentlyContinue
Add-VpnConnection -Name '%s' -ServerAddress '%s' -TunnelType IKEv2 -AuthenticationMethod MachineCertificate -EncryptionLevel Required -RememberCredential -Force
`, escapePowerShellString(p12Password), escapePowerShellString(friendlyLabel),
		escapePowerShellString(p12Path), escapePowerShellString(p12Path),
		escapePowerShellString(i.connName),
		escapePowerShellString(i.connName), escapePowerShellString(profile.Remote.Addr))

	cmd := execHidden("powershell", "-NoProfile", "-Command",
		fmt.Sprintf("Start-Process powershell -ArgumentList '-NoProfile','-Command','%s' -Verb RunAs -Wait -WindowStyle Hidden",
			escapePowerShellString(psScript)))
	setupOut, setupErr := cmd.CombinedOutput()
	if setupErr != nil {
		safeOutput := redactCredentials(string(setupOut), p12Password)
		return fmt.Errorf("failed to configure IPSec on Windows: %s: %w", safeOutput, setupErr)
	}

	i.configured = true
	log.Printf("Windows IKEv2 VPN connection created via UAC fallback: %s -> %s", i.connName, profile.Remote.Addr)
	return nil
}

func (i *IPSecProtocol) configureLinuxFromSSwan(profile *sswanProfile) error {
	// Convert .sswan to swanctl config
	ipsecCfg := &IPSecConfig{
		ConnectionName: i.connName,
		RemoteAddress:  profile.Remote.Addr,
		RemoteID:       profile.Remote.ID,
		LocalID:        profile.Local.ID,
		BypassNetworks: profile.SplitTunneling,
	}
	return i.configureLinux(ipsecCfg)
}

// parseSwanctlVirtualIP extracts the negotiated virtual IPv4/v6
// addresses from `swanctl --list-sas` output. charon emits them on a
// `local` line right after the IKE_SA peer addresses, e.g.:
//
//	local 'foo@bar' @ 1.2.3.4[4500] [10.100.113.6]
//
// or on a separate line:
//
//	local  10.100.113.6/32
//
// (depending on swanctl version + whether vips were negotiated). We
// match BOTH forms because charon switched format twice between 5.9
// and 5.10 — the bracket form is the older one, the indented `local`
// is current. Returns the addresses as a comma-separated CIDR string
// matching the WireGuard format the UI's splitAddresses() expects.
//
// Returns empty string if no virtual IP could be parsed (server
// didn't push one, or the SA is in negotiation). Caller should keep
// the previous localAddr in that case rather than wiping it.
func parseSwanctlVirtualIP(output string) string {
	// Form 1: bracketed virtual IP on the local-id line (older).
	reBracket := regexp.MustCompile(`^\s+local\s+'[^']*'\s+@\s+\S+\s+\[([0-9a-fA-F:.,/\s]+)\]`)
	// Form 2: dedicated indented local-CIDR line (current).
	reLocal := regexp.MustCompile(`^\s+local\s+([0-9a-fA-F:.]+/\d+)(?:\s|$)`)

	var parts []string
	for _, line := range strings.Split(output, "\n") {
		if m := reBracket.FindStringSubmatch(line); m != nil {
			for _, p := range strings.Split(m[1], ",") {
				p = strings.TrimSpace(p)
				if p == "" {
					continue
				}
				if !strings.Contains(p, "/") {
					// Bracket form omits the prefix — assume host route.
					if strings.Contains(p, ":") {
						p += "/128"
					} else {
						p += "/32"
					}
				}
				parts = append(parts, p)
			}
			continue
		}
		if m := reLocal.FindStringSubmatch(line); m != nil {
			parts = append(parts, m[1])
		}
	}
	// Dedup — both regexes can match concurrently on some swanctl
	// versions. Preserve order.
	seen := map[string]bool{}
	uniq := make([]string, 0, len(parts))
	for _, p := range parts {
		if seen[p] {
			continue
		}
		seen[p] = true
		uniq = append(uniq, p)
	}
	return strings.Join(uniq, ", ")
}

// parseSwanctlBytes extracts inbound/outbound byte counters from
// `swanctl --list-sas` human-readable output. Each ESTABLISHED CHILD
// SA carries two indented data lines we care about:
//
//	in  c1234567,    1234 bytes,     12 packets,     5s ago
//	out 89abcdef,    2345 bytes,     23 packets,     5s ago
//
// `bytes_i` (in) = bytes received on the SA = data from peer to us =
// the user-visible "RX". `bytes_o` (out) = "TX". Sums across all
// matching CHILD SAs so multi-tunnel / split-tunnel setups (where
// bytes are spread across several SAs that share an IKE_SA) report
// the aggregate. Lines that don't match the regex are ignored, so
// minor format drifts across strongSwan versions don't crash the
// parse — they just keep the running total at 0 and we degrade
// gracefully.
//
// Why client-side and not in the helper: the helper already returns
// the raw output for the connectivity check (ESTABLISHED string match).
// Parsing client-side avoids a helper API change + reinstall on every
// release that wants tweaked stats handling.
func parseSwanctlBytes(output string) (rx, tx int64) {
	re := regexp.MustCompile(`^\s+(in|out)\s+[0-9a-f]+,\s+(\d+)\s+bytes`)
	for _, line := range strings.Split(output, "\n") {
		m := re.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		n, err := strconv.ParseInt(m[2], 10, 64)
		if err != nil {
			continue
		}
		switch m[1] {
		case "in":
			rx += n
		case "out":
			tx += n
		}
	}
	return rx, tx
}

// escapePowerShellString escapes single quotes for nested PowerShell execution
// deriveIPSecConnName picks a unique-per-profile OS connection name.
// Used cross-platform: on Windows it's the phonebook key, on Linux
// the swanctl conn ID, everywhere the on-disk file basename.
//
// Priority of disambiguators:
//  1. tunnelName — set by the app layer (SetTunnelName) from the
//     stable per-connection slot ID (e.g. "gw-ipsec-7"). The right
//     answer when present.
//  2. profile.UUID — per-profile UUID from the .sswan, gateway-emitted.
//  3. SHA256 of remote+cert — last-resort content hash.
//
// We deliberately do NOT use profile.Name: gateways often emit the same
// human-readable name for every profile of a given user, and on
// Windows that would collide at the phonebook key (the bug fix for
// v0.9.14-and-earlier desktop builds).
func deriveIPSecConnName(tunnelName string, profile *sswanProfile) string {
	if tunnelName != "" {
		return tunnelName
	}
	if u := strings.ReplaceAll(profile.UUID, "-", ""); len(u) >= 8 {
		return "privycs-ipsec-" + u[:8]
	}
	h := sha256.Sum256([]byte(profile.Remote.Addr + ":" + profile.Local.P12))
	return "privycs-ipsec-" + hex.EncodeToString(h[:])[:8]
}

func escapePowerShellString(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

// base64Decode decodes standard or URL-safe base64
func base64Decode(s string) ([]byte, error) {
	// Try standard base64 first
	data, err := base64StdDecode(s)
	if err == nil {
		return data, nil
	}
	// Try URL-safe base64
	return base64StdDecode(strings.NewReplacer("-", "+", "_", "/").Replace(s))
}

func base64StdDecode(s string) ([]byte, error) {
	// Add padding if needed
	switch len(s) % 4 {
	case 2:
		s += "=="
	case 3:
		s += "="
	}
	return encoding_base64.StdEncoding.DecodeString(s)
}

// ============================================================================
// Linux: strongSwan swanctl
// ============================================================================

// linuxFullTS is the dual-stack full tunnel: both families captured.
const linuxFullTS = "0.0.0.0/0, ::/0"

// computeLinuxRemoteTS turns the .sswan split-tunneling (bypass) CIDRs into the
// strongSwan child remote_ts. With no bypass it returns the full dual-stack
// tunnel "0.0.0.0/0, ::/0"; with bypass nets it returns the v4+v6 complement of
// those nets so strongSwan's start_action=trap never traps the bypass CIDRs --
// they flow via the local gateway. IPv6 is now tunnelled (Variant B): the v6
// bypass entries are honoured and ::/0 is captured. If the gateway IKE peer
// only negotiates a v4 child SA, the v6 trap simply has no SA and v6 traffic is
// dropped (contained, not leaked) until the server offers v6. Malformed entries
// are skipped.
func computeLinuxRemoteTS(bypass []string) string {
	var nets []Cidr
	for _, s := range bypass {
		c, err := ParseCidr(s)
		if err != nil {
			continue
		}
		nets = append(nets, c)
	}
	if len(nets) == 0 {
		return linuxFullTS
	}
	var parts []string
	for _, c := range SubtractFromUniverse(nets) {
		parts = append(parts, c.String())
	}
	if len(parts) == 0 {
		// Bypass covered the entire address space -> nothing to tunnel. Fall
		// back to the full tunnel rather than emitting an empty remote_ts.
		return linuxFullTS
	}
	return strings.Join(parts, ", ")
}

func (i *IPSecProtocol) configureLinux(cfg *IPSecConfig) error {
	ikeProposals := cfg.IKEProposals
	if ikeProposals == "" {
		ikeProposals = "aes256-sha256-modp2048"
	}
	espProposals := cfg.ESPProposals
	if espProposals == "" {
		espProposals = "aes256-sha256-modp2048"
	}

	remoteTS := computeLinuxRemoteTS(cfg.BypassNetworks)
	if remoteTS != linuxFullTS {
		log.Printf("IPSec Linux split-tunnel: bypass carved out, remote_ts=%s", remoteTS)
	}

	// Request a dual-stack virtual IP (v4 + v6) via IKEv2 config-mode so the
	// tunnel has a v6 source address to carry ::/0. The server assigns from
	// whichever pools it has; a v4-only server simply ignores the :: request
	// (no v6 vip -> v6 stays contained). Mirrors the Windows/macOS/Android
	// clients, which always request a config-mode address.
	swanctlConf := fmt.Sprintf(`connections {
    %s {
        version = 2
        remote_addrs = %s
        vips = 0.0.0.0, ::
        encap = yes
        mobike = yes
        dpd_delay = 60s
        local {
            auth = pubkey
            id = %s
            certs = privycs-client.pem
        }
        remote {
            auth = pubkey
            id = %s
        }
        children {
            %s {
                remote_ts = %s
                start_action = trap
                dpd_action = restart
                esp_proposals = %s
            }
        }
        proposals = %s
    }
}
`, cfg.ConnectionName, cfg.RemoteAddress, cfg.LocalID, cfg.RemoteID,
		cfg.ConnectionName, remoteTS, espProposals, ikeProposals)

	client := NewHelperClient()
	if !client.IsHelperReachable() {
		return fmt.Errorf("privileged helper not running — install it in Settings → Privileged Helper")
	}
	resp, err := client.SendCommand("ipsec_configure", map[string]string{
		"conn_name":    cfg.ConnectionName,
		"ca_cert":      cfg.CACertPEM,
		"client_cert":  cfg.ClientCertPEM,
		"client_key":   cfg.ClientKeyPEM,
		"swanctl_conf": swanctlConf,
	})
	if err != nil {
		return fmt.Errorf("helper ipsec_configure failed: %w", err)
	}
	if !resp.Success {
		return fmt.Errorf("ipsec configure failed: %s", resp.Error)
	}
	log.Printf("IPSec config installed and loaded via helper")
	return nil
}

func (i *IPSecProtocol) upLinux(ctx context.Context) error {
	client := NewHelperClient()
	if !client.IsHelperReachable() {
		return fmt.Errorf("privileged helper not running — install it in Settings → Privileged Helper")
	}
	args := map[string]string{
		"protocol":        "ipsec",
		"interface":       i.connName,
		"connection_name": i.connName,
	}
	// DNS override forwarding. Helper writes /etc/resolv.conf with
	// backup before swanctl --initiate; on disconnect the backup is
	// restored. Empty string = no override = helper skips the DNS
	// dance.
	if len(i.dnsOverride) > 0 {
		args["dns_servers"] = strings.Join(i.dnsOverride, " ")
		log.Printf("IPSec DNS override: %s", args["dns_servers"])
	}
	resp, err := client.SendCommand("connect", args)
	if err != nil {
		return fmt.Errorf("helper connect failed: %w", err)
	}
	if !resp.Success {
		return fmt.Errorf("swanctl initiate failed: %s", resp.Error)
	}
	i.connectedAt = time.Now()
	log.Println("IPSec connected via helper")
	return nil
}

func (i *IPSecProtocol) downLinux(ctx context.Context) error {
	client := NewHelperClient()
	if client.IsHelperReachable() {
		client.SendCommand("disconnect", map[string]string{
			"protocol":        "ipsec",
			"interface":       i.connName,
			"connection_name": i.connName,
		})
	}
	i.connectedAt = time.Time{}
	log.Println("IPSec disconnected")
	return nil
}

// ============================================================================
// macOS: scutil (built-in IKEv2)
// ============================================================================

func (i *IPSecProtocol) upMacOS(ctx context.Context) error {
	// macOS: NEVPNManager drives the connect. Apple's IKEv2 stack
	// installs its own routing (incl. the default route + DNS pushed
	// in the IKE_AUTH CFG_REPLY), so the manual swanctl-era split-
	// tunnel + v6-default-route helper calls are no longer needed.
	// Snapshot utuns first so we can identify the tunnel utun (for the
	// traffic counter) as the new one that appears after connect.
	i.macosPreUtuns = darwinUtunNames()
	i.macosTunIface = ""
	if err := macosUpNEVPN(i, ctx); err != nil {
		return err
	}
	for name := range darwinUtunNames() {
		if !i.macosPreUtuns[name] {
			i.macosTunIface = name
			break
		}
	}
	return nil
}

func (i *IPSecProtocol) downMacOS(ctx context.Context) error {
	i.macosTunIface = ""
	i.macosPreUtuns = nil
	return macosDownNEVPN(i, ctx)
}

// ============================================================================
// Windows: PowerShell Add-VpnConnection / rasdial
// ============================================================================

func (i *IPSecProtocol) configureWindows(cfg *IPSecConfig) error {
	// Fast path: already configured in this session
	if i.configured && i.serverAddr == cfg.RemoteAddress {
		return nil
	}
	// First time after app start: check if Windows already has this connection
	checkCmd := fmt.Sprintf(
		`(Get-VpnConnection -Name '%s' -ErrorAction SilentlyContinue).ServerAddress`,
		cfg.ConnectionName)
	chkOut, chkErr := execHidden("powershell", "-NoProfile", "-Command", checkCmd).CombinedOutput()
	if chkErr == nil && strings.TrimSpace(string(chkOut)) == cfg.RemoteAddress {
		i.configured = true
		log.Printf("IPSec: '%s' already exists in Windows, skipping create", cfg.ConnectionName)
		return nil
	}

	psScript := fmt.Sprintf(
		`Add-VpnConnection -Name '%s' -ServerAddress '%s' -TunnelType IKEv2 -AuthenticationMethod MachineCertificate -EncryptionLevel Required -Force`,
		cfg.ConnectionName, cfg.RemoteAddress)

	out, err := execHidden("powershell", "-Command", psScript).CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to create VPN connection: %s: %w", string(out), err)
	}
	i.configured = true
	log.Printf("Windows IKEv2 connection created: %s", cfg.ConnectionName)
	return nil
}

func (i *IPSecProtocol) upWindows(ctx context.Context) error {
	log.Printf("Connecting IPSec %s via rasdial...", i.connName)

	// rasdial is purely CLI — no dialog, synchronous exit code reflects the
	// final connect state. Switched back from rasphone -d in v0.9.0.17 because
	// the brief "Connecting..." dialog was visible to users. The earlier
	// rasdial "IKE-Auth-not-acceptable" failures that pushed us to rasphone
	// turned out to be a strongSwan-server-side cert/SAN mismatch, not a
	// rasdial bug — fixed separately in the Agent's IPSec config generator.
	out, err := execHiddenContext(ctx, "rasdial", i.connName).CombinedOutput()
	if err != nil {
		return fmt.Errorf("rasdial failed: %s: %w", string(out), err)
	}
	i.connectedAt = time.Now()
	// v1.0.5.16: short-circuit the next ~5 s of Status() calls. See
	// the windowsConnectedTrustUntil field doc on IPSecProtocol — the
	// poll loop in app.go would otherwise spawn Get-VpnConnection
	// every 250 ms (200-500 ms PS startup each) producing 1-5 s of
	// user-visible "Connecting…" lag even though rasdial blocked
	// until the SA is fully established.
	i.windowsConnectedTrustUntil = time.Now().Add(5 * time.Second)
	log.Printf("IPSec connected: %s", i.connName)
	return nil
}

func (i *IPSecProtocol) downWindows(ctx context.Context) error {
	log.Printf("Disconnecting IPSec %s via rasdial /disconnect...", i.connName)
	execHiddenContext(ctx, "rasdial", i.connName, "/disconnect").Run()
	i.connectedAt = time.Time{}
	// Clear the post-connect "trust Connected" fast-path window. Without this,
	// Status() keeps reporting Connected=true for up to 5s AFTER a disconnect
	// (e.g. a rules-engine no_vpn teardown), masking the teardown so the app
	// still believes the tunnel is up on an excluded network.
	i.windowsConnectedTrustUntil = time.Time{}
	log.Printf("IPSec disconnected: %s", i.connName)
	return nil
}

// isWindowsVPNHealthy reports true when the AllUser-scope VPN connection
// with the given name exists and points to the expected server address.
// Used to decide whether the in-memory configured-cache is still trustworthy.
func isWindowsVPNHealthy(connName, expectedServer string) bool {
	psCmd := fmt.Sprintf(
		`(Get-VpnConnection -Name '%s' -AllUserConnection -ErrorAction SilentlyContinue).ServerAddress`,
		escapePowerShellString(connName))
	out, err := execHidden("powershell", "-NoProfile", "-Command", psCmd).CombinedOutput()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == expectedServer
}

// ============================================================================
// Helpers
// ============================================================================

// redactCredentials replaces sensitive values in output strings for safe logging
func redactCredentials(output string, secrets ...string) string {
	for _, secret := range secrets {
		if secret != "" {
			output = strings.ReplaceAll(output, secret, "***REDACTED***")
		}
	}
	return output
}

// ProtocolError represents a protocol-specific error
type ProtocolError struct {
	Protocol string
	Message  string
}

func (e *ProtocolError) Error() string {
	return e.Protocol + ": " + e.Message
}
