package main

import (
	"log"
	"os/exec"
	"runtime"
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

// NetworkMonitor polls the system network state and triggers VPN
// connect/disconnect based on ConnectOnDemandSettings rules.
type NetworkMonitor struct {
	mu           sync.Mutex
	running      bool
	stopCh       chan struct{}
	settings     *ConnectOnDemandSettings
	connectFn    func()
	disconnectFn func()
	isConnected  func() bool
	lastState    NetworkState
}

// NewNetworkMonitor creates a new network monitor instance
func NewNetworkMonitor() *NetworkMonitor {
	return &NetworkMonitor{}
}

// Start begins polling the network state every 5 seconds.
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

	go nm.pollLoop()
}

// Stop ends network monitoring
func (nm *NetworkMonitor) Stop() {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	if !nm.running {
		return
	}

	close(nm.stopCh)
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

// UpdateSettings updates the rules without restarting the monitor
func (nm *NetworkMonitor) UpdateSettings(settings *ConnectOnDemandSettings) {
	nm.mu.Lock()
	defer nm.mu.Unlock()
	nm.settings = settings
}

func (nm *NetworkMonitor) pollLoop() {
	// Initial delay to let app finish startup
	select {
	case <-time.After(3 * time.Second):
	case <-nm.stopCh:
		return
	}

	// Run initial check immediately
	nm.checkAndAct()

	ticker := time.NewTicker(5 * time.Second)
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

	if match && !connected {
		log.Printf("Network monitor: rules match (type=%s, ssid=%s), triggering connect", state.NetworkType, state.SSID)
		nm.connectFn()
	} else if !match && connected && nm.disconnectFn != nil {
		log.Printf("Network monitor: rules no longer match (type=%s, ssid=%s), triggering disconnect", state.NetworkType, state.SSID)
		nm.disconnectFn()
	}
}

// detectNetworkState determines the current network type and WiFi SSID
// using platform-specific commands.
func detectNetworkState() NetworkState {
	state := NetworkState{NetworkType: "none"}

	ssid := detectSSID()
	if ssid != "" {
		state.NetworkType = "wifi"
		state.SSID = ssid
		return state
	}

	// If no WiFi SSID, check for any network connectivity (ethernet)
	if hasNetworkConnectivity() {
		state.NetworkType = "ethernet"
	}

	return state
}

// detectSSID returns the current WiFi SSID or empty string
func detectSSID() string {
	switch runtime.GOOS {
	case "linux":
		return detectSSIDLinux()
	case "darwin":
		return detectSSIDMacOS()
	case "windows":
		return detectSSIDWindows()
	default:
		return ""
	}
}

func detectSSIDLinux() string {
	out, err := exec.Command("nmcli", "-t", "-f", "active,ssid", "dev", "wifi").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "yes:") {
			ssid := strings.TrimPrefix(line, "yes:")
			return strings.TrimSpace(ssid)
		}
	}
	return ""
}

func detectSSIDMacOS() string {
	out, err := exec.Command(
		"/System/Library/PrivateFrameworks/Apple80211.framework/Versions/Current/Resources/airport",
		"-I",
	).Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "SSID:") {
			ssid := strings.TrimPrefix(line, "SSID:")
			return strings.TrimSpace(ssid)
		}
	}
	return ""
}

func detectSSIDWindows() string {
	out, err := exec.Command("netsh", "wlan", "show", "interfaces").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		// Match "SSID" but not "BSSID"
		if strings.HasPrefix(line, "SSID") && !strings.HasPrefix(line, "BSSID") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				return strings.TrimSpace(parts[1])
			}
		}
	}
	return ""
}

// hasNetworkConnectivity does a quick check for any network interface being up
func hasNetworkConnectivity() bool {
	switch runtime.GOOS {
	case "linux":
		out, err := exec.Command("ip", "route", "show", "default").Output()
		if err != nil {
			return false
		}
		return strings.TrimSpace(string(out)) != ""
	case "darwin":
		out, err := exec.Command("route", "-n", "get", "default").Output()
		if err != nil {
			return false
		}
		return strings.Contains(string(out), "gateway:")
	case "windows":
		out, err := exec.Command("ipconfig").Output()
		if err != nil {
			return false
		}
		return strings.Contains(string(out), "Default Gateway")
	default:
		return false
	}
}

// evaluateRules checks whether the current network state matches the
// connect-on-demand rules defined in settings.
func evaluateRules(settings *ConnectOnDemandSettings, state *NetworkState) bool {
	if !settings.Enabled {
		return false
	}

	// Check trigger type match
	triggerMatch := false
	switch settings.Trigger {
	case "wifi":
		triggerMatch = state.NetworkType == "wifi"
	case "mobile":
		triggerMatch = state.NetworkType == "mobile"
	case "wifi_mobile":
		triggerMatch = state.NetworkType == "wifi" || state.NetworkType == "mobile"
	default:
		// Unknown trigger, default to wifi_mobile behavior
		triggerMatch = state.NetworkType == "wifi" || state.NetworkType == "mobile"
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
