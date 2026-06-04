package engine

import "time"

// Reduce is the PURE core of the engine: (State, Event, EvalContext) ->
// (State, []Action). No goroutines, no I/O, no time.Now(). Deterministic for a
// fixed (state, event, ctx). The MVP uses a static fallback selector
// (Policy.ProtocolOrder); adaptive scoring lands in P4 without changing this
// contract.
func Reduce(s State, ev Event, ctx EvalContext) (State, []Action) {
	now := ctx.Now
	from := s.FSM

	// ───── cross-cutting events (apply across most states) ─────
	switch ev.Kind {
	case EvUserDisconnect:
		if s.FSM == StateIdle {
			return s, nil
		}
		ns := State{FSM: StateIdle, net: s.net}
		return ns, []Action{
			{Kind: ActStopTunnel, Profile: s.Active},
			{Kind: ActCancelTimer, Timer: TimerNone}, // all
			decide(from, StateIdle, "user:disconnect", s.Active, "", now, "decision.disconnected"),
		}

	case EvSuspend:
		if s.FSM == StateSuspended || s.FSM == StateIdle {
			return s, nil
		}
		s.prevFSM = s.FSM
		s.FSM = StateSuspended
		return s, []Action{
			{Kind: ActCancelTimer, Timer: TimerNone},
			{Kind: ActSetCadence, Cadence: 0}, // stop probing
			decide(from, StateSuspended, "system:suspend", s.Active, "", now, "decision.suspended"),
		}

	case EvResume:
		if s.FSM != StateSuspended {
			return s, nil
		}
		if s.Active == "" {
			s.FSM = StateIdle
			return s, []Action{decide(from, StateIdle, "system:resume:idle", "", "", now, "decision.resumed_idle")}
		}
		s.FSM = StateValidating
		s.hs = healthAccum{lastVerdict: HealthHealthy}
		return s, []Action{
			{Kind: ActRunProbe, ProbeKind: ProbePath},
			{Kind: ActArmTimer, Timer: TimerConnect, TimerDur: ctx.Cfg.ConnectTimeout},
			decide(from, StateValidating, "system:resume:revalidate", s.Active, s.Active, now, "decision.resumed_revalidate"),
		}

	case EvPathChanged:
		s.net = ev.Net
		// Roaming: fast re-validate the ACTIVE profile on the new link before
		// any scoring — Wi-Fi↔cellular usually only needs a rebind, not a switch.
		if s.Active != "" && (s.FSM == StateConnected || s.FSM == StateDegraded ||
			s.FSM == StateValidating || s.FSM == StateRecovering) {
			s.FSM = StateValidating
			s.hs = healthAccum{lastVerdict: HealthHealthy}
			return s, []Action{
				{Kind: ActRunProbe, ProbeKind: ProbePath},
				{Kind: ActArmTimer, Timer: TimerConnect, TimerDur: ctx.Cfg.ConnectTimeout},
				decide(from, StateValidating, "roam:revalidate", s.Active, s.Active, now, "decision.roam"),
			}
		}
		return s, nil

	case EvCaptiveDetected:
		switch s.FSM {
		case StateConnecting, StateValidating, StateConnected, StateDegraded, StateRecovering:
			s.FSM = StateCaptivePending
			return s, []Action{
				{Kind: ActNotify, NotifyKey: "notify.captive"},
				{Kind: ActSetCadence, Cadence: ctx.Cfg.CadenceConnectedBg},
				decide(from, StateCaptivePending, "captive:detected", s.Active, "", now, "decision.captive"),
			}
		}
		return s, nil
	}

	// ───── per-state handling ─────
	switch s.FSM {

	case StateIdle, StateFailed:
		if ev.Kind == EvUserConnect {
			return enterSelecting(s, ctx, "user:connect")
		}

	case StateConnecting:
		switch ev.Kind {
		case EvHandshakeOK:
			return enterValidating(s, ctx, from, "handshake:ok")
		case EvHandshakeFail:
			return enterBackoff(s, ctx, "handshake:fail")
		case EvTimer:
			if ev.Timer == TimerConnect {
				return enterBackoff(s, ctx, "connect:timeout")
			}
		}

	case StateValidating:
		switch ev.Kind {
		case EvValidateOK:
			return enterConnected(s, ctx, from, "validate:ok")
		case EvProbeResult:
			if ev.Probe.OK {
				return enterConnected(s, ctx, from, "validate:probe-ok")
			}
			return s, nil // a single bad probe → wait for the deadline
		case EvValidateFail:
			return enterRecovering(s, ctx, "validate:fail")
		case EvTimer:
			if ev.Timer == TimerConnect {
				return enterRecovering(s, ctx, "validate:timeout")
			}
		}

	case StateConnected:
		switch ev.Kind {
		case EvProbeResult, EvTunnelStats:
			s.hs, s.Health = evalHealth(s.hs, ev, now, ctx.Cfg)
			switch s.Health {
			case HealthDegraded:
				s.FSM = StateDegraded
				s.degradedSince = now
				return s, []Action{
					{Kind: ActArmTimer, Timer: TimerDegradeDwell, TimerDur: ctx.Cfg.DegradeDwell},
					{Kind: ActSetCadence, Cadence: ctx.Cfg.CadenceDegraded},
					decide(from, StateDegraded, "health:degraded", s.Active, "", now, "decision.degraded"),
				}
			case HealthDead:
				return enterRecovering(s, ctx, "health:dead")
			}
			return s, nil
		}

	case StateDegraded:
		switch ev.Kind {
		case EvProbeResult, EvTunnelStats:
			s.hs, s.Health = evalHealth(s.hs, ev, now, ctx.Cfg)
			switch s.Health {
			case HealthHealthy:
				s.FSM = StateConnected
				return s, []Action{
					{Kind: ActCancelTimer, Timer: TimerDegradeDwell},
					{Kind: ActSetCadence, Cadence: ctx.Cfg.CadenceConnectedFg},
					decide(from, StateConnected, "health:recovered", s.Active, "", now, "decision.recovered"),
				}
			case HealthDead:
				return enterRecovering(s, ctx, "health:dead")
			}
			return s, nil
		case EvTimer:
			if ev.Timer == TimerDegradeDwell {
				return enterRecovering(s, ctx, "degraded:dwell-elapsed")
			}
		}

	case StateRecovering:
		switch ev.Kind {
		case EvProbeResult:
			if ev.Probe.OK {
				return enterConnected(s, ctx, from, "selfheal:probe-ok")
			}
			return s, nil
		case EvValidateOK:
			return enterConnected(s, ctx, from, "selfheal:validated")
		case EvHandshakeOK: // a restart-same step re-handshook
			return enterValidating(s, ctx, from, "selfheal:restart-ok")
		case EvTimer:
			if ev.Timer == TimerRecoveryStep || ev.Timer == TimerConnect {
				s.ladderStep++
				return recoveryStep(s, ctx, from, "selfheal:next")
			}
		}

	case StateSwitching:
		switch ev.Kind {
		case EvHandshakeOK:
			return enterValidating(s, ctx, from, "switch:handshake-ok")
		case EvHandshakeFail:
			s.Target = ""
			return enterBackoff(s, ctx, "switch:failed")
		case EvTimer:
			if ev.Timer == TimerConnect {
				s.Target = ""
				return enterBackoff(s, ctx, "switch:timeout")
			}
		}

	case StateBackoff:
		if ev.Kind == EvTimer && ev.Timer == TimerBackoff {
			return enterSelecting(s, ctx, "backoff:retry")
		}

	case StateCaptivePending:
		if ev.Kind == EvCaptiveCleared {
			s.FSM = StateValidating
			s.hs = healthAccum{lastVerdict: HealthHealthy}
			return s, []Action{
				{Kind: ActRunProbe, ProbeKind: ProbePath},
				{Kind: ActArmTimer, Timer: TimerConnect, TimerDur: ctx.Cfg.ConnectTimeout},
				decide(from, StateValidating, "captive:cleared", s.Active, s.Active, now, "decision.validating"),
			}
		}
	}

	// Unhandled (event not meaningful in this state) → no change.
	return s, nil
}

// ───────────────────────── transition helpers ─────────────────────────

func enterSelecting(s State, ctx EvalContext, rule string) (State, []Action) {
	from := s.FSM
	best := selectBest(ctx, "")
	if best == "" {
		s.FSM = StateFailed
		return s, []Action{decide(from, StateFailed, rule+":no-candidate", s.Active, "", ctx.Now, "decision.no_profile")}
	}
	s.FSM = StateConnecting
	s.Active = best
	s.Target = ""
	return s, []Action{
		{Kind: ActStartTunnel, Profile: best},
		{Kind: ActArmTimer, Timer: TimerConnect, TimerDur: ctx.Cfg.ConnectTimeout},
		decide(from, StateConnecting, rule, "", best, ctx.Now, "decision.connecting", string(best)),
	}
}

func enterValidating(s State, ctx EvalContext, from FSM, rule string) (State, []Action) {
	s.FSM = StateValidating
	return s, []Action{
		{Kind: ActRunProbe, ProbeKind: ProbePath},
		{Kind: ActArmTimer, Timer: TimerConnect, TimerDur: ctx.Cfg.ConnectTimeout},
		decide(from, StateValidating, rule, s.Active, s.Active, ctx.Now, "decision.validating"),
	}
}

func enterConnected(s State, ctx EvalContext, from FSM, rule string) (State, []Action) {
	acts := []Action{{Kind: ActCancelTimer, Timer: TimerConnect}}
	chosen := s.Active
	if s.Target != "" && s.Target != s.Active {
		s = recordSwitch(s, ctx.Now, ctx.Cfg)
		s.Active = s.Target
		chosen = s.Target
	}
	s.Target = ""
	s.FSM = StateConnected
	s.Health = HealthHealthy
	// NB: do NOT seed lastTrafficAt here — the live-traffic grace must reflect
	// real observed bytes (EvTunnelStats), else it would suppress degradation
	// for the whole grace window right after connecting.
	s.hs = healthAccum{lastVerdict: HealthHealthy}
	s.ladderStep = 0
	s.backoffN = 0
	acts = append(acts,
		Action{Kind: ActSetCadence, Cadence: ctx.Cfg.CadenceConnectedFg},
		decide(from, StateConnected, rule, s.Active, chosen, ctx.Now, "decision.connected", string(s.Active)))
	return s, acts
}

func enterBackoff(s State, ctx EvalContext, rule string) (State, []Action) {
	from := s.FSM
	s.backoffN++
	d := backoffDur(s.backoffN, ctx.Cfg, ctx.Rand)
	s.FSM = StateBackoff
	s.Target = ""
	return s, []Action{
		{Kind: ActStopTunnel, Profile: s.Active},
		{Kind: ActArmTimer, Timer: TimerBackoff, TimerDur: d},
		decide(from, StateBackoff, rule, s.Active, "", ctx.Now, "decision.backoff"),
	}
}

func enterRecovering(s State, ctx EvalContext, rule string) (State, []Action) {
	from := s.FSM
	s.FSM = StateRecovering
	s.ladderStep = 0
	s.Target = ""
	return recoveryStep(s, ctx, from, rule)
}

// recoveryStep emits the action for the current ladder step (cheapest first):
// 0 wait → 1 revalidate → 2 restart-same → then protocol fallback.
func recoveryStep(s State, ctx EvalContext, from FSM, rule string) (State, []Action) {
	switch {
	case s.ladderStep == 0:
		return s, []Action{
			{Kind: ActArmTimer, Timer: TimerRecoveryStep, TimerDur: ctx.Cfg.RecoveryStep},
			decide(from, StateRecovering, rule+":wait", s.Active, "", ctx.Now, "decision.recover_wait"),
		}
	case s.ladderStep == 1:
		return s, []Action{
			{Kind: ActRunProbe, ProbeKind: ProbePath},
			{Kind: ActArmTimer, Timer: TimerRecoveryStep, TimerDur: ctx.Cfg.RecoveryStep},
			decide(from, StateRecovering, rule+":revalidate", s.Active, "", ctx.Now, "decision.recover_revalidate"),
		}
	case s.ladderStep < ctx.Cfg.RecoverySteps:
		return s, []Action{
			{Kind: ActStartTunnel, Profile: s.Active}, // restart same profile (rekey/re-handshake)
			{Kind: ActArmTimer, Timer: TimerConnect, TimerDur: ctx.Cfg.ConnectTimeout},
			decide(from, StateRecovering, rule+":restart", s.Active, s.Active, ctx.Now, "decision.recover_restart"),
		}
	default:
		return enterSwitchingOrFallback(s, ctx, from)
	}
}

// enterSwitchingOrFallback picks the next profile (excluding the failed active);
// switches only if FlapGuard permits, otherwise backs off.
func enterSwitchingOrFallback(s State, ctx EvalContext, from FSM) (State, []Action) {
	next := selectBest(ctx, s.Active)
	if next == "" || next == s.Active || !allowsSwitch(s, ctx.Now, ctx.Cfg) {
		return enterBackoff(s, ctx, "recovery:exhausted")
	}
	s.FSM = StateSwitching
	s.Target = next
	return s, []Action{
		{Kind: ActStartTunnel, Profile: next},
		{Kind: ActArmTimer, Timer: TimerConnect, TimerDur: ctx.Cfg.ConnectTimeout},
		decide(from, StateSwitching, "switch:fallback", s.Active, next, ctx.Now, "decision.switching", string(next)),
	}
}

func decide(from, to FSM, rule string, active, chosen ProfileID, now time.Time, key string, args ...string) Action {
	return Action{Kind: ActEmitDecision, Decision: &DecisionRecord{
		At: now, From: from, To: to, Rule: rule, Active: active, Chosen: chosen, HumanKey: key, Args: args,
	}}
}
