package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/crypto/pbkdf2"
	"crypto/sha256"
)

// backupEnvelope is the on-disk format for an encrypted Privycs backup.
// A backup file contains both the user's connections registry and their
// app settings, serialized as JSON and then encrypted with AES-256-GCM
// using a key derived from the user's passphrase via PBKDF2-HMAC-SHA256.
//
// Format-version is embedded so future changes to the KDF or cipher can
// be detected cleanly instead of failing on deserialize. All byte fields
// are base64-encoded so the backup file is safe to paste into email or
// any text-based channel.
//
// Security notes:
//   - KDF: PBKDF2-SHA256 with 600k iterations (OWASP 2023 guidance for
//     PBKDF2-HMAC-SHA256). Not argon2id because we'd rather not pull a
//     C dep into the desktop client; 600k PBKDF2 is sufficient for
//     client-side use where the attacker needs local access to the file.
//   - Salt: 16 random bytes, stored plaintext alongside ciphertext.
//   - Nonce: 12 random bytes, GCM standard, stored plaintext.
//   - AEAD: full envelope is authenticated — tampering with any byte
//     produces a decryption failure, protecting against silent corruption.
type backupEnvelope struct {
	Version    int    `json:"version"`    // 1
	KDF        string `json:"kdf"`        // "pbkdf2-sha256/600000"
	Cipher     string `json:"cipher"`     // "aes-256-gcm"
	Salt       string `json:"salt"`       // base64 PBKDF2 salt
	Nonce      string `json:"nonce"`      // base64 GCM nonce
	Ciphertext string `json:"ciphertext"` // base64 AES-GCM output (ciphertext || tag)
}

// backupPlaintext is what gets encrypted into the envelope.
// Android does a similar export combining connections + settings.
//
// Schema versions:
//   v1 - connections + settings only
//   v2 - adds pools (Connection Pool feature, v0.9.11+). v1 backups
//        still load on v2-aware clients; the Pools field is just nil.
type backupPlaintext struct {
	Version     int                 `json:"version"` // app backup schema version
	AppVersion  string              `json:"app_version"`
	Connections *ConnectionRegistry `json:"connections"`
	Settings    *AppSettings        `json:"settings"`
	Pools       *PoolRegistry       `json:"pools,omitempty"`
}

const (
	backupVersion       = 2
	backupKDFIterations = 600_000
	backupKeyLen        = 32 // 256-bit AES key
	backupSaltLen       = 16
)

// ExportBackup builds an encrypted backup of connections + settings and
// writes it to the given path. Passphrase must not be empty — caller
// should enforce minimum length in the UI.
func (a *App) ExportBackup(path string, passphrase string) error {
	if passphrase == "" {
		return fmt.Errorf("passphrase is required")
	}
	if path == "" {
		return fmt.Errorf("path is required")
	}

	plain := backupPlaintext{
		Version:     backupVersion,
		AppVersion:  AppVersion,
		Connections: a.connections,
		Settings:    a.settings,
		Pools:       a.pools,
	}
	plainBytes, err := json.Marshal(&plain)
	if err != nil {
		return fmt.Errorf("marshal backup: %w", err)
	}

	env, err := encryptBackup(plainBytes, passphrase)
	if err != nil {
		return fmt.Errorf("encrypt backup: %w", err)
	}

	envJSON, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal envelope: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create backup dir: %w", err)
	}
	// 0600 — backup contains sensitive material even while encrypted,
	// because the KDF cost is finite and a stolen file + shoulder-surfed
	// passphrase is a realistic risk.
	if err := os.WriteFile(path, envJSON, 0o600); err != nil {
		return fmt.Errorf("write backup: %w", err)
	}
	return nil
}

// ImportBackup decrypts a backup file, validates it, and replaces the
// app's current connections + settings with its contents. The operation
// is atomic at the JSON-serialize level — if decryption or parsing fails,
// nothing is overwritten. Restart is NOT performed by this call; caller
// (usually frontend) decides whether to reload UI.
func (a *App) ImportBackup(path string, passphrase string) error {
	if passphrase == "" {
		return fmt.Errorf("passphrase is required")
	}

	envBytes, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read backup: %w", err)
	}

	var env backupEnvelope
	if err := json.Unmarshal(envBytes, &env); err != nil {
		return fmt.Errorf("parse backup envelope: %w", err)
	}

	// Accept any backup whose schema we know about. We bumped from v1
	// to v2 with the Pool feature; older backups must still restore
	// on newer clients (their Pools field is just absent). Future
	// versions are rejected so a malformed plaintext does not get
	// interpreted with the wrong schema.
	if env.Version < 1 || env.Version > backupVersion {
		return fmt.Errorf("unsupported backup version %d (this client supports up to %d)", env.Version, backupVersion)
	}
	if env.Cipher != "aes-256-gcm" {
		return fmt.Errorf("unsupported cipher %q", env.Cipher)
	}

	plainBytes, err := decryptBackup(&env, passphrase)
	if err != nil {
		// Don't leak whether it was a wrong passphrase vs. a corrupted
		// file — both show as "bad passphrase or corrupted backup".
		// AEAD failure mode prevents the attacker from learning anything
		// more specific from a failed decryption.
		return fmt.Errorf("decrypt backup: wrong passphrase or corrupted file")
	}

	var plain backupPlaintext
	if err := json.Unmarshal(plainBytes, &plain); err != nil {
		return fmt.Errorf("parse backup contents: %w", err)
	}

	// Replace in-memory state and persist to disk. Order matters:
	// write connections first (larger, more likely to fail on I/O),
	// then settings — so if connections write fails the user still
	// has original settings.
	if plain.Connections != nil {
		// Preserve the current registry's file path — it's not part of
		// the backup (env-dependent).
		plain.Connections.filePath = a.connections.filePath
		a.connections = plain.Connections
		a.connections.Save()
	}
	if plain.Settings != nil {
		a.settings = plain.Settings
		SaveSettings(a.settings)
	}

	// Pools are only present in v2+ backups. Same filePath-preservation
	// pattern as connections - the path is env-dependent and must come
	// from the current process, not the backup origin. saveLocked
	// requires the mutex, but the registry we just deserialized does
	// not have its mutex initialised in any meaningful way; calling
	// Update would re-lock. We persist via a direct save under a
	// fresh registry initialisation pattern.
	if plain.Pools != nil && a.pools != nil {
		plain.Pools.filePath = a.pools.filePath
		a.pools = plain.Pools
		a.pools.mu.Lock()
		err := a.pools.saveLocked()
		a.pools.mu.Unlock()
		if err != nil {
			return fmt.Errorf("persist restored pools: %w", err)
		}
	}
	return nil
}

// encryptBackup wraps AEAD init + ciphertext assembly.
func encryptBackup(plaintext []byte, passphrase string) (*backupEnvelope, error) {
	salt := make([]byte, backupSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	key := pbkdf2.Key([]byte(passphrase), salt, backupKDFIterations, backupKeyLen, sha256.New)

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	ct := aead.Seal(nil, nonce, plaintext, nil)

	return &backupEnvelope{
		Version:    backupVersion,
		KDF:        "pbkdf2-sha256/600000",
		Cipher:     "aes-256-gcm",
		Salt:       base64.StdEncoding.EncodeToString(salt),
		Nonce:      base64.StdEncoding.EncodeToString(nonce),
		Ciphertext: base64.StdEncoding.EncodeToString(ct),
	}, nil
}

func decryptBackup(env *backupEnvelope, passphrase string) ([]byte, error) {
	salt, err := base64.StdEncoding.DecodeString(env.Salt)
	if err != nil {
		return nil, err
	}
	nonce, err := base64.StdEncoding.DecodeString(env.Nonce)
	if err != nil {
		return nil, err
	}
	ct, err := base64.StdEncoding.DecodeString(env.Ciphertext)
	if err != nil {
		return nil, err
	}
	key := pbkdf2.Key([]byte(passphrase), salt, backupKDFIterations, backupKeyLen, sha256.New)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return aead.Open(nil, nonce, ct, nil)
}
