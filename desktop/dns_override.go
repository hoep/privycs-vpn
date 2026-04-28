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
//   ipsec:     no config-text patch (the .sswan / .mobileconfig
//              format is binary/JSON and gets parsed by strongSwan
//              or the macOS NetworkExtension API). Returns the
//              config unchanged and logs a notice. IPSec DNS
//              override on Desktop would require setting the
//              charon-side dns-servers on the SA - separate fix.
//
// Empty override is a no-op (returns the input unchanged).
func (a *App) applyDnsOverride(cfg []byte, protocol string) []byte {
	if a == nil {
		return cfg
	}
	override := strings.TrimSpace(a.settings.DNSOverride)
	if override == "" {
		return cfg
	}
	servers := parseDnsServers(override)
	if len(servers) == 0 {
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
		log.Printf("DNS override (IPSec): not applied - .sswan/.mobileconfig DNS override requires charon-side fix (deferred)")
		return cfg
	default:
		return cfg
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
