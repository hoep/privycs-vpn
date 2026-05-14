//go:build windows

package main

import (
	"fmt"
	"log"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"

	awgconn "github.com/amnezia-vpn/amneziawg-go/conn"
	awgdevice "github.com/amnezia-vpn/amneziawg-go/device"
	awgtun "github.com/amnezia-vpn/amneziawg-go/tun"
	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

// In-process AmneziaWG tunnel for Windows — Stage 4 of
// AMNEZIAWG_CLIENT_PLAN.md. There is no AmneziaWG tunnel-service
// equivalent of wireguard.exe — the AmneziaWG project ships only
// their own end-user client. So we own the Wintun adapter and the
// AWG device ourselves from inside the privileged helper process
// (running as LocalSystem under a Windows service).
//
// Wintun.dll bundling: amneziawg-go's tun.CreateTUN loads wintun.dll
// at runtime. The installer must ship wintun.dll alongside the
// helper binary (or in System32). Without it, CreateTUN returns an
// "Error loading wintun.dll" error and the tunnel never comes up.
// CI for the Windows release should download wintun-amd64.dll from
// wintun.net and place it next to privycs-vpn.exe — same recipe
// used by tailscaled, wg-quick-windows, and the official WireGuard
// installer.

type awgWindowsTunnelState struct {
	dev    *awgdevice.Device
	tunDev awgtun.Device
	iface  string // wintun adapter name (matches what we requested)
	// We track which interface-scope addresses + routes + DNS we
	// installed so Down can roll them back cleanly. netsh doesn't
	// support a true transactional API, so this is best-effort
	// undo-list.
	addedAddrsV4    []string
	addedAddrsV6    []string
	addedRoutesV4   []string
	addedRoutesV6   []string
	savedDNS        []string // previous DNS servers on the same adapter; not used for restore here because Wintun adapter is destroyed on Down
	previousAdapter string   // adapter name that was holding the default route, for DNS restore — not implemented in this minimum-viable stage
}

var (
	awgWinTunnels   = make(map[string]*awgWindowsTunnelState)
	awgWinTunnelsMu sync.Mutex
)

// wgWindowsUpAwg brings up an in-process AmneziaWG tunnel on Windows.
// ifaceName is the Wintun adapter name (matches the privycs0 / iface
// convention used by the vanilla WG path).
func wgWindowsUpAwg(ifaceName, configContent string) error {
	awgWinTunnelsMu.Lock()
	if _, exists := awgWinTunnels[ifaceName]; exists {
		awgWinTunnelsMu.Unlock()
		return fmt.Errorf("AWG tunnel %q already up", ifaceName)
	}
	awgWinTunnelsMu.Unlock()

	cfg, err := parseWGConf(configContent)
	if err != nil {
		return fmt.Errorf("parse AWG conf: %w", err)
	}

	tunDev, err := awgtun.CreateTUN(ifaceName, cfg.MTU)
	if err != nil {
		return fmt.Errorf("create wintun (awg): %w — verify wintun.dll is present alongside the helper binary", err)
	}
	log.Printf("wgWindowsUpAwg[%s]: Wintun adapter created, applying AWG UAPI", ifaceName)

	log.Printf("wgWindowsUpAwg[%s]: TRACE buildUAPIAwgWin start", ifaceName)
	uapi, err := buildUAPIAwgWin(cfg)
	if err != nil {
		tunDev.Close()
		return fmt.Errorf("build AWG UAPI: %w", err)
	}
	log.Printf("wgWindowsUpAwg[%s]: TRACE buildUAPIAwgWin done (uapi=%d bytes), NewDevice start", ifaceName, len(uapi))
	// Verbose logger so amneziawg-go's internal bind/handshake/peer
	// resolution prints into the per-service log. Without this an
	// internal hang (e.g. DNS resolve, dual-stack UDP bind, peer
	// endpoint reject) is invisible — process appears to "freeze"
	// between our log.Printf calls. Verbose is fine for diagnostics
	// even in production; volume is low compared to amneziawg-go's
	// receive path.
	logger := awgdevice.NewLogger(awgdevice.LogLevelVerbose, fmt.Sprintf("[awg-%s] ", ifaceName))
	dev := awgdevice.NewDevice(tunDev, awgconn.NewDefaultBind(), logger)
	log.Printf("wgWindowsUpAwg[%s]: TRACE NewDevice done, IpcSet start", ifaceName)
	if err := dev.IpcSet(uapi); err != nil {
		dev.Close()
		return fmt.Errorf("AWG IpcSet: %w", err)
	}
	log.Printf("wgWindowsUpAwg[%s]: TRACE IpcSet done, dev.Up start", ifaceName)
	if err := dev.Up(); err != nil {
		dev.Close()
		return fmt.Errorf("AWG device.Up: %w", err)
	}
	log.Printf("wgWindowsUpAwg[%s]: TRACE dev.Up done, configuring netsh addresses", ifaceName)

	state := &awgWindowsTunnelState{
		dev:    dev,
		tunDev: tunDev,
		iface:  ifaceName,
	}

	// v0.9.15.39: switch from netsh subprocess to winipcfg direct-
	// Win32-API for address + route + metric configuration. This is
	// what the official amneziawg-windows / wireguard-windows clients
	// do. Reason: netsh.exe has a race where calls against a freshly-
	// created Wintun adapter hang or fail with "adapter not found"
	// because the OS hasn't propagated the adapter into netsh's user-
	// mode namespace yet. winipcfg uses the Win32 IP Helper API
	// (iphlpapi.dll) directly against the adapter LUID — bypasses
	// netsh's namespace caching entirely.
	nativeTun, ok := tunDev.(*awgtun.NativeTun)
	if !ok {
		awgWinRollbackUp(state)
		return fmt.Errorf("AWG tun is unexpected type %T (want *awgtun.NativeTun)", tunDev)
	}
	luid := winipcfg.LUID(nativeTun.LUID())

	var v4Prefixes, v6Prefixes []netip.Prefix
	for _, addr := range cfg.Addresses {
		pfx, err := netip.ParsePrefix(addr)
		if err != nil {
			awgWinRollbackUp(state)
			return fmt.Errorf("parse address %q: %w", addr, err)
		}
		if pfx.Addr().Is4() {
			v4Prefixes = append(v4Prefixes, pfx)
			state.addedAddrsV4 = append(state.addedAddrsV4, addr)
		} else {
			v6Prefixes = append(v6Prefixes, pfx)
			state.addedAddrsV6 = append(state.addedAddrsV6, addr)
		}
	}
	log.Printf("wgWindowsUpAwg[%s]: TRACE SetIPAddressesForFamily v4=%d v6=%d", ifaceName, len(v4Prefixes), len(v6Prefixes))
	if len(v4Prefixes) > 0 {
		if err := luid.SetIPAddressesForFamily(windows.AF_INET, v4Prefixes); err != nil {
			awgWinRollbackUp(state)
			return fmt.Errorf("winipcfg SetIPAddressesForFamily v4: %w", err)
		}
	}
	if len(v6Prefixes) > 0 {
		if err := luid.SetIPAddressesForFamily(windows.AF_INET6, v6Prefixes); err != nil {
			log.Printf("wgWindowsUpAwg[%s]: SetIPAddressesForFamily v6 warning: %v", ifaceName, err)
		}
	}
	log.Printf("wgWindowsUpAwg[%s]: TRACE addresses applied, building routes", ifaceName)

	// Routes: convert each peer AllowedIP into a winipcfg.RouteData
	// targeting the wintun adapter LUID. Default NextHop = zero
	// address (IPv4 0.0.0.0 / IPv6 ::) — the official wireguard-
	// windows client does the same; winipcfg interprets that as
	// "on-link, send via this interface".
	var v4Routes, v6Routes []*winipcfg.RouteData
	for _, peer := range cfg.Peers {
		for _, raw := range peer.AllowedIPs {
			pfx, err := netip.ParsePrefix(raw)
			if err != nil {
				log.Printf("wgWindowsUpAwg[%s]: invalid AllowedIP %q: %v", ifaceName, raw, err)
				continue
			}
			pfx = pfx.Masked()
			rd := &winipcfg.RouteData{
				Destination: pfx,
				Metric:      0,
			}
			if pfx.Addr().Is4() {
				rd.NextHop = netip.IPv4Unspecified()
				v4Routes = append(v4Routes, rd)
				state.addedRoutesV4 = append(state.addedRoutesV4, raw)
			} else {
				rd.NextHop = netip.IPv6Unspecified()
				v6Routes = append(v6Routes, rd)
				state.addedRoutesV6 = append(state.addedRoutesV6, raw)
			}
		}
	}
	log.Printf("wgWindowsUpAwg[%s]: TRACE SetRoutesForFamily v4=%d v6=%d", ifaceName, len(v4Routes), len(v6Routes))
	if len(v4Routes) > 0 {
		if err := luid.SetRoutesForFamily(windows.AF_INET, v4Routes); err != nil {
			awgWinRollbackUp(state)
			return fmt.Errorf("winipcfg SetRoutesForFamily v4: %w", err)
		}
	}
	if len(v6Routes) > 0 {
		if err := luid.SetRoutesForFamily(windows.AF_INET6, v6Routes); err != nil {
			log.Printf("wgWindowsUpAwg[%s]: SetRoutesForFamily v6 warning: %v", ifaceName, err)
		}
	}

	// Set interface metric to 1 for both families so the wintun
	// adapter beats the physical adapter's default route in the
	// longest-prefix-match tiebreaker.
	for _, family := range []winipcfg.AddressFamily{windows.AF_INET, windows.AF_INET6} {
		ipif, err := luid.IPInterface(family)
		if err != nil {
			log.Printf("wgWindowsUpAwg[%s]: IPInterface family=%d warning: %v", ifaceName, family, err)
			continue
		}
		ipif.UseAutomaticMetric = false
		ipif.Metric = 1
		if err := ipif.Set(); err != nil {
			log.Printf("wgWindowsUpAwg[%s]: IPInterface.Set family=%d warning: %v", ifaceName, family, err)
		}
	}
	log.Printf("wgWindowsUpAwg[%s]: TRACE winipcfg config done, configuring DNS", ifaceName)

	// Adapter index for downstream code that still uses it (DNS section
	// below doesn't need it but keep for diagnostics / future ip route
	// queries).
	if _, err := awgInterfaceIndex(ifaceName); err != nil {
		log.Printf("wgWindowsUpAwg[%s]: adapter index lookup warning: %v", ifaceName, err)
	}

	if len(cfg.DNS) > 0 {
		// v0.9.15.40: migrate DNS configuration from netsh subprocess
		// to winipcfg.LUID.SetDNS — what the official amneziawg-
		// windows and wireguard-windows clients do. Reason: netsh
		// "interface ipv4 set dnsservers" against a freshly-created
		// Wintun adapter triggers a Windows IP-interface-changed
		// notification which the amneziawg-go bind's underlying
		// socket monitor reacts to by rebinding/closing — that's
		// what we saw in the v0.9.15.39 trace: handshake completes,
		// "configuring DNS" log fires, then "Routine: sequential
		// sender - stopped" within milliseconds → process exits
		// (after which the status pipe vanishes and the helper's
		// polls return "file not found"). winipcfg.SetDNS uses the
		// IP Helper API directly without firing the disruptive
		// notification cascade.
		var v4Servers, v6Servers []netip.Addr
		for _, raw := range cfg.DNS {
			addr, err := netip.ParseAddr(raw)
			if err != nil {
				log.Printf("wgWindowsUpAwg[%s]: skip invalid DNS %q: %v", ifaceName, raw, err)
				continue
			}
			if addr.Is4() {
				v4Servers = append(v4Servers, addr)
			} else {
				v6Servers = append(v6Servers, addr)
			}
		}
		log.Printf("wgWindowsUpAwg[%s]: TRACE SetDNS v4=%d v6=%d", ifaceName, len(v4Servers), len(v6Servers))
		if len(v4Servers) > 0 {
			if err := luid.SetDNS(windows.AF_INET, v4Servers, nil); err != nil {
				log.Printf("wgWindowsUpAwg[%s]: SetDNS v4 warning: %v", ifaceName, err)
			}
		}
		if len(v6Servers) > 0 {
			if err := luid.SetDNS(windows.AF_INET6, v6Servers, nil); err != nil {
				log.Printf("wgWindowsUpAwg[%s]: SetDNS v6 warning: %v", ifaceName, err)
			}
		}
		log.Printf("wgWindowsUpAwg[%s]: TRACE SetDNS done", ifaceName)
	}

	awgWinTunnelsMu.Lock()
	awgWinTunnels[ifaceName] = state
	awgWinTunnelsMu.Unlock()

	log.Printf("wgWindowsUpAwg[%s]: AWG tunnel up (%d v4 routes, %d v6 routes, %d obf-keys)",
		ifaceName, len(state.addedRoutesV4), len(state.addedRoutesV6), len(cfg.AwgKeys))
	return nil
}

func awgWinRollbackUp(state *awgWindowsTunnelState) {
	if state.dev != nil {
		state.dev.Close()
	}
	if state.tunDev != nil {
		state.tunDev.Close()
	}
	for _, c := range state.addedRoutesV4 {
		awgWinDeleteRouteV4(c)
	}
	for _, c := range state.addedRoutesV6 {
		awgWinDeleteRouteV6(c, state.iface)
	}
}

// awgWinDeleteRouteV4 deletes an IPv4 route added via route.exe.
// Mirrors the add-side CIDR-to-IP+MASK split — route.exe DELETE
// also rejects CIDR-as-destination.
func awgWinDeleteRouteV4(cidr string) {
	ip, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return
	}
	maskBits, _ := ipnet.Mask.Size()
	execHidden("route", "DELETE", ip.String(),
		"MASK", ipv4MaskFromBits(maskBits)).Run()
}

// awgWinDeleteRouteV6 deletes an IPv6 route added via netsh. Mirrors
// the add-side. iface is required so we delete the route scoped to
// the wintun adapter, not to any other interface.
func awgWinDeleteRouteV6(cidr, iface string) {
	execHidden("netsh", "interface", "ipv6", "delete", "route",
		fmt.Sprintf("prefix=%s", cidr),
		fmt.Sprintf("interface=%s", iface)).Run()
}

// wgWindowsDownAwg tears down an in-process AWG tunnel. The Wintun
// adapter is destroyed by closing the tunDev — DNS entries scoped
// to that adapter disappear with it, so we don't need an explicit
// netsh "set dnsservers source=dhcp" call.
func wgWindowsDownAwg(ifaceName string) error {
	awgWinTunnelsMu.Lock()
	state, ok := awgWinTunnels[ifaceName]
	if ok {
		delete(awgWinTunnels, ifaceName)
	}
	awgWinTunnelsMu.Unlock()
	if !ok {
		log.Printf("wgWindowsDownAwg[%s]: tunnel not in registry, treating as already-down", ifaceName)
		return nil
	}

	for _, c := range state.addedRoutesV4 {
		awgWinDeleteRouteV4(c)
	}
	for _, c := range state.addedRoutesV6 {
		awgWinDeleteRouteV6(c, ifaceName)
	}

	if state.dev != nil {
		state.dev.Close()
	}
	if state.tunDev != nil {
		state.tunDev.Close()
	}
	log.Printf("wgWindowsDownAwg[%s]: AWG tunnel down (Wintun adapter destroyed)", ifaceName)
	return nil
}

// wgWindowsStatusAwg returns the AWG UAPI dump for the named tunnel.
// Parallel to wgDarwinStatusAwg. Used by the protocol_wireguard.go
// Status() override path when variant==amneziawg on Windows.
func wgWindowsStatusAwg(ifaceName string) (uapi string, connected bool, err error) {
	awgWinTunnelsMu.Lock()
	state, ok := awgWinTunnels[ifaceName]
	awgWinTunnelsMu.Unlock()
	if !ok {
		return "", false, fmt.Errorf("AWG tunnel not running")
	}
	out, err := state.dev.IpcGet()
	if err != nil {
		return "", false, fmt.Errorf("AWG IpcGet: %w", err)
	}
	return out, uapiHasRecentHandshake(out), nil
}

// awgInterfaceIndex resolves a Windows adapter name (e.g. "privycs0")
// to its interface index for `route ADD ... IF <idx>`. The Wintun
// adapter is registered with that name in CreateTUN above.
func awgInterfaceIndex(name string) (int, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return 0, fmt.Errorf("net.Interfaces: %w", err)
	}
	for _, ifa := range ifaces {
		if ifa.Name == name {
			return ifa.Index, nil
		}
	}
	return 0, fmt.Errorf("adapter %q not found in net.Interfaces", name)
}

// ipv4MaskFromBits converts /24 → "255.255.255.0" for netsh, which
// requires dotted-quad subnet masks rather than prefix-length.
func ipv4MaskFromBits(bits int) string {
	mask := net.CIDRMask(bits, 32)
	return fmt.Sprintf("%d.%d.%d.%d", mask[0], mask[1], mask[2], mask[3])
}

// splitDNSByFamily partitions a mixed DNS list into v4 and v6 buckets.
// netsh's dnsservers commands operate per-family.
func splitDNSByFamily(dns []string) (v4, v6 []string) {
	for _, s := range dns {
		ip := net.ParseIP(s)
		if ip == nil {
			continue
		}
		if ip.To4() != nil {
			v4 = append(v4, s)
		} else {
			v6 = append(v6, s)
		}
	}
	return
}

// buildUAPIAwgWin builds the AWG UAPI string. Same emission order
// as buildUAPIAwg (wg_macos_awg.go); the only difference is the
// resolveEndpoint / b64ToHex callees come from wg_conf_shared.go
// rather than darwin-tagged helpers.
func buildUAPIAwgWin(cfg *wgConfigParsed) (string, error) {
	var b strings.Builder
	privHex, err := b64ToHex(cfg.PrivateKey)
	if err != nil {
		return "", fmt.Errorf("private_key: %w", err)
	}
	b.WriteString("private_key=" + privHex + "\n")
	if cfg.ListenPort > 0 {
		b.WriteString("listen_port=" + strconv.Itoa(cfg.ListenPort) + "\n")
	}
	for _, k := range awgKeyOrder() {
		if v, ok := cfg.AwgKeys[k]; ok && v != "" {
			b.WriteString(k + "=" + v + "\n")
		}
	}
	b.WriteString("replace_peers=true\n")
	for _, p := range cfg.Peers {
		pubHex, err := b64ToHex(p.PublicKey)
		if err != nil {
			return "", fmt.Errorf("public_key: %w", err)
		}
		b.WriteString("public_key=" + pubHex + "\n")
		if p.PresharedKey != "" {
			pskHex, err := b64ToHex(p.PresharedKey)
			if err != nil {
				return "", fmt.Errorf("preshared_key: %w", err)
			}
			b.WriteString("preshared_key=" + pskHex + "\n")
		}
		if p.Endpoint != "" {
			ep, err := resolveEndpoint(p.Endpoint)
			if err != nil {
				return "", fmt.Errorf("endpoint: %w", err)
			}
			b.WriteString("endpoint=" + ep + "\n")
		}
		if p.PersistentKeepalive > 0 {
			b.WriteString("persistent_keepalive_interval=" + strconv.Itoa(p.PersistentKeepalive) + "\n")
		}
		b.WriteString("replace_allowed_ips=true\n")
		for _, a := range p.AllowedIPs {
			b.WriteString("allowed_ip=" + a + "\n")
		}
	}
	return b.String(), nil
}
