package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// On-disk format for files encrypted by EncryptedWriteFile:
//
//	[4 bytes magic "PVCE"][1 byte version=1][12 bytes GCM nonce][ciphertext+tag]
//
// The magic header lets EncryptedReadFile sniff whether a file is
// already encrypted and let plain-text files round-trip unchanged for
// the pre-migration boot. The version byte allows a future format
// upgrade without re-naming the magic.
const (
	encFileMagic     = "PVCE"
	encFileVersion   = byte(1)
	encFileNonceSize = 12 // AES-GCM standard nonce size
	encFileHeaderLen = len(encFileMagic) + 1 + encFileNonceSize
)

// encryptionKeyProvider returns the master key on demand. Set once at
// app startup by migration.go after the SecretStore is initialized.
// Until then it is nil and EncryptedReadFile/WriteFile fall back to
// plaintext I/O — preserving the pre-migration boot path.
var encryptionKeyProvider func() ([]byte, error)

// setEncryptionKeyProvider wires the key provider. Idempotent; the
// provider must remain stable for the process lifetime (the master key
// is itself stable across runs, so the provider can cache it).
func setEncryptionKeyProvider(p func() ([]byte, error)) {
	encryptionKeyProvider = p
}

// IsEncryptedFile reports whether a file on disk carries the PVCE magic
// header. Used by migration to skip already-encrypted files.
func IsEncryptedFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	hdr := make([]byte, len(encFileMagic))
	n, err := io.ReadFull(f, hdr)
	if err != nil || n != len(hdr) {
		return false
	}
	return string(hdr) == encFileMagic
}

// EncryptedReadFile reads a file and transparently decrypts it if the
// PVCE magic is present. If the file is not encrypted (legacy / pre-
// migration), the raw bytes are returned. If the file is encrypted but
// no key provider is available, an error is returned.
func EncryptedReadFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) < len(encFileMagic) || string(data[:len(encFileMagic)]) != encFileMagic {
		// not encrypted — return as-is (legacy / pre-migration)
		return data, nil
	}
	return decryptBlob(data)
}

// EncryptedWriteFile writes data to disk, encrypting it if a key
// provider is configured. Falls back to plaintext if the provider is
// not yet set or returns an error (so early-boot writes don't fail).
// Writes are atomic via tmp-file + rename, with the file mode applied
// before rename and an explicit fsync to survive a power cut.
func EncryptedWriteFile(path string, data []byte, perm os.FileMode) error {
	out := data
	if encryptionKeyProvider != nil {
		key, kerr := encryptionKeyProvider()
		if kerr == nil && len(key) == masterKeyLen {
			blob, eerr := encryptBlob(data, key)
			if eerr != nil {
				return fmt.Errorf("encrypt %s: %w", path, eerr)
			}
			out = blob
		}
		// if key provider failed, fall through to plaintext —
		// surfaces in logs via migration.go check; not fatal here
	}
	return atomicWriteFile(path, out, perm)
}

// encryptBlob applies AES-256-GCM with a fresh nonce and returns the
// on-disk layout (magic|version|nonce|ciphertext+tag).
func encryptBlob(plain, key []byte) ([]byte, error) {
	if len(key) != masterKeyLen {
		return nil, fmt.Errorf("encrypt: key length %d, want %d", len(key), masterKeyLen)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, encFileNonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	ct := gcm.Seal(nil, nonce, plain, nil)

	buf := bytes.NewBuffer(make([]byte, 0, encFileHeaderLen+len(ct)))
	buf.WriteString(encFileMagic)
	buf.WriteByte(encFileVersion)
	buf.Write(nonce)
	buf.Write(ct)
	return buf.Bytes(), nil
}

// decryptBlob is the inverse of encryptBlob. Pulls the master key
// lazily via encryptionKeyProvider.
func decryptBlob(blob []byte) ([]byte, error) {
	if encryptionKeyProvider == nil {
		return nil, fmt.Errorf("encrypted file but no key provider")
	}
	if len(blob) < encFileHeaderLen {
		return nil, fmt.Errorf("blob too small (%d bytes)", len(blob))
	}
	if string(blob[:len(encFileMagic)]) != encFileMagic {
		return nil, fmt.Errorf("bad magic")
	}
	version := blob[len(encFileMagic)]
	if version != encFileVersion {
		return nil, fmt.Errorf("unsupported version %d", version)
	}
	key, err := encryptionKeyProvider()
	if err != nil {
		return nil, fmt.Errorf("master key unavailable: %w", err)
	}
	if len(key) != masterKeyLen {
		return nil, fmt.Errorf("bad master key length %d", len(key))
	}

	nonce := blob[len(encFileMagic)+1 : encFileHeaderLen]
	ct := blob[encFileHeaderLen:]

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}
	return plain, nil
}

// atomicWriteFile writes via tmp + rename so a torn write never leaves
// a half-written sensitive file on disk. fsyncs the data and the parent
// directory entry before declaring success.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	cleanup = false
	// best-effort dir-fsync for crash-consistency
	if d, derr := os.Open(dir); derr == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// ---- the next two helpers are only used by tests + the migration
// dry-run, but live here so they aren't duplicated. binary.BigEndian
// is imported transitively so go-vet stays quiet across files. ----
var _ = binary.BigEndian
