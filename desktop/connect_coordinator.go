package main

import (
	"context"
	"log"
	"sync"
	"time"
)

// ConnectCoordinator is the Go port of the Android ConnectCoordinator.kt.
// It serializes ALL connect/disconnect intents (UI tap, On-Demand,
// tray, boot, Always-On respawn equivalent) so they cannot race each
// other - the multi-tunnel /dev/tun collision class on Android, and
// the multi-WireGuardTunnel-service collision class on Windows.
//
// SCOPE - what this file IS:
//   - Pure state machine + mutex + watchdog.
//   - Centralizes connect-attempt gating (Kill Switch sinkhole, pause
//     state, in-flight conflict).
//   - Holds NO references to actual VPN protocol implementations -
//     callers feed it a `connectFn` and `disconnectFn` callback that
//     fire only after the gate has accepted the intent.
//
// SCOPE - what this file IS NOT:
//   - Does NOT call into protocol code directly. The fire callbacks
//     are owned by app.go, which knows which protocol to use.
//   - Does NOT touch network state. State machine purely.
//
// Phase 1 sets this up parallel to the existing ad-hoc connect path
// in app.go without wiring it in. Phase 3 (UI) and Phase 4 (COD)
// will route their intents through here, so by the time a connect
// from a network event races with a connect from the user's tap,
// they both go through the same mutex and the slower one becomes a
// no-op.
type ConnectCoordinator struct {
	mu          sync.Mutex
	state       CoordState
	source      IntentSource
	connID      string
	sinceMs     int64
	connectedAt time.Time

	// Injected hooks (set via SetHooks). Coordinator never instantiates
	// these itself - app.go owns them and shares pointers.
	killSwitch   *KillSwitchManager
	pauseManager *PauseManager

	// Callbacks fire OUTSIDE the mutex so a slow protocol Up/Down
	// cannot block another goroutine asking for state. Coordinator
	// remembers the latest watchdog cancel so a fresh transition can
	// preempt the prior one's timeout.
	watchdogCancel context.CancelFunc

	// Watchdog timeouts mirror Android (90s for connect, 5s for
	// disconnect). Connect can include slow IPSec credential install
	// + WG service start; disconnect is just teardown.
	connectTimeout    time.Duration
	disconnectTimeout time.Duration
}

// CoordState mirrors Android State sealed class.
type CoordState int

const (
	CoordIdle          CoordState = 0
	CoordConnecting    CoordState = 1
	CoordConnected     CoordState = 2
	CoordDisconnecting CoordState = 3
)

func (s CoordState) String() string {
	switch s {
	case CoordIdle:
		return "Idle"
	case CoordConnecting:
		return "Connecting"
	case CoordConnected:
		return "Connected"
	case CoordDisconnecting:
		return "Disconnecting"
	default:
		return "Unknown"
	}
}

// IntentSource mirrors Android IntentSource enum. The desktop set
// is smaller - we have no widget, no tile (yet), no Always-On, but
// we keep TRAY and BOOT for symmetry with planned features.
type IntentSource int

const (
	SourceUser     IntentSource = 0
	SourceOnDemand IntentSource = 1
	SourceBoot     IntentSource = 2
	SourceTray     IntentSource = 3
)

func (s IntentSource) String() string {
	switch s {
	case SourceUser:
		return "USER"
	case SourceOnDemand:
		return "ON_DEMAND"
	case SourceBoot:
		return "BOOT"
	case SourceTray:
		return "TRAY"
	default:
		return "UNKNOWN"
	}
}

// CoordResult mirrors Android Result sealed class.
type CoordResult int

const (
	ResultAccepted             CoordResult = 0
	ResultAlreadyConnected     CoordResult = 1
	ResultAlreadyIdle          CoordResult = 2
	ResultAlreadyConnecting    CoordResult = 3
	ResultAlreadyDisconnecting CoordResult = 4
	ResultGated                CoordResult = 5
)

func (r CoordResult) String() string {
	switch r {
	case ResultAccepted:
		return "Accepted"
	case ResultAlreadyConnected:
		return "AlreadyConnected"
	case ResultAlreadyIdle:
		return "AlreadyIdle"
	case ResultAlreadyConnecting:
		return "AlreadyConnecting"
	case ResultAlreadyDisconnecting:
		return "AlreadyDisconnecting"
	case ResultGated:
		return "Gated"
	default:
		return "Unknown"
	}
}

// NewConnectCoordinator returns a coordinator in Idle state with
// default watchdog timeouts (90s connect, 5s disconnect, mirroring
// Android).
func NewConnectCoordinator() *ConnectCoordinator {
	return &ConnectCoordinator{
		connectTimeout:    90 * time.Second,
		disconnectTimeout: 5 * time.Second,
	}
}

// SetHooks injects the optional KillSwitchManager and PauseManager
// gates. Either may be nil - in that case the corresponding gate
// always accepts. App-level wiring sets both during startup.
func (c *ConnectCoordinator) SetHooks(ks *KillSwitchManager, p *PauseManager) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.killSwitch = ks
	c.pauseManager = p
}

// State returns a snapshot of the current state. Useful for the UI
// to render "Connecting..." / "Connected" indicators.
func (c *ConnectCoordinator) State() CoordState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// IsBusy returns true iff a connect or disconnect transition is in
// flight (Connecting or Disconnecting). Callers can use this to
// short-circuit redundant intent firing.
func (c *ConnectCoordinator) IsBusy() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state == CoordConnecting || c.state == CoordDisconnecting
}

// IsConnected returns true iff the coordinator has observed
// MarkConnected for the active intent.
func (c *ConnectCoordinator) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state == CoordConnected
}

// RequestConnect is the gate for every connect intent on the
// platform. Mirrors Android requestConnect. Returns the disposition
// (Accepted / gated / no-op) so the caller can surface UI feedback.
//
// Gates evaluated in order:
//
//  0. Hardcore Kill Switch lock - if SINKHOLE is engaged, NO source
//     can release the lock. The user must toggle KS off.
//  1. Pause (non-USER sources only) - user said "leave me alone".
//  2. State preconditions - already connected, already connecting,
//     in disconnecting transition.
//
// If accepted, the state moves to Connecting and the watchdog timer
// arms. The caller is then responsible for actually firing the
// protocol Up; on success it must call MarkConnected, on failure or
// teardown MarkDisconnected. The watchdog forces back to Idle if
// neither happens within connectTimeout.
func (c *ConnectCoordinator) RequestConnect(source IntentSource, connID string) CoordResult {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Gate 0: Kill Switch sinkhole.
	if c.killSwitch != nil && c.killSwitch.IsSinkholeActive() {
		log.Printf("ConnectCoordinator: requestConnect(%s) refused: sinkhole active - user must toggle KS off", source)
		return ResultGated
	}

	// Gate 1: pause active for non-USER sources.
	if c.pauseManager != nil && c.pauseManager.IsPaused() && source != SourceUser {
		log.Printf("ConnectCoordinator: requestConnect(%s) refused: pause active", source)
		return ResultGated
	}

	switch c.state {
	case CoordConnected:
		return ResultAlreadyConnected

	case CoordConnecting:
		// USER preempts automated sources.
		if source == SourceUser && c.source != SourceUser {
			log.Printf("ConnectCoordinator: requestConnect(USER) preempting %s", c.source)
			c.toConnectingLocked(source, connID)
			return ResultAccepted
		}
		return ResultAlreadyConnecting

	case CoordDisconnecting:
		// Don't queue; let caller retry once disconnect settles.
		return ResultGated

	case CoordIdle:
		log.Printf("ConnectCoordinator: requestConnect(%s) accepted -> Connecting", source)
		c.toConnectingLocked(source, connID)
		return ResultAccepted
	}
	return ResultGated
}

// RequestDisconnect is the gate for every disconnect intent. Mirrors
// Android requestDisconnect.
//
// Kill Switch interaction:
//   - If KS is enabled at disconnect time, leave the manager in ARMED
//     state so the subsequent connected->disconnected transition (or
//     the explicit ForceSinkhole call from the disconnect path)
//     engages the sinkhole.
//   - If KS is disabled, Disarm here so no sinkhole engages on the
//     clean user-initiated disconnect.
//
// The actual sinkhole engagement happens in Phase 2 platform code -
// this Phase 1 file only manages state, never touches firewalls.
func (c *ConnectCoordinator) RequestDisconnect(source IntentSource, killSwitchEnabled bool) CoordResult {
	c.mu.Lock()
	defer c.mu.Unlock()

	switch c.state {
	case CoordIdle:
		return ResultAlreadyIdle
	case CoordDisconnecting:
		return ResultAlreadyDisconnecting
	case CoordConnecting, CoordConnected:
		log.Printf("ConnectCoordinator: requestDisconnect(%s) accepted -> Disconnecting", source)

		// Disarm the KS only when the user has it disabled. With KS
		// enabled the manager stays ARMED until the disconnect path
		// either organically engages the sinkhole or (Phase 2) the
		// platform sinkhole code forces it.
		if !killSwitchEnabled && c.killSwitch != nil {
			c.killSwitch.Disarm()
		}

		c.toDisconnectingLocked()
		return ResultAccepted
	}
	return ResultGated
}

// MarkConnected is called by the protocol-up code after the OS has
// reported the tunnel is alive (WireGuard service running, OpenVPN
// management socket open, IPSec rasdial Connected). Transitions
// Connecting -> Connected and cancels the connect watchdog.
//
// Also fires KillSwitchManager.Arm() when KS is enabled - this is
// the equivalent of Android's defensive arm-on-every-connected-tick.
// Idempotent on already-armed.
func (c *ConnectCoordinator) MarkConnected(killSwitchEnabled bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.state != CoordConnecting {
		log.Printf("ConnectCoordinator: markConnected ignored: state is %s", c.state)
		return
	}

	c.state = CoordConnected
	c.connectedAt = time.Now()
	c.cancelWatchdogLocked()
	log.Printf("ConnectCoordinator: markConnected (connID=%s)", c.connID)

	// Arm the kill switch on a successful connect-while-enabled. This
	// is the path that takes IDLE -> ARMED, or SINKHOLE -> ARMED on
	// reconnect-out-of-sinkhole.
	if killSwitchEnabled && c.killSwitch != nil {
		// Use State() not IsArmed() - the latter returns true for
		// SINKHOLE, which would skip the SINKHOLE -> ARMED transition
		// (Android v0.9.10.5 bug fix).
		if c.killSwitch.State() != KSStateArmed {
			c.killSwitch.Arm()
		}
	}
}

// MarkDisconnected is called by the protocol-down code after the OS
// has reported the tunnel torn down. Transitions any non-Idle state
// to Idle and cancels the disconnect watchdog.
func (c *ConnectCoordinator) MarkDisconnected() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.state == CoordIdle {
		return
	}
	log.Printf("ConnectCoordinator: markDisconnected (was %s)", c.state)
	c.state = CoordIdle
	c.connID = ""
	c.connectedAt = time.Time{}
	c.cancelWatchdogLocked()
}

// toConnectingLocked moves to Connecting and arms a watchdog. The
// caller MUST already hold c.mu.
func (c *ConnectCoordinator) toConnectingLocked(source IntentSource, connID string) {
	c.cancelWatchdogLocked()
	c.state = CoordConnecting
	c.source = source
	c.connID = connID
	c.sinceMs = time.Now().UnixMilli()
	c.armWatchdogLocked(c.connectTimeout, "connect")
}

// toDisconnectingLocked moves to Disconnecting and arms a watchdog.
// The caller MUST already hold c.mu.
func (c *ConnectCoordinator) toDisconnectingLocked() {
	c.cancelWatchdogLocked()
	c.state = CoordDisconnecting
	c.sinceMs = time.Now().UnixMilli()
	c.armWatchdogLocked(c.disconnectTimeout, "disconnect")
}

// armWatchdogLocked launches a goroutine that waits `timeout` and
// then forces state back to Idle if still in transition. Caller MUST
// already hold c.mu.
func (c *ConnectCoordinator) armWatchdogLocked(timeout time.Duration, label string) {
	ctx, cancel := context.WithCancel(context.Background())
	c.watchdogCancel = cancel
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(timeout):
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		// Only fire if we're still in the same transition we armed for.
		if c.state == CoordConnecting || c.state == CoordDisconnecting {
			log.Printf("ConnectCoordinator: %s watchdog fired - forcing back to Idle", label)
			c.state = CoordIdle
			c.connID = ""
		}
	}()
}

// cancelWatchdogLocked cancels any in-flight watchdog goroutine.
// Caller MUST already hold c.mu.
func (c *ConnectCoordinator) cancelWatchdogLocked() {
	if c.watchdogCancel != nil {
		c.watchdogCancel()
		c.watchdogCancel = nil
	}
}
