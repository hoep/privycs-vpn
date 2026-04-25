//go:build linux

package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"runtime"
	"time"
)

// Linux sinkhole. Uses iptables with an isolated chain
// (PRIVYCS_SINKHOLE) so engage and release never touch user-defined
// rules in OUTPUT, FORWARD, or INPUT. The chain holds a single -j
// DROP rule; engage adds a jump from OUTPUT to the chain at position
// 1 (highest priority); release removes the jump and flushes the
// chain.
//
// Why an isolated chain instead of inline DROP rules in OUTPUT:
// inline rules are identified by their match expressions, and
// removing them via "iptables -D OUTPUT -j DROP" is line-position
// dependent if the user has multiple rules. Chains are unambiguous -
// "remove the jump to PRIVYCS_SINKHOLE from OUTPUT" works no matter
// where the user's other rules sit.

func platformDataDir() string {
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return dir + "/privycs-vpn"
	}
	if home, err := os.UserHomeDir(); err == nil {
		return home + "/.local/state/privycs-vpn"
	}
	return "/var/lib/privycs-vpn"
}

// Chain name "PRIVYCS_SINKHOLE" is now defined in the helper
// (see sinkholeLinuxEngage in privileged_helper.go) - the unprivileged
// driver does not need to know it any more, the helper IPC carries
// only the action name.

type linuxSinkhole struct{}

// NewLinuxSinkhole returns the Linux platform driver.
func NewLinuxSinkhole() Sinkhole {
	return &linuxSinkhole{}
}

// NewPlatformSinkhole is the build-tag-resolved factory app.go uses.
func NewPlatformSinkhole() Sinkhole { return NewLinuxSinkhole() }

func (s *linuxSinkhole) Engage(ctx context.Context) error {
	log.Println("Sinkhole(linux): engaging via privileged helper")

	snap := &SinkholeSnapshot{
		Version:   1,
		EngagedAt: time.Now(),
		Platform:  runtime.GOOS,
		Reason:    "kill switch sinkhole engaged",
	}
	if err := SaveSnapshot(snap); err != nil {
		return fmt.Errorf("snapshot save: %w", err)
	}

	// Route through privileged helper - iptables requires root and
	// the Wails app runs as the user. The helper's sinkhole_engage
	// handler does the iptables chain setup with rollback on failure.
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

	log.Println("Sinkhole(linux): engaged successfully via helper")
	return nil
}

func (s *linuxSinkhole) Release(ctx context.Context) error {
	log.Println("Sinkhole(linux): releasing via privileged helper")

	client := NewHelperClient()
	if client.IsHelperReachable() {
		if resp, err := client.SendCommand("sinkhole_release", nil); err != nil {
			log.Printf("Sinkhole(linux): helper IPC release failed: %v", err)
		} else if !resp.Success {
			log.Printf("Sinkhole(linux): helper release reported failure: %s", resp.Error)
		}
	} else {
		log.Println("Sinkhole(linux): helper unreachable - cannot remove iptables rules from unprivileged process")
	}

	if err := DeleteSnapshot(); err != nil {
		log.Printf("Sinkhole(linux): snapshot delete (non-fatal): %v", err)
	}
	log.Println("Sinkhole(linux): released")
	return nil
}

func (s *linuxSinkhole) RecoverFromCrash(ctx context.Context) error {
	if _, err := os.Stat(snapshotPath()); err != nil {
		return nil
	}
	log.Println("Sinkhole(linux): leftover snapshot detected - recovering as Release")
	return s.Release(ctx)
}
