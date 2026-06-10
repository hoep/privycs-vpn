// Package license implements the Privycs ed25519-signed offline
// license-key format used by all 3 client platforms (Desktop / Android
// / iOS). It is intentionally Go-stdlib only — both the desktop client
// (verify) and the cmd/license-gen dev tool (sign) consume identical
// canonical-JSON bytes, so any subtle drift between signer and
// verifier would break activation.
//
// On-wire format:
//
//	PRVC-{base32(canonicalJSON(payload))}-{base32(ed25519-sig)}
//
// See ../../entitlement_crypto.go for the desktop-side wrapper that
// pulls in the embedded public key, and ../../../cmd/license-gen for
// the dev signing tool.
package license

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

const (
	// Prefix is the literal first segment all valid keys share.
	Prefix = "PRVC"
	// Sep is the segment separator inside a key.
	Sep = "-"

	// SKUDesktop is the single-Desktop product code on LemonSqueezy.
	SKUDesktop = "privycs_pro_desktop"
	// SKUBundle is the cross-platform bundle product code (Android +
	// Desktop + iOS in one key).
	SKUBundle = "privycs_pro_bundle_all"

	// PlatformDesktop / PlatformAndroid / PlatformIOS are the values
	// the Platforms array in a payload uses. A client only accepts a
	// key whose Platforms array contains its own platform.
	PlatformDesktop = "desktop"
	PlatformAndroid = "android"
	PlatformIOS     = "ios"

	// CurrentSchemaVersion is what we set on Issue and what Verify
	// rejects as "unsupported" if not matched. Bumping requires a
	// migration path because old apps cannot verify keys with newer
	// schemas.
	CurrentSchemaVersion = 1
)

// Payload is the signed portion of a license key. Field tags are
// alphabetic and match the order used by canonicalJSON's lexicographic
// re-encoding — signer and verifier produce identical bytes regardless
// of struct field order or marshaller quirks.
type Payload struct {
	BuyerEmailHash string   `json:"buyer_email_hash,omitempty"`
	Issued         string   `json:"issued"`
	Platforms      []string `json:"platforms"`
	SKU            string   `json:"sku"`
	Tier           string   `json:"tier"`
	V              int      `json:"v"`
}

// Public verify-side errors. Wrappable with errors.Is so the UI can
// render a specific message ("Wrong platform" vs "Bad signature").
var (
	ErrMalformed     = errors.New("license key malformed")
	ErrBadSignature  = errors.New("license signature invalid")
	ErrUnsupportedV  = errors.New("license schema version not supported by this app")
	ErrWrongPlatform = errors.New("license is not valid for this platform")
	ErrUnknownTier   = errors.New("license tier not recognised")
	ErrNoPublicKey   = errors.New("no public key configured")
)

// Verify parses a raw "PRVC-..." key and checks the ed25519 signature
// against pub. Returns the parsed payload on success. The caller is
// responsible for additionally checking that the payload's Platforms
// includes the caller's own platform (e.g. PlatformDesktop).
//
// Pure / re-entrant — no I/O, no globals.
func Verify(rawKey string, pub ed25519.PublicKey, expectedPlatform string) (*Payload, error) {
	rawKey = strings.TrimSpace(rawKey)
	parts := strings.Split(rawKey, Sep)
	if len(parts) != 3 || parts[0] != Prefix {
		return nil, fmt.Errorf("%w: expected %s-<payload>-<sig>", ErrMalformed, Prefix)
	}
	payloadBytes, err := DecodeBase32(parts[1])
	if err != nil {
		return nil, fmt.Errorf("%w: payload not base32: %v", ErrMalformed, err)
	}
	sigBytes, err := DecodeBase32(parts[2])
	if err != nil {
		return nil, fmt.Errorf("%w: sig not base32: %v", ErrMalformed, err)
	}
	if len(sigBytes) != ed25519.SignatureSize {
		return nil, fmt.Errorf("%w: sig length %d, want %d", ErrMalformed, len(sigBytes), ed25519.SignatureSize)
	}
	if len(pub) != ed25519.PublicKeySize {
		return nil, ErrNoPublicKey
	}
	if !ed25519.Verify(pub, payloadBytes, sigBytes) {
		return nil, ErrBadSignature
	}

	var pl Payload
	if err := json.Unmarshal(payloadBytes, &pl); err != nil {
		return nil, fmt.Errorf("%w: payload not JSON: %v", ErrMalformed, err)
	}
	if pl.V != CurrentSchemaVersion {
		return nil, fmt.Errorf("%w: v=%d", ErrUnsupportedV, pl.V)
	}
	if pl.Tier != "pro" {
		return nil, fmt.Errorf("%w: %q", ErrUnknownTier, pl.Tier)
	}
	if expectedPlatform != "" && !slices.Contains(pl.Platforms, expectedPlatform) {
		return nil, fmt.Errorf("%w: payload.platforms=%v does not include %q", ErrWrongPlatform, pl.Platforms, expectedPlatform)
	}
	return &pl, nil
}

// NOTE: Issue() — the ed25519 PRIVATE-key signer — has been moved out
// of the default build. It now lives in signer.go behind the
// `licensegen` build tag so the shipped client binary contains ONLY
// the verify side. The dev signing tool / webhook builds with
// `-tags licensegen` to pull it back in. See signer.go.

// CanonicalJSON serialises a Payload with keys in lexicographic order
// and no whitespace — the exact byte sequence that ed25519 signs and
// verifies against. Signer + verifier MUST use this same function;
// any drift (e.g. different marshaller) silently breaks activation.
func CanonicalJSON(pl *Payload) ([]byte, error) {
	b, err := json.Marshal(pl)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return marshalSorted(m), nil
}

// marshalSorted emits a JSON object with keys in lexicographic order.
// Recurses into nested objects; preserves array order (array order is
// part of the signed message — flipping ["android","desktop"] to
// ["desktop","android"] changes the signature).
func marshalSorted(m map[string]any) []byte {
	var b strings.Builder
	b.WriteByte('{')
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		writeJSONString(&b, k)
		b.WriteByte(':')
		writeJSONValue(&b, m[k])
	}
	b.WriteByte('}')
	return []byte(b.String())
}

func writeJSONValue(b *strings.Builder, v any) {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case string:
		writeJSONString(b, x)
	case float64:
		// JSON numbers come back as float64 — re-encode integer
		// values without ".0" so they round-trip with the signer's
		// integer-typed payload (e.g. v=1, not v=1.0).
		if x == float64(int64(x)) {
			fmt.Fprintf(b, "%d", int64(x))
		} else {
			fmt.Fprintf(b, "%g", x)
		}
	case map[string]any:
		b.Write(marshalSorted(x))
	case []any:
		b.WriteByte('[')
		for i, item := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			writeJSONValue(b, item)
		}
		b.WriteByte(']')
	default:
		raw, _ := json.Marshal(x)
		b.Write(raw)
	}
}

func writeJSONString(b *strings.Builder, s string) {
	raw, _ := json.Marshal(s)
	b.Write(raw)
}

// EncodeBase32 and DecodeBase32 use the RFC 4648 standard alphabet
// with NO padding so keys round-trip cleanly through email + UI fields
// without the visual clutter of trailing equal signs.
func EncodeBase32(b []byte) string {
	enc := base32.StdEncoding.WithPadding(base32.NoPadding)
	return enc.EncodeToString(b)
}

func DecodeBase32(s string) ([]byte, error) {
	enc := base32.StdEncoding.WithPadding(base32.NoPadding)
	return enc.DecodeString(strings.ToUpper(s))
}

// HashBuyerEmail returns the SHA-256 hex of the lowercased trimmed
// email. Stored as buyer_email_hash inside a payload so the LemonSqueezy
// support tooling can correlate a key to a purchase without leaking the
// email itself into the app or its UI.
func HashBuyerEmail(email string) string {
	h := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email))))
	return hex.EncodeToString(h[:])
}

// PayloadTimestamp returns the day-precision UTC string used for the
// Issued field. Centralised here so signer + UI + tests render the
// same format.
func PayloadTimestamp(t time.Time) string {
	return t.UTC().Format("2006-01-02")
}
