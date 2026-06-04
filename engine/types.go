// Package engine is the headless, deterministic Smart Decision Engine for the
// PrivyCS multi-protocol VPN connector. It chooses, maintains and recovers
// connections from metadata signals only (handshake outcomes, RTT, loss,
// interface events) — never traffic content — and never touches platform VPN
// APIs directly: it emits Actions that a thin platform shell executes, and
// consumes Events the shell produces. See SMART_DECISION_ENGINE.md.
//
// The core (Reduce) is a PURE function: (State, Event, EvalContext) ->
// (State, []Action). No goroutines, no I/O, no time.Now() inside it — all of
// that is injected via EvalContext. This is what makes it deterministic and
// table/replay testable.
package engine

import "time"

// ───────────────────────── enums ─────────────────────────

type Protocol uint8

const (
	ProtoWireGuard Protocol = iota
	ProtoAmnezia
	ProtoOpenVPN
	ProtoIPsec
)

func (p Protocol) String() string {
	switch p {
	case ProtoWireGuard:
		return "wireguard"
	case ProtoAmnezia:
		return "amnezia"
	case ProtoOpenVPN:
		return "openvpn"
	case ProtoIPsec:
		return "ipsec"
	}
	return "unknown"
}

type Transport uint8

const (
	TransportAuto Transport = iota
	TransportUDP
	TransportTCP
)

type Health uint8

const (
	HealthHealthy Health = iota
	HealthDegraded
	HealthDead
)

func (h Health) String() string {
	switch h {
	case HealthHealthy:
		return "healthy"
	case HealthDegraded:
		return "degraded"
	case HealthDead:
		return "dead"
	}
	return "unknown"
}

type NetClass uint8

const (
	ClassUnknown NetClass = iota
	ClassOpen
	ClassRestricted
	ClassCaptive
	ClassMetered
)

type IfaceClass uint8

const (
	IfaceUnknown IfaceClass = iota
	IfaceWifi
	IfaceCellular
	IfaceEthernet
	IfaceOther
)

type FailKind uint8

const (
	FailNone FailKind = iota
	FailHandshake
	FailValidate
	FailTransport
	FailDNS
)

// ───────────────────────── domain ─────────────────────────

type ProfileID string
type NetworkKey string

type Endpoint struct {
	Host      string
	Port      uint16
	IPVersion uint8 // 0=any, 4, 6
}

type Capabilities struct {
	UDP, TCP, PortHopping, MOBIKE, Obfuscation, IPv6 bool
}

type ProviderQuirks struct {
	NeedsMOBIKEForRoam, TCPOnly, NoRekeyOnIdle bool
	MTUClampHint, HandshakeGraceMs             uint32
}

type Profile struct {
	ID        ProfileID
	Protocol  Protocol
	Endpoints []Endpoint
	Transport Transport
	Caps      Capabilities
	Quirks    ProviderQuirks
}

type NetworkContext struct {
	Iface             IfaceClass
	Key               NetworkKey // stable, non-PII (hash of SSID|BSSID|carrier)
	Metered           bool
	HasV4, HasV6      bool
	Class             NetClass
}

// ProfileStats is persisted per (ProfileID, NetworkKey). Used by the adaptive
// scorer (P4); the MVP static selector ignores it.
type ProfileStats struct {
	SuccessEWMA  int32 // 0..1000
	RTTms        int32
	LossPpm      int32
	LastFailAt   time.Time
	LastFailKind FailKind
}

// ProfileSnapshot is the read-only candidate set handed to the pure reducer.
type ProfileSnapshot struct {
	Profiles []Profile
	Stats    map[ProfileID]ProfileStats
}

// ───────────────────────── events (shell -> hub) ─────────────────────────

type EventKind uint8

const (
	EvUserConnect EventKind = iota
	EvUserDisconnect
	EvHandshakeOK
	EvHandshakeFail
	EvValidateOK
	EvValidateFail
	EvProbeResult
	EvTunnelStats
	EvPathChanged
	EvPower
	EvSuspend
	EvResume
	EvCaptiveDetected
	EvCaptiveCleared
	EvTimer
)

type TimerKind uint8

const (
	TimerNone TimerKind = iota
	TimerConnect       // handshake/validate deadline
	TimerDegradeDwell  // must stay degraded this long before switching
	TimerBackoff       // retry after failure
	TimerRecoveryStep  // gap between recovery-ladder steps
)

type ProbeKind uint8

const (
	ProbePath ProbeKind = iota
	ProbeDNS
	ProbeCaptive
)

type ProbeResult struct {
	Kind    ProbeKind
	OK      bool
	RTTms   int32
	LossPpm int32
}

type PowerState struct {
	BatteryPct           int8
	Charging, LowPower   bool
	Foreground           bool
}

type Event struct {
	Kind       EventKind
	Profile    ProfileID
	Probe      ProbeResult
	Net        NetworkContext
	Power      PowerState
	Timer      TimerKind
	FailReason FailKind
	RxBytes    int64
	TxBytes    int64
}

// ───────────────────────── actions (hub -> shell) ─────────────────────────

type ActionKind uint8

const (
	ActStartTunnel ActionKind = iota
	ActStopTunnel
	ActRunProbe
	ActSetCadence
	ActArmTimer
	ActCancelTimer
	ActNotify
	ActEmitDecision
)

type Action struct {
	Kind      ActionKind
	Profile   ProfileID
	ProbeKind ProbeKind
	Cadence   time.Duration
	Timer     TimerKind
	TimerDur  time.Duration
	NotifyKey string
	Decision  *DecisionRecord
}

// ───────────────────────── fsm state ─────────────────────────

type FSM uint8

const (
	StateIdle FSM = iota
	StateSelecting
	StateConnecting
	StateValidating
	StateConnected
	StateDegraded
	StateRecovering
	StateSwitching
	StateBackoff
	StateCaptivePending
	StateSuspended
	StateFailed
)

func (s FSM) String() string {
	switch s {
	case StateIdle:
		return "Idle"
	case StateSelecting:
		return "Selecting"
	case StateConnecting:
		return "Connecting"
	case StateValidating:
		return "Validating"
	case StateConnected:
		return "Connected"
	case StateDegraded:
		return "Degraded"
	case StateRecovering:
		return "Recovering"
	case StateSwitching:
		return "Switching"
	case StateBackoff:
		return "Backoff"
	case StateCaptivePending:
		return "CaptivePending"
	case StateSuspended:
		return "Suspended"
	case StateFailed:
		return "Failed"
	}
	return "Unknown"
}

// healthAccum is the EWMA/hysteresis state carried in State so the reducer
// stays pure.
type healthAccum struct {
	rttEwmaMs   int32
	lossEwmaPpm int32
	lastVerdict Health
	badStreak   int   // consecutive non-healthy probe verdicts
	lastTrafficAt time.Time
}

// State is the complete engine state. Value type (copied on each Reduce) so the
// reducer can return a modified copy without mutating the input.
type State struct {
	FSM           FSM
	Active        ProfileID
	Target        ProfileID // candidate during Switching
	Health        Health
	hs            healthAccum
	ladderStep    int
	backoffN      int
	prevFSM       FSM // FSM before Suspended (to restore on Resume → Validating)
	suspended     bool
	lastSwitchAt  time.Time
	switchTimes   []time.Time // sliding window for switch budget
	degradedSince time.Time
	net           NetworkContext
}

// EvalContext carries everything the pure reducer needs that would otherwise be
// impure (clock, randomness, config, the candidate snapshot).
type EvalContext struct {
	Now   time.Time
	Cfg   Policy
	Net   NetworkContext
	Store ProfileSnapshot
	Rand  func() uint32 // deterministic in tests
}

// DecisionRecord is the explainability payload attached to ActEmitDecision.
type DecisionRecord struct {
	At       time.Time
	From, To FSM
	Rule     string
	Active   ProfileID
	Chosen   ProfileID
	HumanKey string   // stable localization key, rendered at the UI layer
	Args     []string // args for the localized string
}
