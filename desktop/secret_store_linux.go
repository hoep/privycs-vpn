//go:build linux

package main

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/godbus/dbus/v5"
)

// Linux implementation: best-effort libsecret via D-Bus session bus
// (`org.freedesktop.secrets`) — works under GNOME-Keyring, KWallet 5+
// libsecret-compatibility, KeePassXC secret-service. Falls back to a
// per-user file at appDataDir()/.masterkey when no daemon is running
// (headless servers / minimal WMs / first-boot before keyring unlock).
//
// The file-fallback is mode 0600 and lives only under the user's
// profile — protection is filesystem-level, not crypto-level. Good
// enough against same-user other processes and disk-image leaks; NOT
// good enough against a root-level attacker. We surface this clearly
// in logs + UI (TODO settings banner).

type linuxSecretStore struct {
	mu         sync.Mutex
	cached     []byte
	backend    string
	dbusFailed bool
}

func newOSSecretStore() SecretStore {
	return &linuxSecretStore{}
}

func (l *linuxSecretStore) Backend() string {
	if l.backend == "" {
		return "unknown"
	}
	return l.backend
}

// IsAvailable is conservative — we report true even when the dbus
// daemon is down, because the file-fallback works on a vanilla
// filesystem without external services. The caller can inspect
// Backend() after GetOrCreateMasterKey for the actual mechanism.
func (l *linuxSecretStore) IsAvailable() bool {
	return true
}

func (l *linuxSecretStore) GetOrCreateMasterKey() ([]byte, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.cached) == masterKeyLen {
		return l.cached, nil
	}

	// 1) Try libsecret via D-Bus session bus
	if !l.dbusFailed {
		if key, err := l.tryDBusGet(); err == nil && len(key) == masterKeyLen {
			l.cached = key
			l.backend = "libsecret"
			return key, nil
		}
	}

	// 2) Try file-fallback read
	if key, err := l.fileFallbackRead(); err == nil && len(key) == masterKeyLen {
		l.cached = key
		l.backend = "file-fallback"
		return key, nil
	}

	// 3) Generate fresh and persist via whichever backend works
	key, err := generateMasterKey()
	if err != nil {
		return nil, err
	}
	if !l.dbusFailed {
		if err := l.tryDBusStore(key); err == nil {
			l.cached = key
			l.backend = "libsecret"
			return key, nil
		}
		l.dbusFailed = true
	}
	if err := l.fileFallbackWrite(key); err != nil {
		return nil, fmt.Errorf("file-fallback store: %w", err)
	}
	l.cached = key
	l.backend = "file-fallback"
	return key, nil
}

// ---- D-Bus libsecret path (org.freedesktop.secrets) ----
//
// We talk to the SecretService API directly: open the default
// collection, look up an item with our attributes, decode the secret.
// On miss, create the item. Encryption is "plain" — the session bus is
// trusted, the daemon itself manages at-rest encryption.

const (
	secretsService    = "org.freedesktop.secrets"
	secretsPath       = "/org/freedesktop/secrets"
	collectionDefault = "/org/freedesktop/secrets/aliases/default"
	itemAttrService   = "service"
	itemAttrAccount   = "account"
)

func (l *linuxSecretStore) tryDBusGet() ([]byte, error) {
	conn, err := dbus.SessionBus()
	if err != nil {
		return nil, fmt.Errorf("session bus: %w", err)
	}
	service := conn.Object(secretsService, secretsPath)

	// SearchItems with our attributes
	attrs := map[string]string{
		itemAttrService: secretStoreServiceName,
		itemAttrAccount: secretStoreAccount,
	}
	var unlocked, locked []dbus.ObjectPath
	if err := service.Call("org.freedesktop.Secret.Service.SearchItems", 0, attrs).
		Store(&unlocked, &locked); err != nil {
		return nil, fmt.Errorf("search items: %w", err)
	}
	items := append([]dbus.ObjectPath{}, unlocked...)
	items = append(items, locked...)
	if len(items) == 0 {
		return nil, fmt.Errorf("not found")
	}

	// Open a plain session (no encryption negotiation — bus is trusted)
	var sessionPath dbus.ObjectPath
	var sessionAlgoOutput dbus.Variant
	if err := service.Call("org.freedesktop.Secret.Service.OpenSession", 0, "plain", dbus.MakeVariant("")).
		Store(&sessionAlgoOutput, &sessionPath); err != nil {
		return nil, fmt.Errorf("open session: %w", err)
	}

	itemObj := conn.Object(secretsService, items[0])
	var secret struct {
		Session     dbus.ObjectPath
		Parameters  []byte
		Value       []byte
		ContentType string
	}
	if err := itemObj.Call("org.freedesktop.Secret.Item.GetSecret", 0, sessionPath).
		Store(&secret); err != nil {
		return nil, fmt.Errorf("get secret: %w", err)
	}
	return base64.StdEncoding.DecodeString(string(secret.Value))
}

func (l *linuxSecretStore) tryDBusStore(key []byte) error {
	conn, err := dbus.SessionBus()
	if err != nil {
		return fmt.Errorf("session bus: %w", err)
	}
	service := conn.Object(secretsService, secretsPath)

	var sessionPath dbus.ObjectPath
	var sessionAlgoOutput dbus.Variant
	if err := service.Call("org.freedesktop.Secret.Service.OpenSession", 0, "plain", dbus.MakeVariant("")).
		Store(&sessionAlgoOutput, &sessionPath); err != nil {
		return fmt.Errorf("open session: %w", err)
	}

	attrs := map[string]string{
		itemAttrService: secretStoreServiceName,
		itemAttrAccount: secretStoreAccount,
	}
	properties := map[string]dbus.Variant{
		"org.freedesktop.Secret.Item.Label":      dbus.MakeVariant("Privycs VPN master key"),
		"org.freedesktop.Secret.Item.Attributes": dbus.MakeVariant(attrs),
	}
	secret := struct {
		Session     dbus.ObjectPath
		Parameters  []byte
		Value       []byte
		ContentType string
	}{
		Session:     sessionPath,
		Parameters:  []byte{},
		Value:       []byte(base64.StdEncoding.EncodeToString(key)),
		ContentType: "application/octet-stream",
	}

	collection := conn.Object(secretsService, collectionDefault)
	var item, prompt dbus.ObjectPath
	if err := collection.Call("org.freedesktop.Secret.Collection.CreateItem", 0,
		properties, secret, true /* replace */).
		Store(&item, &prompt); err != nil {
		return fmt.Errorf("create item: %w", err)
	}
	// Prompt-handling omitted — default collection is unlocked at
	// login on most distros; if not, the CreateItem returns the prompt
	// object path and we'd need to call Prompt() and wait for the
	// signal. For now, surface as failure → fall back to file.
	if prompt != "/" {
		return fmt.Errorf("collection locked (prompt required)")
	}
	_ = item
	return nil
}

// ---- File fallback ----

func (l *linuxSecretStore) keyFilePath() string {
	return filepath.Join(appDataDir(), ".masterkey")
}

// fileFallbackRead reads the master key from disk. The file stores
// base64(key) plus an integrity-tag (HMAC-style: sha256 of key) — not
// crypto-strong, but catches accidental corruption / wrong-file copies.
func (l *linuxSecretStore) fileFallbackRead() ([]byte, error) {
	data, err := os.ReadFile(l.keyFilePath())
	if err != nil {
		return nil, err
	}
	if len(data) < 64+masterKeyLen {
		return nil, fmt.Errorf("masterkey file too small (%d bytes)", len(data))
	}
	// layout: [32-byte sha256 hash of key (hex-encoded = 64 chars)] [base64 key]
	hexHash := string(data[:64])
	body := data[64:]
	key, err := base64.StdEncoding.DecodeString(string(body))
	if err != nil {
		return nil, fmt.Errorf("decode key: %w", err)
	}
	if len(key) != masterKeyLen {
		return nil, fmt.Errorf("bad key length %d", len(key))
	}
	want := sha256.Sum256(key)
	gotBytes := make([]byte, 32)
	for i := 0; i < 32; i++ {
		fmt.Sscanf(hexHash[i*2:i*2+2], "%02x", &gotBytes[i])
	}
	for i := 0; i < 32; i++ {
		if gotBytes[i] != want[i] {
			return nil, fmt.Errorf("integrity check failed")
		}
	}
	return key, nil
}

func (l *linuxSecretStore) fileFallbackWrite(key []byte) error {
	if len(key) != masterKeyLen {
		return fmt.Errorf("bad key length %d", len(key))
	}
	hash := sha256.Sum256(key)
	hexHash := fmt.Sprintf("%x", hash)
	body := base64.StdEncoding.EncodeToString(key)
	data := []byte(hexHash + body)
	path := l.keyFilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}
