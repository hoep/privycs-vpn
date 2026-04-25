package main

import (
	"testing"
	"time"
)

func TestKillSwitchManager_InitialState(t *testing.T) {
	m := NewKillSwitchManager()
	if got := m.State(); got != KSStateIdle {
		t.Fatalf("initial state: want IDLE, got %s", got)
	}
	if m.IsArmed() {
		t.Fatal("IsArmed must be false in IDLE")
	}
	if m.IsSinkholeActive() {
		t.Fatal("IsSinkholeActive must be false in IDLE")
	}
}

func TestKillSwitchManager_Arm_FromIdle(t *testing.T) {
	m := NewKillSwitchManager()
	m.Arm()
	if got := m.State(); got != KSStateArmed {
		t.Fatalf("after Arm: want ARMED, got %s", got)
	}
	if !m.IsArmed() {
		t.Fatal("IsArmed must be true in ARMED")
	}
	if m.IsSinkholeActive() {
		t.Fatal("IsSinkholeActive must be false in ARMED")
	}
}

func TestKillSwitchManager_Arm_FromSinkhole(t *testing.T) {
	m := NewKillSwitchManager()
	m.Arm()
	m.EngageSinkhole("test")
	if m.State() != KSStateSinkhole {
		t.Fatal("setup failed: state should be SINKHOLE")
	}
	m.Arm()
	if got := m.State(); got != KSStateArmed {
		t.Fatalf("Arm from SINKHOLE: want ARMED, got %s", got)
	}
}

func TestKillSwitchManager_Arm_NoOpOnArmed(t *testing.T) {
	m := NewKillSwitchManager()
	// Subscribe BEFORE any Arm so we observe both transitions.
	sub := m.Subscribe()
	m.Arm() // IDLE -> ARMED, fires event
	if s := receiveOrFail(t, sub); s != KSStateArmed {
		t.Fatalf("first Arm: want ARMED event, got %s", s)
	}
	m.Arm() // already ARMED, no transition, no event
	select {
	case s := <-sub:
		t.Fatalf("expected no transition on second Arm, got %s", s)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestKillSwitchManager_Disarm_ClearsAllStates(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*KillSwitchManager)
	}{
		{"from ARMED", func(m *KillSwitchManager) { m.Arm() }},
		{"from SINKHOLE", func(m *KillSwitchManager) { m.Arm(); m.EngageSinkhole("x") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewKillSwitchManager()
			tc.setup(m)
			m.Disarm()
			if got := m.State(); got != KSStateIdle {
				t.Fatalf("after Disarm: want IDLE, got %s", got)
			}
		})
	}
}

func TestKillSwitchManager_EngageSinkhole_OnlyFromArmed(t *testing.T) {
	cases := []struct {
		name      string
		setup     func(*KillSwitchManager)
		wantState KillSwitchState
	}{
		{"from IDLE: ignored", func(m *KillSwitchManager) {}, KSStateIdle},
		{"from ARMED: engaged", func(m *KillSwitchManager) { m.Arm() }, KSStateSinkhole},
		{"from SINKHOLE: ignored (already engaged)", func(m *KillSwitchManager) { m.Arm(); m.EngageSinkhole("x") }, KSStateSinkhole},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewKillSwitchManager()
			tc.setup(m)
			m.EngageSinkhole("test")
			if got := m.State(); got != tc.wantState {
				t.Fatalf("want %s, got %s", tc.wantState, got)
			}
		})
	}
}

func TestKillSwitchManager_ForceSinkhole_FromAnyState(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*KillSwitchManager)
	}{
		{"from IDLE", func(m *KillSwitchManager) {}},
		{"from ARMED", func(m *KillSwitchManager) { m.Arm() }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewKillSwitchManager()
			tc.setup(m)
			m.ForceSinkhole("test")
			if got := m.State(); got != KSStateSinkhole {
				t.Fatalf("after ForceSinkhole: want SINKHOLE, got %s", got)
			}
		})
	}
}

func TestKillSwitchManager_ReleaseSinkholeToIdle_OnlyFromSinkhole(t *testing.T) {
	cases := []struct {
		name      string
		setup     func(*KillSwitchManager)
		wantState KillSwitchState
	}{
		{"from IDLE: noop", func(m *KillSwitchManager) {}, KSStateIdle},
		{"from ARMED: noop", func(m *KillSwitchManager) { m.Arm() }, KSStateArmed},
		{"from SINKHOLE: released", func(m *KillSwitchManager) { m.Arm(); m.EngageSinkhole("x") }, KSStateIdle},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewKillSwitchManager()
			tc.setup(m)
			m.ReleaseSinkholeToIdle()
			if got := m.State(); got != tc.wantState {
				t.Fatalf("want %s, got %s", tc.wantState, got)
			}
		})
	}
}

func TestKillSwitchManager_Subscribe_ReceivesTransitions(t *testing.T) {
	m := NewKillSwitchManager()
	sub := m.Subscribe()

	m.Arm()
	if s := receiveOrFail(t, sub); s != KSStateArmed {
		t.Fatalf("first event: want ARMED, got %s", s)
	}
	m.EngageSinkhole("test")
	if s := receiveOrFail(t, sub); s != KSStateSinkhole {
		t.Fatalf("second event: want SINKHOLE, got %s", s)
	}
	m.Disarm()
	if s := receiveOrFail(t, sub); s != KSStateIdle {
		t.Fatalf("third event: want IDLE, got %s", s)
	}
}

func TestKillSwitchManager_Subscribe_NonBlockingOnSlowListener(t *testing.T) {
	m := NewKillSwitchManager()
	sub := m.Subscribe() // buffer 8
	// Fire 20 transitions back-to-back without draining. The state
	// machine must not block - dropped events are logged but not
	// propagated.
	for i := 0; i < 20; i++ {
		m.Arm()
		m.Disarm()
	}
	// State must reflect the LAST transition.
	if got := m.State(); got != KSStateIdle {
		t.Fatalf("after spam: want IDLE, got %s", got)
	}
	// Channel should still have a few events (up to buffer size).
	count := 0
drainLoop:
	for {
		select {
		case <-sub:
			count++
		default:
			break drainLoop
		}
	}
	if count == 0 || count > 8 {
		t.Fatalf("expected 1..8 buffered events, got %d", count)
	}
}

func TestKillSwitchManager_HardcoreLockSemantics(t *testing.T) {
	// The complete user-flow: tunnel up + KS enabled, network drops,
	// sinkhole engaged, user toggles KS off -> only release path.
	m := NewKillSwitchManager()

	m.Arm() // initial successful connect
	if !m.IsArmed() {
		t.Fatal("step 1: should be armed after first connect")
	}

	m.EngageSinkhole("network drop") // unexpected drop
	if !m.IsSinkholeActive() {
		t.Fatal("step 2: should be sinkhole after drop")
	}

	// Try to reconnect WITHOUT first disarming. In the real system
	// this would be the hardcore-lock guard refusing the intent;
	// at the manager level we just verify Arm() does the right
	// thing IF called: SINKHOLE -> ARMED. But callers should NOT
	// Arm() while in sinkhole - the gate is in the coordinator.
	m.Arm()
	if got := m.State(); got != KSStateArmed {
		t.Fatalf("step 3a (Arm transitions sinkhole->armed): want ARMED, got %s", got)
	}

	// Reset and verify the hardcore release path: only Disarm /
	// ReleaseSinkholeToIdle takes us out.
	m = NewKillSwitchManager()
	m.Arm()
	m.EngageSinkhole("network drop")
	m.Disarm()
	if got := m.State(); got != KSStateIdle {
		t.Fatalf("step 4 (Disarm releases sinkhole): want IDLE, got %s", got)
	}
}

// helpers

func receiveOrFail(t *testing.T, ch <-chan KillSwitchState) KillSwitchState {
	t.Helper()
	select {
	case s := <-ch:
		return s
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for state event")
		return KSStateIdle // unreachable
	}
}

