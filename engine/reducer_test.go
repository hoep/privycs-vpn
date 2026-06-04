package engine

import (
	"testing"
	"time"
)

// ───────────────────────── test helpers ─────────────────────────

func sampleStore() ProfileSnapshot {
	return ProfileSnapshot{Profiles: []Profile{
		{ID: "wg-1", Protocol: ProtoWireGuard, Endpoints: []Endpoint{{Host: "a", Port: 51820}}},
		{ID: "ovpn-1", Protocol: ProtoOpenVPN, Endpoints: []Endpoint{{Host: "b", Port: 443}}},
		{ID: "ipsec-1", Protocol: ProtoIPsec, Endpoints: []Endpoint{{Host: "c", Port: 500}}},
	}}
}

func ctxAt(sec int) EvalContext {
	return EvalContext{
		Now:   time.Unix(int64(sec), 0),
		Cfg:   DefaultPolicy(),
		Net:   NetworkContext{HasV4: true, Class: ClassOpen},
		Store: sampleStore(),
		Rand:  func() uint32 { return 12345 },
	}
}

var (
	evConnect    = Event{Kind: EvUserConnect}
	evDisconnect = Event{Kind: EvUserDisconnect}
	evHSOK       = Event{Kind: EvHandshakeOK}
	evHSFail     = Event{Kind: EvHandshakeFail}
	probeGood    = Event{Kind: EvProbeResult, Probe: ProbeResult{Kind: ProbePath, OK: true, RTTms: 30, LossPpm: 0}}
	probeDegrade = Event{Kind: EvProbeResult, Probe: ProbeResult{Kind: ProbePath, OK: true, RTTms: 500, LossPpm: 0}}
	probeDead    = Event{Kind: EvProbeResult, Probe: ProbeResult{Kind: ProbePath, OK: false, RTTms: 5000, LossPpm: 500000}}
)

func timer(tk TimerKind) Event { return Event{Kind: EvTimer, Timer: tk} }

func hasAction(acts []Action, k ActionKind) bool {
	for _, a := range acts {
		if a.Kind == k {
			return true
		}
	}
	return false
}

func startProfile(acts []Action) ProfileID {
	for _, a := range acts {
		if a.Kind == ActStartTunnel {
			return a.Profile
		}
	}
	return ""
}

// apply runs one event at a fixed second and returns (newState, actions).
func apply(s State, ev Event, sec int) (State, []Action) {
	return Reduce(s, ev, ctxAt(sec))
}

// ───────────────────────── scenario tests ─────────────────────────

func TestConnectHappyPath(t *testing.T) {
	s := State{FSM: StateIdle}
	var acts []Action

	s, acts = apply(s, evConnect, 0)
	if s.FSM != StateConnecting {
		t.Fatalf("want Connecting, got %v", s.FSM)
	}
	if startProfile(acts) != "wg-1" {
		t.Fatalf("want WG selected first, got %q", startProfile(acts))
	}
	if !hasAction(acts, ActArmTimer) {
		t.Fatalf("want connect timer armed")
	}

	s, acts = apply(s, evHSOK, 1)
	if s.FSM != StateValidating {
		t.Fatalf("want Validating, got %v", s.FSM)
	}
	if !hasAction(acts, ActRunProbe) {
		t.Fatalf("want probe on validate")
	}

	s, acts = apply(s, probeGood, 2)
	if s.FSM != StateConnected {
		t.Fatalf("want Connected, got %v", s.FSM)
	}
	if s.Active != "wg-1" || s.Health != HealthHealthy {
		t.Fatalf("bad connected state: %+v", s)
	}
	if !hasAction(acts, ActSetCadence) {
		t.Fatalf("want cadence set on connect")
	}
}

func TestHandshakeFailBackoff(t *testing.T) {
	s, _ := apply(State{FSM: StateIdle}, evConnect, 0)
	s, acts := apply(s, evHSFail, 1)
	if s.FSM != StateBackoff {
		t.Fatalf("want Backoff, got %v", s.FSM)
	}
	if !hasAction(acts, ActStopTunnel) || !hasAction(acts, ActArmTimer) {
		t.Fatalf("want stop + backoff timer")
	}
	// backoff timer → retry → Connecting
	s, _ = apply(s, timer(TimerBackoff), 2)
	if s.FSM != StateConnecting {
		t.Fatalf("want Connecting after backoff, got %v", s.FSM)
	}
}

func TestConnectTimeoutBackoff(t *testing.T) {
	s, _ := apply(State{FSM: StateIdle}, evConnect, 0)
	s, _ = apply(s, timer(TimerConnect), 1)
	if s.FSM != StateBackoff {
		t.Fatalf("want Backoff on connect timeout, got %v", s.FSM)
	}
}

func TestDegradeDwellNoImmediateSwitch(t *testing.T) {
	s := connected(t)
	// First degraded sample is debounced (stays Connected).
	s, acts := apply(s, probeDegrade, 10)
	if s.FSM != StateConnected {
		t.Fatalf("first degrade sample should debounce, got %v", s.FSM)
	}
	// Second degraded sample → Degraded, dwell armed, NO switch.
	s, acts = apply(s, probeDegrade, 11)
	if s.FSM != StateDegraded {
		t.Fatalf("want Degraded, got %v", s.FSM)
	}
	if hasAction(acts, ActStartTunnel) {
		t.Fatalf("must NOT switch on entering Degraded (anti-flap)")
	}
	if !hasAction(acts, ActArmTimer) {
		t.Fatalf("want dwell timer armed")
	}
	// Dwell elapses while still degraded → Recovering.
	s, _ = apply(s, timer(TimerDegradeDwell), 25)
	if s.FSM != StateRecovering {
		t.Fatalf("want Recovering after dwell, got %v", s.FSM)
	}
}

func TestHealthRecoverFromDegraded(t *testing.T) {
	s := connected(t)
	s, _ = apply(s, probeDegrade, 10)
	s, _ = apply(s, probeDegrade, 11) // now Degraded
	if s.FSM != StateDegraded {
		t.Fatalf("setup: want Degraded, got %v", s.FSM)
	}
	s, acts := apply(s, probeGood, 12)
	if s.FSM != StateConnected {
		t.Fatalf("want Connected after recovery, got %v", s.FSM)
	}
	if !hasAction(acts, ActCancelTimer) {
		t.Fatalf("want dwell timer cancelled on recovery")
	}
}

func TestDeadGoesToRecovering(t *testing.T) {
	s := connected(t)
	s, _ = apply(s, probeDead, 10) // debounced
	s, acts := apply(s, probeDead, 11)
	if s.FSM != StateRecovering {
		t.Fatalf("want Recovering on dead, got %v", s.FSM)
	}
	_ = acts
}

func TestRecoveryLadderFallsBackToOtherProfile(t *testing.T) {
	s := connected(t) // active wg-1
	s, _ = apply(s, probeDead, 10)
	s, _ = apply(s, probeDead, 11) // → Recovering, ladderStep 0 (wait)
	if s.FSM != StateRecovering {
		t.Fatalf("setup recovering, got %v", s.FSM)
	}
	// Walk the ladder: wait → revalidate → restart → fallback.
	sec := 20
	for i := 0; i < DefaultPolicy().RecoverySteps+1 && s.FSM == StateRecovering; i++ {
		s, _ = apply(s, timer(TimerRecoveryStep), sec)
		sec += 5
	}
	if s.FSM != StateSwitching {
		t.Fatalf("want Switching after ladder exhausted, got %v", s.FSM)
	}
	if s.Target == "" || s.Target == s.Active {
		t.Fatalf("want a DIFFERENT fallback target, active=%q target=%q", s.Active, s.Target)
	}
	if s.Target != "ovpn-1" {
		t.Fatalf("want next-by-order ovpn-1, got %q", s.Target)
	}
	// Switch handshake+validate commits the target.
	s, _ = apply(s, evHSOK, sec)
	s, _ = apply(s, probeGood, sec+1)
	if s.FSM != StateConnected || s.Active != "ovpn-1" {
		t.Fatalf("want Connected on ovpn-1, got %v active=%q", s.FSM, s.Active)
	}
}

func TestCaptiveHoldAndClear(t *testing.T) {
	s := connected(t)
	s, acts := apply(s, Event{Kind: EvCaptiveDetected}, 10)
	if s.FSM != StateCaptivePending {
		t.Fatalf("want CaptivePending, got %v", s.FSM)
	}
	if !hasAction(acts, ActNotify) {
		t.Fatalf("want captive notify")
	}
	s, acts = apply(s, Event{Kind: EvCaptiveCleared}, 30)
	if s.FSM != StateValidating || !hasAction(acts, ActRunProbe) {
		t.Fatalf("want re-validate after captive clears, got %v", s.FSM)
	}
}

func TestSuspendResume(t *testing.T) {
	s := connected(t)
	s, acts := apply(s, Event{Kind: EvSuspend}, 10)
	if s.FSM != StateSuspended {
		t.Fatalf("want Suspended, got %v", s.FSM)
	}
	if hasAction(acts, ActRunProbe) {
		t.Fatalf("must not probe while suspending")
	}
	s, acts = apply(s, Event{Kind: EvResume}, 100)
	if s.FSM != StateValidating || !hasAction(acts, ActRunProbe) {
		t.Fatalf("want re-validate on resume, got %v", s.FSM)
	}
}

func TestUserDisconnectFromAnyState(t *testing.T) {
	s := connected(t)
	s, acts := apply(s, evDisconnect, 10)
	if s.FSM != StateIdle {
		t.Fatalf("want Idle, got %v", s.FSM)
	}
	if !hasAction(acts, ActStopTunnel) {
		t.Fatalf("want stop on disconnect")
	}
}

func TestNoCandidateFails(t *testing.T) {
	ec := ctxAt(0)
	ec.Store = ProfileSnapshot{} // empty
	s, acts := Reduce(State{FSM: StateIdle}, evConnect, ec)
	if s.FSM != StateFailed {
		t.Fatalf("want Failed with no candidates, got %v", s.FSM)
	}
	_ = acts
}

func TestRoamRevalidates(t *testing.T) {
	s := connected(t)
	s, acts := apply(s, Event{Kind: EvPathChanged, Net: NetworkContext{Iface: IfaceCellular, HasV4: true}}, 10)
	if s.FSM != StateValidating || !hasAction(acts, ActRunProbe) {
		t.Fatalf("roam should re-validate active, got %v", s.FSM)
	}
}

// connected drives Idle→Connected on wg-1 and returns the state.
func connected(t *testing.T) State {
	t.Helper()
	s, _ := apply(State{FSM: StateIdle}, evConnect, 0)
	s, _ = apply(s, evHSOK, 1)
	s, _ = apply(s, probeGood, 2)
	if s.FSM != StateConnected {
		t.Fatalf("setup connected failed: %v", s.FSM)
	}
	return s
}
