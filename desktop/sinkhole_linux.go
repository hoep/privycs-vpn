//go:build linux

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

const linuxSinkholeChain = "PRIVYCS_SINKHOLE"

type linuxSinkhole struct{}

// NewLinuxSinkhole returns the Linux platform driver.
func NewLinuxSinkhole() Sinkhole {
	return &linuxSinkhole{}
}

// NewPlatformSinkhole is the build-tag-resolved factory app.go uses.
func NewPlatformSinkhole() Sinkhole { return NewLinuxSinkhole() }

func (s *linuxSinkhole) Engage(ctx context.Context) error {
	log.Println("Sinkhole(linux): engaging")

	snap := &SinkholeSnapshot{
		Version:   1,
		EngagedAt: time.Now(),
		Platform:  runtime.GOOS,
		Reason:    "kill switch sinkhole engaged",
		// On Linux we identify our state by the dedicated chain, not
		// by individual rule names. FirewallRulesAdded stays empty.
	}
	if err := SaveSnapshot(snap); err != nil {
		return fmt.Errorf("snapshot save: %w", err)
	}

	// Defensive cleanup of any leftover state from a prior crash
	// before we add fresh rules. Idempotent: failures here are
	// expected on the first ever engage (chain doesn't exist yet).
	s.cleanupChain(ctx)

	// Sequence: create chain, fill chain with DROP, then jump to
	// chain from OUTPUT. Each step is reversible by cleanupChain
	// if a later step fails.
	steps := [][]string{
		{"iptables", "-N", linuxSinkholeChain},
		{"iptables", "-A", linuxSinkholeChain, "-o", "lo", "-j", "RETURN"},
		{"iptables", "-A", linuxSinkholeChain, "-j", "DROP"},
		{"iptables", "-I", "OUTPUT", "1", "-j", linuxSinkholeChain},
	}
	for i, step := range steps {
		if err := runIptables(ctx, step...); err != nil {
			// Rollback: remove anything we added so far.
			log.Printf("Sinkhole(linux): step %d failed (%v) - rolling back", i, err)
			s.cleanupChain(ctx)
			_ = DeleteSnapshot()
			return fmt.Errorf("engage step %d (%v): %w", i, step, err)
		}
	}

	log.Println("Sinkhole(linux): engaged successfully")
	return nil
}

func (s *linuxSinkhole) Release(ctx context.Context) error {
	log.Println("Sinkhole(linux): releasing")
	s.cleanupChain(ctx)
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

// cleanupChain removes our jump from OUTPUT, flushes our chain, and
// deletes our chain. Safe to call when nothing is set up yet
// (failures are expected and ignored - this is best-effort cleanup).
func (s *linuxSinkhole) cleanupChain(ctx context.Context) {
	// Remove the jump first so traffic resumes ASAP. Order matters:
	// once the jump is gone, the user's network is restored even if
	// the chain flush+delete fail.
	_ = runIptables(ctx, "iptables", "-D", "OUTPUT", "-j", linuxSinkholeChain)
	_ = runIptables(ctx, "iptables", "-F", linuxSinkholeChain)
	_ = runIptables(ctx, "iptables", "-X", linuxSinkholeChain)
}

// runIptables wraps the iptables CLI. Linux desktop builds have not
// historically used a native netlink binding; sticking with the CLI
// keeps cross-distro compatibility (Ubuntu, Fedora, Arch, etc.) and
// avoids C bindings.
func runIptables(ctx context.Context, args ...string) error {
	if len(args) == 0 {
		return fmt.Errorf("runIptables: no args")
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w (output=%q)", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
