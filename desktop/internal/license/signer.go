//go:build licensegen

// This file holds the ed25519 PRIVATE-key signer (Issue). It is
// deliberately gated behind the `licensegen` build tag so it is NOT
// compiled into the shipped client binary — the client only ever needs
// Verify(). Build the dev signing tool / webhook with `-tags licensegen`
// to pull this in.
package license

import "crypto/ed25519"

// Issue signs the payload with priv and returns the wire-format
// license key string. Used by the cmd/license-gen dev tool and (via
// the gateway repo) the LemonSqueezy webhook handler. NOT part of the
// shipped client build (gated by the `licensegen` build tag).
func Issue(pl *Payload, priv ed25519.PrivateKey) (string, error) {
	canon, err := CanonicalJSON(pl)
	if err != nil {
		return "", err
	}
	sig := ed25519.Sign(priv, canon)
	return Prefix + Sep + EncodeBase32(canon) + Sep + EncodeBase32(sig), nil
}
