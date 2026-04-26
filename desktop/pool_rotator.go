package main

import (
	"log"
	"sync"
	"time"
)

// preWarmLeadSeconds is how far ahead of a scheduled rotation the
// preWarm callback fires. The App uses this lead time to PICK the
// next member and (where possible) stage its config so the rotation
// itself only has to do the disconnect-then-up dance, not also the
// pick-and-write work. 60 s is generous - WireGuard endpoint
// resolution + MMDB lookup + JSON-marshal-and-disk-write are all
// in the sub-100ms range, but the lead window also gives the UI a
// chance to surface "preparing next server" text.
const preWarmLeadSeconds = 60

// PoolRotator drives the Round-Robin rotation timer for an active
// Pool. Pure goroutine + callbacks - knows nothing about App, Connect,
// or VPN state. The App wires the callbacks to its own connect path.
//
// State machine:
//   STOPPED → Start() → RUNNING (poll-tick every 30s, action every IntervalMin)
//   RUNNING → SetActivePool(nil) → STOPPED
//   RUNNING → Stop() → STOPPED
//
// Idle-aware: if the traffic delta since the last tick is >0 AND the
// idle-aware setting is on, the rotation is suppressed and a "force-
// after" deadline is tracked. Once the force-deadline passes, rotation
// fires regardless of traffic state.
//
// Pre-warm: 60 s before scheduledRotation, the rotator fires onPreWarm
// (once per cycle). The App responds by picking the next member ahead
// of time so the rotation tick itself only has to do
// disconnect+connect, not also the pick-and-IO work.
type PoolRotator struct {
	mu       sync.Mutex
	stopCh   chan struct{}
	running  bool

	// Active pool snapshot - we copy fields out at SetActivePool-time
	// so the rotator does not race with PoolRegistry.Update.
	poolID         string
	intervalMin    int
	idleAware      bool
	forceAfterMin  int

	// Tracking state for idle-detection.
	lastTrafficRx     int64
	lastTrafficTx     int64
	idleBlockedSince  time.Time
	scheduledRotation time.Time

	// preWarmFired: true once onPreWarm has fired for the current
	// cycle. Reset on every rotation. Prevents the pre-warm callback
	// from firing on every tick once the lead window opens.
	preWarmFired bool

	// Callbacks injected by App at Start time.
	onRotate     func(poolID string)
	onPreWarm    func(poolID string)
	getTraffic   func() (rx, tx int64)
	getIsActive  func() bool // returns true if VPN is currently up
}

// NewPoolRotator returns an unstarted rotator. Call Start with
// callbacks, then SetActivePool to begin rotation.
func NewPoolRotator() *PoolRotator {
	return &PoolRotator{}
}

// Start sets up the goroutine and callbacks. Idempotent - calling
// Start a second time replaces the callbacks but does not spawn a
// second goroutine.
func (r *PoolRotator) Start(
	onRotate func(poolID string),
	onPreWarm func(poolID string),
	getTraffic func() (rx, tx int64),
	getIsActive func() bool,
) {
	r.mu.Lock()
	r.onRotate = onRotate
	r.onPreWarm = onPreWarm
	r.getTraffic = getTraffic
	r.getIsActive = getIsActive
	if r.running {
		r.mu.Unlock()
		return
	}
	r.stopCh = make(chan struct{})
	r.running = true
	r.mu.Unlock()

	go r.run()
}

// Stop terminates the rotator. Safe to call multiple times.
func (r *PoolRotator) Stop() {
	r.mu.Lock()
	if !r.running {
		r.mu.Unlock()
		return
	}
	close(r.stopCh)
	r.running = false
	r.poolID = ""
	r.mu.Unlock()
}

// SetActivePool tells the rotator which pool to schedule rotation for.
// Pass nil to deactivate (the rotator stays running but ticks become
// no-ops). Resets the next-rotation deadline based on the pool's
// IntervalMin.
func (r *PoolRotator) SetActivePool(p *Pool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if p == nil {
		r.poolID = ""
		r.intervalMin = 0
		return
	}
	if p.Policy != PolicyRoundRobin {
		// Only Round-Robin schedules rotation; Geo-Nearest and Random
		// pick-once-per-Connect.
		r.poolID = ""
		r.intervalMin = 0
		return
	}

	r.poolID = p.ID
	r.intervalMin = p.Rotation.IntervalMin
	r.idleAware = p.Rotation.IdleAware
	r.forceAfterMin = p.Rotation.ForceAfterMin
	if r.intervalMin <= 0 {
		r.intervalMin = 30
	}
	if r.forceAfterMin <= 0 {
		r.forceAfterMin = 60
	}

	r.scheduledRotation = time.Now().Add(time.Duration(r.intervalMin) * time.Minute)
	r.idleBlockedSince = time.Time{}
	// Same deadlock concern as ResetSchedule: if the caller holds
	// App.mu (e.g. ActivatePool, called from the same goroutine that
	// holds the write lock), invoking r.getTraffic re-enters
	// App.mu via poolTrafficSnapshot and deadlocks. Set baseline to
	// zero; the first tick that runs after this samples naturally.
	r.lastTrafficRx = 0
	r.lastTrafficTx = 0

	log.Printf("PoolRotator: scheduled %s next rotation in %d min (idle-aware=%v, force-after=%d min)",
		r.poolID, r.intervalMin, r.idleAware, r.forceAfterMin)
}

// Status returns a snapshot of the rotator's view for the UI's
// "next rotation in 4:32" indicator.
type RotatorStatus struct {
	Active           bool          `json:"active"`            // a pool is being rotated
	PoolID           string        `json:"pool_id,omitempty"`
	IntervalMin      int           `json:"interval_min"`
	IdleAware        bool          `json:"idle_aware"`
	NextRotationIn   time.Duration `json:"next_rotation_in"`
	IdleBlocked      bool          `json:"idle_blocked"`
	ForceRotateIn    time.Duration `json:"force_rotate_in,omitempty"`
}

// ResetSchedule snaps scheduledRotation to "now + intervalMin", as if
// the user had just started a fresh rotation cycle. Called from
// App.Connect when the VPN actually goes connected so the countdown
// the user sees on screen begins exactly at connect-time, not at
// pool-activate-time.
//
// CRITICAL: must not call r.getTraffic from inside r.mu - the traffic
// callback re-enters App.mu (which the Connect caller already holds
// write-locked) and deadlocks the entire app. The first tick that
// runs after this reset will sample traffic naturally; the lost
// baseline only matters for the initial delta-detection one tick
// later, which is harmless (rx/tx start at 0 anyway just after
// connect).
func (r *PoolRotator) ResetSchedule() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.poolID == "" || r.intervalMin <= 0 {
		return
	}
	r.scheduledRotation = time.Now().Add(time.Duration(r.intervalMin) * time.Minute)
	r.idleBlockedSince = time.Time{}
	r.lastTrafficRx = 0
	r.lastTrafficTx = 0
	r.preWarmFired = false
}

// Status returns the rotator's current state for UI consumption.
//
// Returns Active=false unless ALL of:
//   - the goroutine is running (Start was called)
//   - a Round-Robin pool has been bound via SetActivePool
//   - the VPN is currently connected (via getIsActive callback)
//
// The VPN-connected gate is what makes the countdown only run after
// Connect: Pool selection alone does not start the rotation timer
// the user sees on screen, even though the schedule field is set
// internally for the tick goroutine's bookkeeping.
func (r *PoolRotator) Status() RotatorStatus {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.poolID == "" || !r.running {
		return RotatorStatus{Active: false}
	}
	if r.getIsActive != nil && !r.getIsActive() {
		return RotatorStatus{Active: false}
	}

	now := time.Now()
	until := time.Until(r.scheduledRotation)
	if until < 0 {
		until = 0
	}

	idleBlocked := !r.idleBlockedSince.IsZero()
	forceIn := time.Duration(0)
	if idleBlocked {
		forceIn = r.idleBlockedSince.Add(time.Duration(r.forceAfterMin) * time.Minute).Sub(now)
		if forceIn < 0 {
			forceIn = 0
		}
	}

	return RotatorStatus{
		Active:         true,
		PoolID:         r.poolID,
		IntervalMin:    r.intervalMin,
		IdleAware:      r.idleAware,
		NextRotationIn: until,
		IdleBlocked:    idleBlocked,
		ForceRotateIn:  forceIn,
	}
}

// run is the rotator's tick loop. 30s tick is a balance: fine enough
// that a 5-min interval gets ~10 evaluation points (so traffic-state
// transitions are caught quickly), coarse enough that idle CPU is
// negligible.
func (r *PoolRotator) run() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.stopCh:
			return
		case <-ticker.C:
			r.tick()
		}
	}
}

// tick evaluates whether to fire a rotation. Decision tree:
//   - no pool active OR VPN not up: no-op
//   - scheduledRotation not yet reached: no-op
//   - reached + idle-aware off: ROTATE
//   - reached + idle-aware on + traffic detected: enter idle-blocked,
//     do not rotate
//   - already idle-blocked + force-after deadline passed: ROTATE
//   - already idle-blocked + traffic stopped: ROTATE (the stop is the
//     signal that user finished whatever they were doing)
func (r *PoolRotator) tick() {
	r.mu.Lock()

	if r.poolID == "" || r.onRotate == nil {
		r.mu.Unlock()
		return
	}
	if r.getIsActive != nil && !r.getIsActive() {
		r.mu.Unlock()
		return
	}

	now := time.Now()

	// Pre-warm window: 60 s before scheduled rotation, fire onPreWarm
	// exactly once. The App picks the next member ahead of time so
	// the rotation tick itself only does disconnect+connect, not also
	// the pick-and-IO work. Frontend uses this to surface "Next:
	// <member>" in the rotation indicator.
	preWarmAt := r.scheduledRotation.Add(-time.Duration(preWarmLeadSeconds) * time.Second)
	if !r.preWarmFired && r.onPreWarm != nil && !now.Before(preWarmAt) && now.Before(r.scheduledRotation) {
		r.preWarmFired = true
		cb := r.onPreWarm
		pid := r.poolID
		r.mu.Unlock()
		go func() {
			defer func() {
				if rec := recover(); rec != nil {
					log.Printf("PoolRotator: onPreWarm panic recovered: %v", rec)
				}
			}()
			cb(pid)
		}()
		return
	}

	// Check if it is time to rotate at all.
	if now.Before(r.scheduledRotation) {
		r.mu.Unlock()
		return
	}

	if !r.idleAware {
		r.fireRotationLocked()
		r.mu.Unlock()
		return
	}

	// idle-aware path: compare traffic to last sample.
	hasTraffic := false
	if r.getTraffic != nil {
		rx, tx := r.getTraffic()
		if rx > r.lastTrafficRx || tx > r.lastTrafficTx {
			hasTraffic = true
		}
		r.lastTrafficRx, r.lastTrafficTx = rx, tx
	}

	if !hasTraffic {
		// Either we were never blocked or the user went idle since
		// last tick - either way, rotate now.
		r.fireRotationLocked()
		r.mu.Unlock()
		return
	}

	// Traffic active - enter or extend idle-blocked state.
	if r.idleBlockedSince.IsZero() {
		r.idleBlockedSince = now
		log.Printf("PoolRotator: rotation deferred for pool %s (active traffic)", r.poolID)
	}

	// Force-cap check.
	cap := r.idleBlockedSince.Add(time.Duration(r.forceAfterMin) * time.Minute)
	if now.After(cap) {
		log.Printf("PoolRotator: force-rotating pool %s after %d min idle-blocked", r.poolID, r.forceAfterMin)
		r.fireRotationLocked()
	}

	r.mu.Unlock()
}

// fireRotationLocked invokes the onRotate callback and resets the
// schedule. Caller must hold r.mu.
func (r *PoolRotator) fireRotationLocked() {
	cb := r.onRotate
	pid := r.poolID
	r.scheduledRotation = time.Now().Add(time.Duration(r.intervalMin) * time.Minute)
	r.idleBlockedSince = time.Time{}
	// Reset pre-warm flag so the next cycle's onPreWarm can fire.
	r.preWarmFired = false
	if r.getTraffic != nil {
		r.lastTrafficRx, r.lastTrafficTx = r.getTraffic()
	}

	// Fire on a goroutine so a slow Connect/Disconnect cannot block
	// the tick loop.
	if cb != nil {
		go func() {
			defer func() {
				if rec := recover(); rec != nil {
					log.Printf("PoolRotator: onRotate panic recovered: %v", rec)
				}
			}()
			cb(pid)
		}()
	}
}
