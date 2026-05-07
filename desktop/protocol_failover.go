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

// tryFailoverProtocol cycles through the active connection's AvailableProtocols
// (excluding `excludeOriginal` and any other protocol that already failed in
// this attempt) and tries each. Returns the protocol name that succeeded, or
// "" + error if all candidates failed.
//
// MUST be called while holding a.mu.
//
// Caller responsibilities:
//   - The original protocol is already torn down (callers come from Up()
//     failure or tunnel-dead recovery, both of which leave the previous
//     protocol's Up() either failed or the tunnel deliberately disconnected).
//   - A.connected is set correctly on entry (false). We do not toggle it on
//     failure, only on success.
func (a *App) tryFailoverProtocol(excludeOriginal string) (string, error) {
	conn := a.connections.Active()
	if conn == nil {
		return "", fmt.Errorf("failover: no active connection")
	}
	candidates := conn.AvailableProtocols()
	if len(candidates) <= 1 {
		// Only one (or zero) protocol configured for this connection.
		// No candidate to switch to.
		return "", fmt.Errorf("failover: connection %q has no alternate protocol configured", conn.Name)
	}

	tried := []string{}
	for _, candidate := range candidates {
		if candidate == excludeOriginal {
			continue
		}
		proto, ok := a.protocols[candidate]
		if !ok {
			continue
		}
		if !proto.IsAvailable() {
			log.Printf("Failover: skip %q — not available on this system", candidate)
			tried = append(tried, candidate+"(unavailable)")
			continue
		}
		cfg := conn.GetProtocol(candidate)
		if cfg == nil || cfg.ConfigContent == "" {
			log.Printf("Failover: skip %q — no config payload", candidate)
			tried = append(tried, candidate+"(no-config)")
			continue
		}

		log.Printf("Failover: trying %q (excluded %q, prior tried: %v)", candidate, excludeOriginal, tried)
		wailsRuntime.EventsEmit(a.ctx, "vpn:failover", candidate)

		setTunnelName(proto, sanitizeTunnelName(conn.Name))
		if err := proto.Configure(a.applyDnsOverride([]byte(cfg.ConfigContent), proto.Name())); err != nil {
			log.Printf("Failover: configure %q failed: %v", candidate, err)
			tried = append(tried, candidate+"(configure-err)")
			continue
		}

		// Up with a per-protocol watchdog.
		upCtx, cancel := context.WithTimeout(context.Background(), failoverWatchdog)
		err := proto.Up(upCtx)
		cancel()
		if err != nil {
			log.Printf("Failover: Up(%q) failed: %v", candidate, err)
			// Best-effort cleanup so the next iteration starts clean.
			downCtx, downCancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = proto.Down(downCtx)
			downCancel()
			tried = append(tried, candidate+"(up-err)")
			continue
		}

		// Verify the protocol actually thinks it's connected. Some
		// protocol Up() returns nil before the kernel side has finished
		// (e.g. WireGuard service start on Windows can succeed-then-die
		// with a stale wintun fd). Poll Status() briefly to catch that.
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
			log.Printf("Failover: %q Up() returned nil but Status().Connected stayed false", candidate)
			downCtx, downCancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = proto.Down(downCtx)
			downCancel()
			tried = append(tried, candidate+"(status-not-connected)")
			continue
		}

		// Success — commit the new active protocol.
		conn.ActiveProtocol = candidate
		a.activeProtocol = candidate
		a.settings.ActiveProtocol = candidate
		a.connections.Save()
		SaveSettings(a.settings)
		a.connected = true
		if a.connectedAt.IsZero() {
			a.connectedAt = time.Now()
		}
		log.Printf("Failover: SUCCESS via %q (after trying %s)", candidate, strings.Join(tried, ", "))
		wailsRuntime.EventsEmit(a.ctx, "vpn:failover_success", candidate)
		return candidate, nil
	}

	wailsRuntime.EventsEmit(a.ctx, "vpn:failover_exhausted", strings.Join(tried, ","))
	return "", fmt.Errorf("failover: all alternate protocols failed (tried: %s)", strings.Join(tried, ", "))
}
