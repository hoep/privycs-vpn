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
	mu           sync.Mutex
	running      bool
	stopCh       chan struct{}
	settings     *ConnectOnDemandSettings
	connectFn    func()
	disconnectFn func()
	isConnected  func() bool
	lastState    NetworkState
	stopWatcher  func() // platform watcher teardown
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

	// Start native OS event watcher
	stopWatcher, err := startPlatformWatcher(func() {
		nm.checkAndAct()
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
	nm.lastState = state
	nm.mu.Unlock()

	if settings == nil {
		return
	}

	match := evaluateRules(settings, &state)
	state.RuleMatch = match

	nm.mu.Lock()
	nm.lastState = state
	nm.mu.Unlock()

	connected := nm.isConnected()

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
		log.Printf("Network monitor: rules match (type=%s, ssid=%s), triggering connect", state.NetworkType, state.SSID)
		nm.connectFn()
	} else if !match && connected && nm.disconnectFn != nil {
		log.Printf("Network monitor: rules do not match (type=%s, ssid=%s), triggering disconnect", state.NetworkType, state.SSID)
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
