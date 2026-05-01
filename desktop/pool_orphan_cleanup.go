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
	// Conf-file sweep runs FIRST and CROSS-PLATFORM. The .conf
	// accumulation bug exists on every OS we ship to (each new
	// pool-member name creates a new <appDataDir>/pool-XXX.conf
	// file, no OS removes it on tunnel-down). Service/adapter leak
	// only happens on Windows; the early-return below preserves
	// that gating for the Windows-specific block, but conf-cleanup
	// must not be inside it. v0.9.14.11 fix.
	confsCleaned := a.cleanupOrphanPoolConfs(skipIface)
	if confsCleaned > 0 {
		log.Printf("cleanupOrphan: deleted %d orphan pool-*.conf files", confsCleaned)
	}

	if runtime.GOOS != "windows" {
		// Linux/macOS: wg-quick down removes the WG interface AND
		// releases its kernel state cleanly; no analogous Windows
		// "service registration + wintun adapter" leak. Conf-sweep
		// above is the only cleanup needed on these platforms.
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
	return cleaned
}

// forceUninstallAllPoolServices uninstalls EVERY WireGuardTunnel$pool-*
// service regardless of state (RUNNING, STOPPED, START_PENDING — all
// of them). Called at shutdown so a Quit cannot leave a tunnel up that
// blocks the next App-start's connect attempts.
//
// User report (v0.9.14.10): "beim quit von der letzten version wurde
// der tunnel nicht beendet" — shutdown's `if a.connected { proto.Down() }`
// was conditional on the in-memory connected flag AND only operated on
// proto.ifaceName (the LAST iface configured). Failed-attempt services
// that were "installed but never RUNNING", and earlier rotation
// services whose ifaceName had since changed, both leaked. Result on
// next start: leftover RUNNING service routes traffic, but Privycs's
// new pool-attempts cannot install fresh services beside it.
//
// Filter: only `WireGuardTunnel$pool-*` prefix — user's saved single-
// connection services (`WireGuardTunnel$<connection-name>`) are NOT
// touched. Pool-* prefix is exclusively our naming convention; nothing
// else uses it.
//
// Cross-platform: Windows-only (other OSes have no per-tunnel service
// concept; wg-quick down at proto.Down handles teardown there).
func (a *App) forceUninstallAllPoolServices() int {
	if runtime.GOOS != "windows" {
		return 0
	}

	out, err := execHidden("sc", "query", "type=", "service", "state=", "all").CombinedOutput()
	if err != nil {
		log.Printf("forceUninstall: sc query failed: %v - skipping", err)
		return 0
	}

	var ifaces []string
	for _, rawLine := range strings.Split(string(out), "\n") {
		line := strings.TrimSpace(rawLine)
		if !strings.HasPrefix(line, "SERVICE_NAME:") {
			continue
		}
		svcName := strings.TrimSpace(strings.TrimPrefix(line, "SERVICE_NAME:"))
		if !strings.HasPrefix(svcName, "WireGuardTunnel$pool-") {
			continue
		}
		iface := strings.TrimPrefix(svcName, "WireGuardTunnel$")
		ifaces = append(ifaces, iface)
	}

	if len(ifaces) == 0 {
		return 0
	}

	log.Printf("forceUninstall: tearing down %d pool-* services on shutdown", len(ifaces))

	client := NewHelperClient()
	if !client.IsHelperReachable() {
		log.Printf("forceUninstall: helper unreachable — %d pool-* services may persist after exit", len(ifaces))
		return 0
	}

	cleaned := 0
	for _, iface := range ifaces {
		resp, err := client.SendCommand("disconnect", map[string]string{
			"protocol":  "wireguard",
			"interface": iface,
		})
		if err != nil {
			log.Printf("forceUninstall: %s disconnect failed: %v", iface, err)
			continue
		}
		if !resp.Success {
			log.Printf("forceUninstall: %s helper response: %s (treating as cleaned)", iface, resp.Error)
		}
		cleaned++
	}
	log.Printf("forceUninstall: uninstalled %d pool-* services", cleaned)
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
