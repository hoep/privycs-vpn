//go:build windows

package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"runtime"
	"strings"
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
	log.Println("Sinkhole(windows): engaging")

	// 1. Snapshot pre-engage state. Done BEFORE any change so that
	// even if the OS rejects a rule in step 2, the snapshot already
	// captures original DNS / route metric and Release can restore.
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

	// 2. Defensive cleanup: remove any leftover Privycs-Sinkhole-*
	// rules from a prior crashed run. Without this, our New-
	// NetFirewallRule below would fail on duplicate-name and the
	// rollback branch would be unable to distinguish "added by us"
	// from "leftover from previous us".
	_, _ = execHidden("powershell", "-NoProfile", "-NonInteractive", "-Command",
		`Remove-NetFirewallRule -DisplayName 'Privycs-Sinkhole-*' -ErrorAction SilentlyContinue`,
	).CombinedOutput()

	// 3. Atomic apply: PowerShell try/catch with rollback. If ANY
	// New-NetFirewallRule throws, the catch branch removes every
	// rule it managed to add and rethrows. Caller sees an error and
	// the system is in the pre-step-2 state.
	psScript := `
$ErrorActionPreference = 'Stop'
$added = @()
try {
    New-NetFirewallRule -DisplayName 'Privycs-Sinkhole-AllowLoopback' `+"`"+`
        -Direction Outbound -Action Allow -RemoteAddress 127.0.0.0/8 `+"`"+`
        -Profile Any -Enabled True | Out-Null
    $added += 'Privycs-Sinkhole-AllowLoopback'

    New-NetFirewallRule -DisplayName 'Privycs-Sinkhole-BlockOutbound' `+"`"+`
        -Direction Outbound -Action Block -Profile Any -Enabled True | Out-Null
    $added += 'Privycs-Sinkhole-BlockOutbound'

    New-NetFirewallRule -DisplayName 'Privycs-Sinkhole-BlockInbound' `+"`"+`
        -Direction Inbound -Action Block -Profile Any -Enabled True | Out-Null
    $added += 'Privycs-Sinkhole-BlockInbound'
} catch {
    $errMsg = $_.Exception.Message
    foreach ($name in $added) {
        Remove-NetFirewallRule -DisplayName $name -ErrorAction SilentlyContinue
    }
    Write-Error "Sinkhole engage rolled back: $errMsg"
    exit 1
}
exit 0
`
	out, err := execHidden("powershell", "-NoProfile", "-NonInteractive", "-Command", psScript).CombinedOutput()
	if err != nil {
		// PowerShell already rolled back. Delete the snapshot too -
		// no state was actually committed, so leaving a snapshot
		// would mislead RecoverFromCrash on next start.
		_ = DeleteSnapshot()
		return fmt.Errorf("engage script failed: %w: %s", err, strings.TrimSpace(string(out)))
	}

	log.Println("Sinkhole(windows): engaged successfully (3 rules applied atomically)")
	return nil
}

func (s *windowsSinkhole) Release(ctx context.Context) error {
	log.Println("Sinkhole(windows): releasing")

	// Load the snapshot to know what to undo. If missing, fall back
	// to best-effort cleanup based on the rule-name prefix.
	snap, err := LoadSnapshot()
	if err != nil && os.IsNotExist(err) {
		log.Println("Sinkhole(windows): no snapshot - best-effort cleanup")
		s.bestEffortCleanup()
		return nil
	}
	if err != nil {
		log.Printf("Sinkhole(windows): snapshot load failed (corrupt?): %v - best-effort cleanup", err)
		s.bestEffortCleanup()
		_ = DeleteSnapshot()
		return nil
	}

	// 1. Remove every rule the snapshot recorded as added. Use
	// SilentlyContinue per rule so a missing one (e.g. removed by
	// the user via Group Policy) doesn't abort the rest.
	rulesToRemove := snap.FirewallRulesAdded
	if len(rulesToRemove) == 0 {
		// Old snapshot from a future schema version with no rule
		// list? Fall back to prefix-based cleanup.
		rulesToRemove = windowsSinkholeRules
	}
	psParts := []string{`$ErrorActionPreference = 'Continue'`}
	for _, name := range rulesToRemove {
		// Hard-coded names from our own rule list, not user input.
		// PowerShell quoting is straightforward: single-quote the
		// literal name.
		psParts = append(psParts,
			fmt.Sprintf("Remove-NetFirewallRule -DisplayName '%s' -ErrorAction SilentlyContinue",
				escapePowerShellString(name)),
		)
	}
	psScript := strings.Join(psParts, "; ")
	out, err := execHidden("powershell", "-NoProfile", "-NonInteractive", "-Command", psScript).CombinedOutput()
	if err != nil {
		// Best-effort: log and continue. We MUST attempt the rest of
		// the restore even if a partial PowerShell run had warnings.
		log.Printf("Sinkhole(windows): rule removal had warnings: %v: %s", err, strings.TrimSpace(string(out)))
	}

	// 2. Restore DNS if we modified it. (This implementation does
	// not currently override DNS, so DNSPerInterface is empty and
	// the loop is a no-op. The hook is in place for Phase 4 when
	// DNS-redirect features may use it.)
	for ifName, servers := range snap.DNSPerInterface {
		if len(servers) == 0 {
			continue
		}
		// Use Set-DnsClientServerAddress to restore the saved
		// servers. -ResetServerAddresses without args returns
		// interface to DHCP / automatic.
		var psSet string
		if servers[0] == "AUTO" {
			psSet = fmt.Sprintf(
				"Set-DnsClientServerAddress -InterfaceAlias '%s' -ResetServerAddresses -ErrorAction SilentlyContinue",
				escapePowerShellString(ifName),
			)
		} else {
			quoted := make([]string, 0, len(servers))
			for _, srv := range servers {
				quoted = append(quoted, "'"+escapePowerShellString(srv)+"'")
			}
			psSet = fmt.Sprintf(
				"Set-DnsClientServerAddress -InterfaceAlias '%s' -ServerAddresses (%s) -ErrorAction SilentlyContinue",
				escapePowerShellString(ifName), strings.Join(quoted, ","),
			)
		}
		_, _ = execHidden("powershell", "-NoProfile", "-NonInteractive", "-Command", psSet).CombinedOutput()
	}

	// 3. Delete the snapshot LAST so a crash between rule-removal
	// and snapshot-deletion still leaves a recoverable state on the
	// next startup (RecoverFromCrash will rerun us).
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
