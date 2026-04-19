package main

import (
	"fmt"
	"log"
	"net"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

// IPv6 LAN Bypass
// ================
//
// Server-pushed `route-ipv6 <PREFIX> fe80::<LL>` directives are useless
// on all three desktop OSes — OpenVPN's route-install code path has no
// way to tell the OS which physical interface the link-local gateway
// sits on, so the route either lands on the tunnel adapter (wrong,
// makes traffic loop through the tunnel) or is silently skipped
// (Windows netsh with metric=16 auto-fallback, Linux EHOSTUNREACH).
//
// Correct handling must happen client-side, AFTER the tunnel is up:
//   1. Discover the physical adapter that still has a non-VPN default
//      IPv6 route (i.e. the WiFi / Ethernet carrying the real LAN).
//   2. Extract its IPv6 default gateway (always a link-local address
//      on consumer routers).
//   3. Install one more-specific route per configured LAN prefix via
//      that adapter + gateway with metric=1. Longest-prefix-match then
//      beats the tunnel's ::/1 + 8000::/1 defaults for those prefixes
//      only — everything else still goes through the VPN.
//
// The inverse operation removes the routes on disconnect so the host
// isn't left with stale state after the tunnel goes down.

// ipv6Gateway describes the physical adapter's IPv6 default gateway as
// discovered from the OS routing table. All fields MUST be populated
// for route installation to succeed — a zero-valued struct means
// "discovery failed, cannot install bypass".
type ipv6Gateway struct {
	IfaceName  string // e.g. "wlan0", "en0", "Ethernet 2"
	IfaceIndex int    // Windows uses numeric index; Linux/macOS use name
	NextHop    string // e.g. "fe80::1e0b:8bff:fe16:ee65"
}

func (g ipv6Gateway) valid() bool {
	return g.NextHop != "" && (g.IfaceName != "" || g.IfaceIndex > 0)
}

// ApplyIPv6LANBypass installs more-specific routes for each CIDR in
// `prefixes` through the physical default IPv6 gateway. Returns nil if
// the list is empty (user has no bypass configured). Any single route
// failure is logged and skipped — partial success is better than all-
// or-nothing because users usually want at least the reachable subset.
func ApplyIPv6LANBypass(prefixes []string) error {
	if len(prefixes) == 0 {
		return nil
	}

	gw, err := discoverIPv6Gateway()
	if err != nil {
		return fmt.Errorf("discover IPv6 gateway: %w", err)
	}
	if !gw.valid() {
		// No native IPv6 on the physical adapter. Nothing to bypass TO.
		// Silently skip — this is a legitimate "nothing-to-do" state,
		// not an error.
		log.Printf("IPv6 LAN bypass: no physical IPv6 gateway detected, skipping %d prefix(es)", len(prefixes))
		return nil
	}

	log.Printf("IPv6 LAN bypass: installing %d prefix(es) via %s on %s",
		len(prefixes), gw.NextHop, gw.IfaceName)

	for _, p := range prefixes {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, _, err := net.ParseCIDR(p); err != nil {
			log.Printf("IPv6 LAN bypass: skipping invalid prefix %q: %v", p, err)
			continue
		}
		if err := installIPv6Route(p, gw); err != nil {
			log.Printf("IPv6 LAN bypass: failed to install %s via %s: %v", p, gw.NextHop, err)
			continue
		}
	}
	return nil
}

// RemoveIPv6LANBypass tears down routes installed by ApplyIPv6LANBypass.
// Designed to be safe to call even if ApplyIPv6LANBypass was never run
// or failed — platform delete commands no-op on missing routes.
func RemoveIPv6LANBypass(prefixes []string) error {
	if len(prefixes) == 0 {
		return nil
	}
	// We don't need the gateway for delete on any OS — the route-del
	// command accepts just the destination prefix and interface (or on
	// Windows, just the prefix + interface index). Discover only the
	// interface, without caring about NextHop.
	gw, err := discoverIPv6Gateway()
	if err != nil {
		// On disconnect discovery can fail because the adapter is down
		// — don't error out. We'll attempt a best-effort delete by
		// prefix alone.
		log.Printf("IPv6 LAN bypass remove: gateway discovery failed (%v), attempting prefix-only delete", err)
	}
	for _, p := range prefixes {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if err := removeIPv6Route(p, gw); err != nil {
			log.Printf("IPv6 LAN bypass remove: %s: %v", p, err)
		}
	}
	return nil
}

// ============================================================================
// Platform-specific implementations
// ============================================================================

func discoverIPv6Gateway() (ipv6Gateway, error) {
	switch runtime.GOOS {
	case "linux":
		return discoverIPv6GatewayLinux()
	case "darwin":
		return discoverIPv6GatewayDarwin()
	case "windows":
		return discoverIPv6GatewayWindows()
	default:
		return ipv6Gateway{}, fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}
}

func installIPv6Route(prefix string, gw ipv6Gateway) error {
	switch runtime.GOOS {
	case "linux":
		return installIPv6RouteLinux(prefix, gw)
	case "darwin":
		return installIPv6RouteDarwin(prefix, gw)
	case "windows":
		return installIPv6RouteWindows(prefix, gw)
	default:
		return fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}
}

func removeIPv6Route(prefix string, gw ipv6Gateway) error {
	switch runtime.GOOS {
	case "linux":
		return removeIPv6RouteLinux(prefix, gw)
	case "darwin":
		return removeIPv6RouteDarwin(prefix, gw)
	case "windows":
		return removeIPv6RouteWindows(prefix, gw)
	default:
		return fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}
}

// ----- Linux ---------------------------------------------------------

// discoverIPv6GatewayLinux parses `ip -6 route show default` output.
// Only returns gateways on non-tunnel interfaces — we skip tun*/wg*/
// utun* so the bypass doesn't accidentally point back into the VPN.
//
// Example line:
//
//	default via fe80::1e0b:8bff:fe16:ee65 dev wlan0 proto ra metric 600 pref medium
func discoverIPv6GatewayLinux() (ipv6Gateway, error) {
	out, err := exec.Command("ip", "-6", "route", "show", "default").CombinedOutput()
	if err != nil {
		return ipv6Gateway{}, fmt.Errorf("ip -6 route: %w: %s", err, string(out))
	}
	re := regexp.MustCompile(`default via (\S+) dev (\S+)`)
	for _, line := range strings.Split(string(out), "\n") {
		m := re.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		iface := m[2]
		if isTunnelInterface(iface) {
			continue
		}
		return ipv6Gateway{IfaceName: iface, NextHop: m[1]}, nil
	}
	return ipv6Gateway{}, nil
}

func installIPv6RouteLinux(prefix string, gw ipv6Gateway) error {
	c := exec.Command("ip", "-6", "route", "replace", prefix,
		"via", gw.NextHop, "dev", gw.IfaceName, "metric", "1")
	out, err := c.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ip -6 route replace: %w: %s", err, string(out))
	}
	return nil
}

func removeIPv6RouteLinux(prefix string, gw ipv6Gateway) error {
	args := []string{"-6", "route", "del", prefix}
	if gw.IfaceName != "" {
		args = append(args, "dev", gw.IfaceName)
	}
	c := exec.Command("ip", args...)
	out, err := c.CombinedOutput()
	if err != nil {
		// "No such process" is the kernel's way of saying the route
		// was already gone. Treat as non-error.
		if strings.Contains(string(out), "No such process") {
			return nil
		}
		return fmt.Errorf("ip -6 route del: %w: %s", err, string(out))
	}
	return nil
}

// ----- macOS ---------------------------------------------------------

// discoverIPv6GatewayDarwin parses `netstat -rn -f inet6` output for the
// default route. BSD-style output; first column is destination, second
// gateway, ... flags may include "UG" for default gateway.
func discoverIPv6GatewayDarwin() (ipv6Gateway, error) {
	out, err := exec.Command("netstat", "-rn", "-f", "inet6").CombinedOutput()
	if err != nil {
		return ipv6Gateway{}, fmt.Errorf("netstat: %w: %s", err, string(out))
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		if fields[0] != "default" && fields[0] != "::/0" {
			continue
		}
		// Find the interface column (varies by OS version; usually last).
		iface := fields[len(fields)-1]
		if isTunnelInterface(iface) {
			continue
		}
		// Gateway may be fe80::xxx%en0 form — strip zone for NextHop
		// storage; we'll re-add %iface at install time so Linux-style
		// handlers don't trip.
		gwField := fields[1]
		// Accept both fe80::xxx%en0 and plain fe80::xxx.
		nextHop := gwField
		if idx := strings.Index(gwField, "%"); idx >= 0 {
			nextHop = gwField[:idx]
		}
		return ipv6Gateway{IfaceName: iface, NextHop: nextHop}, nil
	}
	return ipv6Gateway{}, nil
}

func installIPv6RouteDarwin(prefix string, gw ipv6Gateway) error {
	// BSD route(8) wants the zone ID appended to link-local gateways.
	nh := gw.NextHop
	if strings.HasPrefix(nh, "fe80:") && gw.IfaceName != "" && !strings.Contains(nh, "%") {
		nh = nh + "%" + gw.IfaceName
	}
	c := exec.Command("route", "-n", "add", "-inet6", prefix, nh)
	out, err := c.CombinedOutput()
	if err != nil {
		// "File exists" means the route was already there — idempotent.
		if strings.Contains(string(out), "File exists") {
			return nil
		}
		return fmt.Errorf("route add: %w: %s", err, string(out))
	}
	return nil
}

func removeIPv6RouteDarwin(prefix string, _ ipv6Gateway) error {
	c := exec.Command("route", "-n", "delete", "-inet6", prefix)
	out, err := c.CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "not in table") {
			return nil
		}
		return fmt.Errorf("route delete: %w: %s", err, string(out))
	}
	return nil
}

// ----- Windows -------------------------------------------------------

// discoverIPv6GatewayWindows uses PowerShell's Get-NetRoute. Output is
// CSV with header so we can parse robustly regardless of Windows locale.
func discoverIPv6GatewayWindows() (ipv6Gateway, error) {
	ps := `Get-NetRoute -AddressFamily IPv6 -DestinationPrefix '::/0' -ErrorAction SilentlyContinue | ` +
		`Where-Object { $_.NextHop -ne '::' } | ` +
		`Select-Object -First 1 InterfaceIndex, InterfaceAlias, NextHop | ` +
		`ConvertTo-Csv -NoTypeInformation`
	cmd := execHidden("powershell", "-NoProfile", "-Command", ps)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return ipv6Gateway{}, fmt.Errorf("Get-NetRoute: %w: %s", err, string(out))
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	// Expected:
	//   "InterfaceIndex","InterfaceAlias","NextHop"
	//   "10","Ethernet 2","fe80::1e0b:8bff:fe16:ee65"
	if len(lines) < 2 {
		return ipv6Gateway{}, nil // no IPv6 default route
	}
	dataLine := strings.TrimSpace(lines[1])
	dataLine = strings.ReplaceAll(dataLine, `"`, "")
	fields := strings.Split(dataLine, ",")
	if len(fields) < 3 {
		return ipv6Gateway{}, nil
	}
	idx, _ := strconv.Atoi(strings.TrimSpace(fields[0]))
	alias := strings.TrimSpace(fields[1])
	if isTunnelInterface(alias) {
		return ipv6Gateway{}, nil
	}
	return ipv6Gateway{
		IfaceName:  alias,
		IfaceIndex: idx,
		NextHop:    strings.TrimSpace(fields[2]),
	}, nil
}

func installIPv6RouteWindows(prefix string, gw ipv6Gateway) error {
	// netsh syntax: interface ipv6 add route <prefix> interface=<idx> nexthop=<addr> metric=<n> store=active
	// store=active keeps the route in the running config only so it
	// disappears on reboot — no risk of stale routes surviving a
	// non-graceful shutdown (crash, power loss).
	c := execHidden("netsh", "interface", "ipv6", "add", "route",
		prefix,
		fmt.Sprintf("interface=%d", gw.IfaceIndex),
		"nexthop="+gw.NextHop,
		"metric=1",
		"store=active",
	)
	out, err := c.CombinedOutput()
	if err != nil {
		// netsh reports "The object already exists" when the route is
		// already there. `set` works for updating so try that as a
		// fallback — covers the case where a previous disconnect didn't
		// clean up fully.
		if strings.Contains(strings.ToLower(string(out)), "already exists") {
			c2 := execHidden("netsh", "interface", "ipv6", "set", "route",
				prefix,
				fmt.Sprintf("interface=%d", gw.IfaceIndex),
				"nexthop="+gw.NextHop,
				"metric=1",
				"store=active",
			)
			if out2, err2 := c2.CombinedOutput(); err2 != nil {
				return fmt.Errorf("netsh set route: %w: %s", err2, string(out2))
			}
			return nil
		}
		return fmt.Errorf("netsh add route: %w: %s", err, string(out))
	}
	return nil
}

func removeIPv6RouteWindows(prefix string, gw ipv6Gateway) error {
	// interface= is optional for delete, but providing it avoids
	// ambiguity when the same prefix has routes on multiple adapters.
	args := []string{"interface", "ipv6", "delete", "route", prefix}
	if gw.IfaceIndex > 0 {
		args = append(args, fmt.Sprintf("interface=%d", gw.IfaceIndex))
	}
	c := execHidden("netsh", args...)
	out, err := c.CombinedOutput()
	if err != nil {
		if strings.Contains(strings.ToLower(string(out)), "element not found") {
			return nil
		}
		return fmt.Errorf("netsh delete route: %w: %s", err, string(out))
	}
	return nil
}

// isTunnelInterface returns true for adapters we must never choose as
// the "physical" bypass target. Covers Linux/Darwin (tun, utun, wg, tap,
// ppp), Windows (TAP-Windows/Wintun common aliases, and any alias that
// contains the strings "privycs", "openvpn", "wireguard", "tap", "tun"
// case-insensitively).
func isTunnelInterface(name string) bool {
	lower := strings.ToLower(name)
	prefixes := []string{"tun", "tap", "utun", "wg", "ppp"}
	for _, p := range prefixes {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	needles := []string{"privycs", "openvpn", "wireguard", "wintun"}
	for _, n := range needles {
		if strings.Contains(lower, n) {
			return true
		}
	}
	return false
}
