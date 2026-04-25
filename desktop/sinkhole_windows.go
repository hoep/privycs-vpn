//go:build windows

package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"runtime"
	"time"
)

// Windows sinkhole. Uses Windows Firewall (PowerShell Net-Firewall
// cmdlets) with explicit transaction semantics:
//
//   - Engage: take a snapshot first, write it to disk atomically,
//     THEN apply firewall rules in a single PowerShell scriptblock
//     wrapped in try/catch. On any single rule failure the catch
//     branch removes every rule it added in this transaction. Net
//     effect: either all rules are present, or none are - never a
//     partial state.
//
//   - Release: read snapshot, remove every rule we recorded as added,
//     restore DNS if we changed it, finally delete the snapshot file.
//     Best-effort throughout: an individual rule remove that fails
//     (because another tool already deleted it, etc.) is logged but
//     does not stop the rest of the cleanup from running.
//
//   - RecoverFromCrash: if a snapshot is on disk at app startup, the
//     previous run crashed before it could clean up. Run Release.
//     Idempotent.
//
// Naming: rules use prefix "Privycs-Sinkhole-" so they CANNOT collide
// with the existing killswitch.go rules which use "PrivycsKS-".
// Both systems can run in parallel during the migration window;
// Phase 3 will retire the old code path.

// Default data dir on Windows: %PROGRAMDATA%\PrivycsVPN. Falls back
// to C:\ProgramData\PrivycsVPN if env var is missing.
func platformDataDir() string {
	pd := os.Getenv("PROGRAMDATA")
	if pd == "" {
		pd = `C:\ProgramData`
	}
	return pd + `\PrivycsVPN`
}

// Rule names that this implementation owns. Kept in a single var for
// audit + cleanup-by-name in the catch branch.
var windowsSinkholeRules = []string{
	"Privycs-Sinkhole-AllowLoopback",
	"Privycs-Sinkhole-BlockOutbound",
	"Privycs-Sinkhole-BlockInbound",
}

type windowsSinkhole struct{}

// NewWindowsSinkhole returns the Windows platform driver.
func NewWindowsSinkhole() Sinkhole {
	return &windowsSinkhole{}
}

// NewPlatformSinkhole is the build-tag-resolved factory app.go uses.
func NewPlatformSinkhole() Sinkhole { return NewWindowsSinkhole() }

func (s *windowsSinkhole) Engage(ctx context.Context) error {
	log.Println("Sinkhole(windows): engaging via privileged helper")

	// Snapshot first - if helper IPC fails AND somehow leaves partial
	// rules, the snapshot lets RecoverFromCrash clean up next start.
	snap, err := s.captureSnapshot()
	if err != nil {
		return fmt.Errorf("snapshot capture: %w", err)
	}
	snap.Version = 1
	snap.EngagedAt = time.Now()
	snap.Platform = runtime.GOOS
	snap.Reason = "kill switch sinkhole engaged"
	snap.FirewallRulesAdded = append([]string(nil), windowsSinkholeRules...)

	if err := SaveSnapshot(snap); err != nil {
		return fmt.Errorf("snapshot save: %w", err)
	}

	// Route through privileged helper - the unprivileged Wails app
	// process gets "Zugriff verweigert" calling netsh advfirewall
	// add rule directly. The helper runs as SYSTEM and has the
	// privileges. The helper's sinkhole_engage handler is itself
	// transactional with rollback.
	client := NewHelperClient()
	if !client.IsHelperReachable() {
		_ = DeleteSnapshot()
		return fmt.Errorf("helper not reachable - cannot engage sinkhole")
	}
	resp, err := client.SendCommand("sinkhole_engage", nil)
	if err != nil {
		_ = DeleteSnapshot()
		return fmt.Errorf("helper IPC failed: %w", err)
	}
	if !resp.Success {
		_ = DeleteSnapshot()
		return fmt.Errorf("helper engage failed: %s", resp.Error)
	}

	log.Println("Sinkhole(windows): engaged successfully via helper")
	return nil
}

func (s *windowsSinkhole) Release(ctx context.Context) error {
	log.Println("Sinkhole(windows): releasing via privileged helper")

	// Always attempt helper-based release first - it's the path that
	// works (SYSTEM privileges). Best-effort: log warnings but always
	// continue to the snapshot cleanup so we don't leak files.
	client := NewHelperClient()
	if client.IsHelperReachable() {
		if resp, err := client.SendCommand("sinkhole_release", nil); err != nil {
			log.Printf("Sinkhole(windows): helper IPC release failed: %v", err)
		} else if !resp.Success {
			log.Printf("Sinkhole(windows): helper release reported failure: %s", resp.Error)
		}
	} else {
		log.Println("Sinkhole(windows): helper unreachable - cannot remove firewall rules from unprivileged process")
	}

	// Always clean the snapshot file. Even if the firewall release
	// failed, leaving the snapshot would cause RecoverFromCrash to
	// retry on the next start - which is fine but log it.
	if err := DeleteSnapshot(); err != nil {
		log.Printf("Sinkhole(windows): snapshot delete failed (will retry next start): %v", err)
	}

	log.Println("Sinkhole(windows): released")
	return nil
}

func (s *windowsSinkhole) RecoverFromCrash(ctx context.Context) error {
	if _, err := os.Stat(snapshotPath()); err != nil {
		// No snapshot - nothing to recover. Normal cold start.
		return nil
	}
	log.Println("Sinkhole(windows): leftover snapshot detected - recovering as Release")
	return s.Release(ctx)
}

// captureSnapshot reads pre-engage state into a snapshot struct.
// Currently captures nothing dynamic on Windows because this
// implementation does not modify DNS or routing - only firewall
// rules with our own naming, which Release identifies by snapshot
// list. The hook is here for Phase 4 expansion.
func (s *windowsSinkhole) captureSnapshot() (*SinkholeSnapshot, error) {
	return &SinkholeSnapshot{}, nil
}

// bestEffortCleanup runs when the snapshot is missing or corrupt.
// Removes any rule whose name starts with our prefix - errs on the
// side of removing too much over leaving stale block rules around.
func (s *windowsSinkhole) bestEffortCleanup() {
	_, err := execHidden("powershell", "-NoProfile", "-NonInteractive", "-Command",
		`Remove-NetFirewallRule -DisplayName 'Privycs-Sinkhole-*' -ErrorAction SilentlyContinue`,
	).CombinedOutput()
	if err != nil {
		log.Printf("Sinkhole(windows): best-effort cleanup warning: %v", err)
	}
}
