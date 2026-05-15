package main

import (
	"fmt"
	"log"
	"net"
	"strings"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// IPv6 leak-killswitch — App-side detection + helper-RPC dispatch.
//
// Always-on per security requirement (no user setting). Decision
// flow on connect:
//   1. tunnel installed v6 addresses on its tun?  yes → tunnel is
//      dual-stack, no leak risk → skip (CORRECTNESS short-circuit)
//   2. OS has live IPv6 connectivity (any non-tun, non-loopback,
//      non-link-local v6 address)?  no → no leak vector → skip
//   3. both checks pass → call helper "ipv6_block"
//
// On disconnect: ALWAYS call "ipv6_unblock" (idempotent — best-effort
// helper-side rule deletion). We don't track block-state at the App
// level; the helper RPCs are designed to be no-ops if no rules
// exist. This keeps the App resilient to crash-recovery scenarios
// where the App quit unexpectedly with rules in place.

// shouldEnableIPv6Killswitch evaluates runtime network state to
// decide whether the v6-block rules should be in effect right now.
// tunV4 is the tunnel's IPv4 local address, used to identify and
// skip the tun interface during scanning.
//
// Always-on per security requirement — there is NO user setting to
// disable this. Leaving v6 leakable through a v4-only tunnel is a
// critical security bug, not a user preference. The two short-circuits
// below are about CORRECTNESS (no point blocking when there's no
// leak vector), not about user choice.
func (a *App) shouldEnableIPv6Killswitch(tunV4 string) (bool, string) {
	if tunHasIPv6(tunV4) {
		return false, "tunnel is dual-stack (v6 endpoint present), no leak risk"
	}
	if !osHasIPv6Connectivity(tunV4) {
		return false, "OS has no live IPv6 connectivity, no leak vector"
	}
	return true, "v4-only tunnel + dual-stack OS: block IPv6 outbound"
}

// tunHasIPv6 returns true if the tunnel is dual-stack. The protocol
// status LocalAddress is the raw `Address = ...` line from the conf —
// for WG/AWG that's a comma-separated list like
// "10.100.114.2/32, fd45:43:45::2/128". If any v6 literal appears in
// that list, the tunnel itself carries v6 and there is no leak
// vector → return true immediately (CORRECTNESS short-circuit).
//
// Otherwise fall back to the interface scan: find the interface that
// owns the v4 portion and check whether it ALSO has a non-link-local
// v6 address bound. Returns false when no v4 component is identifiable
// (we err on the safe side — let the killswitch protect us).
func tunHasIPv6(tunLocalAddr string) bool {
	if tunLocalAddr == "" {
		return false
	}
	// Pre-check: if the conf-supplied LocalAddress already contains
	// a non-link-local v6 literal, we're dual-stack. This handles
	// "10.100.114.2/32, fd45:43:45::2/128" without needing an iface
	// scan. v0.9.15.46 fix — pre-fix net.ParseIP returned nil on the
	// combined string → false → killswitch fired on dual-stack tunnels.
	var tunV4 string
	for _, part := range strings.Split(tunLocalAddr, ",") {
		s := strings.TrimSpace(part)
		if s == "" {
			continue
		}
		if slash := strings.IndexByte(s, '/'); slash >= 0 {
			s = s[:slash]
		}
		ip := net.ParseIP(s)
		if ip == nil {
			continue
		}
		if ip.To4() != nil {
			if tunV4 == "" {
				tunV4 = s
			}
			continue
		}
		if !ip.IsLinkLocalUnicast() && !ip.IsLoopback() {
			return true
		}
	}
	if tunV4 == "" {
		return false
	}
	target := net.ParseIP(tunV4)
	if target == nil {
		return false
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return false
	}
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		hasTunV4 := false
		var v6OnSameIface bool
		for _, a := range addrs {
			ip, _, err := net.ParseCIDR(a.String())
			if err != nil {
				continue
			}
			if ip.Equal(target) {
				hasTunV4 = true
				continue
			}
			if ip.To4() == nil && !ip.IsLinkLocalUnicast() && !ip.IsLoopback() {
				v6OnSameIface = true
			}
		}
		if hasTunV4 {
			return v6OnSameIface
		}
	}
	return false
}

// osHasIPv6Connectivity returns true if any non-tun, non-loopback,
// up-state interface has a non-link-local IPv6 address — that's the
// leak vector we're trying to block. tunV4 is the tunnel's IPv4
// address; the interface that owns it is identified and skipped.
// Additional defensive skip-by-name covers cases where the tun is
// pre-up or the v4 address didn't bind in time.
func osHasIPv6Connectivity(tunV4 string) bool {
	tunIP := net.ParseIP(tunV4)
	ifaces, err := net.Interfaces()
	if err != nil {
		return false
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		// Skip interfaces whose name marks them as VPN-tun-likely
		// or kernel-private. Belt-and-braces with the tunV4
		// address-match below — covers cases where the tun's v4
		// address hasn't been assigned yet by the time we scan.
		lowName := strings.ToLower(iface.Name)
		if strings.HasPrefix(lowName, "utun") ||
			strings.HasPrefix(lowName, "tun") ||
			strings.HasPrefix(lowName, "wg") ||
			strings.HasPrefix(lowName, "ipsec") ||
			strings.HasPrefix(lowName, "awdl") ||
			strings.HasPrefix(lowName, "llw") ||
			strings.HasPrefix(lowName, "privycs") {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		// Skip if this interface owns the tun's v4 address.
		isTun := false
		if tunIP != nil {
			for _, a := range addrs {
				ip, _, err := net.ParseCIDR(a.String())
				if err == nil && ip.Equal(tunIP) {
					isTun = true
					break
				}
			}
		}
		if isTun {
			continue
		}
		for _, a := range addrs {
			ip, _, err := net.ParseCIDR(a.String())
			if err != nil {
				continue
			}
			if ip.To4() != nil {
				continue
			}
			if ip.IsLinkLocalUnicast() || ip.IsLoopback() {
				continue
			}
			return true
		}
	}
	return false
}

// applyIPv6Killswitch wires the App-level decision into the helper.
// Called after a successful Up() in Connect / connectActiveTarget.
// tunV4 is the tunnel's IPv4 LocalAddress (from ProtocolStatus).
//
// On failure (helper unreachable, RPC error) we surface a Wails
// event "vpn:ipv6_leak_warning" so the frontend can show a banner.
// Critical: a silent failure here would mean the user sees
// "Connected" but their v6 traffic still leaks — must be visible.
func (a *App) applyIPv6Killswitch(tunV4 string) {
	enable, reason := a.shouldEnableIPv6Killswitch(tunV4)
	if !enable {
		log.Printf("IPv6 killswitch: skipping (%s)", reason)
		return
	}
	log.Printf("IPv6 killswitch: enabling (%s, tunV4=%s)", reason, tunV4)
	client := NewHelperClient()
	if !client.IsHelperReachable() {
		msg := "IPv6 leak protection failed: privileged helper unreachable. Your IPv6 traffic may bypass the VPN."
		log.Printf("IPv6 killswitch: helper unreachable, can't block — leak risk remains")
		if a.ctx != nil {
			wailsRuntime.EventsEmit(a.ctx, "vpn:ipv6_leak_warning", msg)
		}
		Notify("IPv6 leak protection failed", msg, NotifyError)
		return
	}
	resp, err := client.SendCommand("ipv6_block", nil)
	if err != nil || !resp.Success {
		errStr := ""
		if err != nil {
			errStr = err.Error()
		} else if resp.Error != "" {
			errStr = resp.Error
		}
		msg := fmt.Sprintf("IPv6 leak protection failed: %s. Your IPv6 traffic may bypass the VPN.", errStr)
		log.Printf("IPv6 killswitch: helper RPC failed: err=%v resp=%+v", err, resp)
		if a.ctx != nil {
			wailsRuntime.EventsEmit(a.ctx, "vpn:ipv6_leak_warning", msg)
		}
		Notify("IPv6 leak protection failed", msg, NotifyError)
		return
	}
	log.Printf("IPv6 killswitch: blocked (%s)", strings.TrimSpace(resp.Output))
}

// clearIPv6Killswitch removes any v6-block firewall rules the helper
// installed earlier. Idempotent — safe to call even if the user's
// tunnel never triggered an enable. Called from disconnectInternal.
func (a *App) clearIPv6Killswitch() {
	client := NewHelperClient()
	if !client.IsHelperReachable() {
		// Helper unreachable — rules (if any) will linger until the
		// helper restarts. That's acceptable behavior because the
		// App couldn't have set them either; system state is
		// consistent with no rules.
		return
	}
	resp, err := client.SendCommand("ipv6_unblock", nil)
	if err != nil || !resp.Success {
		log.Printf("IPv6 killswitch: clear failed (rules may linger until next OS reboot or app reconnect): err=%v resp=%+v", err, resp)
		return
	}
	log.Printf("IPv6 killswitch: cleared (%s)", strings.TrimSpace(resp.Output))
}
