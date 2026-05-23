package main

import (
	"crypto/rand"
	"fmt"
	"runtime"
)

// SecretStore abstracts the per-OS secret-storage backend used to hold
// the 32-byte AES-256 master key. Implementations live in
// secret_store_{darwin,linux,windows}.go and are selected by build tag.
//
// The pattern across platforms is identical: one master key, persisted
// under an OS-specific protection (macOS Keychain / Windows DPAPI /
// Linux libsecret-or-file), used to AES-256-GCM all sensitive files
// in appDataDir() via the encrypted_file.go wrappers.
type SecretStore interface {
	// GetOrCreateMasterKey returns the 32-byte master key, creating a
	// cryptographically random one on first call and persisting it in
	// the OS secret store. Idempotent — repeated calls return the
	// same key.
	GetOrCreateMasterKey() ([]byte, error)

	// IsAvailable reports whether the backend is functional (Keychain
	// reachable / DPAPI present / libsecret-daemon or file-fallback
	// accessible). When this returns false, callers should stay in
	// plaintext mode rather than risk a half-encrypted disk state.
	IsAvailable() bool

	// Backend names the implementation for logging / diagnostics
	// ("keychain" / "dpapi" / "libsecret" / "file-fallback").
	Backend() string
}

// masterKeyLen is the AES-256 key length. Exported as a constant so the
// per-OS implementations all agree on the same generation length.
const masterKeyLen = 32

// generateMasterKey returns a cryptographically random 32-byte key.
// Used by every backend's first-time path.
func generateMasterKey() ([]byte, error) {
	key := make([]byte, masterKeyLen)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate master key: %w", err)
	}
	return key, nil
}

// secretStoreServiceName is the identifier under which the master key
// is stored in the OS secret store. Consistent across platforms.
const secretStoreServiceName = "com.privycs.vpn.masterkey"

// secretStoreAccount is the per-user account label used inside the
// secret store. We use a fixed string ("default") so re-installs on the
// same user account retrieve the same key without prompting.
const secretStoreAccount = "default"

// NewSecretStore returns the per-OS implementation. Real implementations
// live in build-tagged files (secret_store_darwin.go etc.). The
// fallthrough here only runs on a build target we forgot to add — log
// loudly and return a no-op store so the app can still start in plain
// mode rather than refusing to launch.
//
// NOTE: each build-tagged file MUST define `func newOSSecretStore()
// SecretStore` so the platform-specific constructor can be selected at
// compile time. The factory dispatches via runtime.GOOS sanity-check
// but the actual binding happens at compile time via build tags.
func NewSecretStore() SecretStore {
	store := newOSSecretStore()
	if store == nil {
		// build-tag misconfiguration; degrade rather than crash
		fmt.Printf("WARNING: no SecretStore impl for %s; running plain\n", runtime.GOOS)
		return &nopSecretStore{}
	}
	return store
}

// nopSecretStore is the fallback used when no per-OS impl is wired in.
// IsAvailable returns false, so migration.go stays in plaintext mode.
type nopSecretStore struct{}

func (*nopSecretStore) GetOrCreateMasterKey() ([]byte, error) {
	return nil, fmt.Errorf("no secret store available")
}
func (*nopSecretStore) IsAvailable() bool { return false }
func (*nopSecretStore) Backend() string   { return "none" }
