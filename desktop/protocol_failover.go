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
	candidates := conn.OrderedConfigs()
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

		setTunnelName(proto, sanitizeTunnelName(conn.Name))
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
