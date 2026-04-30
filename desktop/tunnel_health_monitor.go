package main

import (
	"context"
	"log"
	"os/exec"
	"runtime"
	"strconv"
	"sync"
	"time"
)

// TunnelHealthMonitor periodically pings a known reliable target
// through the active tunnel and triggers a recovery action after
// N consecutive failures.
//
// Closes the "tunnel up but no traffic" gap that protocols other
// than WireGuard cannot detect on their own. WireGuard already has
// handshake / RX-bytes verification at connect time inside
// PoolConnector; the health monitor adds *ongoing* verification
// regardless of protocol.
//
// Started from App.Connect() on success and stopped from
// App.Disconnect(). The recovery callback typically points at
// app.disconnectInternal() so the existing post-disconnect
// re-evaluation path drives reconnect (COD or pool keepalive).
//
// Why ICMP via subprocess: Go's net package has no out-of-the-box
// privileged-ICMP send. The platform ping(8) / ping.exe binary is
// universally available, exits with 0 on success, gives reliable
// signal, and the spawn cost (5-10ms) is negligible at a 60s
// cadence.
type TunnelHealthMonitor struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	target string
}

const (
	tunnelHealthPingIntervalSec = 60
	tunnelHealthPingTimeoutSec  = 2
	tunnelHealthDeadThreshold   = 3
	tunnelHealthRecoveryGrace   = 30 * time.Second
	tunnelHealthDefaultTarget   = "1.1.1.1"
)

// NewTunnelHealthMonitor creates a stopped monitor. Caller must
// invoke Start() to begin pinging.
func NewTunnelHealthMonitor() *TunnelHealthMonitor {
	return &TunnelHealthMonitor{target: tunnelHealthDefaultTarget}
}

// Start begins the ping loop. Idempotent: a running loop is
// cancelled and replaced. onDead is fired on the IO scope after
// `tunnelHealthDeadThreshold` consecutive failures - typically
// app.disconnectInternal so the post-disconnect path drives
// recovery (COD reconnect, pool keepalive, or stays-down for
// non-COD users).
func (m *TunnelHealthMonitor) Start(target string, onDead func()) {
	m.mu.Lock()
	if m.cancel != nil {
		m.cancel()
	}
	if target == "" {
		target = m.target
	}
	if target == "" {
		target = tunnelHealthDefaultTarget
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.target = target
	m.mu.Unlock()

	log.Printf("TunnelHealth: starting (target=%s, interval=%ds, dead=%d)",
		target, tunnelHealthPingIntervalSec, tunnelHealthDeadThreshold)

	go m.run(ctx, target, onDead)
}

// Stop ends the ping loop. Idempotent.
func (m *TunnelHealthMonitor) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
}

func (m *TunnelHealthMonitor) run(ctx context.Context, target string, onDead func()) {
	ticker := time.NewTicker(tunnelHealthPingIntervalSec * time.Second)
	defer ticker.Stop()
	failures := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ok := pingHost(target, tunnelHealthPingTimeoutSec)
			if ok {
				if failures > 0 {
					log.Printf("TunnelHealth: restored after %d fails", failures)
				}
				failures = 0
				continue
			}
			failures++
			log.Printf("TunnelHealth: ping to %s failed (%d/%d)",
				target, failures, tunnelHealthDeadThreshold)
			if failures >= tunnelHealthDeadThreshold {
				log.Printf("TunnelHealth: tunnel dead, triggering recovery")
				failures = 0
				if onDead != nil {
					// Detach the recovery callback so the ping
					// loop is not blocked by a slow disconnect.
					go onDead()
				}
				// Pause counting failures during the
				// disconnect-then-reconnect cycle so we don't
				// stack false-positives while the new tunnel
				// is coming up.
				select {
				case <-ctx.Done():
					return
				case <-time.After(tunnelHealthRecoveryGrace):
				}
			}
		}
	}
}

// pingHost runs the platform-native ping(8) / ping.exe binary with
// a single-shot timeout-bounded probe and returns true on exit-
// code 0. Identical semantics across Linux, macOS, Windows.
//
// Top-level CommandContext timeout is set slightly longer than
// ping's own -W / -w because some misconfigured iputils builds
// ignore -W and we need a hard ceiling to keep the monitor's
// goroutine moving.
func pingHost(target string, timeoutSec int) bool {
	ctx, cancel := context.WithTimeout(context.Background(),
		time.Duration(timeoutSec+1)*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		// -n 1: count, -w timeout in milliseconds (Windows ping
		// is the odd duck with millisecond timeout flag).
		cmd = exec.CommandContext(ctx, "ping", "-n", "1", "-w",
			strconv.Itoa(timeoutSec*1000), target)
	default:
		// Linux/macOS: -c 1 count, -W timeout in seconds.
		cmd = exec.CommandContext(ctx, "ping", "-c", "1", "-W",
			strconv.Itoa(timeoutSec), target)
	}
	err := cmd.Run()
	return err == nil
}
