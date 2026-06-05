// Package ffi is the cross-platform foreign-function surface for the Smart
// Decision Engine. It exposes a tiny, JSON/string-only, object-oriented API
// that both gomobile (Android AAR) and cgo c-shared (iOS xcframework) can bind
// without exposing the engine's internal enum ordinals.
//
// The contract mirrors the desktop shadow bridge (desktop/engine_bridge.go)
// field-for-field: the platform shell OBSERVES the real connection lifecycle
// (connect / disconnect / health transitions) and POLLS the explainable
// decision log for display. In shadow mode the engine drives nothing — the
// tunnel/prober spokes are no-ops — so wiring this in is zero behaviour change.
// Flipping to active selection is a later slice (same as desktop).
package ffi

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	eng "github.com/hoep/privycs-vpn/engine"
)

// Session is one engine instance bound to a platform shell. gomobile exposes it
// as an object with methods; the cgo wrapper keeps these behind an int handle.
type Session struct {
	eng    *eng.Engine
	cancel context.CancelFunc
	store  *shadowStore

	mu       sync.Mutex
	log      []decisionDTO // drained from eng.Decisions(); newest last
	cap      int
	country  string // user's pre-VPN country (for the network-aware reason)
	awgAvail bool   // does the active connection offer an AmneziaWG profile
}

// decisionDTO is the wire shape returned by PollDecisions — identical JSON to
// the desktop EngineDecisionDTO so all three UIs share one rendering contract.
// reason/reasonArgs carry the network-aware explanation (a stable i18n key +
// the country code) the UI renders as a secondary line.
type decisionDTO struct {
	At         string   `json:"at"`
	From       string   `json:"from"`
	To         string   `json:"to"`
	Rule       string   `json:"rule"`
	Active     string   `json:"active"`
	Chosen     string   `json:"chosen"`
	Key        string   `json:"key"`
	Args       []string `json:"args"`
	Reason     string   `json:"reason"`
	ReasonArgs []string `json:"reasonArgs"`
}

// NewSession builds a shadow-mode engine session. profilesJSON is the JSON
// array of protocol-failover-order tokens (e.g. ["wireguard","amnezia",
// "openvpn","ipsec"]) — the same candidate set the desktop bridge feeds. An
// empty or invalid JSON falls back to the default order.
//
// Named NewSession (not New) so the gomobile Java binding is an unambiguous
// Ffi.newSession(...) — `new` is a reserved Java keyword.
func NewSession(profilesJSON string) *Session {
	var order []string
	if profilesJSON != "" {
		_ = json.Unmarshal([]byte(profilesJSON), &order)
	}
	store := &shadowStore{order: order}
	s := &Session{cap: 50, store: store}
	s.eng = eng.New(eng.Config{
		Policy: eng.DefaultPolicy(),
		Tunnel: shadowTunnel{},
		Prober: shadowProber{},
		Store:  store,
		Notify: nopNotifier{},
	})
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	go s.eng.Run(ctx)
	go s.drain(ctx)
	return s
}

func (s *Session) drain(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case d := <-s.eng.Decisions():
			s.mu.Lock()
			reasonKey, reasonArgs := reasonFor(d, s.country, s.awgAvail)
			s.log = append(s.log, decisionDTO{
				At:         d.At.Format(time.RFC3339),
				From:       d.From.String(),
				To:         d.To.String(),
				Rule:       d.Rule,
				Active:     string(d.Active),
				Chosen:     string(d.Chosen),
				Key:        d.HumanKey,
				Args:       d.Args,
				Reason:     reasonKey,
				ReasonArgs: reasonArgs,
			})
			if len(s.log) > s.cap {
				s.log = s.log[len(s.log)-s.cap:]
			}
			s.mu.Unlock()
		}
	}
}

// ObserveConnect mirrors desktop bridge.observeConnect: user-connect →
// handshake-ok → a healthy validation probe (drives Idle→…→Connected in shadow).
// protocol is the ACTUAL connected protocol token ("wireguard"/"amneziawg"/
// "openvpn"/"ipsec"); it becomes the engine's sole candidate so the decision
// log reflects reality ("Connected via <protocol>") rather than a hypothetical
// failover-order pick. Empty falls back to the configured order.
// country is the user's pre-VPN country code (ISO-3166-1 alpha-2; "" if
// unknown) and awgAvailable reports whether the active connection offers an
// AmneziaWG profile — together they drive the network-aware decision reason.
func (s *Session) ObserveConnect(protocol, country string, awgAvailable bool) {
	if s == nil {
		return
	}
	s.store.setActive(protocol)
	s.mu.Lock()
	s.country = country
	s.awgAvail = awgAvailable
	s.mu.Unlock()
	s.eng.Submit(eng.Event{Kind: eng.EvUserConnect})
	s.eng.Submit(eng.Event{Kind: eng.EvHandshakeOK})
	s.eng.Submit(probeEvent(true, 30, 0))
}

// ObserveDisconnect mirrors desktop bridge.observeDisconnect.
func (s *Session) ObserveDisconnect() {
	if s == nil {
		return
	}
	s.store.setActive("")
	s.eng.Submit(eng.Event{Kind: eng.EvUserDisconnect})
}

// ObserveHealth maps a platform health-monitor transition onto engine probe
// samples. Matches desktop bridge.observeHealth: the platform monitor fires
// only on a CONFIRMED transition, so degraded/recovering emit two samples to
// carry the engine past its own debounce. state ∈ {healthy,degraded,recovering}.
func (s *Session) ObserveHealth(state string) {
	if s == nil {
		return
	}
	switch state {
	case "healthy":
		s.eng.Submit(probeEvent(true, 30, 0))
	case "degraded":
		s.eng.Submit(probeEvent(true, 500, 0))
		s.eng.Submit(probeEvent(true, 500, 0))
	case "recovering":
		s.eng.Submit(probeEvent(false, 5000, 500000))
		s.eng.Submit(probeEvent(false, 5000, 500000))
	}
}

// PollDecisions returns the recent decision log as a JSON array (newest last).
// Empty log → "[]". This is what the Settings "what the engine decided & why"
// panel renders; Key is the stable i18n key (never pre-translated text).
func (s *Session) PollDecisions() string {
	if s == nil {
		return "[]"
	}
	s.mu.Lock()
	out := make([]decisionDTO, len(s.log))
	copy(out, s.log)
	s.mu.Unlock()
	b, err := json.Marshal(out)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// Close stops the engine goroutines. Safe to call once.
func (s *Session) Close() {
	if s == nil || s.cancel == nil {
		return
	}
	s.cancel()
}

func probeEvent(ok bool, rtt, loss int32) eng.Event {
	return eng.Event{Kind: eng.EvProbeResult, Probe: eng.ProbeResult{Kind: eng.ProbePath, OK: ok, RTTms: rtt, LossPpm: loss}}
}

// ── shadow spokes (no-ops; the engine drives nothing in shadow) ──

type shadowTunnel struct{}

func (shadowTunnel) Start(eng.ProfileID) {}
func (shadowTunnel) Stop()               {}

type shadowProber struct{}

func (shadowProber) Run(eng.ProbeKind)        {}
func (shadowProber) SetCadence(time.Duration) {}

type nopNotifier struct{}

func (nopNotifier) Notify(string) {}

// shadowStore presents the candidate set. Once a real connect is observed it
// returns ONLY the active protocol, so the engine's pick (and the decision log)
// reflects the actual connection; before/after that it falls back to the
// configured failover order. Matches desktop's shadowStore.
type shadowStore struct {
	mu     sync.Mutex
	order  []string
	active string
}

func (s *shadowStore) setActive(p string) {
	s.mu.Lock()
	s.active = p
	s.mu.Unlock()
}

func (s *shadowStore) Snapshot() eng.ProfileSnapshot {
	s.mu.Lock()
	order, active := s.order, s.active
	s.mu.Unlock()
	if active != "" {
		order = []string{active}
	} else if len(order) == 0 {
		order = []string{"wireguard", "amnezia", "openvpn", "ipsec"}
	}
	var profs []eng.Profile
	for _, p := range order {
		profs = append(profs, eng.Profile{
			ID:        eng.ProfileID(p),
			Protocol:  protoFromString(p),
			Endpoints: []eng.Endpoint{{Host: p, Port: 443}},
		})
	}
	return eng.ProfileSnapshot{Profiles: profs}
}

func (s *shadowStore) RecordOutcome(eng.ProfileID, eng.NetworkKey, bool, eng.FailKind) {}

// reasonFor computes the network-aware reason for a decision from the user's
// country + AmneziaWG availability. The protocol is taken from the decision's
// chosen (or active) profile.
func reasonFor(d eng.Decision, country string, awgAvail bool) (string, []string) {
	token := string(d.Chosen)
	if token == "" {
		token = string(d.Active)
	}
	if token == "" {
		return eng.ReasonFor(d.HumanKey, eng.ProtoWireGuard, false, country, awgAvail)
	}
	return eng.ReasonFor(d.HumanKey, protoFromString(token), true, country, awgAvail)
}

func protoFromString(p string) eng.Protocol {
	switch p {
	case "wireguard":
		return eng.ProtoWireGuard
	case "amneziawg", "amnezia":
		return eng.ProtoAmnezia
	case "openvpn":
		return eng.ProtoOpenVPN
	case "ipsec":
		return eng.ProtoIPsec
	}
	return eng.ProtoWireGuard
}
