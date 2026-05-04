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
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
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
	ProfileUUID    string
	VPNUUID        string
	CertUUID       string
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

	data := macosMobileConfigData{
		ConnName:       i.connName,
		RemoteAddress:  profile.Remote.Addr,
		RemoteID:       profile.Remote.ID,
		LocalID:        profile.Local.ID,
		PKCS12Base64:   profile.Local.P12,
		PKCS12Password: password,
		HasDNS:         len(profile.DNSServers) > 0,
		DNSServers:     profile.DNSServers,
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

// stableUUID derives a deterministic UUIDv5 from the given seed. macOS
// uses PayloadUUID to identify a profile across re-installs; using a
// content-derived UUID keeps "edit and re-import" flowing through the
// "Replace existing profile" path instead of accumulating duplicates.
func stableUUID(seed string) string {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("privycs-vpn:"+seed)).String()
}
