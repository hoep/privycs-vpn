package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os/exec"
	"runtime"
	"time"
)

// Pre-warm reachability probe for pool members. Goal: catch a dead
// VPN endpoint 60 s before the rotation tick would have switched to
// it, so the rotator can pick a different member silently.
//
// History: v0.9.11.33-36 included a TCP-Dial against :443 and :80 as
// a second-stage probe. This produced a class of FALSE POSITIVES on
// dedicated VPN servers (the typical commercial provider's WireGuard
// fleet runs only the wg UDP socket and nothing else - no web server,
// no SSH, nothing on TCP). Those servers were wrongly flagged dead,
// shrinking the pool's effective size until the 30-min TTL cleared
// them. Reported by user against a Mullvad-style provider archive.
//
// v0.9.11.37 simplified to DNS-only. Reasoning:
//   * DNS failure is unambiguous: the hostname does not resolve.
//   * DNS success says "this host could exist". Whether the wg
//     daemon on the (UDP-only) endpoint is healthy cannot be tested
//     in user-mode without sending a real WireGuard handshake (~200
//     LoC of crypto) or having raw-socket privileges. The actual
//     authoritative health check happens at rotation time anyway:
//       Layer A retries on Up()-failure
//       Layer B verifies via packet-trigger + bytes_rx after Up()
//   * Pre-warm is a HINT, not authoritative. False-positives in the
//     hint hurt more than the marginal benefit of a successful TCP
//     probe.
//
// Worst-case latency now: 2 s DNS only (was 4 s before).

const (
	probeDNSTimeout = 2 * time.Second
)

// probeMember tests whether the member's endpoint is likely reachable.
// Returns nil on probable-reachable, an error describing the failure
// otherwise. Cheap and side-effect-free - safe to call from a
// goroutine without coordination.
func probeMember(m *PoolMember) error {
	if m == nil || m.Config == nil || m.Config.ServerAddress == "" {
		return fmt.Errorf("probe: member has no endpoint")
	}
	host := stripPortIfPresent(m.Config.ServerAddress)
	if host == "" {
		return fmt.Errorf("probe: empty host after strip from %q", m.Config.ServerAddress)
	}

	// DNS resolve. Bare IPs short-circuit (no DNS to do). DNS failure
	// is definitive - the member cannot be reached at any layer.
	if ip := net.ParseIP(host); ip != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeDNSTimeout)
	defer cancel()
	ips, err := net.DefaultResolver.LookupHost(ctx, host)
	if err != nil {
		return fmt.Errorf("probe: dns %s: %w", host, err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("probe: dns %s: no addresses", host)
	}
	return nil
}

// flushOSDNSCache invokes the platform's user-mode DNS cache flush
// after a successful pool rotation. The new tunnel typically routes
// through a different exit IP, which means CDN-resolved hostnames in
// the OS resolver cache (e.g. "cdn.example.com → 1.2.3.4") were
// chosen for the OLD exit's geolocation. After rotation those entries
// are still valid but suboptimal - apps continue to hit the old
// CDN edge instead of one closer to the new exit. Flushing forces
// re-resolution on the next lookup.
//
// Best-effort and silent on failure - a missing/locked-down resolver
// service should not be a rotation-failing condition. Runs as a
// goroutine so the rotation thread is not blocked on the syscall.
func flushOSDNSCache() {
	go func() {
		var cmd *exec.Cmd
		switch runtime.GOOS {
		case "windows":
			cmd = execHidden("ipconfig", "/flushdns")
		case "linux":
			// resolvectl is the systemd-resolved API; falls back
			// silently if not present.
			cmd = exec.Command("resolvectl", "flush-caches")
		case "darwin":
			cmd = exec.Command("dscacheutil", "-flushcache")
		default:
			return
		}
		if cmd == nil {
			return
		}
		if err := cmd.Run(); err != nil {
			log.Printf("Pool: OS DNS cache flush failed: %v", err)
			return
		}
		log.Printf("Pool: OS DNS cache flushed")
	}()
}

// markMemberUnreachable flips the unreachable bit and timestamps it.
// Idempotent - calling twice updates the timestamp (intentional - a
// repeatedly-failing member's TTL effectively resets, so its time
// out of rotation lengthens with continued failures).
//
// Since v0.9.11.39 this is a state.json write (small, debounced) and
// goes through the lock-coordinated state registry. No more direct
// PoolMember pointer mutation.
func (a *App) markMemberUnreachable(pool *Pool, m *PoolMember, reason string) {
	if m == nil || pool == nil {
		return
	}
	a.pools.MarkMemberUnreachable(pool.ID, m.ID, reason)
}
