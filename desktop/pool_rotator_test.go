package main

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// rotatorTestHarness wires up the rotator with controllable callbacks
// for deterministic testing of tick decisions without waiting 30s.
type rotatorTestHarness struct {
	r *PoolRotator

	rotateCount  atomic.Int32
	lastPoolID   atomic.Value
	mu           sync.Mutex
	rx, tx       int64
	isVPNActive  bool
}

func newHarness() *rotatorTestHarness {
	h := &rotatorTestHarness{r: NewPoolRotator()}
	h.isVPNActive = true
	h.lastPoolID.Store("")
	h.r.Start(
		func(poolID string) {
			h.rotateCount.Add(1)
			h.lastPoolID.Store(poolID)
		},
		func() (rx, tx int64) {
			h.mu.Lock()
			defer h.mu.Unlock()
			return h.rx, h.tx
		},
		func() bool {
			h.mu.Lock()
			defer h.mu.Unlock()
			return h.isVPNActive
		},
	)
	return h
}

func (h *rotatorTestHarness) setTraffic(rx, tx int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.rx = rx
	h.tx = tx
}

func (h *rotatorTestHarness) setVPNActive(v bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.isVPNActive = v
}

// We bypass the 30s ticker by calling tick directly; this is a
// white-box test (the package-internal name is fine).

func TestRotator_NoPoolNoRotation(t *testing.T) {
	h := newHarness()
	defer h.r.Stop()
	h.r.tick()
	if got := h.rotateCount.Load(); got != 0 {
		t.Errorf("rotateCount = %d, want 0", got)
	}
}

func TestRotator_NotYetTimeNoRotation(t *testing.T) {
	h := newHarness()
	defer h.r.Stop()
	p := &Pool{ID: "p1", Policy: PolicyRoundRobin, Rotation: PoolRotation{IntervalMin: 30, IdleAware: false}}
	h.r.SetActivePool(p)
	h.r.tick()
	if got := h.rotateCount.Load(); got != 0 {
		t.Errorf("rotateCount = %d, want 0 (not yet time)", got)
	}
}

func TestRotator_FiresWhenScheduled(t *testing.T) {
	h := newHarness()
	defer h.r.Stop()
	p := &Pool{ID: "p1", Policy: PolicyRoundRobin, Rotation: PoolRotation{IntervalMin: 30, IdleAware: false}}
	h.r.SetActivePool(p)
	// Backdate the schedule so the next tick fires.
	h.r.mu.Lock()
	h.r.scheduledRotation = time.Now().Add(-1 * time.Second)
	h.r.mu.Unlock()

	h.r.tick()
	// onRotate fires asynchronously - give it a moment.
	time.Sleep(50 * time.Millisecond)

	if got := h.rotateCount.Load(); got != 1 {
		t.Errorf("rotateCount = %d, want 1", got)
	}
	if got := h.lastPoolID.Load().(string); got != "p1" {
		t.Errorf("lastPoolID = %s, want p1", got)
	}
}

func TestRotator_VPNDownNoRotation(t *testing.T) {
	h := newHarness()
	defer h.r.Stop()
	p := &Pool{ID: "p1", Policy: PolicyRoundRobin, Rotation: PoolRotation{IntervalMin: 30, IdleAware: false}}
	h.r.SetActivePool(p)
	h.setVPNActive(false)

	h.r.mu.Lock()
	h.r.scheduledRotation = time.Now().Add(-1 * time.Second)
	h.r.mu.Unlock()

	h.r.tick()
	time.Sleep(50 * time.Millisecond)
	if got := h.rotateCount.Load(); got != 0 {
		t.Errorf("rotateCount = %d, want 0 (VPN down)", got)
	}
}

func TestRotator_IdleAwareDefersOnTraffic(t *testing.T) {
	h := newHarness()
	defer h.r.Stop()
	p := &Pool{ID: "p1", Policy: PolicyRoundRobin, Rotation: PoolRotation{IntervalMin: 30, IdleAware: true, ForceAfterMin: 60}}
	h.r.SetActivePool(p)
	// Initial traffic snapshot is taken at SetActivePool time (rx=tx=0).
	// Now traffic increases, then the schedule arrives.
	h.setTraffic(1000, 500)
	h.r.mu.Lock()
	h.r.scheduledRotation = time.Now().Add(-1 * time.Second)
	h.r.mu.Unlock()

	h.r.tick()
	time.Sleep(50 * time.Millisecond)
	if got := h.rotateCount.Load(); got != 0 {
		t.Errorf("rotateCount = %d, want 0 (traffic active, idle-aware)", got)
	}

	// Status should reflect idle-blocked.
	st := h.r.Status()
	if !st.IdleBlocked {
		t.Error("Status.IdleBlocked = false, want true")
	}
}

func TestRotator_IdleAwareFiresWhenTrafficStops(t *testing.T) {
	h := newHarness()
	defer h.r.Stop()
	p := &Pool{ID: "p1", Policy: PolicyRoundRobin, Rotation: PoolRotation{IntervalMin: 30, IdleAware: true, ForceAfterMin: 60}}
	h.r.SetActivePool(p)

	// Tick 1: traffic active, schedule reached -> defer.
	h.setTraffic(1000, 500)
	h.r.mu.Lock()
	h.r.scheduledRotation = time.Now().Add(-1 * time.Second)
	h.r.mu.Unlock()
	h.r.tick()
	time.Sleep(20 * time.Millisecond)

	if got := h.rotateCount.Load(); got != 0 {
		t.Fatalf("first tick rotated unexpectedly")
	}

	// Tick 2: traffic frozen (no delta) -> rotation should fire.
	// Reschedule is automatic but the schedule is still in past from
	// our backdate, so a second tick should pick up the no-traffic case.
	h.r.tick()
	time.Sleep(50 * time.Millisecond)

	if got := h.rotateCount.Load(); got != 1 {
		t.Errorf("rotateCount = %d, want 1 (traffic stopped, should rotate)", got)
	}
}

func TestRotator_ForceCapAfterIdleBlocked(t *testing.T) {
	h := newHarness()
	defer h.r.Stop()
	p := &Pool{ID: "p1", Policy: PolicyRoundRobin, Rotation: PoolRotation{IntervalMin: 30, IdleAware: true, ForceAfterMin: 60}}
	h.r.SetActivePool(p)

	// Enter idle-blocked state with backdated idle-since (force-cap exceeded).
	h.setTraffic(1000, 500)
	h.r.mu.Lock()
	h.r.scheduledRotation = time.Now().Add(-1 * time.Second)
	h.r.idleBlockedSince = time.Now().Add(-90 * time.Minute) // > 60 min cap
	h.r.lastTrafficRx, h.r.lastTrafficTx = 1000, 500          // ensure delta > 0 next tick
	h.r.mu.Unlock()

	// Increase traffic so hasTraffic is true and we go down the cap path.
	h.setTraffic(2000, 1000)
	h.r.tick()
	time.Sleep(50 * time.Millisecond)

	if got := h.rotateCount.Load(); got != 1 {
		t.Errorf("rotateCount = %d, want 1 (force-cap should fire)", got)
	}
}

func TestRotator_NonRoundRobinPoolIsNoop(t *testing.T) {
	h := newHarness()
	defer h.r.Stop()
	p := &Pool{ID: "p1", Policy: PolicyGeoNearest, Rotation: PoolRotation{IntervalMin: 5}}
	h.r.SetActivePool(p)

	h.r.mu.Lock()
	h.r.scheduledRotation = time.Now().Add(-1 * time.Second)
	h.r.mu.Unlock()

	h.r.tick()
	time.Sleep(50 * time.Millisecond)
	if got := h.rotateCount.Load(); got != 0 {
		t.Errorf("rotateCount = %d, want 0 (non-RR policy should not schedule rotation)", got)
	}
}

func TestRotator_StatusInactiveWhenNoPool(t *testing.T) {
	r := NewPoolRotator()
	st := r.Status()
	if st.Active {
		t.Errorf("Status.Active = true, want false (no pool)")
	}
}

func TestRotator_StopIsIdempotent(t *testing.T) {
	r := NewPoolRotator()
	r.Start(func(string) {}, nil, nil)
	r.Stop()
	r.Stop() // must not panic
}
