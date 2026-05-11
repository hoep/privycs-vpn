//go:build darwin

package main

import (
	"fmt"
	"log"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	awgconn "github.com/amnezia-vpn/amneziawg-go/conn"
	awgdevice "github.com/amnezia-vpn/amneziawg-go/device"
	awgtun "github.com/amnezia-vpn/amneziawg-go/tun"
)

// In-process AmneziaWG tunnel for macOS — Stage 3 of
// AMNEZIAWG_CLIENT_PLAN.md. Mirrors wgDarwinUp/Down but creates the
// tun and device via the amneziawg-go fork so that the obfuscation
// keys (jc/jmin/jmax/s1-s4/h1-h4/i1-i5/j1-j3) actually take effect on
// the wire. The address/route/DNS apparatus is identical to vanilla
// WG and reuses helpers from wg_macos.go (defaultGatewayIPv4,
// splitDefaultRoute, networkServicesDarwin, getDNSServers, ...).
//
// Why a parallel implementation instead of a runtime-flag on wgDarwinUp:
// the wireguard-go and amneziawg-go device + tun types are
// distinct (despite identical-looking APIs) — Go's type system
// won't let one device factory return both. Keeping them in
// parallel files makes the variant boundary explicit and lets the
// linker drop the unused half of the binary when build tags
// strip a platform.

type awgDarwinTunnelState struct {
	dev              *awgdevice.Device
	tunDev           awgtun.Device
	realIface        string
	addedRoutesV4    []string
	addedRoutesV6    []string
	endpointBypassV4 string
	savedDNS         map[string][]string
	savedSearch      map[string][]string
}

var (
	awgDarwinTunnels   = make(map[string]*awgDarwinTunnelState)
	awgDarwinTunnelsMu sync.Mutex
)

// buildUAPIAwg builds the AmneziaWG UAPI string. It is the WireGuard
// UAPI plus an additional block of obfuscation keys emitted at the
// [Interface] scope before any per-peer state. The fork's IpcSet
// handler at https://github.com/amnezia-vpn/amneziawg-go/blob/v1.0.4/device/uapi.go
// accepts: jc, jmin, jmax, s1, s2, s3, s4, h1, h2, h3, h4, i1, i2,
// i3, i4, i5, j1, j2, j3. We pass anything captured in
// wgConfigParsed.AwgKeys through verbatim — server-side enrollment
// guarantees the values are well-formed.
func buildUAPIAwg(cfg *wgConfigParsed) (string, error) {
	var b strings.Builder

	privHex, err := b64ToHex(cfg.PrivateKey)
	if err != nil {
		return "", fmt.Errorf("private_key: %w", err)
	}
	b.WriteString("private_key=" + privHex + "\n")
	if cfg.ListenPort > 0 {
		b.WriteString("listen_port=" + strconv.Itoa(cfg.ListenPort) + "\n")
	}
	// Obfuscation keys go BEFORE replace_peers — the fork's uapi.go
	// processes them as device-scope state, not per-peer, so the
	// ordering relative to peer blocks is irrelevant in principle,
	// but emitting them up-front keeps the wire format easy to
	// diff against a vanilla wg config.
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

// wgDarwinUpAwg brings up an in-process AmneziaWG tunnel. Parallel
// to wgDarwinUp; see that function's doc for the algorithmic shape.
// On error, partial state is rolled back via awgRollbackUp.
func wgDarwinUpAwg(friendlyName, configContent string) (string, error) {
	awgDarwinTunnelsMu.Lock()
	if existing, ok := awgDarwinTunnels[friendlyName]; ok {
		awgDarwinTunnelsMu.Unlock()
		return existing.realIface, fmt.Errorf("AWG tunnel %q already up on %s", friendlyName, existing.realIface)
	}
	awgDarwinTunnelsMu.Unlock()

	cfg, err := parseWGConf(configContent)
	if err != nil {
		return "", fmt.Errorf("parse conf: %w", err)
	}

	var endpointIPv4 string
	if len(cfg.Peers) > 0 && cfg.Peers[0].Endpoint != "" {
		ep, err := resolveEndpoint(cfg.Peers[0].Endpoint)
		if err != nil {
			return "", fmt.Errorf("resolve endpoint: %w", err)
		}
		host, _, _ := net.SplitHostPort(ep)
		if ip := net.ParseIP(host); ip != nil && ip.To4() != nil {
			endpointIPv4 = ip.String()
		}
	}

	tunDev, err := awgtun.CreateTUN("utun", cfg.MTU)
	if err != nil {
		return "", fmt.Errorf("create utun (awg): %w", err)
	}
	realIface, err := tunDev.Name()
	if err != nil {
		tunDev.Close()
		return "", fmt.Errorf("query tun name (awg): %w", err)
	}
	log.Printf("wgDarwinUpAwg[%s]: TUN allocated as %s, applying AWG UAPI", friendlyName, realIface)

	uapi, err := buildUAPIAwg(cfg)
	if err != nil {
		tunDev.Close()
		return "", fmt.Errorf("build AWG UAPI: %w", err)
	}
	logger := awgdevice.NewLogger(awgdevice.LogLevelError, fmt.Sprintf("[awg-%s] ", realIface))
	dev := awgdevice.NewDevice(tunDev, awgconn.NewDefaultBind(), logger)
	if err := dev.IpcSet(uapi); err != nil {
		dev.Close()
		return "", fmt.Errorf("AWG IpcSet: %w", err)
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		return "", fmt.Errorf("AWG device.Up: %w", err)
	}

	state := &awgDarwinTunnelState{
		dev:         dev,
		tunDev:      tunDev,
		realIface:   realIface,
		savedDNS:    make(map[string][]string),
		savedSearch: make(map[string][]string),
	}

	for _, addr := range cfg.Addresses {
		ip, ipnet, err := net.ParseCIDR(addr)
		if err != nil {
			awgRollbackUp(state)
			return "", fmt.Errorf("parse address %q: %w", addr, err)
		}
		mask, _ := ipnet.Mask.Size()
		var args []string
		if ip.To4() != nil {
			args = []string{realIface, "inet", fmt.Sprintf("%s/%d", ip.String(), mask), ip.String(), "alias"}
		} else {
			args = []string{realIface, "inet6", fmt.Sprintf("%s/%d", ip.String(), mask), "alias"}
		}
		if out, err := exec.Command("/sbin/ifconfig", args...).CombinedOutput(); err != nil {
			awgRollbackUp(state)
			return "", fmt.Errorf("ifconfig %s: %v: %s", addr, err, strings.TrimSpace(string(out)))
		}
	}
	if out, err := exec.Command("/sbin/ifconfig", realIface, "up").CombinedOutput(); err != nil {
		awgRollbackUp(state)
		return "", fmt.Errorf("ifconfig up: %v: %s", err, strings.TrimSpace(string(out)))
	}

	if endpointIPv4 != "" {
		if gw := defaultGatewayIPv4(); gw != "" {
			args := []string{"-q", "-n", "add", "-inet", endpointIPv4 + "/32", "-gateway", gw}
			if out, err := exec.Command("/sbin/route", args...).CombinedOutput(); err == nil {
				state.endpointBypassV4 = endpointIPv4
				log.Printf("wgDarwinUpAwg[%s]: endpoint bypass %s/32 via %s installed", friendlyName, endpointIPv4, gw)
			} else {
				log.Printf("wgDarwinUpAwg[%s]: endpoint bypass install warning: %v: %s", friendlyName, err, strings.TrimSpace(string(out)))
			}
		}
	}

	for _, peer := range cfg.Peers {
		for _, raw := range peer.AllowedIPs {
			for _, c := range splitDefaultRoute(raw) {
				family := "-inet"
				if strings.Contains(c, ":") {
					family = "-inet6"
				}
				args := []string{"-q", "-n", "add", family, c, "-interface", realIface}
				if out, err := exec.Command("/sbin/route", args...).CombinedOutput(); err != nil {
					log.Printf("wgDarwinUpAwg[%s]: route add %s warning: %v: %s", friendlyName, c, err, strings.TrimSpace(string(out)))
					continue
				}
				if family == "-inet" {
					state.addedRoutesV4 = append(state.addedRoutesV4, c)
				} else {
					state.addedRoutesV6 = append(state.addedRoutesV6, c)
				}
			}
		}
	}

	if len(cfg.DNS) > 0 {
		for _, svc := range networkServicesDarwin() {
			if saved, err := getDNSServers(svc); err == nil {
				state.savedDNS[svc] = saved
			}
			if saved, err := getSearchDomains(svc); err == nil {
				state.savedSearch[svc] = saved
			}
			if err := setDNSServers(svc, cfg.DNS); err != nil {
				log.Printf("wgDarwinUpAwg[%s]: setDNS %s: %v", friendlyName, svc, err)
				continue
			}
			_ = setSearchDomains(svc, []string{"Empty"})
		}
	}

	awgDarwinTunnelsMu.Lock()
	awgDarwinTunnels[friendlyName] = state
	awgDarwinTunnelsMu.Unlock()

	log.Printf("wgDarwinUpAwg[%s]: AWG tunnel up on %s (%d v4 routes, %d v6 routes, %d obf-keys)",
		friendlyName, realIface, len(state.addedRoutesV4), len(state.addedRoutesV6), len(cfg.AwgKeys))
	return realIface, nil
}

func awgRollbackUp(state *awgDarwinTunnelState) {
	if state.dev != nil {
		state.dev.Close()
	}
	if state.tunDev != nil {
		state.tunDev.Close()
	}
	for _, c := range state.addedRoutesV4 {
		exec.Command("/sbin/route", "-q", "-n", "delete", "-inet", c).Run()
	}
	for _, c := range state.addedRoutesV6 {
		exec.Command("/sbin/route", "-q", "-n", "delete", "-inet6", c).Run()
	}
	if state.endpointBypassV4 != "" {
		exec.Command("/sbin/route", "-q", "-n", "delete", "-inet", state.endpointBypassV4+"/32").Run()
	}
	for svc, saved := range state.savedDNS {
		_ = setDNSServers(svc, saved)
	}
	for svc, saved := range state.savedSearch {
		_ = setSearchDomains(svc, saved)
	}
}

// wgDarwinDownAwg tears down an AWG tunnel. Mirror of wgDarwinDown.
func wgDarwinDownAwg(friendlyName string) error {
	awgDarwinTunnelsMu.Lock()
	state, ok := awgDarwinTunnels[friendlyName]
	if ok {
		delete(awgDarwinTunnels, friendlyName)
	}
	awgDarwinTunnelsMu.Unlock()
	if !ok {
		log.Printf("wgDarwinDownAwg[%s]: tunnel not in registry, treating as already-down", friendlyName)
		return nil
	}

	for _, c := range state.addedRoutesV4 {
		exec.Command("/sbin/route", "-q", "-n", "delete", "-inet", c).Run()
	}
	for _, c := range state.addedRoutesV6 {
		exec.Command("/sbin/route", "-q", "-n", "delete", "-inet6", c).Run()
	}
	if state.endpointBypassV4 != "" {
		exec.Command("/sbin/route", "-q", "-n", "delete", "-inet", state.endpointBypassV4+"/32").Run()
	}
	for svc, saved := range state.savedDNS {
		if len(saved) == 0 {
			_ = setDNSServers(svc, []string{"Empty"})
		} else {
			_ = setDNSServers(svc, saved)
		}
	}
	for svc, saved := range state.savedSearch {
		if len(saved) == 0 {
			_ = setSearchDomains(svc, []string{"Empty"})
		} else {
			_ = setSearchDomains(svc, saved)
		}
	}

	if state.dev != nil {
		state.dev.Close()
	}
	if state.tunDev != nil {
		state.tunDev.Close()
	}
	log.Printf("wgDarwinDownAwg[%s]: AWG tunnel down (%s released)", friendlyName, state.realIface)
	return nil
}

// wgDarwinStatusAwg returns the AWG UAPI dump for the named tunnel.
// Parallel to wgDarwinStatus. The dump is in the same format as
// vanilla wg's IpcGet — uapiHasRecentHandshake works unchanged.
func wgDarwinStatusAwg(friendlyName string) (uapi string, connected bool, err error) {
	awgDarwinTunnelsMu.Lock()
	state, ok := awgDarwinTunnels[friendlyName]
	awgDarwinTunnelsMu.Unlock()
	if !ok {
		return "", false, fmt.Errorf("AWG tunnel not running")
	}
	out, err := state.dev.IpcGet()
	if err != nil {
		return "", false, fmt.Errorf("AWG IpcGet: %w", err)
	}
	return out, uapiHasRecentHandshake(out), nil
}
