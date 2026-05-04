package main

// macOS Pro: drive libcharon directly via Homebrew strongswan when the
// .sswan profile carries RFC 8784 PPK material. Apple's built-in IKEv2
// stack does not implement the PPK_IDENTITY notify, so a pq_safe
// connection on macOS would silently downgrade to plain certificate
// authentication. Going through swanctl lets the postquantum mixin
// actually run.
//
// Trigger: protocol_ipsec.go:configureFromSSwan flips i.usingSwanctl
// when the profile has both PPK fields populated AND
// findStrongswanBinary("swanctl") returned a usable path. Otherwise
// the Apple-Stack path (configureMacOSFromSSwan in
// protocol_ipsec_macos.go) handles it.
//
// User-side prerequisites:
//   - `brew install strongswan`
//   - `brew services start strongswan`  (or charon already running
//      with the vici socket reachable at <prefix>/var/run/charon.vici)

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
// PKCS#12 bundle, builds a swanctl.conf with PPK secrets, and asks
// the privileged helper to install everything into the Homebrew
// strongswan config dir + run `swanctl --load-all`. The connection
// is then ready for upMacOSViaSwanctl to fire `swanctl --initiate`.
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

	swanctlConf := buildSwanctlConfWithPPK(swanctlConfParams{
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
	log.Printf("IPSec swanctl config installed for %s -> %s (PPK enabled)", i.connName, profile.Remote.Addr)

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

// buildSwanctlConfWithPPK assembles a strongswan swanctl.conf that
// negotiates IKEv2 with certificate auth + RFC 8784 PPK. The PPK
// secret is hex-encoded inline; libcharon parses 0x-prefixed hex into
// raw bytes when loading the secrets section.
//
// Cert/key file references resolve relative to the swanctl conf
// directory's `x509`/`private` subdirs that the helper populates via
// cmdIPSecConfigure.
func buildSwanctlConfWithPPK(p swanctlConfParams) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, `connections {
    %s {
        version = 2
        remote_addrs = %s
        encap = yes
        mobike = yes
        dpd_delay = 60s
        ppk_id = %s
        ppk_required = yes
        # vips = 0.0.0.0,:: requests an IPv4 + IPv6 virtual IP from the
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
                close_action = none
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
    ppk-%s {
        id = %s
        secret = 0x%s
    }
}
`,
		p.ConnName, p.RemoteAddress, p.PPKID, p.LocalID, p.RemoteID, p.ConnName,
		p.ConnName, p.PPKID, p.PPKHex)
	return sb.String()
}
