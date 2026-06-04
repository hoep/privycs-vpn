package engine

import (
	"context"
	"sync"
	"time"
)

// ───────────────────────── spoke interfaces ─────────────────────────
//
// The engine is HEADLESS: it never touches a platform VPN API. It drives these
// spokes and consumes Events they emit (via the Submit callback wired at
// construction). Platform shells implement them.

type TunnelController interface {
	// Start brings up profile p asynchronously; the impl must Submit an
	// EvHandshakeOK / EvHandshakeFail when the attempt resolves.
	Start(p ProfileID)
	// Stop tears down the current tunnel asynchronously.
	Stop()
}

type Prober interface {
	// Run performs a bounded probe asynchronously; the impl must Submit an
	// EvProbeResult (or EvCaptiveDetected) with the outcome.
	Run(kind ProbeKind)
	// SetCadence informs the prober how often to self-trigger (0 = stop).
	SetCadence(d time.Duration)
}

type PlatformBridge interface {
	Path() NetworkContext
	// Events streams EvPathChanged / EvPower / EvSuspend / EvResume /
	// EvCaptive* originating from the OS.
	Events() <-chan Event
}

type ProfileStore interface {
	Snapshot() ProfileSnapshot
	// RecordOutcome updates persisted per-(profile,network) stats (off the hot
	// path). The MVP static selector ignores stats; P4 scoring uses them.
	RecordOutcome(id ProfileID, key NetworkKey, ok bool, kind FailKind)
}

type Notifier interface{ Notify(key string) }

// ───────────────────────── clock ─────────────────────────

type Stopper interface{ Stop() bool }

type Clock interface {
	Now() time.Time
	AfterFunc(d time.Duration, f func()) Stopper
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }
func (realClock) AfterFunc(d time.Duration, f func()) Stopper {
	return time.AfterFunc(d, f)
}

// RealClock is the production clock.
func RealClock() Clock { return realClock{} }

// ───────────────────────── decision log ─────────────────────────

type Decision = DecisionRecord

type DecisionLog struct {
	mu  sync.Mutex
	buf []DecisionRecord
	cap int
}

func NewDecisionLog(capacity int) *DecisionLog {
	if capacity <= 0 {
		capacity = 256
	}
	return &DecisionLog{cap: capacity}
}

func (l *DecisionLog) Add(r DecisionRecord) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf = append(l.buf, r)
	if len(l.buf) > l.cap {
		l.buf = l.buf[len(l.buf)-l.cap:]
	}
}

// Recent returns up to n most-recent records (newest last).
func (l *DecisionLog) Recent(n int) []DecisionRecord {
	l.mu.Lock()
	defer l.mu.Unlock()
	if n <= 0 || n > len(l.buf) {
		n = len(l.buf)
	}
	out := make([]DecisionRecord, n)
	copy(out, l.buf[len(l.buf)-n:])
	return out
}

// ───────────────────────── engine (impure shell) ─────────────────────────

type Config struct {
	Policy Policy
	Clock  Clock
	Tunnel TunnelController
	Prober Prober
	Bridge PlatformBridge
	Store  ProfileStore
	Notify Notifier
}

type Engine struct {
	cfg    Config
	inbox  chan Event
	out    chan Decision
	log    *DecisionLog
	state  State
	timers map[TimerKind]Stopper // mutated only on the Run goroutine
}

func New(cfg Config) *Engine {
	if cfg.Clock == nil {
		cfg.Clock = RealClock()
	}
	if cfg.Policy.ProtocolOrder == nil {
		cfg.Policy = DefaultPolicy()
	}
	return &Engine{
		cfg:    cfg,
		inbox:  make(chan Event, 256),
		out:    make(chan Decision, 256),
		log:    NewDecisionLog(512),
		state:  State{FSM: StateIdle},
		timers: map[TimerKind]Stopper{},
	}
}

// Submit enqueues an event. Safe from any goroutine.
func (e *Engine) Submit(ev Event) {
	select {
	case e.inbox <- ev:
	default: // inbox full → drop oldest semantics not needed for MVP; block-free
		go func() { e.inbox <- ev }()
	}
}

// Decisions streams explainable decisions for the UI/logs.
func (e *Engine) Decisions() <-chan Decision { return e.out }

// Log exposes the ring buffer (for snapshots / crash context).
func (e *Engine) Log() *DecisionLog { return e.log }

// State returns a copy of the current FSM state (call only from outside Run;
// for tests/debug it is read after Run stops).
func (e *Engine) Snapshot() State { return e.state }

// Run is the single-goroutine event loop. Pure Reduce + impure execute.
func (e *Engine) Run(ctx context.Context) {
	// Fan platform events into the inbox.
	if e.cfg.Bridge != nil {
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case ev, ok := <-e.cfg.Bridge.Events():
					if !ok {
						return
					}
					e.Submit(ev)
				}
			}
		}()
	}

	for {
		select {
		case <-ctx.Done():
			e.cancelAllTimers()
			return
		case ev := <-e.inbox:
			ec := EvalContext{
				Now:  e.cfg.Clock.Now(),
				Cfg:  e.cfg.Policy,
				Rand: defaultRand,
			}
			if e.cfg.Bridge != nil {
				ec.Net = e.cfg.Bridge.Path()
			} else {
				ec.Net = e.state.net
			}
			if e.cfg.Store != nil {
				ec.Store = e.cfg.Store.Snapshot()
			}
			next, actions := Reduce(e.state, ev, ec)
			e.state = next
			for _, a := range actions {
				e.execute(a, ec)
			}
			e.recordOutcome(ev, ec)
		}
	}
}

func (e *Engine) execute(a Action, ec EvalContext) {
	switch a.Kind {
	case ActStartTunnel:
		if e.cfg.Tunnel != nil {
			e.cfg.Tunnel.Start(a.Profile)
		}
	case ActStopTunnel:
		if e.cfg.Tunnel != nil {
			e.cfg.Tunnel.Stop()
		}
	case ActRunProbe:
		if e.cfg.Prober != nil {
			e.cfg.Prober.Run(a.ProbeKind)
		}
	case ActSetCadence:
		if e.cfg.Prober != nil {
			e.cfg.Prober.SetCadence(a.Cadence)
		}
	case ActArmTimer:
		if a.Timer == TimerNone {
			e.cancelAllTimers()
			return
		}
		e.cancelTimer(a.Timer)
		tk := a.Timer
		e.timers[tk] = e.cfg.Clock.AfterFunc(a.TimerDur, func() {
			e.Submit(Event{Kind: EvTimer, Timer: tk})
		})
	case ActCancelTimer:
		if a.Timer == TimerNone {
			e.cancelAllTimers()
		} else {
			e.cancelTimer(a.Timer)
		}
	case ActNotify:
		if e.cfg.Notify != nil {
			e.cfg.Notify.Notify(a.NotifyKey)
		}
	case ActEmitDecision:
		if a.Decision != nil {
			e.log.Add(*a.Decision)
			select {
			case e.out <- *a.Decision:
			default:
			}
		}
	}
}

func (e *Engine) recordOutcome(ev Event, ec EvalContext) {
	if e.cfg.Store == nil {
		return
	}
	switch ev.Kind {
	case EvHandshakeFail:
		e.cfg.Store.RecordOutcome(ev.Profile, ec.Net.Key, false, FailHandshake)
	case EvValidateFail:
		e.cfg.Store.RecordOutcome(ev.Profile, ec.Net.Key, false, FailValidate)
	case EvValidateOK:
		e.cfg.Store.RecordOutcome(e.state.Active, ec.Net.Key, true, FailNone)
	}
}

func (e *Engine) cancelTimer(tk TimerKind) {
	if t, ok := e.timers[tk]; ok && t != nil {
		t.Stop()
		delete(e.timers, tk)
	}
}

func (e *Engine) cancelAllTimers() {
	for tk, t := range e.timers {
		if t != nil {
			t.Stop()
		}
		delete(e.timers, tk)
	}
}

// defaultRand is a tiny deterministic-by-seed PRNG used for backoff jitter in
// production. Tests inject their own via EvalContext.Rand.
var randState uint32 = 0x9e3779b9

func defaultRand() uint32 {
	// xorshift32
	randState ^= randState << 13
	randState ^= randState >> 17
	randState ^= randState << 5
	return randState
}
