package main

import (
	"log"
	"sync"
	"time"
)

// PauseManager is the desktop equivalent of the Android
// AlwaysOnDetector pause/cooldown concept. The desktop has no OS-
// level Always-On VPN that respawns the service after a manual
// disconnect, so the detection-by-timing logic from Android is
// unnecessary here. What IS needed is the user-initiated "pause for
// N minutes" feature surfaced from the Android UI - the user wants
// the same affordance on desktop ("disconnect for 15 min then
// auto-reconnect").
//
// SCOPE - what this file IS:
//   - Pure timestamp arithmetic.
//   - Persists nothing; pause is process-lifetime only. (Persistence
//     is a deliberate Phase 4 decision: should "pause until 17:30"
//     survive an app restart? Open question.)
//
// SCOPE - what this file IS NOT:
//   - Does NOT touch network state.
//   - Does NOT enforce the pause - that is the ConnectCoordinator's
//     gate (it consults IsPaused() before accepting non-USER intents).
type PauseManager struct {
	mu       sync.RWMutex
	until    time.Time // zero value = not paused
	now      func() time.Time
}

// NewPauseManager returns a PauseManager that uses time.Now as its
// clock. Test code can substitute via WithClock.
func NewPauseManager() *PauseManager {
	return &PauseManager{now: time.Now}
}

// WithClock replaces the clock function. Intended for tests.
func (p *PauseManager) WithClock(now func() time.Time) *PauseManager {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.now = now
	return p
}

// PauseFor schedules a pause that expires `d` from now. Replaces any
// existing pause. Zero or negative `d` is treated as Cancel().
func (p *PauseManager) PauseFor(d time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if d <= 0 {
		p.until = time.Time{}
		log.Println("PauseManager: cancelled (zero duration)")
		return
	}
	p.until = p.now().Add(d)
	log.Printf("PauseManager: paused until %s (%s from now)", p.until.Format(time.RFC3339), d)
}

// Cancel clears any active pause.
func (p *PauseManager) Cancel() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.until.IsZero() {
		return
	}
	log.Println("PauseManager: cancelled")
	p.until = time.Time{}
}

// IsPaused returns true iff a pause is active and has not yet
// expired.
func (p *PauseManager) IsPaused() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.until.IsZero() {
		return false
	}
	return p.now().Before(p.until)
}

// Remaining returns the time left until the active pause expires, or
// 0 if no pause is active or the pause has already expired.
func (p *PauseManager) Remaining() time.Duration {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.until.IsZero() {
		return 0
	}
	r := p.until.Sub(p.now())
	if r < 0 {
		return 0
	}
	return r
}
