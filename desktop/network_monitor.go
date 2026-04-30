package main

import (
	"log"
	"strings"
	"sync"
	"time"
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
	mu              sync.Mutex
	running         bool
	stopCh          chan struct{}
	settings        *ConnectOnDemandSettings
	connectFn       func()
	disconnectFn    func()
	isConnected     func() bool
	isPaused        func() bool // optional - when set and returns true, monitor suppresses all actions
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
	lastRuleKey string
	lastState       NetworkState
	stopWatcher     func() // platform watcher teardown
	changeObservers []func()
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
	nm.running = false
	log.Println("Network monitor: stopped")
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
	go nm.checkAndAct()
}

// run sets up the platform event watcher and the safety poll timer.
func (nm *NetworkMonitor) run() {
	// Initial delay to let app finish startup
	select {
	case <-time.After(3 * time.Second):
	case <-nm.stopCh:
		return
	}

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
		nm.checkAndAct()
		go func() {
			time.Sleep(2 * time.Second)
			nm.checkAndAct()
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
	nm.checkAndAct()

	// Safety poll at 60-second intervals in case an OS event is missed
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-nm.stopCh:
			return
		case <-ticker.C:
			nm.checkAndAct()
		}
	}
}

func (nm *NetworkMonitor) checkAndAct() {
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

	// If on WiFi, check SSID filter rules
	if state.NetworkType == "wifi" && state.SSID != "" {
		switch settings.SSIDMode {
		case "only":
			// Only connect on listed SSIDs
			if !ssidInList(state.SSID, settings.SSIDList) {
				return false
			}
		case "except":
			// Connect on all SSIDs except listed ones
			if ssidInList(state.SSID, settings.SSIDList) {
				return false
			}
		case "all":
			// Connect on any WiFi - no filtering needed
		default:
			// Unknown mode, treat as "all"
		}
	}

	return true
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
