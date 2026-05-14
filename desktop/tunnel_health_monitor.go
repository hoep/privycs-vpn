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
// TunnelHealthState is the visible health state pushed to the
// frontend via Wails events. Mirrors Android's TunnelHealthMonitor
// .State enum so cross-platform users get identical UI semantics.
type TunnelHealthState string

const (
	TunnelHealthInactive   TunnelHealthState = "inactive"
	TunnelHealthHealthy    TunnelHealthState = "healthy"
	TunnelHealthDegraded   TunnelHealthState = "degraded"
	TunnelHealthRecovering TunnelHealthState = "recovering"
)

type TunnelHealthMonitor struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	target string
	state  TunnelHealthState
	// onStateChange fires on every transition. App wires this
	// to wailsRuntime.EventsEmit("tunnelHealth:state", state) so
	// the Vue ConnectionView traffic-light pill updates live.
	onStateChange func(TunnelHealthState)
	// v0.9.15.30: settings-driven probe cadence. Populated by
	// Start() from AppSettings.TunnelHealthPingIntervalSec /
	// AppSettings.TunnelHealthDeadThreshold. Falls back to the
	// const defaults below if the settings struct is zero-valued.
	intervalSec   int
	deadThreshold int
}

const (
	// v0.9.15.30: tuned for fast server-down failover.
	// 5 s interval × 2 consecutive fails = max 10 s detection vs
	// the previous 60 s × 3 = 3 min. User reported the 3 min lag
	// as unacceptable for the multi-config failover flow. The 2x
	// threshold (vs 1) keeps a single transient ICMP drop on a
	// flaky mobile network from triggering a spurious failover.
	// 12 ICMP probes/min ≈ 1 KB/min ≈ 1.4 MB/day — negligible
	// data cost; battery cost ≈ +2-3 % vs the 60 s baseline.
	tunnelHealthPingIntervalSec = 5
	tunnelHealthPingTimeoutSec  = 2
	tunnelHealthDeadThreshold   = 2
	tunnelHealthRecoveryGrace   = 30 * time.Second
	tunnelHealthDefaultTarget   = "1.1.1.1"
)

// NewTunnelHealthMonitor creates a stopped monitor. Caller must
// invoke Start() to begin pinging.
func NewTunnelHealthMonitor() *TunnelHealthMonitor {
	return &TunnelHealthMonitor{
		target: tunnelHealthDefaultTarget,
		state:  TunnelHealthInactive,
	}
}

// SetOnStateChange registers the state-transition callback. Called
// once at app startup so the App can forward states to the Vue
// frontend via Wails events.
func (m *TunnelHealthMonitor) SetOnStateChange(fn func(TunnelHealthState)) {
	m.mu.Lock()
	m.onStateChange = fn
	m.mu.Unlock()
}

// State returns the current health state. Used by the Wails
// GetTunnelHealthState method so the frontend can synchronise on
// startup or after a panel re-render.
func (m *TunnelHealthMonitor) State() TunnelHealthState {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state
}

func (m *TunnelHealthMonitor) setState(s TunnelHealthState) {
	m.mu.Lock()
	if m.state == s {
		m.mu.Unlock()
		return
	}
	m.state = s
	cb := m.onStateChange
	m.mu.Unlock()
	if cb != nil {
		// Detach so a slow Wails emit can't block the ping loop.
		go cb(s)
	}
}

// Start begins the ping loop. Idempotent: a running loop is
// cancelled and replaced. onDead is fired on the IO scope after
// `tunnelHealthDeadThreshold` consecutive failures - typically
// app.disconnectInternal so the post-disconnect path drives
// recovery (COD reconnect, pool keepalive, or stays-down for
// non-COD users).
func (m *TunnelHealthMonitor) Start(target string, intervalSec, deadThreshold int, onDead func()) {
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
	// v0.9.15.30: settings-driven overrides, fall back to the
	// module constants if the caller passed zero (e.g. legacy
	// settings.json pre-fillTunnelHealthDefaults migration).
	if intervalSec <= 0 {
		intervalSec = tunnelHealthPingIntervalSec
	}
	if deadThreshold <= 0 {
		deadThreshold = tunnelHealthDeadThreshold
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.target = target
	m.intervalSec = intervalSec
	m.deadThreshold = deadThreshold
	m.mu.Unlock()

	log.Printf("TunnelHealth: starting (target=%s, interval=%ds, dead=%d)",
		target, intervalSec, deadThreshold)

	// Start in HEALTHY-by-assumption: tunnel just came up. The
	// first ping at T+60s either confirms or flips to DEGRADED.
	m.setState(TunnelHealthHealthy)

	// Defensive force-emit: setState short-circuits when the
	// state value is unchanged (e.g., Start called twice in a row
	// without intervening Stop, or when Start was already Healthy
	// because of a previous run that never went Inactive). The
	// frontend's Vue ConnectionView listens for tunnelHealth:state
	// events; without a guaranteed emit on every Start call, a
	// re-mount of ConnectionView could miss the current state. Force
	// re-fire here to guarantee the frontend sees the active state.
	// Duplicate emits with the same value are harmless — the Vue
	// store sets the same string value, no UI re-render churn.
	// v0.9.14.2 hardening.
	m.mu.Lock()
	cb := m.onStateChange
	curState := m.state
	m.mu.Unlock()
	if cb != nil {
		go cb(curState)
	}

	go m.run(ctx, target, onDead)
}

// Stop ends the ping loop. Idempotent.
func (m *TunnelHealthMonitor) Stop() {
	m.mu.Lock()
	cancel := m.cancel
	m.cancel = nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	m.setState(TunnelHealthInactive)
}

func (m *TunnelHealthMonitor) run(ctx context.Context, target string, onDead func()) {
	m.mu.Lock()
	intervalSec := m.intervalSec
	deadThreshold := m.deadThreshold
	m.mu.Unlock()
	if intervalSec <= 0 {
		intervalSec = tunnelHealthPingIntervalSec
	}
	if deadThreshold <= 0 {
		deadThreshold = tunnelHealthDeadThreshold
	}
	ticker := time.NewTicker(time.Duration(intervalSec) * time.Second)
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
				m.setState(TunnelHealthHealthy)
				continue
			}
			failures++
			log.Printf("TunnelHealth: ping to %s failed (%d/%d)",
				target, failures, deadThreshold)
			if failures >= deadThreshold {
				log.Printf("TunnelHealth: tunnel dead, triggering recovery")
				failures = 0
				m.setState(TunnelHealthRecovering)
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
			} else {
				m.setState(TunnelHealthDegraded)
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
		// execHiddenContext applies CREATE_NO_WINDOW on Windows so
		// the ping subprocess does not pop a visible console
		// window — without it, every tunnel-health probe (every
		// ~60s on a connected pool) flashed a black box on screen.
		// Plain passthrough to exec.CommandContext on non-Windows.
		cmd = execHiddenContext(ctx, "ping", "-n", "1", "-w",
			strconv.Itoa(timeoutSec*1000), target)
	default:
		// Linux/macOS: -c 1 count, -W timeout in seconds.
		cmd = execHiddenContext(ctx, "ping", "-c", "1", "-W",
			strconv.Itoa(timeoutSec), target)
	}
	err := cmd.Run()
	return err == nil
}
