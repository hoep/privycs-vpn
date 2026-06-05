package engine

import "sort"

// Active protocol selection — the "brain" the platforms call when the
// Automatic-protocol toggle is ON. PURE + deterministic (no floats/time/I/O):
// given the protocols a connection offers, the user's country, the network
// context (interface + metered) and per-protocol success stats, it ranks the
// usable protocols best-first and returns the top pick + the explainable reason.
// The platform's existing connect machinery executes the choice. OFF = the
// platform ignores this and runs its old manual path.
//
// Ranking layers (each refines the previous, all integer / deterministic):
//   1. Context base — restrictive country (DPI/blocking) → AmneziaWG → OpenVPN
//      → WireGuard → IPSec (evasion first); open/normal → WireGuard → AmneziaWG
//      → OpenVPN → IPSec (speed first).
//   2. Roaming — on cellular (open networks), IPSec/IKEv2's MOBIKE survives
//      Wi-Fi↔cellular switches, so it is bumped to second. (Censorship still
//      trumps roaming, so the restrictive order is left evasion-first.)
//   3. Adaptive stats (P4) — a protocol that recently FAILED on this network is
//      demoted; a high rolling success rate is rewarded. So the engine learns
//      and stops re-picking a protocol that keeps failing here.

// ProtoStat is a protocol's learned performance on the current network. Pure
// value type; the platform persists it per network and feeds it to Select.
type ProtoStat struct {
	SuccessEWMA int32 // rolling success rate, 0..1000 (1000 = always succeeds)
	LastFailSec int64 // unix seconds of the last failure here; 0 = never failed
}

// SelectInput is everything the pure selector needs.
type SelectInput struct {
	Available []Protocol           // protocols the connection has a config for
	Country   string               // user's pre-VPN ISO-3166-1 alpha-2 ("" if unknown)
	Net       NetworkContext       // iface / metered / class
	Exclude   []Protocol           // protocols that just failed this round (failover)
	Stats     map[Protocol]ProtoStat // per-protocol learned stats on this network
	NowSec    int64                // current unix seconds (for the recent-fail window)
}

// SelectResult is the decision. Found is false when no usable protocol remains.
type SelectResult struct {
	Protocol   Protocol
	Found      bool
	ReasonKey  string
	ReasonArgs []string
}

// failCooldownSec: a protocol that failed within this window on this network is
// heavily demoted (give the network time to recover / prefer alternatives).
const failCooldownSec = 600 // 10 minutes

// baseContextOrder is the context-ranked order (censorship/speed + roaming),
// most-preferred first, BEFORE adaptive stats.
func baseContextOrder(country string, iface IfaceClass) []Protocol {
	if IsRestrictiveCountry(country) {
		// Evasion first; roaming is secondary to beating DPI.
		return []Protocol{ProtoAmnezia, ProtoOpenVPN, ProtoWireGuard, ProtoIPsec}
	}
	if iface == IfaceCellular {
		// MOBIKE: IPSec rides through Wi-Fi↔cellular handoffs — bump to second.
		return []Protocol{ProtoWireGuard, ProtoIPsec, ProtoAmnezia, ProtoOpenVPN}
	}
	return []Protocol{ProtoWireGuard, ProtoAmnezia, ProtoOpenVPN, ProtoIPsec}
}

// ProtocolOrder is the context-ranked order for a country + interface (no
// stats) — used where a platform only needs the static preference.
func ProtocolOrder(country string, iface IfaceClass) []Protocol {
	return baseContextOrder(country, iface)
}

// SelectOrder ranks the usable protocols best-first by context, roaming and
// adaptive stats. Excludes are dropped. Deterministic.
func SelectOrder(in SelectInput) []Protocol {
	excluded := make(map[Protocol]bool, len(in.Exclude))
	for _, p := range in.Exclude {
		excluded[p] = true
	}
	base := baseContextOrder(in.Country, in.Net.Iface)
	pos := make(map[Protocol]int, len(base))
	for i, p := range base {
		pos[p] = i
	}

	type scored struct {
		p     Protocol
		score int
		base  int
	}
	var list []scored
	seen := make(map[Protocol]bool)
	for _, p := range in.Available {
		if excluded[p] || seen[p] {
			continue
		}
		seen[p] = true
		bpos, ok := pos[p]
		if !ok {
			bpos = len(base) // unknown protocol sorts last
		}
		score := bpos * 100
		if st, ok := in.Stats[p]; ok {
			// Recently failed here → big penalty (deprioritise, don't exclude).
			if st.LastFailSec != 0 && in.NowSec-st.LastFailSec < failCooldownSec {
				score += 500
			}
			// Rolling success (0..1000) → up to 100-point bonus.
			score -= int(st.SuccessEWMA) / 10
		}
		list = append(list, scored{p: p, score: score, base: bpos})
	}
	// Lowest score wins; ties break by the context base position (stable).
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].score != list[j].score {
			return list[i].score < list[j].score
		}
		return list[i].base < list[j].base
	})
	out := make([]Protocol, len(list))
	for i, s := range list {
		out[i] = s.p
	}
	return out
}

// Select returns the best usable protocol for the context, plus the reason.
func Select(in SelectInput) SelectResult {
	order := SelectOrder(in)
	if len(order) == 0 {
		return SelectResult{Found: false, ReasonKey: "decision.no_profile"}
	}
	top := order[0]
	// awgAvailable reflects what is still usable, so once AmneziaWG itself has
	// failed/been excluded the reason stops recommending it.
	awgUsable := false
	for _, p := range order {
		if p == ProtoAmnezia {
			awgUsable = true
			break
		}
	}
	rk, ra := CountryReason(in.Country, top, awgUsable)
	return SelectResult{Protocol: top, Found: true, ReasonKey: rk, ReasonArgs: ra}
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

// IfaceFromString maps a platform interface token to an IfaceClass.
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
