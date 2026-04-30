package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"strings"
	"time"
)

// applyDnsOverride patches a config blob with the user's manual
// DNS override from Settings.DNSOverride. Single source of truth for
// "make sure the user's Settings DNS field actually reaches the
// tunnel" - earlier the field was only persisted to settings.json
// and never reached any of the three protocol Configure paths.
//
// Per-protocol semantics:
//
//   wireguard: replace (or insert) the DNS = ... line in the
//              [Interface] section. wg-quick / wgctrl honor it.
//
//   openvpn:   prepend pull-filter ignore "dhcp-option DNS" plus
//              one dhcp-option DNS <ip> line per override server.
//              The pull-filter drops the server-pushed DNS push
//              so our explicit lines win.
//
//   ipsec:     config text is binary/JSON (.sswan or .mobileconfig)
//              so we cannot patch it like WG/OpenVPN. Instead we
//              forward the override to the IPSecProtocol handler
//              via SetDnsOverride; on Up() the privileged helper
//              writes /etc/resolv.conf with backup, on Down() it
//              restores. Linux only - macOS DNS lives in the
//              .mobileconfig that's installed via prefctl, and
//              Windows IPSec uses rasdial which has no per-tunnel
//              DNS API. macOS / Windows currently log a notice
//              instead of applying.
//
// Empty override is a no-op (returns the input unchanged).
func (a *App) applyDnsOverride(cfg []byte, protocol string) []byte {
	if a == nil {
		return cfg
	}
	// Resolution priority chain: active-pool > active-single-
	// connection > global. Lets users have e.g. "Pool A uses
	// Mullvad DNS, Connection Home uses Pi-hole, everything else
	// uses Cloudflare from Settings" without toggling fields on
	// every switch.
	override := a.resolveDnsOverride()
	if override == "" {
		// Even with empty override, we should clear any stale
		// IPSec dns-override so a previous override doesn't
		// linger after the user removed it from Settings.
		if protocol == "ipsec" {
			a.applyIPSecDnsOverride(nil)
		}
		return cfg
	}
	servers := parseDnsServers(override)
	if len(servers) == 0 {
		if protocol == "ipsec" {
			a.applyIPSecDnsOverride(nil)
		}
		return cfg
	}

	switch protocol {
	case "wireguard":
		patched := patchWireGuardDns(string(cfg), servers)
		log.Printf("DNS override (WG): applied %s", strings.Join(servers, ","))
		return []byte(patched)
	case "openvpn":
		patched := patchOpenVpnDns(string(cfg), servers)
		log.Printf("DNS override (OVPN): applied %s", strings.Join(servers, ","))
		return []byte(patched)
	case "ipsec":
		a.applyIPSecDnsOverride(servers)
		return cfg
	default:
		return cfg
	}
}

// applyIPSecDnsOverride finds the IPSecProtocol handler in the App's
// protocol map and stores the override server list on it via
// SetDnsOverride. On Up() the handler forwards this list to the
// privileged helper which manages /etc/resolv.conf around the
// tunnel lifecycle. Logs and returns silently if the IPSec handler
// is absent (e.g. swanctl not installed).
func (a *App) applyIPSecDnsOverride(servers []string) {
	if a == nil || a.protocols == nil {
		return
	}
	proto, ok := a.protocols["ipsec"]
	if !ok {
		return
	}
	ipsec, ok := proto.(*IPSecProtocol)
	if !ok {
		return
	}
	ipsec.SetDnsOverride(servers)
	if len(servers) > 0 {
		log.Printf("DNS override (IPSec): forwarded to handler %s",
			strings.Join(servers, ","))
	}
}

// parseDnsServers splits a comma- or whitespace-separated string of
// IP addresses into a clean list. Empty entries dropped, surrounding
// whitespace trimmed. Caller decides whether to format them as
// comma-separated (WG DNS line) or one-per-line (OpenVPN dhcp-option).
//
// IPv6 bracket stripping: web-style "[2001:db8::1]" and
// "[2001:db8::1]:53" are reduced to "2001:db8::1" so users can
// paste from URL-style sources without breaking downstream
// formatters that expect a bare address.
func parseDnsServers(s string) []string {
	out := make([]string, 0, 4)
	for _, part := range strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	}) {
		out = append(out, normalizeDnsEntry(strings.TrimSpace(part)))
	}
	// Drop empties produced by all-whitespace entries.
	clean := out[:0]
	for _, e := range out {
		if e != "" {
			clean = append(clean, e)
		}
	}
	return clean
}

// normalizeDnsEntry strips IPv6 brackets and any trailing :port
// suffix. Best-effort: when the result is ambiguous (e.g. a bare
// IPv4 with ":53" appended) we drop the port; the inject paths
// only support default DNS port 53 anyway, custom ports cannot be
// expressed in WireGuard DNS=, OpenVPN dhcp-option DNS, or
// /etc/resolv.conf, so silently dropping the port is the right
// behaviour.
func normalizeDnsEntry(s string) string {
	s = strings.TrimSpace(s)
	// "[ipv6]:port" or "[ipv6]"
	if strings.HasPrefix(s, "[") {
		end := strings.Index(s, "]")
		if end > 0 {
			return s[1:end]
		}
		// Malformed - return as-is for downstream validator to flag.
		return s
	}
	// "ipv4:port" — dotted-quad followed by a single colon-port.
	// IPv6 has multiple colons so we only chop when there is exactly
	// one ':'.
	if strings.Count(s, ":") == 1 {
		return strings.SplitN(s, ":", 2)[0]
	}
	return s
}

// IsValidDnsServer reports whether s parses as a numeric IPv4 or
// IPv6 address. Used by validateDnsOverride and exposed via Wails
// so the Settings UI can highlight invalid entries before save.
// Hostname-style entries (e.g. "dns.cloudflare.com") are rejected
// here - the inject pipeline (WG DNS=, OpenVPN dhcp-option DNS,
// /etc/resolv.conf nameserver) only accepts numeric addresses.
func IsValidDnsServer(s string) bool {
	s = normalizeDnsEntry(s)
	if s == "" {
		return false
	}
	return net.ParseIP(s) != nil
}

// ValidateDnsOverride parses the user-entered DNS override string
// and returns a list of invalid entries (empty list = all valid).
// Used by the Settings UI to surface problems before save.
func (a *App) ValidateDnsOverride(input string) []string {
	parts := parseDnsServers(input)
	bad := make([]string, 0)
	for _, p := range parts {
		if net.ParseIP(p) == nil {
			bad = append(bad, p)
		}
	}
	return bad
}

// DnsTestResult is the wire-shape returned to the Settings UI's
// DNS Test button. ResolverHint is a best-effort label of who
// answered (matched against the well-known provider IPs); empty
// when no match.
type DnsTestResult struct {
	Host         string   `json:"host"`
	Addresses    []string `json:"addresses"`
	DurationMs   int64    `json:"duration_ms"`
	ResolverHint string   `json:"resolver_hint"`
	Error        string   `json:"error,omitempty"`
}

// TestDnsResolution probes the system DNS by resolving a known
// hostname (default cloudflare.com if empty). Used by the Settings
// "Test DNS" button to give the user a visible "DNS works / it
// returned X in Yms" signal instead of having to wait for an
// actual VPN connect to find out.
//
// Note: this resolves through the OS resolver, which on Windows
// and Linux honours whatever DNS is currently in use. While the
// VPN is connected that will be the tunnel's DNS; while
// disconnected it will be the system DNS. Either case is useful
// diagnostic information.
func (a *App) TestDnsResolution(host string) DnsTestResult {
	host = strings.TrimSpace(host)
	if host == "" {
		host = "cloudflare.com"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	addrs, err := net.DefaultResolver.LookupHost(ctx, host)
	elapsed := time.Since(start).Milliseconds()
	res := DnsTestResult{Host: host, DurationMs: elapsed}
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.Addresses = addrs
	// Provider hint: if the resolver IPs matched a known provider's
	// addresses we display that next to the test result. Best-
	// effort, no network round-trip.
	if a != nil {
		hint := DnsProviderForServers(parseDnsServers(a.settings.DNSOverride))
		if hint != "" {
			res.ResolverHint = hint
		}
	}
	return res
}

// DnsProvider is one entry in the Settings dropdown of well-known
// public resolvers. Servers are dual-stack (IPv4 + IPv6) so the
// inject pipeline gets both regardless of where the user's
// current network stack ends up resolving.
type DnsProvider struct {
	ID         string   `json:"id"`
	Label      string   `json:"label"`
	Servers    []string `json:"servers"`
	DotHost    string   `json:"dot_host,omitempty"`    // hostname for Android Private DNS / TLS
	Note       string   `json:"note,omitempty"`        // short pitch (privacy / speed / blocklists)
}

// dnsProvidersList is the canonical preset table. Keep order so
// the Settings dropdown renders deterministically.
var dnsProvidersList = []DnsProvider{
	{
		ID: "cloudflare", Label: "Cloudflare",
		Servers: []string{"1.1.1.1", "1.0.0.1", "2606:4700:4700::1111", "2606:4700:4700::1001"},
		DotHost: "cloudflare-dns.com",
		Note:    "Fast, no logging beyond 24h",
	},
	{
		ID: "cloudflare-malware", Label: "Cloudflare (block malware)",
		Servers: []string{"1.1.1.2", "1.0.0.2", "2606:4700:4700::1112", "2606:4700:4700::1002"},
		DotHost: "security.cloudflare-dns.com",
		Note:    "Blocks known malware domains",
	},
	{
		ID: "cloudflare-family", Label: "Cloudflare (block malware + adult)",
		Servers: []string{"1.1.1.3", "1.0.0.3", "2606:4700:4700::1113", "2606:4700:4700::1003"},
		DotHost: "family.cloudflare-dns.com",
		Note:    "Family-safe filtering",
	},
	{
		ID: "google", Label: "Google",
		Servers: []string{"8.8.8.8", "8.8.4.4", "2001:4860:4860::8888", "2001:4860:4860::8844"},
		DotHost: "dns.google",
		Note:    "Reliable, logs queries",
	},
	{
		ID: "quad9", Label: "Quad9 (block malware)",
		Servers: []string{"9.9.9.9", "149.112.112.112", "2620:fe::fe", "2620:fe::9"},
		DotHost: "dns.quad9.net",
		Note:    "Swiss, malware blocking, no logging",
	},
	{
		ID: "adguard", Label: "AdGuard (block ads + trackers)",
		Servers: []string{"94.140.14.14", "94.140.15.15", "2a10:50c0::ad1:ff", "2a10:50c0::ad2:ff"},
		DotHost: "dns.adguard-dns.com",
		Note:    "Default - blocks ads and trackers",
	},
	{
		ID: "adguard-family", Label: "AdGuard Family (block ads + trackers + adult)",
		Servers: []string{"94.140.14.15", "94.140.15.16", "2a10:50c0::bad1:ff", "2a10:50c0::bad2:ff"},
		DotHost: "family.adguard-dns.com",
		Note:    "Family-safe content filtering on top of ad blocking",
	},
	{
		ID: "adguard-unfiltered", Label: "AdGuard (no filtering)",
		Servers: []string{"94.140.14.140", "94.140.14.141", "2a10:50c0::1:ff", "2a10:50c0::2:ff"},
		DotHost: "unfiltered.adguard-dns.com",
		Note:    "Pass-through, no blocking",
	},
	{
		ID: "mullvad", Label: "Mullvad",
		Servers: []string{"194.242.2.2", "2a07:e340::2"},
		DotHost: "dns.mullvad.net",
		Note:    "Logging-free, run by Mullvad VPN",
	},
	{
		ID: "mullvad-adblock", Label: "Mullvad (block ads + trackers)",
		Servers: []string{"194.242.2.3", "2a07:e340::3"},
		DotHost: "adblock.dns.mullvad.net",
		Note:    "Mullvad with content blocking",
	},
}

// GetDnsProviders returns the preset table for the Settings UI.
func (a *App) GetDnsProviders() []DnsProvider {
	return dnsProvidersList
}

// DnsProviderForServers returns the human-readable provider name
// when the supplied server list matches a known preset. Empty when
// no match. Used by Settings UI to show "This is Cloudflare - tip:
// also enable Private DNS for DoT" on the Android side, and by
// TestDnsResolution as a hint label on Desktop.
func DnsProviderForServers(servers []string) string {
	if len(servers) == 0 {
		return ""
	}
	// Build a normalised lookup set from input.
	want := make(map[string]struct{}, len(servers))
	for _, s := range servers {
		ip := net.ParseIP(s)
		if ip == nil {
			continue
		}
		want[ip.String()] = struct{}{}
	}
	if len(want) == 0 {
		return ""
	}
	// A provider matches if the input is a subset of the provider's
	// canonical server list (so user with "1.1.1.1" alone still
	// matches Cloudflare; but "1.1.1.1, 8.8.8.8" matches none).
	for _, p := range dnsProvidersList {
		canonical := make(map[string]struct{}, len(p.Servers))
		for _, s := range p.Servers {
			ip := net.ParseIP(s)
			if ip == nil {
				continue
			}
			canonical[ip.String()] = struct{}{}
		}
		matched := true
		for w := range want {
			if _, ok := canonical[w]; !ok {
				matched = false
				break
			}
		}
		if matched {
			return p.Label
		}
	}
	return ""
}

// resolveDnsOverride returns the most-specific DNS override for
// the current selection, walking this priority chain:
//
//   1. Active pool's per-pool DnsOverride (if non-empty)
//   2. Active single-connection's per-connection DnsOverride
//      (if non-empty)
//   3. Global Settings.DNSOverride
//   4. Empty - the protocol's own DNS push wins
//
// Pool active and connection active are mutually exclusive in the
// data model (ActivatePool clears connections.SetActive("")), so
// the two specific branches don't fire on the same connect; the
// chain just enumerates all possible override sources by precedence.
//
// Mirrors Android's resolveDnsOverrideServers chain in
// PrivycsVpnService for cross-platform parity.
func (a *App) resolveDnsOverride() string {
	if a == nil {
		return ""
	}
	a.mu.RLock()
	poolID := a.activePoolID
	a.mu.RUnlock()
	if poolID != "" {
		if pool := a.pools.Get(poolID); pool != nil && strings.TrimSpace(pool.DnsOverride) != "" {
			return strings.TrimSpace(pool.DnsOverride)
		}
	}
	if conn := a.connections.Active(); conn != nil && strings.TrimSpace(conn.DnsOverride) != "" {
		return strings.TrimSpace(conn.DnsOverride)
	}
	return strings.TrimSpace(a.settings.DNSOverride)
}

// patchWireGuardDns replaces (or inserts) the DNS line in the
// [Interface] section. Same approach as Android's
// patchWireGuardDnsOverride for cross-platform parity.
func patchWireGuardDns(content string, servers []string) string {
	newLine := fmt.Sprintf("DNS = %s", strings.Join(servers, ", "))
	lines := strings.Split(content, "\n")
	interfaceStart := -1
	interfaceEnd := len(lines)
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.EqualFold(trimmed, "[Interface]") {
			interfaceStart = i
		} else if interfaceStart >= 0 && strings.HasPrefix(trimmed, "[") {
			interfaceEnd = i
			break
		}
	}
	if interfaceStart < 0 {
		log.Println("DNS override (WG): [Interface] section not found, override skipped")
		return content
	}
	for i := interfaceStart + 1; i < interfaceEnd; i++ {
		t := strings.TrimSpace(lines[i])
		if strings.HasPrefix(strings.ToUpper(t), "DNS") &&
			strings.Contains(t, "=") {
			lines[i] = newLine
			return strings.Join(lines, "\n")
		}
	}
	// No DNS line present - insert at end of [Interface] section.
	out := make([]string, 0, len(lines)+1)
	out = append(out, lines[:interfaceEnd]...)
	out = append(out, newLine)
	out = append(out, lines[interfaceEnd:]...)
	return strings.Join(out, "\n")
}

// patchOpenVpnDns prepends pull-filter + dhcp-option directives to
// drop the server's DNS push and emit the override values instead.
func patchOpenVpnDns(content string, servers []string) string {
	var sb strings.Builder
	sb.WriteString("pull-filter ignore \"dhcp-option DNS\"\n")
	for _, s := range servers {
		sb.WriteString("dhcp-option DNS ")
		sb.WriteString(s)
		sb.WriteByte('\n')
	}
	sb.WriteString(content)
	return sb.String()
}
