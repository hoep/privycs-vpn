//go:build darwin

package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"runtime"
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

// Anchor name "com.privycs/sinkhole" is now defined in the helper
// (see sinkholeMacOSEngage in privileged_helper.go) - the unprivileged
// driver does not need to know it any more, the helper IPC carries
// only the action name.

type darwinSinkhole struct{}

// NewDarwinSinkhole returns the macOS platform driver.
func NewDarwinSinkhole() Sinkhole {
	return &darwinSinkhole{}
}

// NewPlatformSinkhole is the build-tag-resolved factory app.go uses.
func NewPlatformSinkhole() Sinkhole { return NewDarwinSinkhole() }

func (s *darwinSinkhole) Engage(ctx context.Context) error {
	log.Println("Sinkhole(darwin): engaging via privileged helper")

	snap := &SinkholeSnapshot{
		Version:   1,
		EngagedAt: time.Now(),
		Platform:  runtime.GOOS,
		Reason:    "kill switch sinkhole engaged",
	}
	if err := SaveSnapshot(snap); err != nil {
		return fmt.Errorf("snapshot save: %w", err)
	}

	// pfctl requires root. Helper runs as root via LaunchDaemon.
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

	log.Println("Sinkhole(darwin): engaged successfully via helper")
	return nil
}

func (s *darwinSinkhole) Release(ctx context.Context) error {
	log.Println("Sinkhole(darwin): releasing via privileged helper")

	client := NewHelperClient()
	if client.IsHelperReachable() {
		if resp, err := client.SendCommand("sinkhole_release", nil); err != nil {
			log.Printf("Sinkhole(darwin): helper IPC release failed: %v", err)
		} else if !resp.Success {
			log.Printf("Sinkhole(darwin): helper release reported failure: %s", resp.Error)
		}
	} else {
		log.Println("Sinkhole(darwin): helper unreachable - cannot flush pf anchor from unprivileged process")
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
