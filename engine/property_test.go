package engine

import (
	"testing"
	"time"
)

// Oscillating health (degraded↔good) must NEVER cause a protocol switch: a good
// sample recovers fast, and degradation only escalates after the dwell timer —
// which oscillation never lets elapse. This is the core anti-flap invariant.
func TestFlapNoSwitchOnOscillation(t *testing.T) {
	s := connected(t)
	sec := 10
	for i := 0; i < 200; i++ {
		ev := probeGood
		if i%3 != 0 { // mostly bad, occasionally good — keep flapping
			ev = probeDegrade
		}
		var acts []Action
		s, acts = apply(s, ev, sec)
		sec++
		if hasAction(acts, ActStartTunnel) {
			t.Fatalf("flap produced a switch at step %d (state %v)", i, s.FSM)
		}
		if s.FSM != StateConnected && s.FSM != StateDegraded {
			t.Fatalf("flap left the Connected/Degraded band: %v at step %d", s.FSM, i)
		}
	}
}

// allowsSwitch must enforce both cooldown and the per-window budget.
func TestSwitchBudgetAndCooldown(t *testing.T) {
	cfg := DefaultPolicy()
	cfg.SwitchCooldown = 0 // isolate the budget check
	s := State{}
	now := time.Unix(1000, 0)

	for i := 0; i < cfg.SwitchBudgetCount; i++ {
		if !allowsSwitch(s, now, cfg) {
			t.Fatalf("switch %d should be allowed within budget", i)
		}
		s = recordSwitch(s, now, cfg)
		now = now.Add(time.Second)
	}
	if allowsSwitch(s, now, cfg) {
		t.Fatalf("budget exhausted — switch must be denied")
	}
	// After the window passes, the budget resets.
	now = now.Add(cfg.SwitchBudgetWin + time.Second)
	if !allowsSwitch(s, now, cfg) {
		t.Fatalf("switch budget should reset after the window")
	}

	// Cooldown alone blocks back-to-back switches.
	cfg2 := DefaultPolicy()
	s2 := recordSwitch(State{}, time.Unix(2000, 0), cfg2)
	if allowsSwitch(s2, time.Unix(2001, 0), cfg2) {
		t.Fatalf("cooldown must block a switch 1s later")
	}
	if !allowsSwitch(s2, time.Unix(2000, 0).Add(cfg2.SwitchCooldown+time.Second), cfg2) {
		t.Fatalf("switch allowed after cooldown")
	}
}

// backoffDur (no jitter) is exponential, non-decreasing, and capped.
func TestBackoffMonotonicCapped(t *testing.T) {
	cfg := DefaultPolicy()
	prev := time.Duration(0)
	for n := 1; n <= 12; n++ {
		d := backoffDur(n, cfg, nil) // nil rand → no jitter → raw curve
		if d < prev {
			t.Fatalf("backoff decreased at n=%d: %v < %v", n, d, prev)
		}
		if d > cfg.BackoffCap {
			t.Fatalf("backoff exceeded cap at n=%d: %v", n, d)
		}
		prev = d
	}
	if prev != cfg.BackoffCap {
		t.Fatalf("backoff should reach the cap, got %v", prev)
	}
}

// The reducer is pure: identical (state,event,ctx) inputs yield identical
// outputs across runs (guards against map-iteration / time / RNG leaks).
func TestDeterminism(t *testing.T) {
	seq := []Event{evConnect, evHSOK, probeGood, probeDegrade, probeDegrade,
		timer(TimerDegradeDwell), timer(TimerRecoveryStep), timer(TimerRecoveryStep),
		timer(TimerRecoveryStep), timer(TimerRecoveryStep)}

	runOnce := func() (FSM, []ActionKind) {
		s := State{FSM: StateIdle}
		var kinds []ActionKind
		for i, ev := range seq {
			var acts []Action
			s, acts = apply(s, ev, i)
			for _, a := range acts {
				kinds = append(kinds, a.Kind)
			}
		}
		return s.FSM, kinds
	}
	f1, k1 := runOnce()
	f2, k2 := runOnce()
	if f1 != f2 {
		t.Fatalf("non-deterministic final FSM: %v vs %v", f1, f2)
	}
	if len(k1) != len(k2) {
		t.Fatalf("non-deterministic action count: %d vs %d", len(k1), len(k2))
	}
	for i := range k1 {
		if k1[i] != k2[i] {
			t.Fatalf("action stream diverged at %d: %v vs %v", i, k1[i], k2[i])
		}
	}
}
