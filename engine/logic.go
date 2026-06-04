package engine

import (
	"sort"
	"time"
)

// evalHealth folds one signal into the carried health accumulator and returns
// the new accumulator + verdict. Pure. Hysteresis: slow to leave Healthy (needs
// 2 consecutive bad samples), fast to return (1 good). Live traffic overrides
// to Healthy regardless of probe outcome.
func evalHealth(hs healthAccum, ev Event, now time.Time, cfg Policy) (healthAccum, Health) {
	// Observed traffic = positive proof the tunnel carries data.
	if ev.Kind == EvTunnelStats && (ev.RxBytes > 0 || ev.TxBytes > 0) {
		hs.lastTrafficAt = now
		hs.badStreak = 0
		hs.lastVerdict = HealthHealthy
		return hs, HealthHealthy
	}

	if ev.Kind != EvProbeResult {
		return hs, hs.lastVerdict
	}

	pr := ev.Probe
	// EWMA (integer, weight 3/4 old + 1/4 new) — kept for reporting + the P4
	// scorer, NOT for the verdict (which uses the raw sample so recovery is
	// fast and degradation is debounced, per the design).
	if hs.rttEwmaMs == 0 {
		hs.rttEwmaMs = pr.RTTms
		hs.lossEwmaPpm = pr.LossPpm
	} else {
		hs.rttEwmaMs = (hs.rttEwmaMs*3 + pr.RTTms) / 4
		hs.lossEwmaPpm = (hs.lossEwmaPpm*3 + pr.LossPpm) / 4
	}

	// Verdict from the RAW sample: slow to leave Healthy (debounce 2 bad),
	// fast to return (1 good).
	var cand Health
	switch {
	case !pr.OK || pr.LossPpm > cfg.LossDeadPpm || pr.RTTms > cfg.RTTDeadMs:
		cand = HealthDead
	case pr.LossPpm > cfg.LossDegradedPpm || pr.RTTms > cfg.RTTDegradedMs:
		cand = HealthDegraded
	default:
		cand = HealthHealthy
	}

	var verdict Health
	if cand == HealthHealthy {
		hs.badStreak = 0
		verdict = HealthHealthy
	} else {
		hs.badStreak++
		// Debounce the FIRST bad sample only when currently Healthy.
		if hs.lastVerdict == HealthHealthy && hs.badStreak < 2 {
			verdict = HealthHealthy
		} else {
			verdict = cand
		}
	}

	// Live-traffic grace window overrides a transient bad probe.
	if !hs.lastTrafficAt.IsZero() && now.Sub(hs.lastTrafficAt) < cfg.LiveTrafficGrace {
		verdict = HealthHealthy
		hs.badStreak = 0
	}

	hs.lastVerdict = verdict
	return hs, verdict
}

// pruneSwitches drops switch timestamps older than the budget window.
func pruneSwitches(times []time.Time, now time.Time, win time.Duration) []time.Time {
	out := times[:0:0]
	for _, t := range times {
		if now.Sub(t) < win {
			out = append(out, t)
		}
	}
	return out
}

// allowsSwitch enforces cooldown + per-window switch budget. Pure.
func allowsSwitch(s State, now time.Time, cfg Policy) bool {
	if !s.lastSwitchAt.IsZero() && now.Sub(s.lastSwitchAt) < cfg.SwitchCooldown {
		return false
	}
	recent := pruneSwitches(s.switchTimes, now, cfg.SwitchBudgetWin)
	return len(recent) < cfg.SwitchBudgetCount
}

// recordSwitch stamps a switch into the sliding window.
func recordSwitch(s State, now time.Time, cfg Policy) State {
	s.switchTimes = append(pruneSwitches(s.switchTimes, now, cfg.SwitchBudgetWin), now)
	s.lastSwitchAt = now
	return s
}

// backoffDur computes exponential backoff with full jitter, capped. Integer
// math + injected RNG → deterministic in tests.
func backoffDur(n int, cfg Policy, rand func() uint32) time.Duration {
	d := cfg.BackoffBase
	for i := 0; i < n; i++ {
		d *= time.Duration(cfg.BackoffFactor)
		if d >= cfg.BackoffCap {
			d = cfg.BackoffCap
			break
		}
	}
	if d > cfg.BackoffCap {
		d = cfg.BackoffCap
	}
	// Full jitter in [base, d].
	if d > cfg.BackoffBase && rand != nil {
		span := uint64(d - cfg.BackoffBase)
		j := uint64(rand()) % (span + 1)
		d = cfg.BackoffBase + time.Duration(j)
	}
	return d
}

// eligible filters profiles that cannot run on the current network context.
// Conservative: only excludes clearly-impossible profiles.
func eligible(snap ProfileSnapshot, net NetworkContext) []Profile {
	out := make([]Profile, 0, len(snap.Profiles))
	for _, p := range snap.Profiles {
		if !profileFits(p, net) {
			continue
		}
		out = append(out, p)
	}
	return out
}

func profileFits(p Profile, net NetworkContext) bool {
	// If every endpoint is IPv6-only and the link has no v6, it can't connect.
	if len(p.Endpoints) > 0 {
		anyReachable := false
		for _, e := range p.Endpoints {
			switch e.IPVersion {
			case 6:
				if net.HasV6 || (!net.HasV4 && !net.HasV6) {
					anyReachable = true
				}
			case 4:
				if net.HasV4 || (!net.HasV4 && !net.HasV6) {
					anyReachable = true
				}
			default:
				anyReachable = true
			}
		}
		if !anyReachable {
			return false
		}
	}
	return true
}

// selectBest is the MVP static selector: most-preferred protocol first, then
// stable by ProfileID. `exclude` lets fallback skip the just-failed profile.
// Returns "" when nothing is eligible.
func selectBest(ctx EvalContext, exclude ProfileID) ProfileID {
	cands := eligible(ctx.Store, ctx.Net)
	sort.SliceStable(cands, func(i, j int) bool {
		ri, rj := ctx.Cfg.protocolRank(cands[i].Protocol), ctx.Cfg.protocolRank(cands[j].Protocol)
		if ri != rj {
			return ri < rj
		}
		return cands[i].ID < cands[j].ID
	})
	for _, p := range cands {
		if p.ID == exclude {
			continue
		}
		return p.ID
	}
	// Only the excluded one is eligible → fall back to it rather than nothing.
	if exclude != "" {
		for _, p := range cands {
			if p.ID == exclude {
				return p.ID
			}
		}
	}
	return ""
}
