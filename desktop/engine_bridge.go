package main

import (
	"context"
	"log"
	"sync"
	"time"

	eng "github.com/hoep/privycs-vpn/engine"
)

// decisionBridge wires the headless Smart Decision Engine (engine/) into the
// desktop app in SHADOW MODE: it observes the real connection lifecycle +
// health + (later) network changes, runs the engine's pure FSM, and LOGS what
// the engine WOULD decide — without taking any action (the tunnel/prober spokes
// are no-ops). This lets us validate the engine against real behavior before
// the AutoProtocolSelection toggle flips it to active (P1b). Zero behavior
// change while shadowing.
type decisionBridge struct {
	eng    *eng.Engine
	cancel context.CancelFunc
	store  *shadowStore

	mu       sync.Mutex
	country  string // user's pre-VPN country for the network-aware reason
	awgAvail bool   // active connection offers an AmneziaWG profile
}

func newDecisionBridge(getOrder func() []string) *decisionBridge {
	store := &shadowStore{getOrder: getOrder}
	e := eng.New(eng.Config{
		Policy: eng.DefaultPolicy(),
		Tunnel: shadowTunnel{},
		Prober: shadowProber{},
		Store:  store,
		Notify: logNotifier{},
		// No PlatformBridge: in shadow we feed events explicitly via Observe*.
	})
	return &decisionBridge{eng: e, store: store}
}

func (b *decisionBridge) start(ctx context.Context) {
	cctx, cancel := context.WithCancel(ctx)
	b.cancel = cancel
	go b.eng.Run(cctx)
	go func() {
		for {
			select {
			case <-cctx.Done():
				return
			case d := <-b.eng.Decisions():
				// Human-readable "what + why"; surfaces in LogsView. The
				// HumanKey is the stable i18n key the UI will localize (P1b).
				log.Printf("[engine/shadow] %s→%s rule=%q active=%q chosen=%q key=%q",
					d.From, d.To, d.Rule, d.Active, d.Chosen, d.HumanKey)
			}
		}
	}()
}

func (b *decisionBridge) stop() {
	if b.cancel != nil {
		b.cancel()
	}
}

// EngineDecisionDTO is the Wails-marshalled decision record for the UI's
// "what the engine decided & why" panel. Key is the stable i18n key the
// frontend localizes (the engine never emits pre-translated text).
type EngineDecisionDTO struct {
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

func (b *decisionBridge) recent(n int) []EngineDecisionDTO {
	if b == nil {
		return nil
	}
	recs := b.eng.Log().Recent(n)
	b.mu.Lock()
	country, awg := b.country, b.awgAvail
	b.mu.Unlock()
	out := make([]EngineDecisionDTO, 0, len(recs))
	for _, r := range recs {
		token := string(r.Chosen)
		if token == "" {
			token = string(r.Active)
		}
		reasonKey, reasonArgs := eng.ReasonFor(r.HumanKey, protoFromString(token), token != "", country, awg)
		out = append(out, EngineDecisionDTO{
			At:         r.At.Format(time.RFC3339),
			From:       r.From.String(),
			To:         r.To.String(),
			Rule:       r.Rule,
			Active:     string(r.Active),
			Chosen:     string(r.Chosen),
			Key:        r.HumanKey,
			Args:       r.Args,
			Reason:     reasonKey,
			ReasonArgs: reasonArgs,
		})
	}
	return out
}

// activeConnHasAWG reports whether the active connection offers an AmneziaWG
// profile, so the engine can recommend it as the reason on restrictive networks.
func (a *App) activeConnHasAWG() bool {
	conn := a.connections.Active()
	if conn == nil {
		return false
	}
	for _, pc := range conn.Protocols {
		if pc != nil && pc.Protocol == "amneziawg" {
			return true
		}
	}
	return false
}

// enginePickProtocol returns the engine's network-aware protocol token for the
// active connection (excluding the given tokens, for failover), or "" if the
// engine has no usable pick. Used only when AutoProtocolSelection is on.
func (a *App) enginePickProtocol(exclude []string) string {
	conn := a.connections.Active()
	if conn == nil {
		return ""
	}
	var avail []eng.Protocol
	for _, tok := range conn.AvailableProtocols() {
		if p, ok := eng.ParseProtocol(tok); ok {
			avail = append(avail, p)
		}
	}
	var excl []eng.Protocol
	for _, tok := range exclude {
		if p, ok := eng.ParseProtocol(tok); ok {
			excl = append(excl, p)
		}
	}
	res := eng.Select(eng.SelectInput{Available: avail, Country: a.SelfIPCountry(), Exclude: excl})
	if !res.Found {
		return ""
	}
	return res.Protocol.Token()
}

// engineFailoverOrder returns the engine's country-aware protocol order as
// tokens (most-preferred first) for driving failover when the engine is active.
func (a *App) engineFailoverOrder() []string {
	var out []string
	for _, p := range eng.ProtocolOrder(a.SelfIPCountry()) {
		out = append(out, p.Token())
	}
	return out
}

// EngineDecisions is the Wails-bound accessor for the recent Smart-Decision-
// Engine decision log (newest last). Powers the Settings "Engine decisions"
// panel.
func (a *App) EngineDecisions() []EngineDecisionDTO {
	if a.engineBridge == nil {
		return nil
	}
	return a.engineBridge.recent(50)
}

// ── observations fed from the real app flow ──

// observeConnect feeds the engine a real connect. protocol is the ACTUAL
// connected protocol token; it becomes the engine's sole candidate so the
// decision log reflects reality ("Connected via <protocol>") instead of a
// hypothetical failover-order pick.
// country is the user's pre-VPN country (ISO alpha-2, from selfip) and
// awgAvailable reports whether the active connection has an AmneziaWG profile —
// together they drive the network-aware decision reason.
func (b *decisionBridge) observeConnect(protocol, country string, awgAvailable bool) {
	if b == nil {
		return
	}
	b.store.setActive(protocol)
	b.mu.Lock()
	b.country = country
	b.awgAvail = awgAvailable
	b.mu.Unlock()
	b.eng.Submit(eng.Event{Kind: eng.EvUserConnect})
	b.eng.Submit(eng.Event{Kind: eng.EvHandshakeOK})
	b.eng.Submit(probeEvent(true, 30, 0)) // validate → Connected in shadow
}

func (b *decisionBridge) observeDisconnect() {
	if b == nil {
		return
	}
	b.store.setActive("")
	b.eng.Submit(eng.Event{Kind: eng.EvUserDisconnect})
}

// observeHealth maps the desktop TunnelHealthMonitor's (already-debounced) state
// transitions onto engine probe samples. The desktop monitor only fires on a
// CONFIRMED transition, so we emit two samples to carry the engine past its own
// debounce — keeping the shadow verdict aligned with reality.
func (b *decisionBridge) observeHealth(s TunnelHealthState) {
	if b == nil {
		return
	}
	switch s {
	case TunnelHealthHealthy:
		b.eng.Submit(probeEvent(true, 30, 0))
	case TunnelHealthDegraded:
		b.eng.Submit(probeEvent(true, 500, 0))
		b.eng.Submit(probeEvent(true, 500, 0))
	case TunnelHealthRecovering:
		b.eng.Submit(probeEvent(false, 5000, 500000))
		b.eng.Submit(probeEvent(false, 5000, 500000))
	}
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

type logNotifier struct{}

func (logNotifier) Notify(key string) { log.Printf("[engine/shadow] notify %q", key) }

// shadowStore presents the configured protocol-failover order as the candidate
// set so the engine's static selector has something to choose from. Real
// per-connection profiles + persisted stats arrive with active mode / P4.
type shadowStore struct {
	getOrder func() []string
	mu       sync.Mutex
	active   string
}

func (s *shadowStore) setActive(p string) {
	s.mu.Lock()
	s.active = p
	s.mu.Unlock()
}

func (s *shadowStore) Snapshot() eng.ProfileSnapshot {
	s.mu.Lock()
	active := s.active
	s.mu.Unlock()
	var order []string
	if active != "" {
		order = []string{active} // reflect the real connection, not a hypothetical pick
	} else {
		order = s.getOrder()
	}
	if len(order) == 0 {
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
