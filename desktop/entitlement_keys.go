package main

// licensePublicKey is the ed25519 public key the Desktop app trusts to
// validate Pro license signatures. The matching private key lives on
// the Gateway server (env-secret), is used by the LemonSqueezy
// webhook handler to sign license payloads, and MUST NEVER appear in
// this repo.
//
// Default = all-zero placeholder so a misconfigured release build can
// never accidentally claim "everything is Pro": the verifier rejects a
// zero-length / wrong-length key with ErrLicenseEmptyPublic and any
// signature check against a zero pubkey will fail with
// ErrLicenseBadSignature anyway. Real keys ship via -ldflags at build
// time:
//
//	go build -ldflags "-X main.licensePublicKeyHex=$PUBKEY_HEX" ...
//
// The pubkey is a 32-byte ed25519 PublicKey (64 hex chars).
//
// For local development, run cmd/license-gen which prints a freshly
// generated keypair and the matching -X ldflags value to use.
var licensePublicKeyHex = ""

// embeddedLicensePublicKey decodes licensePublicKeyHex once per call.
// Returns nil if not set or malformed — VerifyLicenseKey treats this
// as "no key embedded" and fails closed.
func embeddedLicensePublicKey() []byte {
	if licensePublicKeyHex == "" {
		return nil
	}
	// 32 bytes = 64 hex chars
	if len(licensePublicKeyHex) != 64 {
		return nil
	}
	out := make([]byte, 32)
	for i := 0; i < 32; i++ {
		var b byte
		_, err := fmtSscanfHexByte(licensePublicKeyHex[i*2:i*2+2], &b)
		if err != nil {
			return nil
		}
		out[i] = b
	}
	return out
}

// fmtSscanfHexByte parses two hex chars into a single byte. Pulled out
// so the embed-decoder doesn't drag in fmt at the top — keeps
// init-time work minimal in the hot path.
func fmtSscanfHexByte(s string, b *byte) (int, error) {
	if len(s) != 2 {
		return 0, errInvalidHexPair
	}
	var v byte
	for i := 0; i < 2; i++ {
		v <<= 4
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
			v |= c - '0'
		case c >= 'a' && c <= 'f':
			v |= c - 'a' + 10
		case c >= 'A' && c <= 'F':
			v |= c - 'A' + 10
		default:
			return 0, errInvalidHexPair
		}
	}
	*b = v
	return 1, nil
}

type sentinelErr string

func (e sentinelErr) Error() string { return string(e) }

const errInvalidHexPair = sentinelErr("invalid hex pair")
