package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/hoep/privycs/desktop/internal/license"
)

// freshTestKeypair generates a fresh ed25519 keypair and rebinds the
// global licensePublicKeyHex so VerifyLicenseKey trusts the matching
// private key for the duration of the test. Restores prior state on
// cleanup so tests don't leak.
func freshTestKeypair(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	prev := licensePublicKeyHex
	licensePublicKeyHex = hex.EncodeToString(pub)
	t.Cleanup(func() { licensePublicKeyHex = prev })
	return priv
}

func issueKey(t *testing.T, priv ed25519.PrivateKey, pl *license.Payload) string {
	t.Helper()
	key, err := license.Issue(pl, priv)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	return key
}

func validPayload() *license.Payload {
	return &license.Payload{
		V:              1,
		Tier:           "pro",
		SKU:            SKUDesktop,
		Platforms:      []string{PlatformDesktop},
		Issued:         "2026-05-23",
		BuyerEmailHash: license.HashBuyerEmail("test@example.com"),
	}
}

func TestVerifyLicenseKey_HappyPath(t *testing.T) {
	priv := freshTestKeypair(t)
	key := issueKey(t, priv, validPayload())

	got, err := VerifyLicenseKey(key)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got.Tier != "pro" || got.SKU != SKUDesktop {
		t.Fatalf("payload roundtrip mismatch: %+v", got)
	}
}

func TestVerifyLicenseKey_TamperedPayload(t *testing.T) {
	priv := freshTestKeypair(t)
	key := issueKey(t, priv, validPayload())

	// Toggle a byte inside the payload b32 segment — signature must fail.
	tampered := []byte(key)
	tampered[6] ^= 1
	if _, err := VerifyLicenseKey(string(tampered)); !errors.Is(err, ErrLicenseBadSignature) && !errors.Is(err, ErrLicenseMalformed) {
		t.Fatalf("expected bad-signature or malformed, got %v", err)
	}
}

func TestVerifyLicenseKey_WrongPlatform(t *testing.T) {
	priv := freshTestKeypair(t)
	pl := validPayload()
	pl.Platforms = []string{"android"} // android-only key on desktop
	key := issueKey(t, priv, pl)

	if _, err := VerifyLicenseKey(key); !errors.Is(err, ErrLicenseWrongPlatform) {
		t.Fatalf("expected wrong-platform, got %v", err)
	}
}

func TestVerifyLicenseKey_UnsupportedVersion(t *testing.T) {
	priv := freshTestKeypair(t)
	pl := validPayload()
	pl.V = 2
	key := issueKey(t, priv, pl)

	if _, err := VerifyLicenseKey(key); !errors.Is(err, ErrLicenseUnsupportedV) {
		t.Fatalf("expected unsupported-v, got %v", err)
	}
}

func TestVerifyLicenseKey_BundleAllValidOnDesktop(t *testing.T) {
	priv := freshTestKeypair(t)
	pl := validPayload()
	pl.SKU = SKUBundle
	pl.Platforms = []string{"android", PlatformDesktop, "ios"}
	key := issueKey(t, priv, pl)

	got, err := VerifyLicenseKey(key)
	if err != nil {
		t.Fatalf("bundle key should verify on desktop: %v", err)
	}
	if got.SKU != SKUBundle {
		t.Fatalf("bundle SKU lost in roundtrip: %s", got.SKU)
	}
}

func TestVerifyLicenseKey_NoEmbeddedKey(t *testing.T) {
	prev := licensePublicKeyHex
	licensePublicKeyHex = ""
	t.Cleanup(func() { licensePublicKeyHex = prev })

	// Even a structurally-valid key fails when no pubkey is embedded.
	if _, err := VerifyLicenseKey("PRVC-AAAA-BBBB"); !errors.Is(err, ErrLicenseEmptyPublic) && !errors.Is(err, ErrLicenseMalformed) {
		t.Fatalf("expected empty-pubkey or malformed, got %v", err)
	}
}

func TestVerifyLicenseKey_Malformed(t *testing.T) {
	// Set a valid public key first so empty-pubkey doesn't shadow
	// the malformed-key path.
	freshTestKeypair(t)
	cases := []string{
		"",
		"not-a-license",
		"FOO-bar-baz",
		"PRVC-onlyone",
		"PRVC-A-B-C-D",
	}
	for _, c := range cases {
		if _, err := VerifyLicenseKey(c); !errors.Is(err, ErrLicenseMalformed) {
			t.Fatalf("%q: expected malformed, got %v", c, err)
		}
	}
}

func TestHashBuyerEmail_DeterministicLowercase(t *testing.T) {
	a := HashBuyerEmail("User@Example.com")
	b := HashBuyerEmail("user@example.com")
	c := HashBuyerEmail("  USER@EXAMPLE.COM  ")
	if a != b || a != c {
		t.Fatalf("hash should be case+whitespace-stable, got %s/%s/%s", a, b, c)
	}
	if len(a) != 64 {
		t.Fatalf("expected 64-char sha256 hex, got %d", len(a))
	}
}
