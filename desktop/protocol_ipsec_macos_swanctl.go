package main

// macOS IPSec/IKEv2: drive libcharon directly via Homebrew strongswan.
// This is the SOLE macOS IPSec path for the direct-distribution
// (non-App-Store) build flavor. The previous Apple-Stack approach
// (.mobileconfig + AppleScript-System-Events) is dead code: Sequoia's
// security boundary explicitly forbids apps from controlling
// profile-installed VPNs (Apple-DTS-Forum 663468), so every connect
// attempt failed with osascript -1728 even though the tunnel itself
// would have worked. The Mac-App-Store flavor (build-tag `appstore`,
// arriving in a follow-up tag) takes a third path: NEVPNManager
// in-app, since that does work for app-owned configs.
//
// User-side prerequisites:
//   - `brew install strongswan`
//   - `brew services start strongswan`  (or charon already running
//      with the vici socket reachable at <prefix>/var/run/charon.vici)
//
// Surface area:
//   - configureMacOSFromSSwanViaSwanctl: PEM extraction + swanctl.conf
//     write via the privileged helper (charon's vici socket is
//     root-only, so client-side write would fail).
//   - upMacOSViaSwanctl / downMacOSViaSwanctl: helper-driven
//     `swanctl --initiate` / `--terminate`.
//   - buildSwanctlConf: produces conf with optional RFC 8784 PPK block
//     when the .sswan ships PPK material. Cert-only auth otherwise.

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	pkcs12 "software.sslmate.com/src/go-pkcs12"
)

// MacOSIPSecDependencies captures the three install-state signals
// the privileged helper reports for the Homebrew strongSwan stack.
// Used by the Configure-time pre-flight check to render precise
// install hints instead of a generic "swanctl not found" error.
type MacOSIPSecDependencies struct {
	BrewInstalled       bool
	StrongswanInstalled bool
	CharonRunning       bool
}

// CheckMacOSIPSecDependencies asks the privileged helper for the
// current Homebrew strongSwan install state. Returns zero-value
// (all false) and a non-nil error if the helper isn't reachable —
// callers should treat that as a "helper not running, install/start
// it first" signal, not a "deps missing" signal.
func CheckMacOSIPSecDependencies() (MacOSIPSecDependencies, error) {
	var deps MacOSIPSecDependencies
	client := NewHelperClient()
	if !client.IsHelperReachable() {
		return deps, fmt.Errorf("privileged helper not running")
	}
	resp, err := client.SendCommand("ipsec_check_dependencies", nil)
	if err != nil {
		return deps, err
	}
	if !resp.Success {
		return deps, fmt.Errorf("helper ipsec_check_dependencies: %s", resp.Error)
	}
	for _, line := range strings.Split(resp.Output, "\n") {
		switch strings.TrimSpace(line) {
		case "brew_installed=true":
			deps.BrewInstalled = true
		case "strongswan_installed=true":
			deps.StrongswanInstalled = true
		case "charon_running=true":
			deps.CharonRunning = true
		}
	}
	return deps, nil
}

// findStrongswanBinary returns the absolute path to a Homebrew-
// installed strongswan binary (`swanctl`, `charon`, etc.) or empty
// when not found. Mirrors findOpenVPNExe / findWGBinary so the
// launchd-PATH gotcha (CLAUDE.md) does not bite: GUI apps and
// LaunchDaemons get a minimal PATH that excludes Homebrew dirs, and
// exec.LookPath alone misses them.
func findStrongswanBinary(name string) string {
	candidates := []string{
		"/opt/homebrew/sbin/" + name, // Apple Silicon Homebrew
		"/opt/homebrew/bin/" + name,
		"/usr/local/sbin/" + name, // Intel Homebrew
		"/usr/local/bin/" + name,
		"/usr/sbin/" + name,
		"/usr/bin/" + name,
	}
	for _, p := range candidates {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}

// configureMacOSFromSSwanViaSwanctl extracts PEMs from the .sswan
// PKCS#12 bundle, builds a swanctl.conf (with optional PPK block when
// the profile ships RFC 8784 material), and asks the privileged helper
// to install everything into the Homebrew strongswan config dir + run
// `swanctl --load-all`. The connection is then ready for
// upMacOSViaSwanctl to fire `swanctl --initiate`.
func (i *IPSecProtocol) configureMacOSFromSSwanViaSwanctl(profile *sswanProfile) error {
	password := profile.Local.P12Password
	if password == "" {
		password = "privycs"
	}
	p12Bytes, err := base64Decode(profile.Local.P12)
	if err != nil {
		return fmt.Errorf("decode .sswan PKCS#12: %w", err)
	}
	caPEM, certPEM, keyPEM, err := extractPKCS12ToPEMs(p12Bytes, password)
	if err != nil {
		return fmt.Errorf("extract PKCS#12: %w", err)
	}

	swanctlConf := buildSwanctlConf(swanctlConfParams{
		ConnName:      i.connName,
		RemoteAddress: profile.Remote.Addr,
		LocalID:       profile.Local.ID,
		RemoteID:      profile.Remote.ID,
		PPKID:         profile.PPKID,
		PPKHex:        profile.PPKPSK,
	})

	client := NewHelperClient()
	if !client.IsHelperReachable() {
		return fmt.Errorf("privileged helper not running — install it in Settings → Privileged Helper")
	}
	resp, err := client.SendCommand("ipsec_configure", map[string]string{
		"conn_name":    i.connName,
		"ca_cert":      caPEM,
		"client_cert":  certPEM,
		"client_key":   keyPEM,
		"swanctl_conf": swanctlConf,
	})
	if err != nil {
		return fmt.Errorf("helper ipsec_configure failed: %w", err)
	}
	if !resp.Success {
		return fmt.Errorf("ipsec configure via swanctl: %s", resp.Error)
	}
	i.serverAddr = profile.Remote.Addr
	i.configured = true
	authMode := "cert-only"
	if profile.PPKID != "" && profile.PPKPSK != "" {
		authMode = "cert + RFC 8784 PPK"
	}
	log.Printf("IPSec swanctl config installed for %s -> %s (%s)", i.connName, profile.Remote.Addr, authMode)

	// Migration nudge: if a previous import used the Apple-Stack path
	// (.mobileconfig installed in System Settings under the same
	// name) and the user is now reimporting with PPK, the old profile
	// still lives in System Settings. We cannot uninstall it
	// programmatically. Tell the user so they can clean up — the
	// stale profile would otherwise sit there forever shadowing any
	// scutil --nc lookups by the same name.
	if isMacOSVPNConfigInstalled(i.connName) {
		// Best-effort: also wipe our cached .mobileconfig so a future
		// non-PPK reimport doesn't think it's still installed.
		_ = os.Remove(filepath.Join(appDataDir(), i.connName+".mobileconfig"))
		openMacOSProfilesPane()
		Notify(
			"Old Apple-Stack VPN profile still present",
			fmt.Sprintf("This connection is now driven via Homebrew strongswan (PPK enabled). The previous Apple-Stack profile %q in System Settings is no longer used. Open System Settings → Privacy & Security → Profiles and remove it to keep things tidy.",
				i.connName),
			NotifyInfo,
		)
	}

	return nil
}

// upMacOSViaSwanctl asks the helper to fire `swanctl --initiate`. The
// helper's connectIPSec already does the right thing on darwin once
// the conf has been loaded by ipsec_configure. After the SA is up we
// also push the user's DNS-Override (if any) through the helper so
// the macOS resolver sees it — charon on macOS does NOT itself touch
// system DNS via attribute payloads.
func (i *IPSecProtocol) upMacOSViaSwanctl(_ context.Context) error {
	client := NewHelperClient()
	if !client.IsHelperReachable() {
		return fmt.Errorf("privileged helper not running — install it in Settings → Privileged Helper")
	}
	resp, err := client.SendCommand("connect", map[string]string{
		"protocol":        "ipsec",
		"interface":       i.connName,
		"connection_name": i.connName,
	})
	if err != nil {
		return fmt.Errorf("helper connect failed: %w", err)
	}
	if !resp.Success {
		return fmt.Errorf("swanctl initiate failed: %s", resp.Error)
	}

	// DNS-Override on the swanctl path only takes effect post-SA-up
	// because we set primary-service DNS via networksetup. Without an
	// up tunnel, the override would route DNS queries that resolve to
	// VPN-only-reachable IPs through the LAN — broken. Helper applies
	// idempotently; missing dnsOverride is a no-op caller-side.
	if len(i.dnsOverride) > 0 {
		dnsResp, dnsErr := client.SendCommand("macos_dns_override_set", map[string]string{
			"connection_name": i.connName,
			"dns_servers":     strings.Join(i.dnsOverride, " "),
		})
		if dnsErr != nil || !dnsResp.Success {
			log.Printf("IPSec: DNS-Override apply failed (swanctl-darwin): err=%v resp=%+v", dnsErr, dnsResp)
		} else {
			log.Printf("IPSec: %s", dnsResp.Output)
		}
	}
	return nil
}

// downMacOSViaSwanctl tears the SA down via the helper, then restores
// any DNS-Override backup. Restore order matters: terminate first so
// charon stops responding on the VPN's DNS, then revert the system
// resolver to its pre-VPN state.
func (i *IPSecProtocol) downMacOSViaSwanctl(_ context.Context) error {
	client := NewHelperClient()
	if !client.IsHelperReachable() {
		return nil
	}
	client.SendCommand("disconnect", map[string]string{
		"protocol":        "ipsec",
		"interface":       i.connName,
		"connection_name": i.connName,
	})
	// Always attempt restore even if no override was active — helper
	// is idempotent: missing backup = no-op.
	if resp, err := client.SendCommand("macos_dns_override_restore", map[string]string{
		"connection_name": i.connName,
	}); err == nil && resp.Success && resp.Output != "no backup to restore" {
		log.Printf("IPSec: %s", resp.Output)
	}
	return nil
}

// extractPKCS12ToPEMs converts a PKCS#12 (.p12) blob into the three
// PEMs strongswan's swanctl path expects: CA chain, leaf certificate,
// private key. Privycs gateway emits PKCS#12 with the LegacyDES
// algorithm for Android compatibility, which the standard library's
// crypto/pkcs12 cannot decode. We use software.sslmate.com/src/
// go-pkcs12 which handles both modern (AES-CBC + HMAC-SHA256) and
// legacy (RC2-CBC, 3DES, etc.) encodings.
func extractPKCS12ToPEMs(p12 []byte, password string) (caPEM, certPEM, keyPEM string, err error) {
	priv, leaf, cas, dErr := pkcs12.DecodeChain(p12, password)
	if dErr != nil {
		return "", "", "", fmt.Errorf("pkcs12 decode: %w", dErr)
	}
	if leaf != nil {
		certPEM = string(pem.EncodeToMemory(&pem.Block{
			Type:  "CERTIFICATE",
			Bytes: leaf.Raw,
		}))
	}
	for _, ca := range cas {
		caPEM += string(pem.EncodeToMemory(&pem.Block{
			Type:  "CERTIFICATE",
			Bytes: ca.Raw,
		}))
	}
	keyDER, mErr := x509.MarshalPKCS8PrivateKey(priv)
	if mErr != nil {
		// MarshalPKCS8PrivateKey accepts *rsa, *ecdsa, ed25519 keys.
		// Anything else is a server-side mistake we can't recover from.
		return "", "", "", fmt.Errorf("marshal private key: %w", mErr)
	}
	keyPEM = string(pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: keyDER,
	}))
	return caPEM, certPEM, keyPEM, nil
}

type swanctlConfParams struct {
	ConnName      string
	RemoteAddress string
	LocalID       string
	RemoteID      string
	PPKID         string
	PPKHex        string
}

// buildSwanctlConf assembles a strongswan swanctl.conf that negotiates
// IKEv2 with certificate auth, optionally adding an RFC 8784 PPK
// block when ppk_id + ppk_psk are supplied. The PPK secret is
// hex-encoded inline; libcharon parses 0x-prefixed hex into raw bytes
// when loading the secrets section.
//
// Cert/key file references resolve relative to the swanctl conf
// directory's `x509`/`private` subdirs that the helper populates via
// cmdIPSecConfigure.
func buildSwanctlConf(p swanctlConfParams) string {
	hasPPK := p.PPKID != "" && p.PPKHex != ""

	var ppkLines string
	if hasPPK {
		ppkLines = fmt.Sprintf(
			"        ppk_id = %s\n        ppk_required = yes\n",
			p.PPKID)
	}

	var ppkSecretBlock string
	if hasPPK {
		ppkSecretBlock = fmt.Sprintf(
			"    ppk-%s {\n        id = %s\n        secret = 0x%s\n    }\n",
			p.ConnName, p.PPKID, p.PPKHex)
	}

	var sb strings.Builder
	// dpd_delay 30s + dpd_timeout 90s + close_action=restart together
	// form the macOS-sleep-survival baseline: when the system suspends
	// (lid close / idle sleep), all userspace including charon is
	// frozen, the UDP NAT mapping at the upstream router expires after
	// 30-60 s, and on wake the SA is technically dead. Without these
	// settings the SA stayed in ESTABLISHED state forever, packets
	// black-holed into a kernel SA that the peer no longer recognised,
	// and the user had to manually disconnect to clear stuck routes.
	// With them: 30 s after wake the next DPD goes out, fails, after
	// 90 s charon declares the peer dead, close_action=restart
	// terminates the dead SA + initiates a fresh one. Recovery without
	// user action in ~1-2 min. Privycs-side wake-listener (Phase 2)
	// still wins on speed (1-3 s) but this is the safety net for when
	// Privycs itself was killed / not running.
	fmt.Fprintf(&sb, `connections {
    %s {
        version = 2
        remote_addrs = %s
        encap = yes
        mobike = yes
        dpd_delay = 30s
        dpd_timeout = 90s
%s        # vips = 0.0.0.0,:: requests an IPv4 + IPv6 virtual IP from the
        # server during IKE_AUTH. Without this the client never asks
        # for one, which most Privycs-style overlay servers expect —
        # the SA establishes but no traffic flows because there's no
        # tunnel-side source address.
        vips = 0.0.0.0,::
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
                remote_ts = 0.0.0.0/0,::/0
                start_action = none
                close_action = restart
                esp_proposals = aes256gcm16-noesn,aes256-sha256-modp2048-noesn
            }
        }
        proposals = aes256gcm16-prfsha384-curve25519,aes256-sha256-modp2048
    }
}
secrets {
    private-privycs-client {
        file = privycs-client.pem
    }
%s}
`,
		p.ConnName, p.RemoteAddress, ppkLines, p.LocalID, p.RemoteID, p.ConnName, ppkSecretBlock)
	return sb.String()
}
