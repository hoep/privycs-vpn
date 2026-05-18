package main

import (
	"log"
	"strings"
	"sync"
	"time"
)

const (
	// Befund 3 (Android-parität) — debounce window applied by the
	// single eval consumer. A platform event fires an immediate
	// trigger plus a deliberate +2s follow-up; the safety ticker and
	// Reevaluate() add more. Collapsing a sub-second burst into one
	// serialized evaluation removes the data race on lastRuleKey and
	// the unserialized connect/disconnect calls that concurrent
	// checkAndAct invocations had. 250ms << the intentional 2s
	// follow-up, so that re-check still lands as its own evaluation.
	networkMonitorDebounce = 250 * time.Millisecond

	// Befund 4 — startup grace. Within this window after Start(), an
	// "except"-mode WiFi whose SSID is not yet resolved is treated
	// conservatively (defer connect) instead of the steady-state
	// "unknown SSID = untrusted = connect" default, so the app does
	// not briefly auto-connect on a possibly-trusted WiFi before the
	// SSID has populated (D-Bus/WlanAPI lag, association settling —
	// the same case the run() "+2s follow-up" comment acknowledges).
	// After the window the documented steady-state behaviour resumes.
	networkMonitorStartupGrace = 15 * time.Second
)

// NetworkState represents the current network environment
type NetworkState struct {
	NetworkType string `json:"network_type"` // "wifi", "ethernet", "mobile", "none"
	SSID        string `json:"ssid"`         // current WiFi SSID, empty if not on WiFi
	RuleMatch   bool   `json:"rule_match"`   // whether connect-on-demand rules match
}

// NetworkMonitor watches for network state changes via platform-specific
// event APIs and triggers VPN connect/disconnect based on
// ConnectOnDemandSettings rules.  A safety poll at 60-second intervals
// ensures changes are never missed even if the OS event is lost.
type NetworkMonitor struct {
	mu           sync.Mutex
	running      bool
	stopCh       chan struct{}
	settings     *ConnectOnDemandSettings
	connectFn    func()
	disconnectFn func()
	isConnected  func() bool
	isPaused     func() bool // optional - when set and returns true, monitor suppresses all actions
	// ruleResolver returns the RuleResolution for the current
	// network state; empty Action = no rule matched, fall through
	// to legacy COD logic. Set via SetRuleEngine.
	ruleResolver func(networkType, ssid, bssid string) RuleResolution
	// ruleApplier is invoked when a rule matched. Receives the
	// resolution and is responsible for triggering the right
	// switch/disconnect path. Set via SetRuleEngine.
	ruleApplier func(RuleResolution)
	// lastRuleKey tracks the most recently applied rule
	// resolution so we only fire the applier on TRANSITION (=
	// resolved target changed). Without this guard the engine
	// fires applier on every poll tick, which causes
	// ActivatePool / SwitchActiveConnection / disconnectInternal
	// to ping-pong: tunnel-up triggers a new network event ->
	// next tick -> apply -> disconnect -> reconnect -> loop.
	lastRuleKey     string
	lastState       NetworkState
	stopWatcher     func() // platform watcher teardown
	changeObservers []func()

	// Befund 3 — conflated trigger consumed by ONE goroutine. Every
	// trigger source (platform watcher immediate + 2s follow-up,
	// 60s safety ticker, Reevaluate, initial) sends here instead of
	// calling checkAndAct directly / in its own goroutine. Buffered
	// cap-1 = conflated: extra triggers while one is pending are
	// dropped (the pending one already covers them). Created fresh
	// per Start() so a Stop()+Start() cycle is restart-safe.
	evalCh    chan struct{}
	startedAt time.Time
}

// SetRuleEngine wires the per-network rules engine into the
// monitor's evaluator. Called from App.startup once the
// NetworkRulesRegistry is initialised. Both callbacks may be
// nil to disable the rules engine entirely.
func (nm *NetworkMonitor) SetRuleEngine(
	resolver func(networkType, ssid, bssid string) RuleResolution,
	applier func(RuleResolution),
) {
	nm.mu.Lock()
	nm.ruleResolver = resolver
	nm.ruleApplier = applier
	nm.mu.Unlock()
}

// OnChange registers a callback fired (asynchronously, on its own
// goroutine) whenever the platform watcher reports a network change.
// Used by Self-IP-Cache to invalidate its cached country on network
// roam, and by Pool to re-pick a Geo-Nearest member after the user's
// location changes. Callbacks must be cheap and non-blocking - the
// monitor does not serialise them.
func (nm *NetworkMonitor) OnChange(fn func()) {
	if fn == nil {
		return
	}
	nm.mu.Lock()
	nm.changeObservers = append(nm.changeObservers, fn)
	nm.mu.Unlock()
}

// fireChangeObservers dispatches all registered observers on
// independent goroutines so a slow observer cannot block the monitor.
func (nm *NetworkMonitor) fireChangeObservers() {
	nm.mu.Lock()
	obs := make([]func(), len(nm.changeObservers))
	copy(obs, nm.changeObservers)
	nm.mu.Unlock()
	for _, fn := range obs {
		go func(f func()) {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("Network monitor: observer panic recovered: %v", r)
				}
			}()
			f()
		}(fn)
	}
}

// SetPauseCheck installs an optional callback the monitor consults
// before firing any connect/disconnect action. If the callback
// returns true the tick is treated as a no-op - this lets a user
// pause the auto-management without having to stop the entire
// monitor (which would lose the platform-watcher subscription).
func (nm *NetworkMonitor) SetPauseCheck(fn func() bool) {
	nm.mu.Lock()
	nm.isPaused = fn
	nm.mu.Unlock()
}

// NewNetworkMonitor creates a new network monitor instance
func NewNetworkMonitor() *NetworkMonitor {
	return &NetworkMonitor{}
}

// Start begins watching for network changes using native OS event APIs
// with a 60-second safety poll fallback.
// connectFn is called when rules match and VPN is disconnected.
// disconnectFn is called when rules no longer match and VPN is connected (can be nil).
// isConnectedFn reports whether the VPN is currently connected.
func (nm *NetworkMonitor) Start(settings *ConnectOnDemandSettings, connectFn func(), disconnectFn func(), isConnectedFn func() bool) {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	if nm.running {
		return
	}

	nm.settings = settings
	nm.connectFn = connectFn
	nm.disconnectFn = disconnectFn
	nm.isConnected = isConnectedFn
	nm.stopCh = make(chan struct{})
	nm.evalCh = make(chan struct{}, 1)
	nm.startedAt = time.Now()
	nm.running = true

	log.Printf("Network monitor: started (trigger=%s, ssid_mode=%s)", settings.Trigger, settings.SSIDMode)

	go nm.run()
}

// Stop ends network monitoring
func (nm *NetworkMonitor) Stop() {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	if !nm.running {
		return
	}

	close(nm.stopCh)
	if nm.stopWatcher != nil {
		nm.stopWatcher()
		nm.stopWatcher = nil
	}
	nm.evalCh = nil
	nm.running = false
	log.Println("Network monitor: stopped")
}

// trigger requests one evaluation via the conflated channel (Befund
// 3). Non-blocking: if a request is already pending it is dropped —
// the pending evaluation will observe the latest state anyway. Safe
// to call before Start()/after Stop() (nil channel = no-op).
func (nm *NetworkMonitor) trigger() {
	nm.mu.Lock()
	ch := nm.evalCh
	nm.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- struct{}{}:
	default:
	}
}

// inStartupGrace reports whether we are still within
// networkMonitorStartupGrace of Start() (Befund 4).
func (nm *NetworkMonitor) inStartupGrace() bool {
	nm.mu.Lock()
	t := nm.startedAt
	nm.mu.Unlock()
	return !t.IsZero() && time.Since(t) < networkMonitorStartupGrace
}

// IsRunning returns whether the monitor is active
func (nm *NetworkMonitor) IsRunning() bool {
	nm.mu.Lock()
	defer nm.mu.Unlock()
	return nm.running
}

// CurrentState returns the last observed network state
func (nm *NetworkMonitor) CurrentState() NetworkState {
	nm.mu.Lock()
	defer nm.mu.Unlock()
	return nm.lastState
}

// UpdateSettings updates the rules without restarting the monitor.
// Does NOT trigger an immediate re-evaluation - caller should follow
// with Reevaluate() if the settings change should take effect right
// away (e.g. user-initiated trigger or SSID list change).
func (nm *NetworkMonitor) UpdateSettings(settings *ConnectOnDemandSettings) {
	nm.mu.Lock()
	defer nm.mu.Unlock()
	nm.settings = settings
}

// Reevaluate triggers an immediate rules check off the monitor's
// own goroutines. Used after settings changes so the user sees
// connect/disconnect actions take effect within ~1s instead of
// waiting up to 60s for the next safety-poll tick.
//
// Why a separate method instead of folding it into UpdateSettings:
// some callers (e.g. internal state-sync paths) want to push new
// settings WITHOUT firing a re-eval. Keeping the two operations
// separate lets the caller pick the right behaviour.
func (nm *NetworkMonitor) Reevaluate() {
	nm.trigger()
}

// run sets up the platform event watcher and the safety poll timer.
func (nm *NetworkMonitor) run() {
	// Initial delay to let app finish startup
	select {
	case <-time.After(3 * time.Second):
	case <-nm.stopCh:
		return
	}

	// Befund 3 — single eval consumer. ALL trigger sources feed
	// nm.trigger(); this one goroutine serializes evaluateOnce() so
	// lastRuleKey / connectFn / disconnectFn are never touched
	// concurrently.
	go nm.runEvalConsumer()

	// Start native OS event watcher.
	//
	// Each platform event triggers TWO checkAndAct calls: one
	// immediate, one 2s later. The follow-up catches transient
	// states that were not yet visible at event-time:
	//
	//   - WLAN association in progress when NotifyAddrChange fired
	//     (IP is up but SSID slot in netsh output is still empty).
	//   - DHCP renewal racing with default-route update.
	//   - Network type detection that depends on multiple
	//     interfaces stabilising.
	//
	// 2s is empirically enough on Windows for SSID to populate
	// after IP-acquired; on Linux/macOS the platform events tend to
	// be more synchronous so the follow-up is just a no-op.
	stopWatcher, err := startPlatformWatcher(func() {
		nm.fireChangeObservers()
		nm.trigger()
		go func() {
			time.Sleep(2 * time.Second)
			nm.trigger()
		}()
	})
	if err != nil {
		log.Printf("Network monitor: platform watcher failed (%v), using poll-only mode", err)
	} else {
		nm.mu.Lock()
		nm.stopWatcher = stopWatcher
		nm.mu.Unlock()
	}

	// Run initial check immediately
	nm.trigger()

	// Safety poll at 60-second intervals in case an OS event is missed
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-nm.stopCh:
			return
		case <-ticker.C:
			nm.trigger()
		}
	}
}

// runEvalConsumer is the single goroutine that performs evaluations
// (Befund 3). It owns lastRuleKey and is the only caller of
// evaluateOnce(), so no evaluation ever races another. A short
// debounce collapses the immediate+ticker burst; the deliberate +2s
// platform-watcher follow-up (2s >> debounce) still arrives as its
// own trigger and re-evaluates.
func (nm *NetworkMonitor) runEvalConsumer() {
	for {
		select {
		case <-nm.stopCh:
			return
		case <-nm.evalCh:
			select {
			case <-time.After(networkMonitorDebounce):
			case <-nm.stopCh:
				return
			}
			nm.evaluateOnce()
		}
	}
}

// evaluateOnce performs one full rule evaluation + action. Called
// ONLY by runEvalConsumer (Befund 3) so lastRuleKey and the
// connect/disconnect calls are never concurrent.
func (nm *NetworkMonitor) evaluateOnce() {
	state := detectNetworkState()

	nm.mu.Lock()
	settings := nm.settings
	pauseCheck := nm.isPaused
	nm.lastState = state
	nm.mu.Unlock()

	if settings == nil {
		return
	}

	// Pause guard: if a user-initiated pause is currently active,
	// skip the entire match-evaluation + action step. The monitor
	// stays subscribed to platform events so it resumes immediately
	// when the pause expires (next event tick re-evaluates fresh).
	// Without this, a COD trigger="any" rule would fire connect()
	// on the next platform event, defeating the pause within
	// seconds.
	if pauseCheck != nil && pauseCheck() {
		log.Printf("Network monitor: paused - skipping rule eval (type=%s ssid=%q)",
			state.NetworkType, state.SSID)
		return
	}

	// Phase 2: rule engine takes priority over legacy COD logic.
	// When the resolver returns a non-empty Action, that resolution
	// drives the lifecycle and we skip the legacy match-based
	// path. Empty Action / nil resolver = fall through to legacy.
	if nm.ruleResolver != nil {
		bssid := detectCurrentBssid()
		if res := nm.ruleResolver(state.NetworkType, state.SSID, bssid); res.Action != "" {
			// Transition guard: only apply when the resolved
			// target changed since the last applied rule. Without
			// this every tick re-applies the same rule which
			// triggers ActivatePool / disconnect / reconnect in
			// a ping-pong loop. The legacy COD logic below is
			// already idempotent so we skip it on a quiet rule
			// match too.
			key := res.Action + ":" + res.TargetID
			if key != nm.lastRuleKey {
				nm.lastRuleKey = key
				log.Printf("Network monitor: rule transition -> %s (%s)", res.Action, res.TargetID)
				if nm.ruleApplier != nil {
					nm.ruleApplier(res)
				}
			}
			return
		}
		// No rule matched - reset the transition key so the next
		// match fires applier even if it lands on the previously
		// applied target.
		nm.lastRuleKey = ""
	}

	match := evaluateRules(settings, &state)
	state.RuleMatch = match

	nm.mu.Lock()
	nm.lastState = state
	nm.mu.Unlock()

	connected := nm.isConnected()

	// Diagnostic log: every checkAndAct emits one line summarising
	// what was detected and what action (if any) followed. Helps the
	// user explain "I joined a matching WiFi, why did nothing
	// happen?" - the log shows whether type/ssid were detected
	// correctly, whether match=true, and whether the action fired.
	log.Printf("Network monitor: type=%s ssid=%q match=%v connected=%v trigger=%s ssid_mode=%s",
		state.NetworkType, state.SSID, match, connected,
		settings.Trigger, settings.SSIDMode)

	// Simple match-based actions. Connect when rules match and VPN
	// is down; disconnect when rules do not match and VPN is up.
	//
	// This intentionally treats COD-enabled as authoritative: if the
	// user has COD on with restrictive rules, manual connects that
	// fall outside those rules will be torn down. If the user wants
	// manual control, they should either disable COD or set the
	// trigger to "any". An earlier transition-based variant (only
	// fire on edge prev->current) was tried but had a hole: VPN that
	// was already up at app start with no-longer-matching rules
	// stayed up indefinitely, contradicting user expectation.
	// Befund 4 — startup-grace conservatism. evaluateRules() returns
	// true for except-mode WiFi with an empty SSID ("unknown =
	// untrusted = connect"), which is the right steady-state default
	// but wrong in the first seconds after Start() when the SSID has
	// simply not populated yet (D-Bus/WlanAPI lag) — it would briefly
	// auto-connect on a possibly-trusted WiFi. Defer; the SSID
	// resolves within seconds and the next trigger re-evaluates.
	if match && !connected && nm.inStartupGrace() &&
		state.NetworkType == "wifi" && state.SSID == "" &&
		settings.SSIDMode == "except" {
		log.Printf("Network monitor: startup grace - except-mode WiFi SSID not yet resolved, deferring connect")
		return
	}

	if match && !connected {
		log.Printf("Network monitor: triggering connect")
		nm.connectFn()
	} else if !match && connected && nm.disconnectFn != nil {
		log.Printf("Network monitor: triggering disconnect")
		nm.disconnectFn()
	}
}

// detectNetworkState determines the current network type and WiFi SSID
// using platform-specific functions.
func detectNetworkState() NetworkState {
	netType := getNetworkTypePlatform()
	state := NetworkState{NetworkType: netType}

	if netType == "wifi" {
		state.SSID = getCurrentSSIDPlatform()
	}

	return state
}

// evaluateRules checks whether the current network state matches the
// connect-on-demand rules defined in settings.
func evaluateRules(settings *ConnectOnDemandSettings, state *NetworkState) bool {
	if !settings.Enabled {
		return false
	}

	// Check trigger type match.
	//
	// Desktop-relevant additions over the original wifi/mobile/
	// wifi_mobile triple:
	//   - "ethernet": only on wired connections (e.g. "VPN at the
	//     office desk; not from my laptop on the go").
	//   - "any": matches as long as any non-loopback connectivity is
	//     present. Right default for desktop where the user does not
	//     need to think about network class at all.
	triggerMatch := false
	switch settings.Trigger {
	case "wifi":
		triggerMatch = state.NetworkType == "wifi"
	case "mobile":
		triggerMatch = state.NetworkType == "mobile"
	case "ethernet":
		triggerMatch = state.NetworkType == "ethernet"
	case "wifi_mobile":
		triggerMatch = state.NetworkType == "wifi" || state.NetworkType == "mobile"
	case "any":
		triggerMatch = state.NetworkType != "none"
	default:
		// Unknown trigger - safest default is "any" so users do not
		// silently lose COD on settings.json upgrades that introduce
		// new trigger names.
		triggerMatch = state.NetworkType != "none"
	}

	if !triggerMatch {
		return false
	}

	// SSID filter rules. Apply ONLY when on WiFi; for non-WiFi
	// transports (mobile / ethernet) the trigger-type check above
	// is sufficient and we already passed it. Empty-SSID handling
	// matches the Android NetworkMonitor.evaluateRules() defaults
	// so identical user-configured rules behave the same on both
	// platforms (v0.9.14.73 alignment).
	if state.NetworkType == "wifi" {
		switch settings.SSIDMode {
		case "only":
			// Only connect on listed SSIDs. Empty-SSID = cannot
			// determine which WiFi we're on → conservative refuse:
			// the whole point of "only [home, office]" is to NOT
			// connect on unknown networks. Pre-v0.9.14.73 desktop
			// fell through to the unconditional `return true` below
			// when SSID detection failed and silently connected
			// against user intent.
			list := filterNonBlank(settings.SSIDList)
			switch {
			case len(list) == 0:
				return false
			case state.SSID == "":
				return false
			case !ssidInList(state.SSID, list):
				return false
			}
		case "except":
			// Connect on all SSIDs except listed ones. Empty-SSID
			// here is treated as "untrusted by default" → connect.
			// Symmetric mirror of the "only" empty-SSID-conservative
			// path: when in doubt, the more-protective decision wins.
			list := filterNonBlank(settings.SSIDList)
			if len(list) > 0 && state.SSID != "" && ssidInList(state.SSID, list) {
				return false
			}
		case "all":
			// Connect on any WiFi - no filtering needed.
		default:
			// Unknown mode, treat as "all".
		}
	}

	return true
}

// filterNonBlank returns a copy of list with empty / whitespace-only
// entries removed. The Settings UI accepts an editable comma-list and
// blank entries can leak in if the user types ", ," — without this
// filter an unintentionally-blank list would short-circuit "only"
// to false-on-everything.
func filterNonBlank(list []string) []string {
	out := make([]string, 0, len(list))
	for _, s := range list {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

// ssidInList checks whether ssid appears in the list (case-insensitive)
func ssidInList(ssid string, list []string) bool {
	lower := strings.ToLower(ssid)
	for _, s := range list {
		if strings.ToLower(strings.TrimSpace(s)) == lower {
			return true
		}
	}
	return false
}
