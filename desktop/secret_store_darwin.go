//go:build darwin

package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os/exec"
	"strings"
	"sync"
)

// macOS implementation: shells out to the built-in `security` CLI so we
// avoid a cgo dependency on Security.framework. The CLI exists on every
// macOS install we ship to (10.15+); CI's GitHub macOS runners include
// it. The trade-off is one process spawn per Get/Create, but those only
// run at app startup so the cost is irrelevant.
//
// Storage location: the user's default Keychain (login.keychain-db),
// auto-unlocked when the user is logged in. If the keychain is locked,
// the security CLI emits the standard password-prompt — UX-equivalent
// to using the Keychain Access app, which is what we want.

type macSecretStore struct {
	mu     sync.Mutex
	cached []byte
}

func newOSSecretStore() SecretStore {
	return &macSecretStore{}
}

func (m *macSecretStore) Backend() string { return "keychain" }

// IsAvailable probes the security CLI itself — if it's missing the
// install is broken and we should fall back to plaintext rather than
// crash. The probe is a no-op "list" call (no Keychain mutation).
func (m *macSecretStore) IsAvailable() bool {
	cmd := exec.Command("security", "list-keychains")
	return cmd.Run() == nil
}

func (m *macSecretStore) GetOrCreateMasterKey() ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.cached) == masterKeyLen {
		return m.cached, nil
	}

	// Try to read first
	if existing, err := m.readKey(); err == nil {
		m.cached = existing
		return existing, nil
	}

	// Not present — generate and persist
	key, err := generateMasterKey()
	if err != nil {
		return nil, err
	}
	if err := m.writeKey(key); err != nil {
		return nil, fmt.Errorf("persist key in keychain: %w", err)
	}
	m.cached = key
	return key, nil
}

// readKey shells out `security find-generic-password -w` which prints
// the password value to stdout. We store the master key base64-encoded
// because the security CLI's -w flag treats the value as a string and
// would mangle raw binary bytes.
func (m *macSecretStore) readKey() ([]byte, error) {
	cmd := exec.Command("security", "find-generic-password",
		"-a", secretStoreAccount,
		"-s", secretStoreServiceName,
		"-w",
	)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("keychain read: %w (%s)", err, strings.TrimSpace(errBuf.String()))
	}
	raw := strings.TrimSpace(out.String())
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("decode key: %w", err)
	}
	if len(key) != masterKeyLen {
		return nil, fmt.Errorf("stored key length %d, want %d", len(key), masterKeyLen)
	}
	return key, nil
}

// writeKey stores via `security add-generic-password -U` (update if
// exists). -T "" allows our own app (signed/notarised) to access the
// item without prompting; the system prompts for first-time entries
// only.
func (m *macSecretStore) writeKey(key []byte) error {
	encoded := base64.StdEncoding.EncodeToString(key)
	cmd := exec.Command("security", "add-generic-password",
		"-U", // update if exists
		"-a", secretStoreAccount,
		"-s", secretStoreServiceName,
		"-w", encoded,
		"-T", "", // restrict ACL — only the calling app may read
	)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("keychain write: %w (%s)", err, strings.TrimSpace(errBuf.String()))
	}
	return nil
}
