package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"sync"
	"time"
)

// Pro-tier entitlement state + persistence.
//
// State lives in appDataDir()/entitlement.dat, encrypted-at-rest via
// the same SecretStore-backed master key as the VPN configs (see
// encrypted_file.go). The on-disk representation contains the raw
// license-key string + a few derived fields; on every app start we
// re-verify the signature so a key revoked via a future pubkey
// rotation (v2.0) automatically downgrades the user to free tier.

// EntitlementSource is the SKU the active entitlement came from.
// Empty string = no entitlement (free tier).
type EntitlementSource string

// EntitlementState is what the UI sees + what gets persisted.
type EntitlementState struct {
	// Source is the SKU ("privycs_pro_desktop" or
	// "privycs_pro_bundle_all"). Empty when IsPro=false.
	Source EntitlementSource `json:"source"`
	// LicenseKey is the raw PRVC-...-... string that was activated.
	// Re-verified on each app start.
	LicenseKey string `json:"license_key,omitempty"`
	// IsPro is the boolean the gating logic actually checks. False
	// when free, true when an unexpired Pro license is active.
	IsPro bool `json:"is_pro"`
	// FirstActivated is the wall-clock time the user first turned
	// this license on. Used for support diagnostics + UI hint.
	FirstActivated time.Time `json:"first_activated,omitempty"`
	// LastVerified is the wall-clock time of the most recent
	// successful re-verification. Bumped on every app start that
	// passes the signature check.
	LastVerified time.Time `json:"last_verified,omitempty"`
}

// EntitlementRepo owns the on-disk entitlement state + the in-memory
// cache. Methods are safe to call concurrently.
type EntitlementRepo struct {
	mu       sync.RWMutex
	state    EntitlementState
	filePath string
	// onChange is invoked from a goroutine after every state
	// mutation so the App can emit a Wails event to the frontend.
	// Optional — nil before app.go wires the binding.
	onChange func(EntitlementState)
}

// NewEntitlementRepo constructs the repo, loads the on-disk state if
// any, and re-verifies the embedded license against the current build's
// public key. Verification failures wipe the cached state but do NOT
// touch the file — a future app build with a fresh pubkey rotation
// will re-verify cleanly.
func NewEntitlementRepo() *EntitlementRepo {
	r := &EntitlementRepo{
		filePath: filepath.Join(appDataDir(), "entitlement.dat"),
	}
	r.load()
	return r
}

// SetOnChange wires a callback fired after every successful mutation.
// Used by app.go to emit the Wails 'entitlement:changed' event.
func (r *EntitlementRepo) SetOnChange(cb func(EntitlementState)) {
	r.mu.Lock()
	r.onChange = cb
	r.mu.Unlock()
}

// State returns a copy of the current entitlement state. The struct
// is small and value-copyable; callers don't have to worry about
// concurrent mutation of the returned struct.
func (r *EntitlementRepo) State() EntitlementState {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.state
}

// IsPro is the hot path most callers actually need.
func (r *EntitlementRepo) IsPro() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.state.IsPro
}

// Activate verifies the raw key, persists it on success, and emits an
// onChange event. Returns the new state on success. Errors are
// distinguishable via errors.Is (ErrLicenseBadSignature etc.).
func (r *EntitlementRepo) Activate(rawKey string) (EntitlementState, error) {
	pl, err := VerifyLicenseKey(rawKey)
	if err != nil {
		return EntitlementState{}, err
	}

	r.mu.Lock()
	now := time.Now()
	if r.state.FirstActivated.IsZero() {
		r.state.FirstActivated = now
	}
	r.state = EntitlementState{
		Source:         EntitlementSource(pl.SKU),
		LicenseKey:     rawKey,
		IsPro:          true,
		FirstActivated: r.state.FirstActivated,
		LastVerified:   now,
	}
	if r.state.FirstActivated.IsZero() {
		r.state.FirstActivated = now
	}
	stateCopy := r.state
	cb := r.onChange
	r.mu.Unlock()

	r.persist(stateCopy)
	if cb != nil {
		go cb(stateCopy)
	}
	log.Printf("entitlement: activated %s (lic prefix %s)", pl.SKU, safePrefix(rawKey, 12))
	return stateCopy, nil
}

// Deactivate clears the entitlement, persists the empty state, and
// emits an onChange. Used by the "Sign out" button in the UI.
func (r *EntitlementRepo) Deactivate() {
	r.mu.Lock()
	r.state = EntitlementState{}
	stateCopy := r.state
	cb := r.onChange
	r.mu.Unlock()

	r.persist(stateCopy)
	if cb != nil {
		go cb(stateCopy)
	}
	log.Println("entitlement: deactivated")
}

// load reads the on-disk file (if any), re-verifies the signature
// against the current build's public key, and caches the result. A
// verification failure (pubkey rotated, key revoked) wipes the
// in-memory cache but leaves the file alone — the next Activate call
// can re-write with a fresh key.
func (r *EntitlementRepo) load() {
	data, err := EncryptedReadFile(r.filePath)
	if err != nil {
		return // missing file = free tier, normal
	}
	var s EntitlementState
	if err := json.Unmarshal(data, &s); err != nil {
		log.Printf("entitlement: load failed (%v) — staying free tier", err)
		return
	}
	if s.LicenseKey == "" {
		// Empty file (post-deactivate) — keep as free.
		return
	}
	pl, err := VerifyLicenseKey(s.LicenseKey)
	if err != nil {
		// Key fails the current pubkey — log + drop. Don't wipe
		// disk; a future build that rotates pubkey may verify it.
		if errors.Is(err, ErrLicenseEmptyPublic) {
			log.Println("entitlement: skipping load (no pubkey in build)")
		} else {
			log.Printf("entitlement: stored key fails verify (%v) — degrading to free", err)
		}
		return
	}
	s.IsPro = true
	s.LastVerified = time.Now()
	s.Source = EntitlementSource(pl.SKU)
	r.mu.Lock()
	r.state = s
	r.mu.Unlock()
	// best-effort re-persist with bumped LastVerified
	r.persist(s)
	log.Printf("entitlement: loaded %s (lic prefix %s)", pl.SKU, safePrefix(s.LicenseKey, 12))
}

// persist writes the state to disk via EncryptedWriteFile. Best-effort
// — a write failure is logged but doesn't roll back the in-memory
// state (better to give the user the Pro features they just activated
// than to lose the state because of a transient I/O glitch).
func (r *EntitlementRepo) persist(s EntitlementState) {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		log.Printf("entitlement: marshal: %v", err)
		return
	}
	if err := EncryptedWriteFile(r.filePath, data, 0600); err != nil {
		log.Printf("entitlement: persist: %v", err)
	}
}

// safePrefix returns the first n characters of a license string for
// log lines. Avoids dumping the full key into a log file the user
// might paste publicly.
func safePrefix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// ProGatingEnabled is the global master-switch for feature gates.
// While false, IsProUnlocked() returns true everywhere and the
// upgrade dialog never appears — matches the Android Pro-Phase-3
// pattern (v0.9.15.77) that ships gating-code dormant and flips to
// production via a code-change later. The flip lives in this constant
// rather than a config flag so a malicious user can't enable Pro by
// editing JSON on disk.
const ProGatingEnabled = false

// IsProUnlocked is the helper feature-gate callers ask. Returns true
// when gating is globally off OR the active entitlement says Pro.
//
// repo may be nil during early boot (App constructor before NewEntitlementRepo).
// We treat that as Pro-locked rather than Pro-unlocked — fail-closed.
func IsProUnlocked(repo *EntitlementRepo) bool {
	if !ProGatingEnabled {
		return true
	}
	if repo == nil {
		return false
	}
	return repo.IsPro()
}

// formatHumanTime is a tiny helper for the UI to render times consistently.
// Centralised so the UI + the support diagnostics ("entitlement first
// activated 2026-06-01") produce identical strings.
func formatHumanTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02 15:04 UTC")
}

// stateSummary is used by the support tab for a one-line dump.
func (s EntitlementState) stateSummary() string {
	if !s.IsPro {
		return "free"
	}
	return fmt.Sprintf("Pro (%s) activated=%s last-verified=%s",
		string(s.Source),
		formatHumanTime(s.FirstActivated),
		formatHumanTime(s.LastVerified))
}
