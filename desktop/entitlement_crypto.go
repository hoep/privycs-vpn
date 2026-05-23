package main

import (
	"github.com/hoep/privycs/desktop/internal/license"
)

// Desktop-side wrapper around internal/license. The real algorithm
// + format definition lives in the package so that cmd/license-gen
// (the dev signer) and the LemonSqueezy webhook (gateway repo) all
// emit byte-identical canonical JSON. This file just plugs in the
// embedded public key + the desktop platform constraint.

// Re-exports so existing call-sites in the desktop package stay short.
// Keeping the same names as pre-refactor avoids a sweeping rename in
// the rest of the package (entitlement.go, app.go bindings, UI).
type LicensePayload = license.Payload

const (
	SKUDesktop      = license.SKUDesktop
	SKUBundle       = license.SKUBundle
	PlatformDesktop = license.PlatformDesktop
)

// Verify-side error aliases — kept as locals so callers can use
// errors.Is(err, ErrLicenseBadSignature) without importing the
// internal/license package directly.
var (
	ErrLicenseMalformed     = license.ErrMalformed
	ErrLicenseBadSignature  = license.ErrBadSignature
	ErrLicenseUnsupportedV  = license.ErrUnsupportedV
	ErrLicenseWrongPlatform = license.ErrWrongPlatform
	ErrLicenseUnknownTier   = license.ErrUnknownTier
	ErrLicenseEmptyPublic   = license.ErrNoPublicKey
)

// VerifyLicenseKey is the desktop-app entry point. It pulls in the
// embedded public key (placeholder all-zero until -ldflags inject the
// real one) and rejects keys whose platforms[] doesn't include
// "desktop".
//
// Returns (payload, nil) on success or (nil, wrapped-error) on
// failure. Callers should inspect the wrapped error via errors.Is to
// render a specific UI message.
func VerifyLicenseKey(rawKey string) (*LicensePayload, error) {
	pub := embeddedLicensePublicKey()
	if len(pub) == 0 {
		return nil, ErrLicenseEmptyPublic
	}
	return license.Verify(rawKey, pub, license.PlatformDesktop)
}

// HashBuyerEmail re-exported for any caller that needs to hash a user-
// entered email (UI-side correlation with a license, etc.).
func HashBuyerEmail(email string) string { return license.HashBuyerEmail(email) }
