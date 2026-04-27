package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"time"
)

// Pre-warm reachability probe for pool members. Goal: catch a dead
// VPN endpoint 60 s before the rotation tick would have switched to
// it, so the rotator can pick a different member silently.
//
// Tradeoff space (see decision in v0.9.11.33 commit):
//   * ICMP ping: needs admin on Windows, often blocked by VPN providers.
//   * Real WireGuard handshake: 200+ LoC of crypto. Overkill for a
//     pre-warm hint - the rotation-time handshake check (B) catches
//     these cases definitively.
//   * TCP-Dial to a related port: cheap, cross-platform, no privileges.
//     False-negative rate (provider blocks both :443 and :80) is
//     acceptable - pre-warm is a HINT, not authoritative.
//
// Strategy: DNS resolve, then sequential TCP-Dial against :443 and :80
// with short timeouts. If DNS fails the host is genuinely unreachable.
// If both TCP probes fail we report "suspect" rather than "dead" via
// the returned error - caller decides how to weight that.
//
// Total worst-case latency: 2 s DNS + 1 s :443 + 1 s :80 = ~4 s. Pre-
// warm fires 60 s ahead of rotation so even three sequential probes
// of three members fit comfortably (~12 s).

const (
	probeDNSTimeout = 2 * time.Second
	probeTCPTimeout = 1 * time.Second
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

	// Step 1: DNS resolve. Bare IPs short-circuit (LookupHost is fast
	// for them but we save a syscall). DNS failure is definitive -
	// the member cannot be reached.
	if ip := net.ParseIP(host); ip == nil {
		ctx, cancel := context.WithTimeout(context.Background(), probeDNSTimeout)
		defer cancel()
		ips, err := net.DefaultResolver.LookupHost(ctx, host)
		if err != nil {
			return fmt.Errorf("probe: dns %s: %w", host, err)
		}
		if len(ips) == 0 {
			return fmt.Errorf("probe: dns %s: no addresses", host)
		}
	}

	// Step 2: TCP-Dial common service ports. Either succeeding means
	// the host is alive at L3/L4; we cannot tell if the VPN daemon
	// itself is healthy from here, but the rotation-time handshake
	// check (B) catches that.
	if err := dialOK(host, "443"); err == nil {
		return nil
	}
	if err := dialOK(host, "80"); err == nil {
		return nil
	}

	return fmt.Errorf("probe: %s unreachable on tcp:443 and tcp:80", host)
}

// dialOK attempts a TCP connect with the probe timeout and closes
// immediately on success. Returns nil if the dial succeeded.
func dialOK(host, port string) error {
	d := net.Dialer{Timeout: probeTCPTimeout}
	conn, err := d.Dial("tcp", net.JoinHostPort(host, port))
	if err != nil {
		return err
	}
	_ = conn.Close()
	return nil
}

// markMemberUnreachable flips the unreachable bit and timestamps it.
// Idempotent - calling twice has no extra effect besides updating the
// timestamp. Persistence is best-effort: if the registry write fails
// we still keep the in-memory flag, so the current rotation cycle's
// retry loop sees the bad member excluded.
func (a *App) markMemberUnreachable(pool *Pool, m *PoolMember, reason string) {
	if m == nil || pool == nil {
		return
	}
	m.Unreachable = true
	m.LastUnreachable = time.Now()
	if reason != "" {
		m.LastError = reason
	}
	if err := a.pools.Update(pool); err != nil {
		log.Printf("Pool %s: persist Unreachable=true on %s failed: %v", pool.Name, m.Name, err)
	}
}
