package main

import (
	"fmt"
	"log"
	"runtime"
	"strings"
	"time"
)

// ListNetworkRules returns the user's rule list in priority order
// for the Settings UI. Wails marshals slice-of-pointer fine.
func (a *App) ListNetworkRules() []*NetworkRule {
	if a.networkRules == nil {
		return []*NetworkRule{}
	}
	return a.networkRules.List()
}

// NetworkRulesEvalSnapshot is the structured result the
// NetworkRulesView LiveEvalCard renders. v1.0.5.23 added — gives the
// Vue frontend everything it needs to render the user's current
// engine decision in their own language, without having to subscribe
// to every internal state flow.
type NetworkRulesEvalSnapshot struct {
	NetworkType   string `json:"network_type"`    // "wifi" | "mobile" | "ethernet" | "none"
	SSID          string `json:"ssid"`            // empty unless networkType == "wifi" AND SSID has been latched
	MasterEnabled bool   `json:"master_enabled"`  // settings.NetworkRulesEnabled
	HasRules      bool   `json:"has_rules"`       // at least one rule exists in the registry
	EngineActive  bool   `json:"engine_active"`   // MasterEnabled && HasRules — the engine is actually deciding
	RuleMatching  bool   `json:"rule_matching"`   // engine active AND current network matches a rule
}

// GetCurrentNetworkRulesEval returns a snapshot of the current
// engine decision context — current network type/SSID, master-toggle
// state, whether any rule currently matches. The Vue
// NetworkRulesView LiveEvalCard polls this every 2s and renders the
// human-readable decision in the user's locale.
func (a *App) GetCurrentNetworkRulesEval() *NetworkRulesEvalSnapshot {
	snap := &NetworkRulesEvalSnapshot{}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.settings != nil {
		snap.MasterEnabled = a.settings.NetworkRulesEnabled
	}
	if a.networkRules != nil {
		snap.HasRules = len(a.networkRules.List()) > 0
	}
	snap.EngineActive = snap.MasterEnabled && snap.HasRules
	if a.autoConnect != nil {
		if nm := a.autoConnect.NetworkMonitor(); nm != nil {
			state := nm.CurrentState()
			snap.NetworkType = state.NetworkType
			snap.SSID = state.SSID
			snap.RuleMatching = snap.EngineActive && state.RuleMatch
		}
	}
	return snap
}

// AddNetworkRule appends a new rule to the end of the list. Caller
// can reorder via SetNetworkRulesOrder afterwards.
func (a *App) AddNetworkRule(rule *NetworkRule) error {
	if err := a.gateNetworkRules(); err != nil {
		return err
	}
	if a.networkRules == nil {
		return fmt.Errorf("rules registry not ready")
	}
	if rule == nil {
		return fmt.Errorf("nil rule")
	}
	if err := a.networkRules.Add(rule); err != nil {
		return err
	}
	// Trigger an immediate re-evaluation so a freshly-added rule
	// takes effect right away instead of waiting for the next
	// NetworkMonitor tick.
	a.triggerNetworkReeval()
	return nil
}

// UpdateNetworkRule replaces the rule with the matching ID.
func (a *App) UpdateNetworkRule(rule *NetworkRule) error {
	if a.networkRules == nil {
		return fmt.Errorf("rules registry not ready")
	}
	if err := a.networkRules.Update(rule); err != nil {
		return err
	}
	a.triggerNetworkReeval()
	return nil
}

// DeleteNetworkRule removes the rule with the supplied ID.
func (a *App) DeleteNetworkRule(id string) error {
	if a.networkRules == nil {
		return fmt.Errorf("rules registry not ready")
	}
	if err := a.networkRules.Delete(id); err != nil {
		return err
	}
	a.triggerNetworkReeval()
	return nil
}

// SetNetworkRulesOrder reorders the rules to match orderedIDs.
// Unknown IDs are silently dropped; missing IDs from the current
// list are removed (defensive, the UI should always send all).
func (a *App) SetNetworkRulesOrder(orderedIDs []string) error {
	if a.networkRules == nil {
		return fmt.Errorf("rules registry not ready")
	}
	if err := a.networkRules.Reorder(orderedIDs); err != nil {
		return err
	}
	a.triggerNetworkReeval()
	return nil
}

// triggerNetworkReeval kicks the NetworkMonitor's evaluator so
// rule edits take effect immediately. No-op when monitor is not
// running (= COD off + no rules previously); the next start()
// will pick up the rules naturally.
func (a *App) triggerNetworkReeval() {
	nm := a.autoConnect.NetworkMonitor()
	if nm != nil && nm.IsRunning() {
		nm.Reevaluate()
	}
}

// applyRuleResolution drives target switching when a rule matched.
// Mirrors Android NetworkMonitor.applyRuleResolution. Routes
// through the existing switch helpers so we get the same
// disconnect-wait-reconnect grace, Coordinator gating, and
// status flow propagation.
func (a *App) applyRuleResolution(res RuleResolution) {
	switch res.Action {
	case "no_vpn":
		a.mu.RLock()
		connected := a.connected
		a.mu.RUnlock()
		if connected {
			log.Printf("NetworkRules: rule -> NO_VPN, disconnecting")
			go func() {
				a.mu.Lock()
				_ = a.disconnectInternal()
				a.mu.Unlock()
			}()
		}
	case "pool":
		a.mu.RLock()
		currentPool := a.activePoolID
		connected := a.connected
		a.mu.RUnlock()
		if currentPool == res.TargetID && connected {
			return
		}
		log.Printf("NetworkRules: rule -> POOL %s", res.TargetID)
		// ActivatePool clears single-active and sets pool-active;
		// existing logic disconnects current tunnel. After the
		// post-disconnect path settles, the user / next tick
		// can drive reconnect. For an immediate switch we also
		// fire connectActiveTarget once the disconnect lands.
		_ = a.ActivatePool(res.TargetID)
		go func() {
			time.Sleep(2 * time.Second)
			a.connectActiveTarget()
		}()
	case "connection":
		a.mu.RLock()
		currentSingleID := ""
		if act := a.connections.Active(); act != nil {
			currentSingleID = act.ID
		}
		connected := a.connected
		a.mu.RUnlock()
		if currentSingleID == res.TargetID && connected {
			return
		}
		log.Printf("NetworkRules: rule -> CONNECTION %s", res.TargetID)
		_, _ = a.SwitchActiveConnection(res.TargetID, "")
	}
}

// detectCurrentBssid runs the platform-native helper to read the
// MAC of the access point we're currently associated with. Phase 3
// of the auto-tunnel roadmap. Returns "" when not on Wi-Fi or
// when the helper fails (permission, deprecated tool, parse
// error). BSSID-match rules silently no-op in that case.
//
// Best-effort across platforms - locale-dependent output, deprecated
// tools (macOS 14+ `airport`), and root-only commands (Linux `iw`)
// all degrade to "no BSSID known" rather than aborting the whole
// network evaluation.
func detectCurrentBssid() string {
	switch runtime.GOOS {
	case "linux":
		return detectBssidLinux()
	case "darwin":
		return detectBssidDarwin()
	case "windows":
		return detectBssidWindows()
	}
	return ""
}

func detectBssidLinux() string {
	// Try nmcli first - structured output, no parsing pain.
	// `nmcli -t -f IN-USE,BSSID dev wifi` gives us "*:AA:BB:..."
	// with the active row prefixed by "*".
	if out, err := runCmd("nmcli", "-t", "-f", "IN-USE,BSSID", "dev", "wifi"); err == nil {
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(line, "*:") {
				bssid := strings.TrimPrefix(line, "*:")
				bssid = strings.TrimSpace(bssid)
				if bssid != "" {
					return strings.ToLower(strings.ReplaceAll(bssid, "\\:", ":"))
				}
			}
		}
	}
	// Fallback: iw dev wlan0 link. Requires the wlan0 name to be
	// fixed (often it isn't on systemd predictable-network-names).
	// Skip iface detection complexity - if nmcli wasn't there
	// we just give up and return "".
	return ""
}

func detectBssidDarwin() string {
	// macOS 14+ deprecated /System/Library/PrivateFrameworks/Apple
	// 80211.framework/Versions/A/Resources/airport. New official:
	// `wdutil info` (root-only) or `system_profiler SPAirPortDataType`
	// (structured, slow ~500ms but works without root).
	// Try wdutil first (instant), system_profiler as fallback.
	if out, err := runCmd("wdutil", "info"); err == nil {
		for _, line := range strings.Split(out, "\n") {
			t := strings.TrimSpace(line)
			if strings.HasPrefix(t, "BSSID") {
				if i := strings.Index(t, ":"); i > 0 {
					bssid := strings.TrimSpace(t[i+1:])
					if bssid != "" && bssid != "<redacted>" {
						return strings.ToLower(bssid)
					}
				}
			}
		}
	}
	if out, err := runCmd("system_profiler", "SPAirPortDataType"); err == nil {
		// Look for "Current Network Information" block, then BSSID.
		inCurrent := false
		for _, line := range strings.Split(out, "\n") {
			t := strings.TrimSpace(line)
			if strings.Contains(t, "Current Network") {
				inCurrent = true
				continue
			}
			if inCurrent && strings.HasPrefix(t, "BSSID:") {
				bssid := strings.TrimSpace(strings.TrimPrefix(t, "BSSID:"))
				if bssid != "" {
					return strings.ToLower(bssid)
				}
			}
		}
	}
	return ""
}

func detectBssidWindows() string {
	// `netsh wlan show interfaces` output format includes a
	// "BSSID                  : aa:bb:cc:dd:ee:ff" line. Locale-
	// dependent label ("BSSID" same in all locales luckily).
	out, err := runCmd("netsh", "wlan", "show", "interfaces")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(out, "\n") {
		t := strings.TrimSpace(line)
		// netsh produces "BSSID                  : AA:BB:..."
		// with variable whitespace; split on ':' once.
		if strings.HasPrefix(t, "BSSID") {
			parts := strings.SplitN(t, ":", 2)
			if len(parts) == 2 {
				bssid := strings.TrimSpace(parts[1])
				if bssid != "" {
					return strings.ToLower(bssid)
				}
			}
		}
	}
	return ""
}

func runCmd(name string, args ...string) (string, error) {
	// execHidden applies CREATE_NO_WINDOW on Windows so the netsh
	// BSSID-detect subprocess does not pop a console window every
	// time NetworkMonitor's rule-engine tick reads the active SSID's
	// BSSID. Pass-through to exec.Command on Linux/macOS where the
	// flag is meaningless. v0.9.14.1 fix — the v0.9.13.8 sweep that
	// hid pingHost + autostart reg commands missed this site because
	// it was gated behind the opt-in rules engine.
	cmd := execHidden(name, args...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}
