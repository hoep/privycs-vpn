package engine

import "strings"

// Network-aware decision reasoning. PURE + deterministic (no floats/time/I/O) so
// it replays identically on Go, the JVM and Swift.
//
// The core signal is the USER's own country, resolved from their pre-VPN public
// IP (each platform observes it: desktop via the selfip package, iOS/Android via
// the Cloudflare trace `loc=` field). In a country with systemic DPI / VPN
// blocking, plain WireGuard/OpenVPN handshakes are fingerprintable and get
// throttled or dropped — there, AmneziaWG (obfuscated WireGuard) is the right
// protocol. So the reason explains, against the real location:
//   restrictive country + on AmneziaWG          → confirmed good choice
//   restrictive country + AWG profile available  → recommend switching to it
//   restrictive country + no AWG profile         → warn (this protocol may be blocked)
//   non-restrictive country                      → a fast protocol is fine
//
// restrictiveCountries lists ISO-3166-1 alpha-2 codes with well-documented
// systemic VPN/DPI blocking where obfuscation materially helps. Deliberately
// conservative (core, persistent regimes) to avoid alarming false positives on
// countries with only sporadic/partial blocks. Adjust as the landscape changes.
var restrictiveCountries = map[string]bool{
	"CN": true, // China — Great Firewall, active probing, blocks WG/OVPN
	"IR": true, // Iran — DPI, protocol whitelisting, throttling
	"RU": true, // Russia — DPI (TSPU), VPN blocking + throttling
	"BY": true, // Belarus
	"TM": true, // Turkmenistan — among the most restrictive
	"KP": true, // North Korea
	"SY": true, // Syria
	"CU": true, // Cuba
	"MM": true, // Myanmar — post-2021 DPI rollout
	"AE": true, // UAE — VPN/VoIP restrictions
	"OM": true, // Oman
	"EG": true, // Egypt — DPI, periodic VPN blocks
}

// IsRestrictiveCountry reports whether cc (ISO-3166-1 alpha-2, any case) is a
// country with systemic VPN/DPI blocking where AmneziaWG's obfuscation helps.
func IsRestrictiveCountry(cc string) bool {
	return restrictiveCountries[strings.ToUpper(strings.TrimSpace(cc))]
}

// CountryReason returns a stable i18n reason key (+ args) explaining the active
// protocol's fitness given the user's country and whether an AmneziaWG profile
// is available on the active connection. Empty key = no reason (country
// unknown). args[0] is the upper-cased country code; the UI resolves it to a
// localized country name.
func CountryReason(country string, active Protocol, awgAvailable bool) (string, []string) {
	cc := strings.ToUpper(strings.TrimSpace(country))
	if cc == "" {
		return "", nil // no location signal yet → make no claim
	}
	if !IsRestrictiveCountry(cc) {
		return "reason.country_open", []string{cc}
	}
	switch {
	case active == ProtoAmnezia:
		return "reason.country_restrictive_awg", []string{cc}
	case awgAvailable:
		return "reason.country_restrictive_use_awg", []string{cc}
	default:
		return "reason.country_restrictive_no_awg", []string{cc}
	}
}

// ReasonFor maps a decision (by its HumanKey) to an explanatory reason. Only
// protocol-selection decisions carry the country-aware reason; other decisions'
// messages are already self-explanatory. hasProto is false when the decision
// has no associated protocol.
func ReasonFor(humanKey string, active Protocol, hasProto bool, country string, awgAvailable bool) (string, []string) {
	switch humanKey {
	case "decision.connecting", "decision.validating", "decision.connected", "decision.switching":
		if hasProto {
			return CountryReason(country, active, awgAvailable)
		}
	}
	return "", nil
}
