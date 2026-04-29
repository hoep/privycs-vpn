package main

import (
	"fmt"
	"log"
	"strings"
)

// SplitTunnelInject patches a config blob with split-tunnel bypass
// directives derived from the pool's PoolSplitTunnel settings.
// Mirror of Android's data.SplitTunnelInjector.
//
//   wireguard: replace [Peer] AllowedIPs with the complement.
//   openvpn:   prepend route + route-ipv6 net_gateway directives
//              plus pull-filter ignore for matching server pushes.
//   ipsec:     no-op + warning. Traffic selectors require server
//              cooperation.
//
// Returns (patchedConfig, applied, skipReason).
//   applied=true  -> caller can log success; patchedConfig is the
//                    modified blob.
//   applied=false -> skipReason explains why (member config not
//                    full-tunnel, IPSec, etc); patchedConfig equals
//                    the input.
func SplitTunnelInject(configContent string, protocol string, st PoolSplitTunnel) (string, bool, string) {
	if !st.IsActive() {
		return configContent, false, ""
	}

	parsed := make([]Cidr, 0, len(st.BypassCidrs))
	for _, raw := range st.BypassCidrs {
		c, err := ParseCidr(raw)
		if err == nil {
			parsed = append(parsed, c)
		}
	}
	if st.ExcludePrivateNetworks {
		parsed = append(parsed, PrivateNetworks...)
	}
	if len(parsed) == 0 {
		return configContent, false, "no valid CIDRs after parse"
	}

	switch protocol {
	case "wireguard":
		return patchWGSplit(configContent, parsed)
	case "openvpn":
		return patchOVPNSplit(configContent, parsed)
	case "ipsec":
		log.Printf("split tunnel skipped: IPSec doesn't support client-side traffic-selector narrowing")
		return configContent, false, "IPSec member - client-side split tunnel not supported"
	default:
		return configContent, false, "unknown protocol"
	}
}

// patchWGSplit: WireGuard config-text patch. Find AllowedIPs lines
// in any [Peer] section, parse, compute complement, replace.
// Disable if no full-tunnel coverage in original.
func patchWGSplit(content string, bypass []Cidr) (string, bool, string) {
	lines := strings.Split(content, "\n")
	allowedIdxs := []int{}
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if !strings.HasPrefix(strings.ToLower(t), "allowedips") {
			continue
		}
		if !strings.Contains(t, "=") {
			continue
		}
		allowedIdxs = append(allowedIdxs, i)
	}
	if len(allowedIdxs) == 0 {
		return content, false, "no AllowedIPs line in config"
	}

	// Aggregate existing AllowedIPs CIDRs across the lines.
	existing := []Cidr{}
	for _, idx := range allowedIdxs {
		rhs := strings.TrimSpace(lines[idx][strings.IndexByte(lines[idx], '=')+1:])
		for _, raw := range strings.Split(rhs, ",") {
			c, err := ParseCidr(strings.TrimSpace(raw))
			if err != nil {
				continue
			}
			existing = append(existing, c)
		}
	}

	hasV4Univ := false
	hasV6Univ := false
	for _, c := range existing {
		if c.Prefix != 0 {
			continue
		}
		if c.IsV4() {
			hasV4Univ = true
		} else {
			hasV6Univ = true
		}
	}

	if !hasV4Univ && !hasV6Univ {
		log.Printf("split tunnel skipped: AllowedIPs is not full-tunnel " +
			"(no 0.0.0.0/0 or ::/0) - existing routes would conflict")
		return content, false, "member config is not full-tunnel; pool-level bypass cannot apply safely"
	}

	// Compute complement only for the families present.
	effectiveBypass := make([]Cidr, 0, len(bypass))
	for _, c := range bypass {
		if (c.IsV4() && hasV4Univ) || (!c.IsV4() && hasV6Univ) {
			effectiveBypass = append(effectiveBypass, c)
		}
	}
	complement := SubtractFromUniverse(effectiveBypass)
	filtered := complement[:0]
	for _, c := range complement {
		if (c.IsV4() && hasV4Univ) || (!c.IsV4() && hasV6Univ) {
			filtered = append(filtered, c)
		}
	}
	if len(filtered) == 0 {
		log.Printf("split tunnel skipped: bypass covers full universe")
		return content, false, "bypass set covers entire address space"
	}

	parts := make([]string, 0, len(filtered))
	for _, c := range filtered {
		parts = append(parts, c.String())
	}
	replacement := "AllowedIPs = " + strings.Join(parts, ", ")

	// Replace first AllowedIPs line; remove any subsequent ones
	// (multi-line entries collapsed into one for readability).
	out := make([]string, 0, len(lines))
	firstReplaced := false
	for i, l := range lines {
		isAllowed := false
		for _, idx := range allowedIdxs {
			if idx == i {
				isAllowed = true
				break
			}
		}
		if !isAllowed {
			out = append(out, l)
			continue
		}
		if !firstReplaced {
			out = append(out, replacement)
			firstReplaced = true
		}
		// subsequent AllowedIPs lines: drop
	}
	log.Printf("WG split tunnel applied: %d bypass CIDRs, %d resulting AllowedIPs entries",
		len(effectiveBypass), len(filtered))
	return strings.Join(out, "\n"), true, ""
}

// patchOVPNSplit: prepend route net_gateway directives + pull-filter
// ignore for matching server pushes.
func patchOVPNSplit(content string, bypass []Cidr) (string, bool, string) {
	var sb strings.Builder
	sb.WriteString("# Privycs split tunnel: client-side bypass routes\n")
	for _, c := range bypass {
		ip := strings.SplitN(c.String(), "/", 2)[0]
		if c.IsV4() {
			fmt.Fprintf(&sb, "pull-filter ignore \"route %s\"\n", ip)
		} else {
			fmt.Fprintf(&sb, "pull-filter ignore \"route-ipv6 %s\"\n", ip)
		}
	}
	for _, c := range bypass {
		if c.IsV4() {
			ip := strings.SplitN(c.String(), "/", 2)[0]
			mask := prefixToIPv4Mask(c.Prefix)
			fmt.Fprintf(&sb, "route %s %s net_gateway\n", ip, mask)
		} else {
			fmt.Fprintf(&sb, "route-ipv6 %s net_gateway_ipv6\n", c.String())
		}
	}
	sb.WriteString(content)
	log.Printf("OpenVPN split tunnel applied: %d bypass CIDRs", len(bypass))
	return sb.String(), true, ""
}

func prefixToIPv4Mask(prefix int) string {
	if prefix < 0 || prefix > 32 {
		return "255.255.255.255"
	}
	var mask uint32
	if prefix > 0 {
		mask = 0xFFFFFFFF << (32 - prefix)
	}
	return fmt.Sprintf("%d.%d.%d.%d",
		(mask>>24)&0xFF, (mask>>16)&0xFF, (mask>>8)&0xFF, mask&0xFF)
}
