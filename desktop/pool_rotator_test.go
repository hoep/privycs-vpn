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
	preWarmCount atomic.Int32
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
		func(poolID string) {
			h.preWarmCount.Add(1)
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
	r.Start(func(string) {}, func(string) {}, nil, nil)
	r.Stop()
	r.Stop() // must not panic
}

// TestRotator_PreWarmFiresOnceWithinLeadWindow verifies the rotator
// pre-warm contract: 60 s before scheduledRotation, the onPreWarm
// callback fires EXACTLY ONCE per cycle, regardless of how many
// ticks happen inside that window. The App side uses pre-warm to
// pick the next member ahead of time so the rotation tick itself
// does not also do the pick-and-IO work in the critical path.
func TestRotator_PreWarmFiresOnceWithinLeadWindow(t *testing.T) {
	h := newHarness()
	defer h.r.Stop()
	p := &Pool{
		ID:       "p1",
		Policy:   PolicyRoundRobin,
		Rotation: PoolRotation{IntervalMin: 5, IdleAware: false},
	}
	h.r.SetActivePool(p)

	// Backdate scheduledRotation so we sit INSIDE the pre-warm
	// window (less than 60s away) but not yet at rotation time.
	h.r.mu.Lock()
	h.r.scheduledRotation = time.Now().Add(30 * time.Second)
	h.r.preWarmFired = false
	h.r.mu.Unlock()

	// First tick fires onPreWarm.
	h.r.tick()
	time.Sleep(50 * time.Millisecond)
	if got := h.preWarmCount.Load(); got != 1 {
		t.Errorf("after first tick in lead window, preWarmCount = %d, want 1", got)
	}
	if got := h.rotateCount.Load(); got != 0 {
		t.Errorf("rotation must NOT fire while we are still in lead window, got %d", got)
	}

	// Subsequent ticks must NOT re-fire onPreWarm in the same cycle.
	h.r.tick()
	h.r.tick()
	h.r.tick()
	time.Sleep(50 * time.Millisecond)
	if got := h.preWarmCount.Load(); got != 1 {
		t.Errorf("preWarm fired %d times, expected exactly 1 per cycle", got)
	}
}

// TestRotator_PreWarmResetsAfterRotation: once a rotation actually
// fires, the pre-warm flag must reset so the NEXT cycle's pre-warm
// can fire again.
func TestRotator_PreWarmResetsAfterRotation(t *testing.T) {
	h := newHarness()
	defer h.r.Stop()
	p := &Pool{
		ID:       "p1",
		Policy:   PolicyRoundRobin,
		Rotation: PoolRotation{IntervalMin: 5, IdleAware: false},
	}
	h.r.SetActivePool(p)

	// Force pre-warm flag set as if it already fired this cycle,
	// then move past scheduledRotation to trigger fireRotation.
	h.r.mu.Lock()
	h.r.preWarmFired = true
	h.r.scheduledRotation = time.Now().Add(-1 * time.Second)
	h.r.mu.Unlock()

	h.r.tick()
	time.Sleep(50 * time.Millisecond)

	h.r.mu.Lock()
	stillFired := h.r.preWarmFired
	h.r.mu.Unlock()
	if stillFired {
		t.Error("preWarmFired should be reset to false after rotation fires")
	}
}

// TestRotator_NoDeadlockWhenCallerHoldsSimulatedAppMutex is a
// regression test for v0.9.11.17's CRITICAL deadlock: SetActivePool
// and ResetSchedule were calling r.getTraffic from inside r.mu.Lock,
// and r.getTraffic in production = App.poolTrafficSnapshot which
// acquires App.mu.RLock. Both of those rotator methods are called
// from goroutines that ALREADY hold App.mu.Lock (write):
//
//   - SetActivePool   <- App.ActivatePool       (holds App.mu.Lock)
//   - ResetSchedule   <- App.Connect            (holds App.mu.Lock)
//
// Self-deadlock: the same goroutine waits on a write-locked mutex
// it itself owns. App froze entirely - no status updates, no
// disconnect, only restart recovered. User reported this as
// "sowas DARF NICHT PASSIEREN".
//
// The test simulates the production setup with a parallel
// sync.RWMutex standing in for App.mu, has the getTraffic callback
// try to RLock that mutex, then calls SetActivePool + ResetSchedule
// from a goroutine that holds the simulated lock in WRITE mode. If
// either rotator method calls getTraffic synchronously while
// holding r.mu, the goroutine deadlocks and the test times out
// after 2 seconds. Without the deadlock both calls return in <1ms.
func TestRotator_NoDeadlockWhenCallerHoldsSimulatedAppMutex(t *testing.T) {
	var simAppMu sync.RWMutex

	r := NewPoolRotator()
	r.Start(
		func(string) {},
		func(string) {},
		func() (int64, int64) {
			// Simulates App.poolTrafficSnapshot: acquires the
			// App read lock briefly. If the rotator method that
			// invoked us holds the App WRITE lock from the same
			// goroutine, this is a self-deadlock.
			simAppMu.RLock()
			defer simAppMu.RUnlock()
			return 0, 0
		},
		func() bool { return true },
	)
	defer r.Stop()

	pool := &Pool{
		ID:      "p1",
		Policy:  PolicyRoundRobin,
		Members: []*PoolMember{{ID: "m1", Active: true, Region: "Europe"}},
		Rotation: PoolRotation{
			IntervalMin: 5,
			IdleAware:   true,
		},
	}

	done := make(chan struct{})
	go func() {
		simAppMu.Lock() // mirror App.ActivatePool / App.Connect
		defer simAppMu.Unlock()

		// Both methods used to call r.getTraffic inside r.mu, which
		// would re-enter simAppMu.RLock from this same goroutine
		// and deadlock.
		r.SetActivePool(pool)
		r.ResetSchedule()
		close(done)
	}()

	select {
	case <-done:
		// pass
	case <-time.After(2 * time.Second):
		t.Fatal("DEADLOCK: rotator method called getTraffic which re-entered the App lock held by the same goroutine. Either remove the getTraffic call from the lock-held method or release r.mu before calling it.")
	}
}
