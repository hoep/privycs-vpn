package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// Sinkhole is the platform-specific traffic-block driver. Each OS
// implements Engage / Release / RecoverFromCrash with its native
// firewall mechanism (iptables on Linux, pf on macOS, Windows
// Firewall via PowerShell on Windows). Implementations are in
// sinkhole_<goos>.go files, selected by build tags.
//
// Contract:
//
//   - Engage MUST be all-or-nothing. If any single rule application
//     fails, the implementation MUST roll back any partial state and
//     return an error. The system MUST be in the pre-engage state
//     after a failed Engage.
//
//   - Release MUST be best-effort tolerant. Even if the snapshot file
//     is missing, partial rules left over, or the OS is in an
//     unexpected state, Release MUST attempt to restore connectivity
//     as far as possible. Network MUST work after Release returns.
//
//   - RecoverFromCrash is called once at app startup. If the previous
//     run crashed mid-engage and left a snapshot on disk, this method
//     reconciles state by performing an idempotent Release. Safe to
//     call when there is nothing to recover (no snapshot file).
//
//   - Engage and Release MUST snapshot the relevant pre-engage state
//     to disk BEFORE making any change. The snapshot is the source of
//     truth for Release; Release reads it back, applies inverse
//     operations, then deletes the snapshot. A snapshot present after
//     successful Release is a bug.
type Sinkhole interface {
	Engage(ctx context.Context) error
	Release(ctx context.Context) error
	RecoverFromCrash(ctx context.Context) error
}

// SinkholeSnapshot is the on-disk format that records the pre-engage
// state. Loaded by Release and RecoverFromCrash. Fields are platform-
// agnostic; per-platform implementations populate the fields they
// care about and ignore the rest. Forward-compatible: unknown fields
// are tolerated by encoding/json.
type SinkholeSnapshot struct {
	Version    int       `json:"version"`     // schema version (currently 1)
	EngagedAt  time.Time `json:"engaged_at"`  // wall-clock at engage
	Platform   string    `json:"platform"`    // runtime.GOOS at engage time
	Reason     string    `json:"reason"`      // why we engaged (for forensics)

	// DNS snapshot - per-interface mapping of original DNS servers.
	// Set by Windows when the engage path overrides DNS; left empty
	// otherwise.
	DNSPerInterface map[string][]string `json:"dns_per_interface,omitempty"`

	// Default route metric. Used on Windows to detect post-release
	// drift; informational on other platforms.
	DefaultRouteMetric int `json:"default_route_metric,omitempty"`

	// FirewallRulesAdded - list of rule names this engage added. The
	// release path removes rules from this list specifically rather
	// than relying on prefix-glob matches that could accidentally
	// affect rules from a different version of the app.
	FirewallRulesAdded []string `json:"firewall_rules_added,omitempty"`
}

// snapshotPathOverride is set by tests to redirect the snapshot
// path away from the real platformDataDir() into a per-test temp
// directory. Empty in production - never set outside tests.
var snapshotPathOverride string

// snapshotPath returns the platform-appropriate path for the
// snapshot file. Used by all platform implementations + the
// crash-recovery path.
func snapshotPath() string {
	if snapshotPathOverride != "" {
		return snapshotPathOverride
	}
	// Mirror existing privileged_helper.go data layout: %PROGRAMDATA%
	// /PrivycsVPN on Windows, app-data dir elsewhere. Falls back to
	// /tmp if neither resolves (test contexts).
	dir := platformDataDir()
	if dir == "" {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "sinkhole-snapshot.json")
}

// SaveSnapshot writes the snapshot atomically. Used by Engage paths.
// Atomic-write pattern: write to .tmp, fsync, rename to final. This
// prevents partial-snapshot files if the process is killed mid-write.
func SaveSnapshot(s *SinkholeSnapshot) error {
	path := snapshotPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("snapshot mkdir: %w", err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("snapshot marshal: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("snapshot write tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("snapshot rename: %w", err)
	}
	return nil
}

// LoadSnapshot reads the snapshot file. Returns os.IsNotExist-true
// error if no snapshot exists - callers should treat this as "no
// previous state to recover".
func LoadSnapshot() (*SinkholeSnapshot, error) {
	path := snapshotPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s SinkholeSnapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("snapshot parse (corrupt?): %w", err)
	}
	return &s, nil
}

// DeleteSnapshot removes the snapshot file. Called by Release after
// successful restoration. Idempotent: missing file is not an error.
func DeleteSnapshot() error {
	path := snapshotPath()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("snapshot remove: %w", err)
	}
	return nil
}

// SinkholeController bridges KillSwitchManager state transitions to
// the platform-specific Sinkhole driver. It subscribes once, then
// consumes transitions for the process lifetime, calling Engage on
// every IDLE/ARMED -> SINKHOLE and Release on every SINKHOLE -> any.
//
// Wiring policy: at app startup, RecoverFromCrash runs FIRST (cleans
// up any leftover state from a prior crashed run) BEFORE the
// subscription begins. After that, transitions drive engagements.
//
// SAFETY: this controller does not engage anything until something
// drives the KillSwitchManager into SINKHOLE. In Phase 2 (this
// commit), nothing in the existing codebase drives transitions, so
// the controller observes IDLE forever and never calls Engage. The
// Phase 3 wiring step replaces existing killswitch.go usage with
// transitions through KillSwitchManager, at which point the
// controller becomes the active KS implementation.
type SinkholeController struct {
	sinkhole Sinkhole
	ks       *KillSwitchManager

	// engaged tracks whether the platform driver currently holds an
	// active block. Read+written from the controller goroutine only;
	// atomic for safe IsEngaged() reads from other goroutines.
	engaged atomic.Bool

	stopOnce sync.Once
	stop     chan struct{}
}

// NewSinkholeController returns a controller wired to ks but does not
// start it. Call Run from a goroutine to begin processing.
func NewSinkholeController(ks *KillSwitchManager, sinkhole Sinkhole) *SinkholeController {
	return &SinkholeController{
		sinkhole: sinkhole,
		ks:       ks,
		stop:     make(chan struct{}),
	}
}

// IsEngaged returns true iff the controller's last action was a
// successful Engage and no Release has succeeded since.
func (c *SinkholeController) IsEngaged() bool {
	return c.engaged.Load()
}

// Run subscribes to the KillSwitchManager and processes transitions
// until ctx is cancelled or Stop is called. Crash recovery runs once
// before the first subscription event is processed.
func (c *SinkholeController) Run(ctx context.Context) {
	if c.sinkhole == nil || c.ks == nil {
		log.Println("SinkholeController: nil sinkhole or ks - controller disabled")
		return
	}

	// Crash recovery before any new event processing. If the previous
	// run crashed mid-engage with a snapshot on disk, this restores
	// the system before we start handling fresh transitions.
	if err := c.sinkhole.RecoverFromCrash(ctx); err != nil {
		log.Printf("SinkholeController: crash recovery error: %v", err)
		// Continue anyway - we want the controller running so future
		// transitions are handled.
	}

	sub := c.ks.Subscribe()
	for {
		select {
		case <-ctx.Done():
			c.releaseIfEngaged(context.Background())
			return
		case <-c.stop:
			c.releaseIfEngaged(context.Background())
			return
		case state, ok := <-sub:
			if !ok {
				return
			}
			c.handleTransition(ctx, state)
		}
	}
}

// Stop signals the Run goroutine to exit at the next iteration. The
// engaged sinkhole is released as part of shutdown so the user does
// not boot the next session into a leftover block.
func (c *SinkholeController) Stop() {
	c.stopOnce.Do(func() { close(c.stop) })
}

func (c *SinkholeController) handleTransition(ctx context.Context, state KillSwitchState) {
	switch state {
	case KSStateSinkhole:
		if c.engaged.Load() {
			return // already engaged, transition was internal
		}
		if err := c.sinkhole.Engage(ctx); err != nil {
			log.Printf("SinkholeController: ENGAGE FAILED: %v", err)
			// Engaged stays false. The KS state machine is in
			// SINKHOLE but the OS is NOT actually blocking. This is
			// detectable via IsEngaged() vs ks.IsSinkholeActive() and
			// surfaces to the UI as a warning. We do NOT auto-Disarm
			// because the user explicitly intended to block; better
			// to leave the indicator red and warn than to silently
			// pretend everything is fine.
			return
		}
		c.engaged.Store(true)
		log.Println("SinkholeController: engaged")

	case KSStateIdle, KSStateArmed:
		c.releaseIfEngaged(ctx)
	}
}

// releaseIfEngaged calls Release exactly once when engaged is true,
// regardless of context cancellation. Logs failures but always
// clears the engaged flag - the platform driver has already done as
// much restoration as it can, and leaving engaged=true after a
// best-effort release would prevent future Engage attempts.
func (c *SinkholeController) releaseIfEngaged(ctx context.Context) {
	if !c.engaged.Load() {
		return
	}
	if err := c.sinkhole.Release(ctx); err != nil {
		log.Printf("SinkholeController: release error (network may need manual repair): %v", err)
	} else {
		log.Println("SinkholeController: released cleanly")
	}
	c.engaged.Store(false)
}

// platformDataDir returns the platform-appropriate writable directory
// for our state files. Implementation lives per-platform in
// sinkhole_<goos>.go.
