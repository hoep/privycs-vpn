package main

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// initEncryptionAtRest is the v1.0.0 boot-sequence step that wires the
// OS-specific SecretStore into the global encryptionKeyProvider so the
// EncryptedReadFile / EncryptedWriteFile wrappers transparently
// encrypt new writes and decrypt prior writes.
//
// Pipeline (called once at app startup BEFORE any sensitive file load):
//  1. Construct SecretStore (per-OS via build tags).
//  2. If !IsAvailable → stay plaintext (log warning, app degrades but
//     continues). This handles e.g. Linux headless without keyring.
//  3. Get-or-create the 32-byte master key.
//  4. Wire encryptionKeyProvider so EncryptedRead/Write work.
//  5. Caller (app.go) later runs MigrateAppDataToEncrypted to encrypt
//     any pre-existing plaintext files in appDataDir().
//
// Returns (store, ok). ok=false means we stay plaintext and the caller
// MUST NOT call MigrateAppDataToEncrypted.
var (
	encInitMu   sync.Mutex
	encInitDone bool
	encStore    SecretStore
)

func initEncryptionAtRest() (SecretStore, bool) {
	encInitMu.Lock()
	defer encInitMu.Unlock()
	if encInitDone {
		return encStore, encStore != nil && encStore.IsAvailable()
	}
	encInitDone = true

	store := NewSecretStore()
	if !store.IsAvailable() {
		log.Printf("encryption-at-rest: SecretStore (%s) not available, staying plaintext", store.Backend())
		return nil, false
	}
	encStore = store

	// Force a master-key fetch up-front so a failed prompt / locked
	// keychain surfaces here (and is logged) rather than mid-write.
	key, err := store.GetOrCreateMasterKey()
	if err != nil {
		log.Printf("encryption-at-rest: master key fetch FAILED (%s): %v — staying plaintext", store.Backend(), err)
		encStore = nil
		return nil, false
	}
	if len(key) != masterKeyLen {
		log.Printf("encryption-at-rest: bad master key length %d from %s — staying plaintext", len(key), store.Backend())
		encStore = nil
		return nil, false
	}

	// Wire the provider. The store impls cache the key after the
	// first call, so the provider is cheap (no IPC per write).
	setEncryptionKeyProvider(func() ([]byte, error) {
		return store.GetOrCreateMasterKey()
	})
	log.Printf("encryption-at-rest: %s ready (key %d bytes)", store.Backend(), len(key))
	return store, true
}

// sensitiveFilePatterns lists the file-name suffixes / exact basenames
// that contain VPN private material. Migration encrypts any matching
// file in appDataDir() that isn't already encrypted.
//
// Globs are matched against the basename only — no recursion below
// appDataDir() so we don't accidentally touch ~/.config/ etc.
var sensitiveFilePatterns = []string{
	"settings.json",
	"connections.json",
	"pools.json",
	"entitlement.dat", // Pro license payload (encrypted-at-rest reused)
	"*.conf",          // WireGuard / AmneziaWG
	"*.ovpn",          // OpenVPN
	"*.sswan",         // strongSwan / IPSec
	"*.p12",           // PKCS#12 client cert bundles
	"*.mobileconfig",  // legacy macOS IPSec
}

// MigrateAppDataToEncrypted scans appDataDir() for sensitive files and
// encrypts each one in place. Idempotent — already-encrypted files
// (PVCE magic) are skipped. Per-file rollback via .bak: if any step
// fails, the original is restored and the function returns the error
// without continuing — half-encrypted disk state is the worst outcome.
//
// Caller MUST ensure initEncryptionAtRest succeeded; otherwise the
// EncryptedWriteFile path falls back to plaintext and the migration is
// a no-op.
//
// Returns the number of files encrypted (zero on a clean
// already-migrated run).
func MigrateAppDataToEncrypted() (int, error) {
	dir := appDataDir()
	encrypted := 0

	files, err := listSensitiveFiles(dir)
	if err != nil {
		return 0, fmt.Errorf("scan: %w", err)
	}
	for _, path := range files {
		if IsEncryptedFile(path) {
			continue
		}
		if err := encryptInPlace(path); err != nil {
			return encrypted, fmt.Errorf("encrypt %s: %w", path, err)
		}
		encrypted++
	}
	if encrypted > 0 {
		log.Printf("encryption-at-rest: migrated %d file(s) to encrypted form", encrypted)
	}
	return encrypted, nil
}

// listSensitiveFiles returns every file in dir whose basename matches
// any sensitiveFilePatterns entry. .bak files (our own rollback
// artefacts) are excluded.
func listSensitiveFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".bak") || strings.HasSuffix(name, ".tmp") {
			continue
		}
		for _, pat := range sensitiveFilePatterns {
			matched, _ := filepath.Match(pat, name)
			if matched {
				out = append(out, filepath.Join(dir, name))
				break
			}
		}
	}
	return out, nil
}

// encryptInPlace reads a plaintext file, makes a .bak copy, writes the
// encrypted version, round-trip verifies, then removes the .bak. Any
// failure restores the .bak so the on-disk state is consistent.
func encryptInPlace(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	if len(data) == 0 {
		// empty file — encryption is a no-op but we still rewrite
		// so future reads recognize the format. Skip instead.
		return nil
	}

	bakPath := path + ".bak"
	if err := os.WriteFile(bakPath, data, 0600); err != nil {
		return fmt.Errorf("backup: %w", err)
	}

	if err := EncryptedWriteFile(path, data, 0600); err != nil {
		// rollback
		_ = os.Rename(bakPath, path)
		return fmt.Errorf("encrypt write: %w", err)
	}

	// Verify round-trip
	got, err := EncryptedReadFile(path)
	if err != nil {
		_ = os.Rename(bakPath, path)
		return fmt.Errorf("verify read: %w", err)
	}
	if !bytes.Equal(got, data) {
		_ = os.Rename(bakPath, path)
		return fmt.Errorf("verify mismatch (got %d bytes, want %d)", len(got), len(data))
	}

	// Success — drop the .bak
	_ = os.Remove(bakPath)
	return nil
}
