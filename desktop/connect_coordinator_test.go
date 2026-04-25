package main

import (
	"testing"
	"time"
)

func TestCoordinator_RequestConnect_FromIdle_Accepted(t *testing.T) {
	c := NewConnectCoordinator()
	r := c.RequestConnect(SourceUser, "conn-1")
	if r != ResultAccepted {
		t.Fatalf("want Accepted, got %s", r)
	}
	if c.State() != CoordConnecting {
		t.Fatalf("want Connecting, got %s", c.State())
	}
}

func TestCoordinator_RequestConnect_AlreadyConnected(t *testing.T) {
	c := NewConnectCoordinator()
	c.RequestConnect(SourceUser, "conn-1")
	c.MarkConnected(false)
	r := c.RequestConnect(SourceUser, "conn-1")
	if r != ResultAlreadyConnected {
		t.Fatalf("want AlreadyConnected, got %s", r)
	}
}

func TestCoordinator_RequestConnect_USER_PreemptsAutomated(t *testing.T) {
	c := NewConnectCoordinator()
	c.RequestConnect(SourceOnDemand, "conn-1")
	if c.State() != CoordConnecting {
		t.Fatal("setup: expected Connecting")
	}
	r := c.RequestConnect(SourceUser, "conn-2")
	if r != ResultAccepted {
		t.Fatalf("USER should preempt ON_DEMAND, got %s", r)
	}
}

func TestCoordinator_RequestConnect_AutomatedDoesNotPreemptAutomated(t *testing.T) {
	c := NewConnectCoordinator()
	c.RequestConnect(SourceOnDemand, "conn-1")
	r := c.RequestConnect(SourceBoot, "conn-2")
	if r != ResultAlreadyConnecting {
		t.Fatalf("BOOT should not preempt ON_DEMAND, got %s", r)
	}
}

func TestCoordinator_RequestConnect_GatedBySinkhole(t *testing.T) {
	c := NewConnectCoordinator()
	ks := NewKillSwitchManager()
	c.SetHooks(ks, nil)

	ks.ForceSinkhole("test")

	// Every source must be refused while sinkhole is active.
	for _, src := range []IntentSource{SourceUser, SourceOnDemand, SourceBoot, SourceTray} {
		r := c.RequestConnect(src, "conn-x")
		if r != ResultGated {
			t.Fatalf("source %s: want Gated, got %s", src, r)
		}
	}

	// State must remain Idle - no transition fired.
	if c.State() != CoordIdle {
		t.Fatalf("state should still be Idle, got %s", c.State())
	}
}

func TestCoordinator_RequestConnect_GatedByPause_NonUserOnly(t *testing.T) {
	c := NewConnectCoordinator()
	p := NewPauseManager()
	c.SetHooks(nil, p)

	p.PauseFor(5 * time.Minute)

	// Non-USER sources are blocked.
	if r := c.RequestConnect(SourceOnDemand, "x"); r != ResultGated {
		t.Fatalf("ON_DEMAND during pause: want Gated, got %s", r)
	}

	// USER preempts the pause.
	if r := c.RequestConnect(SourceUser, "x"); r != ResultAccepted {
		t.Fatalf("USER during pause: want Accepted, got %s", r)
	}
}

func TestCoordinator_RequestDisconnect_KSEnabled_LeavesArmed(t *testing.T) {
	c := NewConnectCoordinator()
	ks := NewKillSwitchManager()
	c.SetHooks(ks, nil)

	c.RequestConnect(SourceUser, "conn-1")
	c.MarkConnected(true) // arm KS as part of MarkConnected

	if !ks.IsArmed() {
		t.Fatal("KS should be armed after MarkConnected(killSwitchEnabled=true)")
	}

	r := c.RequestDisconnect(SourceUser, true) // KS enabled -> stay armed
	if r != ResultAccepted {
		t.Fatalf("disconnect: want Accepted, got %s", r)
	}
	if !ks.IsArmed() {
		t.Fatal("KS must stay ARMED after RequestDisconnect with killSwitchEnabled=true")
	}
}

func TestCoordinator_RequestDisconnect_KSDisabled_DisarmsKS(t *testing.T) {
	c := NewConnectCoordinator()
	ks := NewKillSwitchManager()
	c.SetHooks(ks, nil)

	c.RequestConnect(SourceUser, "conn-1")
	c.MarkConnected(true)

	if !ks.IsArmed() {
		t.Fatal("KS should be armed before disconnect")
	}

	c.RequestDisconnect(SourceUser, false) // KS disabled -> disarm
	if ks.IsArmed() {
		t.Fatal("KS should be IDLE after RequestDisconnect with killSwitchEnabled=false")
	}
}

func TestCoordinator_MarkConnected_SinkholeReleasesToArmed(t *testing.T) {
	// Mirrors Android v0.9.10.5 fix: when reconnecting from SINKHOLE,
	// MarkConnected should arm KS so the SINKHOLE -> ARMED transition
	// fires. (The desktop hardcore-lock guard prevents most paths
	// from reaching this case, but the state machine must handle it.)
	c := NewConnectCoordinator()
	ks := NewKillSwitchManager()
	c.SetHooks(ks, nil)

	ks.Arm()
	ks.EngageSinkhole("test")
	if ks.State() != KSStateSinkhole {
		t.Fatal("setup failed")
	}

	// Force a connecting state directly (bypass RequestConnect to
	// avoid the sinkhole gate, simulating the case where sinkhole
	// engages mid-connect).
	c.mu.Lock()
	c.state = CoordConnecting
	c.mu.Unlock()

	c.MarkConnected(true)
	if ks.State() != KSStateArmed {
		t.Fatalf("expected SINKHOLE -> ARMED on reconnect, got %s", ks.State())
	}
}

func TestCoordinator_MarkConnected_NoOpWhenNotConnecting(t *testing.T) {
	c := NewConnectCoordinator()
	c.MarkConnected(false)
	if c.State() != CoordIdle {
		t.Fatalf("MarkConnected from Idle should be noop, state=%s", c.State())
	}
}

func TestCoordinator_MarkDisconnected_FromAnyNonIdle(t *testing.T) {
	c := NewConnectCoordinator()
	c.RequestConnect(SourceUser, "conn-1")
	c.MarkDisconnected()
	if c.State() != CoordIdle {
		t.Fatalf("MarkDisconnected: want Idle, got %s", c.State())
	}
}

func TestCoordinator_ConnectWatchdog_FiresAndResets(t *testing.T) {
	c := NewConnectCoordinator()
	c.connectTimeout = 50 * time.Millisecond // short for test

	c.RequestConnect(SourceUser, "conn-1")
	if c.State() != CoordConnecting {
		t.Fatal("setup failed")
	}

	time.Sleep(150 * time.Millisecond)
	if c.State() != CoordIdle {
		t.Fatalf("watchdog should have reset to Idle, got %s", c.State())
	}
}

func TestCoordinator_DisconnectWatchdog_FiresAndResets(t *testing.T) {
	c := NewConnectCoordinator()
	c.disconnectTimeout = 50 * time.Millisecond

	c.RequestConnect(SourceUser, "conn-1")
	c.MarkConnected(false)
	c.RequestDisconnect(SourceUser, false)
	if c.State() != CoordDisconnecting {
		t.Fatal("setup failed")
	}

	time.Sleep(150 * time.Millisecond)
	if c.State() != CoordIdle {
		t.Fatalf("watchdog should have reset to Idle, got %s", c.State())
	}
}

func TestCoordinator_Watchdog_CancelledOnMarkConnected(t *testing.T) {
	c := NewConnectCoordinator()
	c.connectTimeout = 50 * time.Millisecond

	c.RequestConnect(SourceUser, "conn-1")
	c.MarkConnected(false) // cancels watchdog before timeout

	// Wait past the watchdog timeout - state must remain Connected,
	// not get stomped to Idle by a delayed watchdog firing.
	time.Sleep(150 * time.Millisecond)
	if c.State() != CoordConnected {
		t.Fatalf("watchdog should have been cancelled, got %s", c.State())
	}
}

func TestPauseManager_BasicLifecycle(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	p := NewPauseManager().WithClock(clock)

	if p.IsPaused() {
		t.Fatal("fresh pause manager should not be paused")
	}

	p.PauseFor(5 * time.Minute)
	if !p.IsPaused() {
		t.Fatal("after PauseFor should be paused")
	}
	if r := p.Remaining(); r != 5*time.Minute {
		t.Fatalf("Remaining: want 5m, got %s", r)
	}

	now = now.Add(3 * time.Minute)
	if !p.IsPaused() {
		t.Fatal("3min in: still paused")
	}
	if r := p.Remaining(); r != 2*time.Minute {
		t.Fatalf("Remaining at 3m: want 2m, got %s", r)
	}

	now = now.Add(3 * time.Minute) // total 6m, past expiry
	if p.IsPaused() {
		t.Fatal("after expiry: should not be paused")
	}
	if r := p.Remaining(); r != 0 {
		t.Fatalf("Remaining post-expiry: want 0, got %s", r)
	}
}

func TestPauseManager_Cancel(t *testing.T) {
	p := NewPauseManager()
	p.PauseFor(5 * time.Minute)
	p.Cancel()
	if p.IsPaused() {
		t.Fatal("after Cancel should not be paused")
	}
}

func TestPauseManager_PauseForZeroIsCancel(t *testing.T) {
	p := NewPauseManager()
	p.PauseFor(5 * time.Minute)
	p.PauseFor(0)
	if p.IsPaused() {
		t.Fatal("PauseFor(0) should clear active pause")
	}
}
