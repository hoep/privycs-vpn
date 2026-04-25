package main

import (
	"log"
	"sync"
	"sync/atomic"
)

// KillSwitchManager is a Go port of the Android
// KillSwitchManager.kt state machine. It is the foundation for the
// desktop port of the hardcore Kill Switch behaviour ported from
// Android v0.9.10.5+.
//
// SCOPE - what this file IS:
//   - Pure in-process state machine: IDLE / ARMED / SINKHOLE.
//   - State transitions are serialized via a mutex.
//   - Listener pattern: subscribers receive every transition on a
//     buffered channel. Listeners that don't drain their channel
//     don't block the state machine - dropped events are logged.
//
// SCOPE - what this file IS NOT:
//   - It does NOT touch the firewall, routing table, DNS, or any
//     OS-level network state.
//   - It does NOT install or remove WFP filters, iptables rules,
//     pf anchors, or netsh entries.
//   - It does NOT replace the existing killswitch.go on Windows
//     (which is still the only code that actually engages the
//     OS-level traffic block today).
//
// Phase 2 will add platform-specific sinkhole implementations
// (sinkhole_linux.go, sinkhole_darwin.go, sinkhole_windows.go) that
// SUBSCRIBE to this state machine and engage / release the OS block
// in response to transitions. Until then this manager exists in the
// binary but is not wired to anything observable in network state -
// so a bug in this file CANNOT break the user's network connection.
//
// Mirror semantics from Android KillSwitchManager.kt:
//   - IDLE: KS off, no protection.
//   - ARMED: KS enabled and tunnel up. Watching for unexpected drops.
//   - SINKHOLE: traffic-block engaged. Hardcore lock - the only path
//     out is the user toggling the KS setting off (Disarm() / Release
//     SinkholeToIdle()), which the desktop UI must surface.
type KillSwitchManager struct {
	state     atomic.Int32 // KillSwitchState
	mu        sync.Mutex   // serializes transitions and listener-list mutation
	listeners []chan KillSwitchState
}

// KillSwitchState is the in-memory state. Stored as int32 so it can
// be loaded with a single atomic.Load and read from any goroutine
// without holding the mutex.
type KillSwitchState int32

const (
	KSStateIdle     KillSwitchState = 0
	KSStateArmed    KillSwitchState = 1
	KSStateSinkhole KillSwitchState = 2
)

// String returns a stable human-readable name for log output. The
// names match the Android side exactly so logs from both platforms
// can be diffed without a lookup table.
func (s KillSwitchState) String() string {
	switch s {
	case KSStateIdle:
		return "IDLE"
	case KSStateArmed:
		return "ARMED"
	case KSStateSinkhole:
		return "SINKHOLE"
	default:
		return "UNKNOWN"
	}
}

// NewKillSwitchManager returns a fresh manager in IDLE state.
func NewKillSwitchManager() *KillSwitchManager {
	return &KillSwitchManager{}
}

// State returns the current state via a single atomic load. Safe to
// call from any goroutine without holding any lock.
func (m *KillSwitchManager) State() KillSwitchState {
	return KillSwitchState(m.state.Load())
}

// IsArmed returns true iff the state is ARMED or SINKHOLE - i.e. the
// session has had at least one successful arm() and the user has not
// since toggled KS off.
func (m *KillSwitchManager) IsArmed() bool {
	s := m.State()
	return s == KSStateArmed || s == KSStateSinkhole
}

// IsSinkholeActive returns true iff the state is SINKHOLE.
func (m *KillSwitchManager) IsSinkholeActive() bool {
	return m.State() == KSStateSinkhole
}

// Arm transitions IDLE -> ARMED or SINKHOLE -> ARMED. No-op when
// already ARMED. Called after a successful tunnel-up event when KS
// is enabled.
func (m *KillSwitchManager) Arm() {
	m.transition(func(prev KillSwitchState) (KillSwitchState, bool) {
		switch prev {
		case KSStateIdle:
			log.Println("KillSwitch: armed (first successful connect)")
			return KSStateArmed, true
		case KSStateSinkhole:
			log.Println("KillSwitch: armed (sinkhole released via reconnect)")
			return KSStateArmed, true
		default:
			return prev, false
		}
	})
}

// Disarm transitions any state to IDLE. Called when the user toggles
// the KS setting off. CRITICAL: this is the SOLE release path out of
// SINKHOLE under the hardcore-lock semantics.
func (m *KillSwitchManager) Disarm() {
	m.transition(func(prev KillSwitchState) (KillSwitchState, bool) {
		if prev == KSStateIdle {
			return prev, false
		}
		log.Printf("KillSwitch: disarmed (was %s)", prev)
		return KSStateIdle, true
	})
}

// EngageSinkhole transitions ARMED -> SINKHOLE on detection of an
// unexpected tunnel drop (network loss while armed). No-op for any
// other state. The reason string goes to the log only.
func (m *KillSwitchManager) EngageSinkhole(reason string) {
	m.transition(func(prev KillSwitchState) (KillSwitchState, bool) {
		if prev == KSStateArmed {
			log.Printf("KillSwitch: sinkhole engaged: %s", reason)
			return KSStateSinkhole, true
		}
		log.Printf("KillSwitch: engageSinkhole ignored: state is %s", prev)
		return prev, false
	})
}

// ForceSinkhole transitions ANY state to SINKHOLE. Used in two cases
// where user intent is explicit:
//
//  1. User enables KS while disconnected (with a configured connection
//     present). Industry-standard hardcore behaviour: block immediately.
//  2. User manually disconnects while KS is armed. Same intent.
func (m *KillSwitchManager) ForceSinkhole(reason string) {
	m.transition(func(prev KillSwitchState) (KillSwitchState, bool) {
		if prev == KSStateSinkhole {
			return prev, false
		}
		log.Printf("KillSwitch: sinkhole forced (was %s): %s", prev, reason)
		return KSStateSinkhole, true
	})
}

// ReleaseSinkholeToIdle is the explicit SINKHOLE -> IDLE transition.
// Equivalent to Disarm() called from SINKHOLE; provided as a separate
// method so callers can express intent ("release" vs "disarm any
// state").
func (m *KillSwitchManager) ReleaseSinkholeToIdle() {
	m.transition(func(prev KillSwitchState) (KillSwitchState, bool) {
		if prev == KSStateSinkhole {
			log.Println("KillSwitch: sinkhole released to idle (user toggled KS off)")
			return KSStateIdle, true
		}
		return prev, false
	})
}

// Subscribe returns a channel that receives every state transition.
// Buffered (8) so a brief listener pause does not block the state
// machine. If a listener falls more than 8 transitions behind, events
// are dropped (logged) rather than blocking - this is the right
// trade-off because state-machine progress must never depend on a
// listener's responsiveness.
//
// Subscribers should drain the channel in a goroutine and treat each
// received state as the new authoritative state. The channel is
// never closed; the caller is expected to live for the process
// lifetime (this matches all current desktop subscribers).
func (m *KillSwitchManager) Subscribe() <-chan KillSwitchState {
	ch := make(chan KillSwitchState, 8)
	m.mu.Lock()
	m.listeners = append(m.listeners, ch)
	m.mu.Unlock()
	return ch
}

// transition is the only path that mutates state. It holds the
// mutex while reading prev, asking decide() what to do, writing the
// new state, and dispatching to listeners. Listener writes are
// non-blocking - we never want the state machine to stall on a slow
// subscriber.
func (m *KillSwitchManager) transition(decide func(prev KillSwitchState) (KillSwitchState, bool)) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	prev := KillSwitchState(m.state.Load())
	next, changed := decide(prev)
	if !changed {
		return false
	}
	m.state.Store(int32(next))
	for _, ch := range m.listeners {
		select {
		case ch <- next:
		default:
			log.Printf("KillSwitch: listener channel full, dropping %s event", next)
		}
	}
	return true
}
