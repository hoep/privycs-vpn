package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

// v1.0.5.30 — Helper IPC peer-UID whitelist.
//
// Companion to the peer-cred (SO_PEERCRED / LOCAL_PEERCRED) check
// implemented in helper_peercred_{linux,darwin}.go. The peer-cred
// check tells us WHO is connecting; the whitelist below tells us
// WHO IS ALLOWED.
//
// Model
//
//   - The privileged helper persists a whitelist of UIDs at
//     /etc/privycs-vpn/allowed-uids (Linux + macOS). File is owned
//     root:root mode 0644 (world-readable so the unprivileged app
//     can inspect its own enrolment state via the cmdHelperStatus
//     IPC reply; root-only writable so a local attacker cannot
//     pre-populate it).
//   - UID 0 (root) is always implicitly allowed — covers the case
//     of admin running `privycs-vpn` as root for diagnostic tests
//     and the daemon talking to itself.
//   - Enrolment is Trust-On-First-Use (TOFU): if the whitelist is
//     empty, the first non-root caller's UID is recorded and only
//     that UID (plus root) is allowed thereafter. Subsequent
//     enrolment attempts from other UIDs are rejected.
//   - The app proactively calls the `enroll_uid` IPC on first
//     successful connect, so enrolment happens in the install
//     time-window (microseconds-to-seconds after the helper plist
//     is loaded), shrinking the TOFU race window to near zero.
//
// Migration semantics
//
//   - Pre-v1.0.5.30 installs have NO whitelist file. On first
//     start after upgrade, the helper logs a one-time warning
//     ("operating in unrestricted mode — first connect will
//     enrol") and accepts the first peer-cred-verified caller.
//   - Once enrolment runs once, subsequent helper restarts
//     enforce strictly — file is the source of truth.
//
// Windows
//
//   - peerCredSupported() returns false on Windows; the IPC layer
//     bypasses the UID gate entirely there. The icacls
//     Authenticated-Users ACL on the socket file is the actual
//     enforcement until the v1.0.6 named-pipe transition.

const (
	allowedUIDsDirUnix  = "/etc/privycs-vpn"
	allowedUIDsFileUnix = "/etc/privycs-vpn/allowed-uids"
	allowedUIDsFileMode = 0o644
	allowedUIDsDirMode  = 0o755
)

// allowedUIDsState is an in-memory cache of the on-disk whitelist.
// Loaded on first access, refreshed on every successful write.
// Read-mostly access pattern (one read per IPC accept), so RWMutex
// keeps the fast path lock-free except for occasional writers.
var allowedUIDsState struct {
	sync.RWMutex
	loaded bool
	uids   map[uint32]struct{}
}

// allowedUIDsPath returns the on-disk whitelist file path for this
// platform. Empty string on Windows (whitelist not used there).
func allowedUIDsPath() string {
	switch runtime.GOOS {
	case "linux", "darwin":
		return allowedUIDsFileUnix
	default:
		return ""
	}
}

// loadAllowedUIDs reads the on-disk whitelist into the in-memory
// cache. Safe to call concurrently. Missing file -> empty set
// (caller decides whether that means "deny all" or "TOFU mode").
func loadAllowedUIDs() {
	path := allowedUIDsPath()
	allowedUIDsState.Lock()
	defer allowedUIDsState.Unlock()
	allowedUIDsState.uids = map[uint32]struct{}{}
	allowedUIDsState.loaded = true
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		// Missing file is a normal pre-enrolment state — log only
		// at debug level. Real errors (permission denied reading
		// our own root-owned file) get a louder warning.
		if !os.IsNotExist(err) {
			logHelperWarn("allowed-uids read failed: %v", err)
		}
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		v, err := strconv.ParseUint(line, 10, 32)
		if err != nil {
			logHelperWarn("allowed-uids: skip malformed entry %q: %v", line, err)
			continue
		}
		allowedUIDsState.uids[uint32(v)] = struct{}{}
	}
}

// isAllowedUID reports whether the given UID is enrolled in the
// whitelist. UID 0 (root) is always allowed. Loads the cache on
// first call.
func isAllowedUID(uid uint32) bool {
	if uid == 0 {
		return true
	}
	allowedUIDsState.RLock()
	loaded := allowedUIDsState.loaded
	allowedUIDsState.RUnlock()
	if !loaded {
		loadAllowedUIDs()
	}
	allowedUIDsState.RLock()
	defer allowedUIDsState.RUnlock()
	_, ok := allowedUIDsState.uids[uid]
	return ok
}

// whitelistIsEmpty reports whether the whitelist contains zero
// enrolled UIDs (TOFU eligibility check). UID 0 doesn't count.
func whitelistIsEmpty() bool {
	allowedUIDsState.RLock()
	loaded := allowedUIDsState.loaded
	allowedUIDsState.RUnlock()
	if !loaded {
		loadAllowedUIDs()
	}
	allowedUIDsState.RLock()
	defer allowedUIDsState.RUnlock()
	return len(allowedUIDsState.uids) == 0
}

// enrollUID adds the given UID to the on-disk whitelist and the
// in-memory cache. Idempotent — re-enrolling a UID that's already
// present is a no-op success. Writes the file atomically via
// temp + rename so a crash mid-write cannot leave the whitelist in
// an inconsistent state. Returns the new total count of enrolled
// UIDs for the caller's log line.
func enrollUID(uid uint32) (int, error) {
	path := allowedUIDsPath()
	if path == "" {
		// Windows: no-op success. Logged at the call site so the
		// audit trail still shows the enrolment attempt.
		return 0, nil
	}

	allowedUIDsState.Lock()
	if allowedUIDsState.uids == nil {
		allowedUIDsState.uids = map[uint32]struct{}{}
	}
	allowedUIDsState.uids[uid] = struct{}{}
	// Snapshot under lock for the on-disk write so concurrent
	// enrolments don't lose entries.
	snapshot := make([]uint32, 0, len(allowedUIDsState.uids))
	for k := range allowedUIDsState.uids {
		snapshot = append(snapshot, k)
	}
	count := len(snapshot)
	allowedUIDsState.loaded = true
	allowedUIDsState.Unlock()

	if err := os.MkdirAll(allowedUIDsDirUnix, allowedUIDsDirMode); err != nil {
		return count, fmt.Errorf("enrollUID mkdir: %w", err)
	}

	var sb strings.Builder
	sb.WriteString("# Privycs VPN — helper-IPC peer-UID whitelist\n")
	sb.WriteString("# Auto-generated by the privileged helper on enrol-UID IPC.\n")
	sb.WriteString("# One UID per line. UID 0 (root) is always allowed implicitly.\n")
	for _, u := range snapshot {
		sb.WriteString(strconv.FormatUint(uint64(u), 10))
		sb.WriteByte('\n')
	}

	tmp := filepath.Join(allowedUIDsDirUnix, ".allowed-uids.tmp")
	if err := os.WriteFile(tmp, []byte(sb.String()), allowedUIDsFileMode); err != nil {
		return count, fmt.Errorf("enrollUID write temp: %w", err)
	}
	if err := os.Rename(tmp, allowedUIDsFileUnix); err != nil {
		_ = os.Remove(tmp)
		return count, fmt.Errorf("enrollUID rename: %w", err)
	}
	return count, nil
}

// logHelperWarn is a logging helper distinct from log.Printf so the
// peer-cred + enrolment messages can be filtered easily in the
// helper log (look for "PEERCRED" / "ENROL" prefixes). Cheaper than
// pulling a structured-logger dependency.
func logHelperWarn(format string, args ...interface{}) {
	// Wired into Go's default logger; explicit prefix keeps the
	// audit trail greppable.
	fmt.Fprintf(os.Stderr, "[PEERCRED-WARN] "+format+"\n", args...)
}
