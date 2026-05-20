package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// ============================================================================
// Multi-protocol failover
// ============================================================================
//
// Walks the active connection's AvailableProtocols and attempts to bring up
// each in turn until one succeeds. Used when:
//
//   1. Initial Up() fails immediately (caller wraps Connect with a fallback).
//   2. tunnel_health_monitor reports the tunnel as dead — instead of blindly
//      reconnecting via the same protocol that just died (the previous
//      behaviour) we try a different protocol first.
//
// Failover treats a "successful" connection as Up() returning nil AND the
// protocol's Status().Connected being true within a short watchdog. We do
// NOT wait for tunnel_health_monitor to confirm reachability — that runs on
// a 60s cadence and would make every failover 60s long. The user can flip
// us back to a known-good state manually via the protocol selector if a
// freshly-connected protocol is silently broken downstream.
//
// Single-protocol connections (only one entry in AvailableProtocols) are a
// no-op: no candidate to switch to. Caller should still see the original
// error.

// failoverWatchdog is the per-protocol time budget for Up() + Status check.
// Keep this comfortably above slowest realistic connect (IPSec credential
// install on macOS, Windows-WG service start) so we don't false-fail a
// protocol that's just slow rather than broken.
const failoverWatchdog = 30 * time.Second

// verifyTunnelAliveBudget caps how long we wait for the first peer-
// liveness signal after a protocol reports Connected=true. Mirrors
// Android PrivycsVpnService.kt's UP_TIMEOUT_MS verify-phase added in
// v0.9.15.20: Status().Connected flips as soon as the daemon/helper
// RPC returns, which is BEFORE any peer round-trip — pre-verify a
// blackholed tunnel (handshake never lands, e.g. Windows AWG with
// broken route install, server obfuscation profile mismatch, etc.)
// sat in "Connected" for the full TunnelHealth probe window
// (60 s × 3 = 3 min) before falling over. With this verify-phase we
// fail fast within 20 s and trigger failover immediately.
const verifyTunnelAliveBudget = 20 * time.Second

// verifyTunnelAlivePoll is the polling cadence used while waiting
// for the first liveness signal. 500 ms matches Android's
// UP_VERIFY_POLL_MS and keeps the syscall cost trivial (Status() is
// a cheap in-memory read on every protocol).
const verifyTunnelAlivePoll = 500 * time.Millisecond

// verifyTunnelAlive polls proto.Status() for evidence of an actual
// peer round-trip — not just "daemon started" but "peer responded".
// Returns true on the first liveness signal:
//   - BytesRx > 0           data has flowed in through the tunnel
//   - LastHandshake non-zero a WG/AWG handshake epoch was recorded
//
// Returns false if the budget elapses without any signal. The
// LocalAddress field is intentionally NOT used as a liveness signal:
// for WG/AWG the inner IP is configured by us at IpcSet time and is
// non-empty even when the peer never responds, so it would give
// false positives in the very blackhole case we're trying to catch.
func verifyTunnelAlive(proto VPNProtocol, budget, poll time.Duration) bool {
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		s := proto.Status()
		if s.BytesRx > 0 {
			return true
		}
		if hs := strings.TrimSpace(s.LastHandshake); hs != "" && hs != "0" {
			return true
		}
		time.Sleep(poll)
	}
	return false
}

// tryFailoverProtocol cycles through the active connection's
// ProtocolConfigs in failover-preference order (OrderedConfigs:
// amneziawg → wireguard → openvpn → ipsec, then insertion order
// within a protocol) and tries each. Multi-config-aware: a
// connection with two WireGuard configs (e.g. UDP and TCP
// endpoints to the same server) walks both as separate candidates.
//
// excludeOriginalConfigID skips a specific failed config-id (not
// the whole protocol type) — so if WG-UDP just died we still try
// WG-TCP as the next candidate, not jump straight to OpenVPN.
//
// Returns the protocol name of the candidate that succeeded, or
// "" + error if all failed. MUST be called while holding a.mu.
func (a *App) tryFailoverProtocol(excludeOriginalConfigID string) (string, error) {
	conn := a.connections.Active()
	if conn == nil {
		return "", fmt.Errorf("failover: no active connection")
	}
	// v0.9.15.70 — read user-configured failover order (default =
	// pre-v0.9.15.70 enum order when empty/nil).
	failoverOrder := a.settings.ProtocolFailoverOrder
	candidates := conn.OrderedConfigsFor(failoverOrder)
	if len(candidates) <= 1 {
		return "", fmt.Errorf("failover: connection %q has no alternate config", conn.Name)
	}

	tried := []string{}
	for _, cfg := range candidates {
		if cfg.ID == excludeOriginalConfigID {
			continue
		}
		// Human-friendly label for logs: "wireguard:Home-UDP" so
		// the failover trail in logs distinguishes two configs
		// of the same protocol type.
		label := cfg.Protocol
		if cfg.Nickname != "" {
			label = fmt.Sprintf("%s:%s", cfg.Protocol, cfg.Nickname)
		} else if cfg.Filename != "" {
			label = fmt.Sprintf("%s:%s", cfg.Protocol, cfg.Filename)
		}

		proto, ok := a.protocols[cfg.Protocol]
		if !ok {
			tried = append(tried, label+"(handler-missing)")
			continue
		}
		if !proto.IsAvailable() {
			log.Printf("Failover: skip %s — handler not available", label)
			tried = append(tried, label+"(unavailable)")
			continue
		}
		if cfg.ConfigContent == "" {
			log.Printf("Failover: skip %s — empty config", label)
			tried = append(tried, label+"(no-config)")
			continue
		}

		log.Printf("Failover: trying %s (excluded id=%s, prior tried: %v)", label, excludeOriginalConfigID, tried)
		wailsRuntime.EventsEmit(a.ctx, "vpn:failover", label)

		setTunnelName(proto, tunnelNameForSlot(cfg.ID, conn.Name))
		if err := proto.Configure(a.applyDnsOverride([]byte(cfg.ConfigContent), proto.Name())); err != nil {
			log.Printf("Failover: configure %s failed: %v", label, err)
			tried = append(tried, label+"(configure-err)")
			continue
		}

		upCtx, cancel := context.WithTimeout(context.Background(), failoverWatchdog)
		err := proto.Up(upCtx)
		cancel()
		if err != nil {
			log.Printf("Failover: Up(%s) failed: %v", label, err)
			downCtx, downCancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = proto.Down(downCtx)
			downCancel()
			tried = append(tried, label+"(up-err)")
			continue
		}

		connected := false
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if proto.Status().Connected {
				connected = true
				break
			}
			time.Sleep(250 * time.Millisecond)
		}
		if !connected {
			log.Printf("Failover: %s Up() returned nil but Status().Connected stayed false", label)
			downCtx, downCancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = proto.Down(downCtx)
			downCancel()
			tried = append(tried, label+"(status-not-connected)")
			continue
		}

		// Liveness verify: Status().Connected only proves the daemon
		// started; the peer may still be unreachable. Wait up to
		// verifyTunnelAliveBudget for actual RxBytes / handshake.
		// Without this an obfuscation-profile mismatch or a broken
		// route-install (the v0.9.15.x Windows-AWG bug) committed
		// failover to a silently-dead protocol.
		if !verifyTunnelAlive(proto, verifyTunnelAliveBudget, verifyTunnelAlivePoll) {
			log.Printf("Failover: %s connected but no peer signal within %v — blackhole, trying next", label, verifyTunnelAliveBudget)
			downCtx, downCancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = proto.Down(downCtx)
			downCancel()
			tried = append(tried, label+"(no-peer-signal)")
			continue
		}

		// Success — pin the new active config.
		conn.ActiveConfigID = cfg.ID
		conn.ActiveProtocol = cfg.Protocol
		a.activeProtocol = cfg.Protocol
		a.settings.ActiveProtocol = cfg.Protocol
		a.connections.Save()
		SaveSettings(a.settings)
		a.connected = true
		if a.connectedAt.IsZero() {
			a.connectedAt = time.Now()
		}
		log.Printf("Failover: SUCCESS via %s (after trying %s)", label, strings.Join(tried, ", "))
		wailsRuntime.EventsEmit(a.ctx, "vpn:failover_success", cfg.Protocol)
		return cfg.Protocol, nil
	}

	wailsRuntime.EventsEmit(a.ctx, "vpn:failover_exhausted", strings.Join(tried, ","))
	return "", fmt.Errorf("failover: all alternate configs failed (tried: %s)", strings.Join(tried, ", "))
}
