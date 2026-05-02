//go:build darwin

package main

import (
	"bufio"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun"
)

// In-process WireGuard tunnel for macOS — replaces the wg-quick + wireguard-go
// binary chain that breaks under launchd because wg-quick detects launchd and
// becomes a foreground process whose teardown trap removes the userspace
// socket. This file owns the utun and the WireGuard device directly inside
// the privileged helper process; status/handshake queries are then trivial
// IpcGet calls instead of brittle `wg show` execs against an external socket.
//
// Architectural reference: tailscaled embeds wireguard-go the same way for
// the same reason. The official WireGuard.app for macOS uses NetworkExtension
// which is the next architectural layer above this; we deliberately stay
// below it to avoid the Apple Developer Program + entitlement workflow.

// wgPeer holds the parsed [Peer] block from a .conf file.
type wgPeer struct {
	PublicKey           string // base64, 32-byte
	PresharedKey        string // base64, 32-byte (optional)
	Endpoint            string // host:port — hostname is resolved to IP at apply time
	AllowedIPs          []string
	PersistentKeepalive int
}

// wgConfigParsed holds the parsed [Interface] block + all [Peer] blocks.
type wgConfigParsed struct {
	PrivateKey string // base64, 32-byte
	Addresses  []string
	DNS        []string
	MTU        int
	ListenPort int
	Peers      []wgPeer
}

// wgDarwinTunnel is the per-tunnel runtime state we own once Up returns.
type wgDarwinTunnel struct {
	dev          *device.Device
	tunDev       tun.Device
	realIface    string                // e.g. "utun8" — kernel-allocated
	addedRoutesV4 []string             // CIDRs we added on darwin via /sbin/route
	addedRoutesV6 []string
	endpointBypassV4 string             // /32 host route to the WG server's IP
	savedDNS     map[string][]string   // network-service → previous DNS server list
	savedSearch  map[string][]string   // network-service → previous search domains
}

var (
	wgDarwinTunnels   = make(map[string]*wgDarwinTunnel) // friendly name → state
	wgDarwinTunnelsMu sync.Mutex
)

// parseWGConf reads a wg-quick-style .conf and returns the parsed structure.
// The grammar tolerates whitespace, comments (# or ;), and the standard wg
// camelCase keys (PrivateKey, AllowedIPs, ...). Multiple comma-separated
// values per line are split. AllowedIPs aggregate across multiple lines
// within a [Peer] block.
func parseWGConf(text string) (*wgConfigParsed, error) {
	cfg := &wgConfigParsed{}
	var currentPeer *wgPeer
	section := ""

	scanner := bufio.NewScanner(strings.NewReader(text))
	for scanner.Scan() {
		raw := scanner.Text()
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(strings.Trim(line, "[]"))
			if section == "peer" {
				cfg.Peers = append(cfg.Peers, wgPeer{})
				currentPeer = &cfg.Peers[len(cfg.Peers)-1]
			}
			continue
		}
		// Skip wg-quick directives we don't honor in-process (PostUp/PreDown
		// are handled by our own routing/DNS code below, not by re-running
		// shell snippets). These keys are silently dropped, same as wg
		// userspace tooling treats them.
		eq := strings.Index(line, "=")
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		keyLower := strings.ToLower(key)

		switch section {
		case "interface":
			switch keyLower {
			case "privatekey":
				cfg.PrivateKey = val
			case "address":
				for _, p := range splitCSV(val) {
					cfg.Addresses = append(cfg.Addresses, p)
				}
			case "dns":
				for _, p := range splitCSV(val) {
					cfg.DNS = append(cfg.DNS, p)
				}
			case "mtu":
				if n, err := strconv.Atoi(val); err == nil {
					cfg.MTU = n
				}
			case "listenport":
				if n, err := strconv.Atoi(val); err == nil {
					cfg.ListenPort = n
				}
			}
		case "peer":
			if currentPeer == nil {
				continue
			}
			switch keyLower {
			case "publickey":
				currentPeer.PublicKey = val
			case "presharedkey":
				currentPeer.PresharedKey = val
			case "endpoint":
				currentPeer.Endpoint = val
			case "allowedips":
				for _, p := range splitCSV(val) {
					currentPeer.AllowedIPs = append(currentPeer.AllowedIPs, p)
				}
			case "persistentkeepalive":
				if n, err := strconv.Atoi(val); err == nil {
					currentPeer.PersistentKeepalive = n
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan conf: %w", err)
	}
	if cfg.PrivateKey == "" {
		return nil, fmt.Errorf("missing PrivateKey in [Interface]")
	}
	if len(cfg.Peers) == 0 {
		return nil, fmt.Errorf("no [Peer] blocks")
	}
	if cfg.MTU == 0 {
		cfg.MTU = 1420 // wireguard-go default; wg-quick on Mac uses 1340 for our endpoint but 1420 is the safe upstream default
	}
	return cfg, nil
}

// splitCSV splits "a, b , c" into ["a", "b", "c"].
func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		t := strings.TrimSpace(p)
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

// b64ToHex decodes a 32-byte WireGuard key encoded as base64 (the .conf
// format) and re-encodes it as hex (the UAPI format). Returns "" if val
// is empty so we can branch on emptiness in the caller.
func b64ToHex(val string) (string, error) {
	if val == "" {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(val)
	if err != nil {
		return "", fmt.Errorf("base64 decode: %w", err)
	}
	if len(raw) != 32 {
		return "", fmt.Errorf("expected 32-byte key, got %d", len(raw))
	}
	return hex.EncodeToString(raw), nil
}

// resolveEndpoint takes "hostname:port" or "ip:port" (v4 or v6 in brackets)
// and returns "ip:port" suitable for the UAPI endpoint= directive. The
// wireguard-go device does not resolve hostnames itself — passing a host
// name results in the peer being permanently unrouted.
func resolveEndpoint(ep string) (string, error) {
	host, port, err := net.SplitHostPort(ep)
	if err != nil {
		return "", fmt.Errorf("split endpoint %q: %w", ep, err)
	}
	if ip := net.ParseIP(host); ip != nil {
		return ep, nil
	}
	addrs, err := net.LookupIP(host)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", host, err)
	}
	if len(addrs) == 0 {
		return "", fmt.Errorf("no addresses for %s", host)
	}
	// Prefer IPv4 when available — matches wg-quick's behavior. The bypass
	// route logic at the caller side relies on the IP family choice here.
	for _, a := range addrs {
		if v4 := a.To4(); v4 != nil {
			return net.JoinHostPort(v4.String(), port), nil
		}
	}
	return net.JoinHostPort(addrs[0].String(), port), nil
}

// buildUAPI assembles the WireGuard userspace IPC configuration string from
// the parsed conf. Format documented at https://www.wireguard.com/xplatform/.
// "set=1" is the v1 protocol prefix; replace_peers=true ensures the device
// is reset to exactly this peer set; per-peer replace_allowed_ips=true
// likewise resets each peer's allowed-ip set.
func buildUAPI(cfg *wgConfigParsed) (string, error) {
	var b strings.Builder

	privHex, err := b64ToHex(cfg.PrivateKey)
	if err != nil {
		return "", fmt.Errorf("private_key: %w", err)
	}
	b.WriteString("private_key=" + privHex + "\n")
	if cfg.ListenPort > 0 {
		b.WriteString("listen_port=" + strconv.Itoa(cfg.ListenPort) + "\n")
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

// wgDarwinUp brings up an in-process WireGuard tunnel. friendlyName is the
// name that the rest of the app uses (e.g. "privycs-ob-d11c"); the kernel
// assigns the actual utunN at TUN-create time and we record both for later
// teardown. configContent is the .conf text as the user/gateway provided.
//
// On error, partially-applied state is rolled back: the TUN closed, any
// added routes deleted, any DNS swap reverted. Caller does NOT need a
// defer-cleanup.
func wgDarwinUp(friendlyName, configContent string) (string, error) {
	wgDarwinTunnelsMu.Lock()
	if existing, ok := wgDarwinTunnels[friendlyName]; ok {
		wgDarwinTunnelsMu.Unlock()
		return existing.realIface, fmt.Errorf("tunnel %q already up on %s", friendlyName, existing.realIface)
	}
	wgDarwinTunnelsMu.Unlock()

	cfg, err := parseWGConf(configContent)
	if err != nil {
		return "", fmt.Errorf("parse conf: %w", err)
	}

	// Resolve the WG server endpoint up-front so we can install the bypass
	// host route before any catch-all routes via the tunnel are added —
	// otherwise the very first handshake packet to the server would match
	// 0.0.0.0/1 and loop into the tunnel that's still trying to handshake.
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

	// Open utun via wireguard-go's tun library. The "utun" name (no number
	// suffix) lets the kernel allocate a free utunN slot. Real interface
	// name comes back from TunDevice.Name().
	tunDev, err := tun.CreateTUN("utun", cfg.MTU)
	if err != nil {
		return "", fmt.Errorf("create utun: %w", err)
	}
	realIface, err := tunDev.Name()
	if err != nil {
		tunDev.Close()
		return "", fmt.Errorf("query tun name: %w", err)
	}
	log.Printf("wgDarwinUp[%s]: TUN allocated as %s, applying UAPI", friendlyName, realIface)

	// Build the UAPI config and create+start the device. The Up call
	// activates packet processing; without it the device exists but does
	// not actually move bytes.
	uapi, err := buildUAPI(cfg)
	if err != nil {
		tunDev.Close()
		return "", fmt.Errorf("build UAPI: %w", err)
	}
	logger := device.NewLogger(device.LogLevelError, fmt.Sprintf("[wg-%s] ", realIface))
	dev := device.NewDevice(tunDev, conn.NewDefaultBind(), logger)
	if err := dev.IpcSet(uapi); err != nil {
		dev.Close()
		return "", fmt.Errorf("IpcSet: %w", err)
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		return "", fmt.Errorf("device.Up: %w", err)
	}

	state := &wgDarwinTunnel{
		dev:         dev,
		tunDev:      tunDev,
		realIface:   realIface,
		savedDNS:    make(map[string][]string),
		savedSearch: make(map[string][]string),
	}

	// Configure the interface itself (addresses, MTU, up-flag). MTU was
	// passed to CreateTUN already so we don't repeat it here. ifconfig
	// inet uses the wg-quick "alias" form so multiple addresses can co-
	// exist on the same utun.
	for _, addr := range cfg.Addresses {
		ip, ipnet, err := net.ParseCIDR(addr)
		if err != nil {
			rollbackUp(state)
			return "", fmt.Errorf("parse address %q: %w", addr, err)
		}
		if ip.To4() != nil {
			// IPv4: ifconfig <utun> inet <ip>/<mask> <ip> alias
			mask, _ := ipnet.Mask.Size()
			args := []string{realIface, "inet", fmt.Sprintf("%s/%d", ip.String(), mask), ip.String(), "alias"}
			if out, err := exec.Command("/sbin/ifconfig", args...).CombinedOutput(); err != nil {
				rollbackUp(state)
				return "", fmt.Errorf("ifconfig inet %s: %v: %s", addr, err, strings.TrimSpace(string(out)))
			}
		} else {
			mask, _ := ipnet.Mask.Size()
			args := []string{realIface, "inet6", fmt.Sprintf("%s/%d", ip.String(), mask), "alias"}
			if out, err := exec.Command("/sbin/ifconfig", args...).CombinedOutput(); err != nil {
				rollbackUp(state)
				return "", fmt.Errorf("ifconfig inet6 %s: %v: %s", addr, err, strings.TrimSpace(string(out)))
			}
		}
	}
	if out, err := exec.Command("/sbin/ifconfig", realIface, "up").CombinedOutput(); err != nil {
		rollbackUp(state)
		return "", fmt.Errorf("ifconfig up: %v: %s", err, strings.TrimSpace(string(out)))
	}

	// Endpoint bypass route — must be installed BEFORE the catch-all routes
	// so the first handshake packet escapes the tunnel-loop. v0.9.14.24's
	// PostUp injection in buildWGConfigWithBypass solved the same problem
	// when wg-quick was the path; here we do it directly.
	if endpointIPv4 != "" {
		if gw := defaultGatewayIPv4(); gw != "" {
			args := []string{"-q", "-n", "add", "-inet", endpointIPv4 + "/32", "-gateway", gw}
			if out, err := exec.Command("/sbin/route", args...).CombinedOutput(); err == nil {
				state.endpointBypassV4 = endpointIPv4
				log.Printf("wgDarwinUp[%s]: endpoint bypass route %s/32 via %s installed", friendlyName, endpointIPv4, gw)
			} else {
				log.Printf("wgDarwinUp[%s]: endpoint bypass install warning: %v: %s", friendlyName, err, strings.TrimSpace(string(out)))
			}
		} else {
			log.Printf("wgDarwinUp[%s]: no IPv4 default gateway found, skipping endpoint bypass — handshake may loop", friendlyName)
		}
	}

	// Catch-all routes for each peer's AllowedIPs. We add each CIDR as
	// directly-attached to the utun. wg-quick on Mac splits 0.0.0.0/0 into
	// /1+/1 to avoid replacing the system default; we do the same here so
	// the user's existing default route stays intact.
	for _, peer := range cfg.Peers {
		for _, raw := range peer.AllowedIPs {
			cidrs := splitDefaultRoute(raw)
			for _, c := range cidrs {
				family := "-inet"
				if strings.Contains(c, ":") {
					family = "-inet6"
				}
				args := []string{"-q", "-n", "add", family, c, "-interface", realIface}
				if out, err := exec.Command("/sbin/route", args...).CombinedOutput(); err != nil {
					// route add can fail with EEXIST on overlapping
					// CIDRs from previous runs. Log and keep going —
					// stopping on the first overlap would abort
					// otherwise-valid setups.
					log.Printf("wgDarwinUp[%s]: route add %s warning: %v: %s", friendlyName, c, err, strings.TrimSpace(string(out)))
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

	// DNS swap — per network service. Wi-Fi, Thunderbolt Bridge, etc.
	// We snapshot the previous setting via networksetup -getdnsservers
	// so wgDarwinDown can restore it. Failure to query/set a single
	// service is a warning, not a fatal — some users have services that
	// don't accept DNS overrides (PPP, CarPlay, etc.).
	if len(cfg.DNS) > 0 {
		services := networkServicesDarwin()
		for _, svc := range services {
			if saved, err := getDNSServers(svc); err == nil {
				state.savedDNS[svc] = saved
			}
			if saved, err := getSearchDomains(svc); err == nil {
				state.savedSearch[svc] = saved
			}
			if err := setDNSServers(svc, cfg.DNS); err != nil {
				log.Printf("wgDarwinUp[%s]: setDNS %s: %v", friendlyName, svc, err)
				continue
			}
			// Match wg-quick: clear search domains by setting them to "Empty".
			_ = setSearchDomains(svc, []string{"Empty"})
		}
	}

	wgDarwinTunnelsMu.Lock()
	wgDarwinTunnels[friendlyName] = state
	wgDarwinTunnelsMu.Unlock()

	log.Printf("wgDarwinUp[%s]: tunnel up on %s (%d v4 routes, %d v6 routes)", friendlyName, realIface, len(state.addedRoutesV4), len(state.addedRoutesV6))
	return realIface, nil
}

// rollbackUp performs partial-failure cleanup. Called from wgDarwinUp on
// any post-Up error.
func rollbackUp(state *wgDarwinTunnel) {
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

// wgDarwinDown tears down a previously-Upped tunnel. Idempotent: returns nil
// if the tunnel is not currently up. Removes routes, restores DNS, closes
// the device, then closes the TUN.
func wgDarwinDown(friendlyName string) error {
	wgDarwinTunnelsMu.Lock()
	state, ok := wgDarwinTunnels[friendlyName]
	if ok {
		delete(wgDarwinTunnels, friendlyName)
	}
	wgDarwinTunnelsMu.Unlock()
	if !ok {
		log.Printf("wgDarwinDown[%s]: tunnel not in registry, treating as already-down", friendlyName)
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
	// device.Close handles tun.Close internally; still belt-and-suspenders
	// in case of partial-init paths.
	if state.tunDev != nil {
		state.tunDev.Close()
	}
	log.Printf("wgDarwinDown[%s]: tunnel down (%s released)", friendlyName, state.realIface)
	return nil
}

// wgDarwinStatus returns the WireGuard userspace UAPI dump for the named
// tunnel, plus a "connected" bool derived from peer last-handshake age.
// Used by privycs-vpn helper's cmdStatus on darwin.
func wgDarwinStatus(friendlyName string) (uapi string, connected bool, err error) {
	wgDarwinTunnelsMu.Lock()
	state, ok := wgDarwinTunnels[friendlyName]
	wgDarwinTunnelsMu.Unlock()
	if !ok {
		return "", false, fmt.Errorf("tunnel not running")
	}
	out, err := state.dev.IpcGet()
	if err != nil {
		return "", false, fmt.Errorf("IpcGet: %w", err)
	}
	connected = uapiHasRecentHandshake(out)
	return out, connected, nil
}

// uapiHasRecentHandshake parses an IpcGet output for a non-zero
// last_handshake_time_sec. WireGuard sends a handshake at most every 120s
// when traffic is flowing; presence of any non-zero handshake time means
// the peer responded at least once and the tunnel is alive.
func uapiHasRecentHandshake(uapi string) bool {
	for _, line := range strings.Split(uapi, "\n") {
		if strings.HasPrefix(line, "last_handshake_time_sec=") {
			val := strings.TrimPrefix(line, "last_handshake_time_sec=")
			if n, _ := strconv.ParseInt(strings.TrimSpace(val), 10, 64); n > 0 {
				return true
			}
		}
	}
	return false
}

// splitDefaultRoute mirrors wg-quick's split of 0.0.0.0/0 → /1+/1 and
// ::/0 → /1+/1. Without this, `route add 0.0.0.0/0 -interface utun` would
// REPLACE the existing default route, which the OS treats specially and
// which would trap our endpoint bypass into the tunnel.
func splitDefaultRoute(cidr string) []string {
	switch cidr {
	case "0.0.0.0/0":
		return []string{"0.0.0.0/1", "128.0.0.0/1"}
	case "::/0":
		return []string{"::/1", "8000::/1"}
	}
	return []string{cidr}
}

// defaultGatewayIPv4 returns the current IPv4 default gateway as a dotted-quad
// string, or "" if none. Reads `route -n get default` and scrapes the
// "gateway:" line. Used for endpoint bypass route construction.
func defaultGatewayIPv4() string {
	out, err := exec.Command("/sbin/route", "-n", "get", "default").CombinedOutput()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "gateway:") {
			return strings.TrimSpace(strings.TrimPrefix(t, "gateway:"))
		}
	}
	return ""
}

// networkServicesDarwin enumerates active network services that accept DNS
// override. Filters out the "*" disabled marker that networksetup -listallnetworkservices
// puts in front of disabled services, plus the boilerplate first line.
func networkServicesDarwin() []string {
	out, err := exec.Command("/usr/sbin/networksetup", "-listallnetworkservices").CombinedOutput()
	if err != nil {
		return nil
	}
	services := []string{}
	for i, line := range strings.Split(string(out), "\n") {
		t := strings.TrimSpace(line)
		if t == "" || i == 0 {
			// first line is "An asterisk (*) denotes that a network service is disabled."
			continue
		}
		if strings.HasPrefix(t, "*") {
			// disabled service
			continue
		}
		services = append(services, t)
	}
	return services
}

// getDNSServers returns the DNS servers currently configured on the named
// service, or an empty slice if "There aren't any DNS Servers set" is
// reported (the typical "use DHCP" / no-override state).
func getDNSServers(service string) ([]string, error) {
	out, err := exec.Command("/usr/sbin/networksetup", "-getdnsservers", service).CombinedOutput()
	if err != nil {
		return nil, err
	}
	t := strings.TrimSpace(string(out))
	if strings.Contains(t, "aren't any DNS Servers set") || strings.Contains(t, "no DNS") {
		return nil, nil
	}
	return strings.Fields(t), nil
}

func getSearchDomains(service string) ([]string, error) {
	out, err := exec.Command("/usr/sbin/networksetup", "-getsearchdomains", service).CombinedOutput()
	if err != nil {
		return nil, err
	}
	t := strings.TrimSpace(string(out))
	if strings.Contains(t, "aren't any Search Domains set") || strings.Contains(t, "no Search") {
		return nil, nil
	}
	return strings.Fields(t), nil
}

func setDNSServers(service string, servers []string) error {
	args := []string{"-setdnsservers", service}
	if len(servers) == 0 {
		args = append(args, "Empty")
	} else {
		args = append(args, servers...)
	}
	return exec.Command("/usr/sbin/networksetup", args...).Run()
}

func setSearchDomains(service string, domains []string) error {
	args := []string{"-setsearchdomains", service}
	if len(domains) == 0 {
		args = append(args, "Empty")
	} else {
		args = append(args, domains...)
	}
	return exec.Command("/usr/sbin/networksetup", args...).Run()
}
