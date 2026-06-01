//go:build darwin

package main

import (
	"context"
	"fmt"
	"log"
	"time"
)

// macOS IPSec via NEVPNManager (Apple built-in IKEv2). These bridge the
// cross-platform IPSecProtocol dispatch (protocol_ipsec.go) to the cgo
// NEVPNManager wrappers in nevpn_macos.go. The non-darwin build gets the
// stubs in protocol_ipsec_nevpn_other.go.

// macosConfigureNEVPN parses the .sswan's cert + identifiers and writes
// a Personal-VPN IKEv2 configuration owned by this app.
func macosConfigureNEVPN(i *IPSecProtocol, profile *sswanProfile) error {
	if profile.Local.P12 == "" {
		return fmt.Errorf("IPSec .sswan profile has no client certificate (p12) — required for NEVPNManager cert auth")
	}
	p12, err := base64Decode(profile.Local.P12)
	if err != nil {
		return fmt.Errorf("decode .sswan PKCS#12: %w", err)
	}
	remoteID := profile.Remote.ID
	if remoteID == "" {
		remoteID = profile.Remote.Addr
	}
	if err := nevpnConfigure(i.connName, profile.Remote.Addr, remoteID, profile.Local.ID, p12, profile.Local.P12Password); err != nil {
		return fmt.Errorf("NEVPNManager configure: %w", err)
	}
	if profile.PPKID != "" {
		// Apple's IKEv2 stack has no public PPK (RFC 8784) API — a
		// pq_safe profile downgrades to cert-only auth on macOS.
		log.Printf("IPSec: .sswan carries PPK material; Apple IKEv2 ignores PPK — using cert-only auth for %s", i.connName)
	}
	log.Printf("IPSec NEVPNManager config installed for %s -> %s", i.connName, profile.Remote.Addr)
	return nil
}

// macosUpNEVPN starts the tunnel and waits for NEVPNStatusConnected (3).
func macosUpNEVPN(i *IPSecProtocol, _ context.Context) error {
	if err := nevpnStart(); err != nil {
		return fmt.Errorf("NEVPNManager start: %w", err)
	}
	// Poll up to 20s for the IKE_SA to come up. NEVPNStatus 3 = connected.
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if nevpnStatusRaw() == 3 {
			i.connectedAt = time.Now()
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	if nevpnStatusRaw() == 3 {
		i.connectedAt = time.Now()
		return nil
	}
	return fmt.Errorf("IPSec did not establish within 20s (NEVPNManager status=%d)", nevpnStatusRaw())
}

// macosDownNEVPN stops the tunnel.
func macosDownNEVPN(i *IPSecProtocol, _ context.Context) error {
	err := nevpnStop()
	i.connectedAt = time.Time{}
	return err
}

// macosStatusNEVPN reports connected-state. NEVPNConnection exposes no
// public byte counters and no easy inner-IP accessor, so rx/tx stay 0
// and localAddr is empty (same limitation as the iOS IKEv2 path).
func macosStatusNEVPN(_ *IPSecProtocol) (connected bool, localAddr string) {
	return nevpnStatusRaw() == 3, ""
}
