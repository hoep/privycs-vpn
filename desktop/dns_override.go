package main

import (
	"fmt"
	"log"
	"strings"
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
	override := strings.TrimSpace(a.settings.DNSOverride)
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
func parseDnsServers(s string) []string {
	out := make([]string, 0, 4)
	for _, part := range strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	}) {
		t := strings.TrimSpace(part)
		if t != "" {
			out = append(out, t)
		}
	}
	return out
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
