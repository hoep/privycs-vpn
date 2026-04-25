//go:build darwin

package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// macOS sinkhole. Uses pf (Packet Filter) anchors. An anchor is a
// scoped ruleset that can be loaded / flushed atomically without
// touching the user's main pf rules. Engage loads a "block all"
// anchor; release flushes the anchor.
//
// Anchor name "com.privycs/sinkhole" stays in the com.privycs/
// namespace so other Privycs subsystems (or future apps) can use
// sibling anchors without conflict.
//
// pf is atomic by design: -f - replaces the entire anchor's
// ruleset in one transaction, and -F all flushes it in one
// transaction. There is no half-applied state to worry about, which
// is why the macOS path is simpler than Windows.

func platformDataDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return home + "/Library/Application Support/Privycs VPN"
	}
	return "/var/lib/privycs-vpn"
}

const darwinSinkholeAnchor = "com.privycs/sinkhole"

type darwinSinkhole struct{}

// NewDarwinSinkhole returns the macOS platform driver.
func NewDarwinSinkhole() Sinkhole {
	return &darwinSinkhole{}
}

// NewPlatformSinkhole is the build-tag-resolved factory app.go uses.
func NewPlatformSinkhole() Sinkhole { return NewDarwinSinkhole() }

func (s *darwinSinkhole) Engage(ctx context.Context) error {
	log.Println("Sinkhole(darwin): engaging")

	snap := &SinkholeSnapshot{
		Version:   1,
		EngagedAt: time.Now(),
		Platform:  runtime.GOOS,
		Reason:    "kill switch sinkhole engaged",
	}
	if err := SaveSnapshot(snap); err != nil {
		return fmt.Errorf("snapshot save: %w", err)
	}

	// Make sure pf is enabled. -E increments the enable reference
	// count; -X decrements. Multiple processes can enable pf without
	// stepping on each other; we balance the reference count in
	// Release.
	if out, err := exec.CommandContext(ctx, "pfctl", "-E").CombinedOutput(); err != nil {
		// pfctl -E exits 0 with status output to stderr. Some
		// versions return non-zero with a "pf enabled" message;
		// treat as success if the output indicates pf is up.
		if !strings.Contains(string(out), "pf enabled") &&
			!strings.Contains(string(out), "Token") {
			_ = DeleteSnapshot()
			return fmt.Errorf("pfctl -E: %w (out=%s)", err, strings.TrimSpace(string(out)))
		}
	}

	// Atomic anchor load. The ruleset blocks all outbound except
	// loopback, mirroring the intent of the Windows + Linux paths.
	rules := strings.Join([]string{
		"set skip on lo0",
		"block out all",
		"block in all",
	}, "\n") + "\n"

	cmd := exec.CommandContext(ctx, "pfctl", "-a", darwinSinkholeAnchor, "-f", "-")
	cmd.Stdin = strings.NewReader(rules)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Anchor may have partial state if the load was interrupted;
		// flush as part of rollback.
		_ = exec.CommandContext(ctx, "pfctl", "-a", darwinSinkholeAnchor, "-F", "all").Run()
		_ = DeleteSnapshot()
		return fmt.Errorf("pfctl anchor load: %w (out=%s)", err, strings.TrimSpace(string(out)))
	}

	log.Println("Sinkhole(darwin): engaged successfully")
	return nil
}

func (s *darwinSinkhole) Release(ctx context.Context) error {
	log.Println("Sinkhole(darwin): releasing")

	// Flush the anchor (atomic). Any rule we put there is gone after
	// this returns successfully.
	if out, err := exec.CommandContext(ctx, "pfctl", "-a", darwinSinkholeAnchor, "-F", "all").CombinedOutput(); err != nil {
		log.Printf("Sinkhole(darwin): anchor flush (non-fatal): %v: %s", err, strings.TrimSpace(string(out)))
	}

	// Balance the -E from Engage. Failures here are non-fatal; pf
	// staying enabled is harmless because there are no rules in our
	// anchor anymore.
	if out, err := exec.CommandContext(ctx, "pfctl", "-X").CombinedOutput(); err != nil {
		log.Printf("Sinkhole(darwin): pfctl -X (non-fatal): %v: %s", err, strings.TrimSpace(string(out)))
	}

	if err := DeleteSnapshot(); err != nil {
		log.Printf("Sinkhole(darwin): snapshot delete (non-fatal): %v", err)
	}

	log.Println("Sinkhole(darwin): released")
	return nil
}

func (s *darwinSinkhole) RecoverFromCrash(ctx context.Context) error {
	if _, err := os.Stat(snapshotPath()); err != nil {
		return nil
	}
	log.Println("Sinkhole(darwin): leftover snapshot detected - recovering as Release")
	return s.Release(ctx)
}
