package main

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
)

// fixedKeyProvider returns the same key for the whole test — emulates
// what migration.go does once SecretStore.GetOrCreateMasterKey caches.
func fixedKeyProvider(t *testing.T) (func() ([]byte, error), []byte) {
	t.Helper()
	key := make([]byte, masterKeyLen)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return func() ([]byte, error) { return key, nil }, key
}

// withKeyProvider temporarily wires a key provider and restores the
// previous one when the test ends. Lets tests run in any order without
// leaking global state.
func withKeyProvider(t *testing.T, p func() ([]byte, error)) {
	t.Helper()
	prev := encryptionKeyProvider
	setEncryptionKeyProvider(p)
	t.Cleanup(func() { setEncryptionKeyProvider(prev) })
}

func TestEncryptedFile_RoundTrip(t *testing.T) {
	provider, _ := fixedKeyProvider(t)
	withKeyProvider(t, provider)

	dir := t.TempDir()
	path := filepath.Join(dir, "secret.conf")
	plain := []byte("PrivateKey = AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\n[Peer]\n...")

	if err := EncryptedWriteFile(path, plain, 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !IsEncryptedFile(path) {
		t.Fatalf("file should be detected as encrypted (no PVCE magic)")
	}

	// Raw bytes on disk must be ciphertext, not plain
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("raw read: %v", err)
	}
	if bytes.Contains(raw, []byte("PrivateKey")) {
		t.Fatalf("plaintext leaked into ciphertext file on disk")
	}
	if string(raw[:4]) != encFileMagic {
		t.Fatalf("missing PVCE magic header")
	}

	got, err := EncryptedReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("round-trip mismatch: got %q want %q", got, plain)
	}
}

func TestEncryptedFile_PlaintextPassthrough(t *testing.T) {
	provider, _ := fixedKeyProvider(t)
	withKeyProvider(t, provider)

	// Write a plain file (no PVCE magic). EncryptedReadFile should
	// return it verbatim — the pre-migration backward-compat path.
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.conf")
	plain := []byte("legacy plain config\n")
	if err := os.WriteFile(path, plain, 0600); err != nil {
		t.Fatalf("write plain: %v", err)
	}
	if IsEncryptedFile(path) {
		t.Fatalf("plain file should not be detected as encrypted")
	}
	got, err := EncryptedReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("pass-through changed bytes")
	}
}

func TestEncryptedFile_WrongKeyFails(t *testing.T) {
	provider1, _ := fixedKeyProvider(t)
	withKeyProvider(t, provider1)
	dir := t.TempDir()
	path := filepath.Join(dir, "x.dat")
	plain := []byte("secret payload")
	if err := EncryptedWriteFile(path, plain, 0600); err != nil {
		t.Fatalf("write: %v", err)
	}

	// switch keys — decrypt must fail (auth tag mismatch)
	provider2, _ := fixedKeyProvider(t)
	withKeyProvider(t, provider2)
	if _, err := EncryptedReadFile(path); err == nil {
		t.Fatalf("expected decrypt to fail with wrong key")
	}
}

func TestEncryptedFile_NoKeyProvider_FailsToReadEncrypted(t *testing.T) {
	// Encrypt with a key, then strip the provider — the encrypted
	// file must error rather than silently returning ciphertext.
	provider, _ := fixedKeyProvider(t)
	withKeyProvider(t, provider)
	dir := t.TempDir()
	path := filepath.Join(dir, "x.dat")
	if err := EncryptedWriteFile(path, []byte("secret"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}

	setEncryptionKeyProvider(nil)
	t.Cleanup(func() { setEncryptionKeyProvider(provider) })
	if _, err := EncryptedReadFile(path); err == nil {
		t.Fatalf("expected error reading encrypted file with no key provider")
	}
}

func TestEncryptedFile_NoKeyProvider_PassthroughOnWrite(t *testing.T) {
	// Without a provider, writes go to disk as plaintext (early-boot
	// path before SecretStore is initialised). Must not crash.
	setEncryptionKeyProvider(nil)
	dir := t.TempDir()
	path := filepath.Join(dir, "early.conf")
	plain := []byte("early-boot bytes")
	if err := EncryptedWriteFile(path, plain, 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if IsEncryptedFile(path) {
		t.Fatalf("file should be plaintext when no provider")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(raw, plain) {
		t.Fatalf("plaintext-fallback corrupted bytes")
	}
}

func TestEncryptedFile_TamperDetection(t *testing.T) {
	provider, _ := fixedKeyProvider(t)
	withKeyProvider(t, provider)
	dir := t.TempDir()
	path := filepath.Join(dir, "tampered.dat")
	if err := EncryptedWriteFile(path, []byte("important"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Flip one byte in the ciphertext region (past the header)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(data) <= encFileHeaderLen {
		t.Fatalf("file too short to tamper")
	}
	data[encFileHeaderLen] ^= 0xFF
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if _, err := EncryptedReadFile(path); err == nil {
		t.Fatalf("expected GCM auth failure on tampered file")
	}
}

func TestEncryptedFile_AtomicWrite(t *testing.T) {
	// Ensure no .tmp-* files are left behind after a successful write
	provider, _ := fixedKeyProvider(t)
	withKeyProvider(t, provider)
	dir := t.TempDir()
	path := filepath.Join(dir, "atomic.dat")
	if err := EncryptedWriteFile(path, []byte("payload"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != "atomic.dat" {
			t.Fatalf("leftover file after atomic write: %s", e.Name())
		}
	}
}
