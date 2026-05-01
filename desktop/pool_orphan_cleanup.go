package main

import (
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// cleanupOrphanPoolServices uninstalls STOPPED WireGuardTunnel$pool-*
// services on Windows so each retried pool member's installtunnelservice
// gets a fresh wintun adapter slot to allocate from.
//
// User-reported scenario (v0.9.14.x): pool retry loop tried 12+ different
// members, each one creating a unique pool-XXXXXXXX service-name. The
// wintun.sys driver leaked an adapter per crashed-during-init service
// (WIN32_EXIT_CODE 5010 = ERROR_DLL_INIT_FAILED). After enough leaks
// every new install failed with 5010 — the user found 15 orphan
// WireGuardTunnel$ services in their Windows Services list. Singles
// were unaffected because their service-name is the connection-name
// (stable, recycled).
//
// Skip the currently-targeted iface so we do not race with our own
// Up() that just installed the new service. Only touch pool-* prefix
// (don't kill user's saved single-connection services that happen to
// be stopped). Runs through the privileged helper's existing
// disconnect command (which calls /uninstalltunnelservice). No new
// helper command needed — disconnect on a stopped service is a clean
// uninstall.
//
// Returns the number of services successfully uninstalled. Cheap to
// call (sc query is fast, helper IPC is local-pipe). Safe to skip on
// non-Windows.
func (a *App) cleanupOrphanPoolServices(skipIface string) int {
	if runtime.GOOS != "windows" {
		return 0
	}

	// Enumerate every Windows service so we can filter by name prefix.
	// "type= service state= all" lists running + stopped services in
	// one pass; cheaper than two separate queries.
	out, err := execHidden("sc", "query", "type=", "service", "state=", "all").CombinedOutput()
	if err != nil {
		log.Printf("cleanupOrphan: sc query failed: %v - skipping", err)
		return 0
	}

	var orphanIfaces []string
	for _, rawLine := range strings.Split(string(out), "\n") {
		line := strings.TrimSpace(rawLine)
		if !strings.HasPrefix(line, "SERVICE_NAME:") {
			continue
		}
		svcName := strings.TrimSpace(strings.TrimPrefix(line, "SERVICE_NAME:"))
		// Pool services only — the prefix lives entirely in our naming
		// convention so this never touches user-named singles.
		if !strings.HasPrefix(svcName, "WireGuardTunnel$pool-") {
			continue
		}
		ifaceName := strings.TrimPrefix(svcName, "WireGuardTunnel$")
		if ifaceName == skipIface {
			continue
		}
		// Per-service state probe. STOPPED + missing-service (1060)
		// both mean "no live tunnel"; either is safe to clean.
		stateOut, _ := execHidden("sc", "query", svcName).CombinedOutput()
		stateStr := string(stateOut)
		if !strings.Contains(stateStr, "STOPPED") && !strings.Contains(stateStr, "1060") {
			continue
		}
		orphanIfaces = append(orphanIfaces, ifaceName)
	}

	if len(orphanIfaces) == 0 {
		return 0
	}

	log.Printf("cleanupOrphan: found %d orphan pool-* services to uninstall", len(orphanIfaces))

	// Uninstall via the privileged helper. Helper's existing
	// "disconnect" handler runs uninstalltunnelservice for the named
	// interface, which clears the service registration AND releases
	// the wintun adapter — exactly what we need.
	client := NewHelperClient()
	if !client.IsHelperReachable() {
		log.Printf("cleanupOrphan: helper unreachable, %d orphans left in place (run with --install-helper to recover)", len(orphanIfaces))
		return 0
	}
	cleaned := 0
	for _, iface := range orphanIfaces {
		resp, err := client.SendCommand("disconnect", map[string]string{
			"protocol":  "wireguard",
			"interface": iface,
		})
		if err != nil {
			log.Printf("cleanupOrphan: disconnect %s failed: %v", iface, err)
			continue
		}
		if !resp.Success {
			// "service not running" / "already uninstalled" are
			// success-equivalent for cleanup purposes — bookkeeping
			// only. Helper reports them as Success=false but we
			// still got the registry entry off our path.
			log.Printf("cleanupOrphan: %s helper response: %s (treating as cleaned)", iface, resp.Error)
		}
		cleaned++
	}
	log.Printf("cleanupOrphan: uninstalled %d orphan pool-* services", cleaned)

	// Phase 2 (v0.9.14.10): sweep orphan pool-*.conf files. The helper's
	// uninstalltunnelservice removes the service registration + the
	// wintun adapter, but leaves the .conf file on disk in
	// %LOCALAPPDATA%\privycs-vpn\. Over time this accumulates one file
	// per pool-member ever connected — user reported "Unmengen von pool
	// confs in der Windows dir". Each file is ~1KB so it's not a disk-
	// space issue, but disk-clutter and confusing during diagnostics.
	// Sweep every pool-*.conf whose iface is NOT the current target and
	// NOT the pre-warmed pending member, delete it.
	confsCleaned := a.cleanupOrphanPoolConfs(skipIface)
	if confsCleaned > 0 {
		log.Printf("cleanupOrphan: deleted %d orphan pool-*.conf files", confsCleaned)
	}

	return cleaned
}

// cleanupOrphanPoolConfs sweeps the appData directory for pool-*.conf
// files and deletes any whose corresponding service is gone. Skips the
// current iface (skipIface) AND the pre-warmed pending member's iface
// (looked up from the active pool's pending state) so the next
// rotation can adopt the pre-written config without rebuilding it.
//
// Cross-platform helper, but the file naming convention pool-<id>.conf
// is identical on Linux/macOS, so the sweep is safe everywhere even
// though the practical leak is Windows-only.
func (a *App) cleanupOrphanPoolConfs(skipIface string) int {
	dir := appDataDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		log.Printf("cleanupOrphanConfs: ReadDir %s failed: %v - skipping", dir, err)
		return 0
	}

	// Build the keep-set: current target + pre-warmed pending member.
	// Pending lookup needs the active pool — when no pool is active,
	// only skipIface is preserved.
	keep := map[string]struct{}{}
	if skipIface != "" {
		keep[skipIface] = struct{}{}
	}
	a.mu.RLock()
	activePoolID := a.activePoolID
	a.mu.RUnlock()
	if activePoolID != "" && a.pools != nil {
		if pendingID := a.pools.PendingMemberID(activePoolID); pendingID != "" {
			keep["pool-"+shortID(pendingID)] = struct{}{}
		}
	}

	deleted := 0
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "pool-") || !strings.HasSuffix(name, ".conf") {
			continue
		}
		iface := strings.TrimSuffix(name, ".conf")
		if _, isKept := keep[iface]; isKept {
			continue
		}
		path := filepath.Join(dir, name)
		if err := os.Remove(path); err != nil {
			log.Printf("cleanupOrphanConfs: remove %s failed: %v", name, err)
			continue
		}
		deleted++
	}
	return deleted
}
