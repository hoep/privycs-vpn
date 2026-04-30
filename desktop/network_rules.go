package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/google/uuid"
)

// NetworkRule mirrors Android's NetworkRule data class. Stored in
// network_rules.json next to settings.json. Walks the list in
// priority order (= list index) on every NetworkMonitor tick;
// first matching rule wins. Rules engine becomes authoritative
// for the connect lifecycle when at least one rule exists; with
// zero rules the legacy COD logic in NetworkMonitor.checkAndAct
// runs as before so existing users see no change until they add
// a rule.
type NetworkRule struct {
	ID         string `json:"id"`
	Priority   int    `json:"priority"`
	MatchType  string `json:"match_type"`  // ssid_exact / ssid_pattern / network_type / bssid / any
	MatchValue string `json:"match_value"` // pattern / SSID / network type / BSSID
	Action     string `json:"action"`      // no_vpn / pool / connection
	TargetID   string `json:"target_id,omitempty"`
	Enabled    bool   `json:"enabled"`
	Name       string `json:"name,omitempty"`
}

// Matches returns true when the rule applies to the supplied
// network state. State fields are populated by detectNetworkState
// + a platform helper for BSSID. Empty bssid means "not on Wi-Fi
// or unable to read MAC" (e.g. macOS 14+ deprecated airport,
// Linux nmcli not installed) - BSSID rules silently no-op there.
func (r *NetworkRule) Matches(networkType, ssid, bssid string) bool {
	if !r.Enabled {
		return false
	}
	switch r.MatchType {
	case "ssid_exact":
		return networkType == "wifi" && strings.EqualFold(ssid, r.MatchValue)
	case "ssid_pattern":
		return networkType == "wifi" && ssid != "" && globMatches(r.MatchValue, ssid)
	case "network_type":
		v := strings.ToLower(r.MatchValue)
		switch v {
		case "any":
			return networkType != "none"
		case "wifi", "mobile", "ethernet":
			return networkType == v
		case "wifi_mobile":
			return networkType == "wifi" || networkType == "mobile"
		}
		return false
	case "bssid":
		return networkType == "wifi" && bssid != "" && strings.EqualFold(bssid, r.MatchValue)
	case "any":
		return networkType != "none"
	}
	return false
}

// globMatches is a glob-to-regex helper for SSID_PATTERN rules.
// '*' matches any substring (incl. empty), '?' matches a single
// char. Case-insensitive. Special regex characters in the
// pattern are escaped so a literal "Cafe (1)" still matches.
func globMatches(pattern, input string) bool {
	var sb strings.Builder
	sb.WriteString("(?i)^")
	for _, c := range pattern {
		switch c {
		case '*':
			sb.WriteString(".*")
		case '?':
			sb.WriteString(".")
		case '\\', '.', '[', ']', '(', ')', '{', '}', '+', '|', '^', '$':
			sb.WriteByte('\\')
			sb.WriteRune(c)
		default:
			sb.WriteRune(c)
		}
	}
	sb.WriteString("$")
	re, err := regexp.Compile(sb.String())
	if err != nil {
		return false
	}
	return re.MatchString(input)
}

// RuleResolution mirrors Android's sealed class. Empty Action
// means NoMatch (= fall through to legacy COD).
type RuleResolution struct {
	Action   string // "" / "no_vpn" / "pool" / "connection"
	TargetID string
}

// NetworkRulesRegistry manages the on-disk rule list with mutex-
// protected CRUD. Single instance per App. Concurrency: every
// public method takes the lock; matching is read-only via
// Resolve().
type NetworkRulesRegistry struct {
	mu    sync.RWMutex
	rules []*NetworkRule
	path  string
}

func NewNetworkRulesRegistry() *NetworkRulesRegistry {
	r := &NetworkRulesRegistry{
		path: filepath.Join(appDataDir(), "network_rules.json"),
	}
	r.load()
	return r
}

func (r *NetworkRulesRegistry) load() {
	r.mu.Lock()
	defer r.mu.Unlock()
	data, err := os.ReadFile(r.path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("NetworkRules: load failed: %v", err)
		}
		r.rules = []*NetworkRule{}
		return
	}
	if err := json.Unmarshal(data, &r.rules); err != nil {
		log.Printf("NetworkRules: parse failed (resetting): %v", err)
		r.rules = []*NetworkRule{}
	}
	if r.rules == nil {
		r.rules = []*NetworkRule{}
	}
}

func (r *NetworkRulesRegistry) saveLocked() error {
	for i, rule := range r.rules {
		rule.Priority = i
	}
	data, err := json.MarshalIndent(r.rules, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(r.path, data, 0o644)
}

// List returns a copy of the rules slice for safe Wails marshalling
// (Wails serialises off-goroutine, so handing out the live slice
// would race with a concurrent Save).
func (r *NetworkRulesRegistry) List() []*NetworkRule {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*NetworkRule, len(r.rules))
	copy(out, r.rules)
	return out
}

func (r *NetworkRulesRegistry) Add(rule *NetworkRule) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rule.ID == "" {
		rule.ID = uuid.NewString()
	}
	r.rules = append(r.rules, rule)
	return r.saveLocked()
}

func (r *NetworkRulesRegistry) Update(rule *NetworkRule) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, existing := range r.rules {
		if existing.ID == rule.ID {
			r.rules[i] = rule
			return r.saveLocked()
		}
	}
	return fmt.Errorf("rule not found: %s", rule.ID)
}

func (r *NetworkRulesRegistry) Delete(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.rules[:0]
	for _, rule := range r.rules {
		if rule.ID != id {
			out = append(out, rule)
		}
	}
	r.rules = out
	return r.saveLocked()
}

func (r *NetworkRulesRegistry) Reorder(orderedIDs []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	byID := make(map[string]*NetworkRule, len(r.rules))
	for _, rule := range r.rules {
		byID[rule.ID] = rule
	}
	out := make([]*NetworkRule, 0, len(orderedIDs))
	for _, id := range orderedIDs {
		if rule, ok := byID[id]; ok {
			out = append(out, rule)
		}
	}
	r.rules = out
	return r.saveLocked()
}

// Resolve walks the rule list in priority order; first matching
// rule wins. Returns RuleResolution{} (empty Action) for "no
// rules" or "no match" - the caller falls through to legacy COD.
func (r *NetworkRulesRegistry) Resolve(networkType, ssid, bssid string) RuleResolution {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.rules) == 0 {
		return RuleResolution{}
	}
	for _, rule := range r.rules {
		if rule.Matches(networkType, ssid, bssid) {
			return RuleResolution{Action: rule.Action, TargetID: rule.TargetID}
		}
	}
	return RuleResolution{}
}
