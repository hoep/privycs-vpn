//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows implementation: DPAPI (Data Protection API) via direct
// crypt32 syscalls. The master key blob is stored in a file at
// appDataDir()/.masterkey — the file itself is DPAPI-encrypted with
// the current user's profile entropy, so another user on the same
// machine cannot decrypt it, and copying the file to another machine
// also fails.
//
// Why a file instead of the Credential Manager: DPAPI is per-user
// CRYPTO, the Credential Manager is per-user STORAGE. We don't need
// both; a plain file with DPAPI-protected contents is simpler, faster,
// and avoids cgo / unmaintained credential-manager wrapper libs.

var (
	modCrypt32          = windows.NewLazySystemDLL("crypt32.dll")
	procCryptProtect    = modCrypt32.NewProc("CryptProtectData")
	procCryptUnprotect  = modCrypt32.NewProc("CryptUnprotectData")
	procLocalFree       = windows.NewLazySystemDLL("kernel32.dll").NewProc("LocalFree")
	dpapiFlagLocalMachi = uint32(0x4) // CRYPTPROTECT_LOCAL_MACHINE (unused — user-scope only)
)

type windowsDataBlob struct {
	cbData uint32
	pbData *byte
}

type winSecretStore struct {
	mu     sync.Mutex
	cached []byte
}

func newOSSecretStore() SecretStore {
	return &winSecretStore{}
}

func (w *winSecretStore) Backend() string { return "dpapi" }

func (w *winSecretStore) IsAvailable() bool {
	// crypt32.dll ships with every supported Windows. Probe by
	// resolving the proc address — Find returns nil error when ok.
	return procCryptProtect.Find() == nil
}

func (w *winSecretStore) GetOrCreateMasterKey() ([]byte, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.cached) == masterKeyLen {
		return w.cached, nil
	}

	path := w.keyFilePath()
	if blob, err := os.ReadFile(path); err == nil {
		// existing DPAPI blob — unprotect it
		key, derr := dpapiUnprotect(blob)
		if derr == nil && len(key) == masterKeyLen {
			w.cached = key
			return key, nil
		}
		// fall through: blob unreadable (profile migration?), regen
	}

	key, err := generateMasterKey()
	if err != nil {
		return nil, err
	}
	blob, perr := dpapiProtect(key)
	if perr != nil {
		return nil, fmt.Errorf("dpapi protect: %w", perr)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, blob, 0600); err != nil {
		return nil, fmt.Errorf("write masterkey: %w", err)
	}
	w.cached = key
	return key, nil
}

func (w *winSecretStore) keyFilePath() string {
	return filepath.Join(appDataDir(), ".masterkey")
}

// dpapiProtect wraps CryptProtectData (user-scope).
func dpapiProtect(plain []byte) ([]byte, error) {
	if len(plain) == 0 {
		return nil, fmt.Errorf("empty plaintext")
	}
	in := windowsDataBlob{
		cbData: uint32(len(plain)),
		pbData: &plain[0],
	}
	var out windowsDataBlob
	r1, _, e := procCryptProtect.Call(
		uintptr(unsafe.Pointer(&in)),
		0, // szDataDescr
		0, // pOptionalEntropy
		0, // pvReserved
		0, // pPromptStruct
		0, // dwFlags — user scope
		uintptr(unsafe.Pointer(&out)),
	)
	if r1 == 0 {
		return nil, fmt.Errorf("CryptProtectData failed: %v", e)
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(out.pbData)))
	return windowsBlobBytes(out), nil
}

// dpapiUnprotect wraps CryptUnprotectData.
func dpapiUnprotect(blob []byte) ([]byte, error) {
	if len(blob) == 0 {
		return nil, fmt.Errorf("empty blob")
	}
	in := windowsDataBlob{
		cbData: uint32(len(blob)),
		pbData: &blob[0],
	}
	var out windowsDataBlob
	r1, _, e := procCryptUnprotect.Call(
		uintptr(unsafe.Pointer(&in)),
		0, 0, 0, 0, 0,
		uintptr(unsafe.Pointer(&out)),
	)
	if r1 == 0 {
		return nil, fmt.Errorf("CryptUnprotectData failed: %v", e)
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(out.pbData)))
	return windowsBlobBytes(out), nil
}

// windowsBlobBytes copies the OS-managed buffer into a Go slice so we
// can LocalFree the original safely.
func windowsBlobBytes(b windowsDataBlob) []byte {
	out := make([]byte, b.cbData)
	src := unsafe.Slice(b.pbData, int(b.cbData))
	copy(out, src)
	return out
}
