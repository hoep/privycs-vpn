package engine

import "time"

// Policy is the engine's tunable behaviour. Hot-reloadable; ships with
// DefaultPolicy(). All durations are real time; all scores are fixed-point
// milli-units ("mu") — no floats, for cross-arch determinism.
type Policy struct {
	ProtocolOrder []Protocol // preference order, index 0 = most preferred

	// anti-flap
	SwitchMarginMu    int32
	StickinessMu      int32
	DegradeDwell      time.Duration
	SwitchCooldown    time.Duration
	SwitchBudgetCount int
	SwitchBudgetWin   time.Duration

	// timeouts / retry
	ConnectTimeout time.Duration
	RecoveryStep   time.Duration
	BackoffBase    time.Duration
	BackoffFactor  int // integer factor (2 = double), kept int for determinism
	BackoffCap     time.Duration

	// health thresholds
	RTTDegradedMs   int32
	RTTDeadMs       int32
	LossDegradedPpm int32
	LossDeadPpm     int32
	LiveTrafficGrace time.Duration

	// probe cadence
	CadenceConnectedFg time.Duration
	CadenceConnectedBg time.Duration
	CadenceDegraded    time.Duration
	LowPowerMultiplier int

	// recovery ladder length (number of self-heal steps before fallback)
	RecoverySteps int
}

func DefaultPolicy() Policy {
	return Policy{
		ProtocolOrder: []Protocol{ProtoWireGuard, ProtoAmnezia, ProtoOpenVPN, ProtoIPsec},

		SwitchMarginMu:    150,
		StickinessMu:      80,
		DegradeDwell:      12 * time.Second,
		SwitchCooldown:    60 * time.Second,
		SwitchBudgetCount: 3,
		SwitchBudgetWin:   10 * time.Minute,

		ConnectTimeout: 15 * time.Second,
		RecoveryStep:   3 * time.Second,
		BackoffBase:    800 * time.Millisecond,
		BackoffFactor:  2,
		BackoffCap:     60 * time.Second,

		RTTDegradedMs:    350,
		RTTDeadMs:        1500,
		LossDegradedPpm:  50000,  // 5%
		LossDeadPpm:      200000, // 20%
		LiveTrafficGrace: 20 * time.Second,

		CadenceConnectedFg: 20 * time.Second,
		CadenceConnectedBg: 120 * time.Second,
		CadenceDegraded:    3 * time.Second,
		LowPowerMultiplier: 3,

		RecoverySteps: 3, // wait → revalidate → restart-same, then fallback
	}
}

// protocolRank returns the preference index of p (lower = preferred). Unknown
// protocols sort last, deterministically.
func (pol Policy) protocolRank(p Protocol) int {
	for i, q := range pol.ProtocolOrder {
		if q == p {
			return i
		}
	}
	return len(pol.ProtocolOrder) + int(p)
}
