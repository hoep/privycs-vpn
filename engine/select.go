package engine

// Active protocol selection — the "brain" the platforms call when the
// Automatic-protocol toggle is ON. PURE + deterministic: given the protocols a
// connection actually offers, the user's country and the network context, it
// returns the best protocol to use (and, for failover, the next-best excluding
// the ones that just failed) plus the explainable reason. The platform's
// existing connect machinery executes the choice; the engine never touches a
// VPN API. When the toggle is OFF the platform ignores this entirely and runs
// its old manual path.
//
// Context-aware ranking (constrained to Available):
//   restrictive country (DPI/VPN-blocking) → AmneziaWG → OpenVPN(TCP) → WireGuard → IPSec   (evasion first)
//   open / normal network                  → WireGuard → AmneziaWG → OpenVPN → IPSec         (speed first)

// SelectInput is everything the pure selector needs.
type SelectInput struct {
	Available []Protocol     // protocols the connection has a config for
	Country   string         // user's pre-VPN ISO-3166-1 alpha-2 ("" if unknown)
	Net       NetworkContext // iface / metered / class
	Exclude   []Protocol     // protocols that just failed this round (failover)
}

// SelectResult is the decision. Found is false when no usable protocol remains
// (all available ones excluded / none configured).
type SelectResult struct {
	Protocol   Protocol
	Found      bool
	ReasonKey  string
	ReasonArgs []string
}

// IfaceFromString maps a platform interface token to an IfaceClass. (Select
// doesn't weigh the interface yet — reserved for network-aware ranking — but
// the FFI populates NetworkContext for forward-compatibility.)
func IfaceFromString(s string) IfaceClass {
	switch s {
	case "wifi", "wlan", "wi-fi":
		return IfaceWifi
	case "cellular", "mobile", "cell", "wwan":
		return IfaceCellular
	case "ethernet", "wired", "eth":
		return IfaceEthernet
	case "":
		return IfaceUnknown
	}
	return IfaceOther
}

// ProtocolOrder is the context-ranked protocol preference for a country
// (most-preferred first) — the same ordering Select uses. Platforms use it to
// drive failover ordering in active mode.
func ProtocolOrder(country string) []Protocol {
	if IsRestrictiveCountry(country) {
		return []Protocol{ProtoAmnezia, ProtoOpenVPN, ProtoWireGuard, ProtoIPsec}
	}
	return []Protocol{ProtoWireGuard, ProtoAmnezia, ProtoOpenVPN, ProtoIPsec}
}

// Token is the canonical wire token for a protocol — matches the platform
// config tokens (NB: AmneziaWG is "amneziawg" here, vs String()'s "amnezia").
func (p Protocol) Token() string {
	switch p {
	case ProtoWireGuard:
		return "wireguard"
	case ProtoAmnezia:
		return "amneziawg"
	case ProtoOpenVPN:
		return "openvpn"
	case ProtoIPsec:
		return "ipsec"
	}
	return ""
}

// ParseProtocol maps a wire token to a Protocol; ok=false for an unknown token.
func ParseProtocol(token string) (Protocol, bool) {
	switch token {
	case "wireguard":
		return ProtoWireGuard, true
	case "amneziawg", "amnezia":
		return ProtoAmnezia, true
	case "openvpn":
		return ProtoOpenVPN, true
	case "ipsec":
		return ProtoIPsec, true
	}
	return ProtoWireGuard, false
}

// Select returns the best available protocol for the context, plus the reason.
func Select(in SelectInput) SelectResult {
	excluded := make(map[Protocol]bool, len(in.Exclude))
	for _, p := range in.Exclude {
		excluded[p] = true
	}
	usable := make(map[Protocol]bool, len(in.Available))
	for _, p := range in.Available {
		if !excluded[p] {
			usable[p] = true
		}
	}
	if len(usable) == 0 {
		return SelectResult{Found: false, ReasonKey: "decision.no_profile"}
	}

	for _, p := range ProtocolOrder(in.Country) {
		if usable[p] {
			// awgAvailable reflects what is still usable this round, so once
			// AmneziaWG itself has failed the reason stops recommending it.
			rk, ra := CountryReason(in.Country, p, usable[ProtoAmnezia])
			return SelectResult{Protocol: p, Found: true, ReasonKey: rk, ReasonArgs: ra}
		}
	}

	// Unreachable while order covers every Protocol, but stay safe.
	return SelectResult{Found: false, ReasonKey: "decision.no_profile"}
}
