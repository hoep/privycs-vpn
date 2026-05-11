package main

import (
	"regexp"
	"strings"
)

// AmneziaWG (AWG) obfuscation foundation — Stage 0 of the
// AMNEZIAWG_CLIENT_PLAN.md rollout.
//
// This file defines:
//   - the typed `ObfuscationConfig` struct that mirrors the server-side
//     enrollment-response field of the same name (see plan §2.1),
//   - a content-based "is this conf an AWG conf?" detector used by
//     protocol_wireguard.go to pick the right tool / library variant,
//   - splice helpers for the .conf text path.
//
// No protocol-logic touches yet — protocol_wireguard.go consumes
// these in Stage 2 (Linux) / Stage 3 (macOS) / Stage 4 (Windows).

// ObfuscationConfig is the typed Go mirror of the server-side
// enrollment "obfuscation" object. Field semantics match plan §2.2:
//
//	Jc       int 0-128    — junk packets before handshake
//	Jmin     int 0-1280   — junk packet size range (lower)
//	Jmax     int 0-1280   — junk packet size range (upper, >= Jmin)
//	S1-S4    int 0-64     — per-message padding lengths (S4 is 0-32)
//	H1-H4    uint32       — dynamic magic-header bytes replacing
//	                       WG's fixed 0x01-0x04 markers
//	I1-I5    hex string   — optional mimicry-packet blobs
//
// Plain WG tunnels have Enabled = false; this struct is zero-valued
// and the protocol handler stays on the vanilla path. AWG tunnels
// have Enabled = true and all fields populated.
type ObfuscationConfig struct {
	Enabled bool   `json:"enabled"`
	Jc      int    `json:"jc,omitempty"`
	Jmin    int    `json:"jmin,omitempty"`
	Jmax    int    `json:"jmax,omitempty"`
	S1      int    `json:"s1,omitempty"`
	S2      int    `json:"s2,omitempty"`
	S3      int    `json:"s3,omitempty"`
	S4      int    `json:"s4,omitempty"`
	H1      uint32 `json:"h1,omitempty"`
	H2      uint32 `json:"h2,omitempty"`
	H3      uint32 `json:"h3,omitempty"`
	H4      uint32 `json:"h4,omitempty"`
	I1      string `json:"i1,omitempty"`
	I2      string `json:"i2,omitempty"`
	I3      string `json:"i3,omitempty"`
	I4      string `json:"i4,omitempty"`
	I5      string `json:"i5,omitempty"`
}

// Variant constants used for ProtocolStatus.Variant and for
// internal switch statements between the wg-quick / awg-quick paths
// (Linux) and between the wireguard-go / amneziawg-go libraries
// (macOS in-process, Windows sidecar).
const (
	VariantWireGuard = "wireguard"
	VariantAmnezia   = "amneziawg"
)

// awgConfMarkerRe matches any [Interface]-section line that the
// AmneziaWG fork emits (`Jc = ...`, `Jmin = ...`, ..., `H4 = ...`).
// Any one of these in the conf is sufficient evidence that the
// conf was rendered by an AWG-aware generator and must be loaded by
// awg-quick / amneziawg-go, NOT vanilla wg-quick / wireguard-go.
// Vanilla wg-quick rejects these unknown keys with a parse error.
var awgConfMarkerRe = regexp.MustCompile(
	`(?im)^[ \t]*(Jc|Jmin|Jmax|S[1-4]|H[1-4]|I[1-5])[ \t]*=`,
)

// DetectVariant inspects the textual .conf payload and returns
// VariantAmnezia when any AWG-specific [Interface] key is present,
// VariantWireGuard otherwise. Decisively content-based — does NOT
// trust filenames or server-supplied hints — so a misnamed file
// from an admin's copy/paste still routes through the right tool.
func DetectVariant(confContent string) string {
	if awgConfMarkerRe.MatchString(confContent) {
		return VariantAmnezia
	}
	return VariantWireGuard
}

// SpliceObfuscationLines appends a server-supplied obfuscation
// config block (server-side field `obfuscation_config_lines`, plan
// §2.4) to the [Interface] section of an existing WG .conf. Used
// when the caller has a typed ObfuscationConfig OR a pre-rendered
// string and needs to materialise a single .conf string for
// awg-quick / amneziawg-go to consume.
//
// Returns the input unchanged when block is empty or the conf is
// already AWG-aware (avoids duplicate lines on re-splice).
func SpliceObfuscationLines(confContent, block string) string {
	block = strings.TrimSpace(block)
	if block == "" {
		return confContent
	}
	if awgConfMarkerRe.MatchString(confContent) {
		return confContent
	}
	// Insert immediately AFTER the [Interface] header so awg-quick
	// sees the AWG fields before any [Peer] section starts.
	lines := strings.Split(confContent, "\n")
	for i, ln := range lines {
		if strings.EqualFold(strings.TrimSpace(ln), "[Interface]") {
			out := append([]string{}, lines[:i+1]...)
			out = append(out, block)
			out = append(out, lines[i+1:]...)
			return strings.Join(out, "\n")
		}
	}
	// No [Interface] header found — append at end as last resort
	// rather than silently drop. awg-quick will reject the result
	// but the error will be actionable for the user.
	return confContent + "\n[Interface]\n" + block + "\n"
}
