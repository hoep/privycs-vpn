package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// AppVersion is set at build time via ldflags:
//
//	-X main.AppVersion=v0.8.1.29
//
// Falls back to "dev" if not set.
var AppVersion = "dev"

// ConnectOnDemandSettings controls network-aware automatic VPN connection.
// When enabled, the VPN connects/disconnects based on the current network
// environment (WiFi SSID, network type) instead of a simple on-start toggle.
type ConnectOnDemandSettings struct {
	Enabled  bool     `json:"enabled"`
	Trigger  string   `json:"trigger"`   // "wifi", "mobile", "wifi_mobile"
	SSIDMode string   `json:"ssid_mode"` // "all", "only", "except"
	SSIDList []string `json:"ssid_list"`
}

// AppSettings holds the application settings
type AppSettings struct {
	ActiveProtocol     string                  `json:"active_protocol"`
	KillSwitchEnabled  bool                    `json:"kill_switch_enabled"`
	AutoConnectOnStart bool                    `json:"auto_connect_on_start"` // legacy, kept for backward compat
	ConnectOnDemand    ConnectOnDemandSettings `json:"connect_on_demand"`
	AutostartEnabled   bool                    `json:"autostart_enabled"`
	MinimizeToTray     bool                    `json:"minimize_to_tray"`
	Theme              string                  `json:"theme"`                  // dark, light, system
	AppLanguage        string                  `json:"app_language,omitempty"` // "" = system default; en/de/es/fr otherwise
	DNSOverride        string                  `json:"dns_override,omitempty"`
	LogLevel           string                  `json:"log_level"` // debug, info, warn, error
	GatewayURL         string                  `json:"gateway_url,omitempty"`
	APIKey             string                  `json:"api_key,omitempty"`
	// Tunnel-health monitoring (Phase 1 visible UX). Mode is one
	// of "auto" / "always" / "off"; auto means pool=on, single=off.
	// Empty target falls back to built-in default 1.1.1.1.
	TunnelHealthMode   string `json:"tunnel_health_mode,omitempty"`
	TunnelHealthTarget string `json:"tunnel_health_target,omitempty"`
	// v0.9.15.30: tuneable probe cadence. Defaults
	// (PingIntervalSec=5, DeadThreshold=2) come from
	// tunnel_health_monitor.go's constants when these are 0. Both
	// fields land in the JSON-serialised settings + backup so a
	// restore on a fresh device picks up the user's tuning.
	// User explicitly requested overridability after the v0.9.15.30
	// 60→5 s / 3→2 default change ("konfigurierbar wir haben doch
	// tunnel health settings (server)").
	TunnelHealthPingIntervalSec int `json:"tunnel_health_ping_interval_sec,omitempty"`
	TunnelHealthDeadThreshold   int `json:"tunnel_health_dead_threshold,omitempty"`
	// Per-network rules engine (Phase 2). Default off in v0.9.13.4
	// after the v0.9.13.0..3 instability reports - opt-in until
	// the connect-cascade interaction is fully understood.
	NetworkRulesEnabled bool `json:"network_rules_enabled,omitempty"`
	// (Phase 3 / connectivity watchdog already lives in
	// tunnel_health_monitor.go and is exposed as TunnelHealthMode
	// above — no separate toggle here.)
	//
	// ReconnectOnSystemWake (macOS only): subscribe to NSWorkspace
	// did-wake notifications and force a clean Down → Up immediately
	// after wake. Recovers from sleep-induced dead SAs in 1-3s vs the
	// 1-2 min charon-DPD path or the 30-60s watchdog path. No-op on
	// non-darwin. Default ON.
	ReconnectOnSystemWake *bool `json:"reconnect_on_system_wake,omitempty"`
	// PreventDisplaySleep (macOS only): while a tunnel is up, keep
	// display + idle awake via `caffeinate -di`. Use case is Privacy/
	// stability-first users who want zero sleep-related VPN drops.
	// Trades battery life for connection stability. Default OFF — opt-
	// in. No-op on non-darwin.
	PreventDisplaySleep bool `json:"prevent_display_sleep,omitempty"`
	// v0.9.15.70 — user-configurable protocol failover order. When a
	// connection holds multiple ProtocolConfigs (one or more per
	// protocol class), SavedConnection.OrderedConfigsFor returns
	// them in THIS order — driving the recovery target picked by
	// tryFailoverProtocol when the current tunnel fails. Default
	// (empty/nil) = the pre-v0.9.15.70 hard-coded enum order
	// (amneziawg → wireguard → openvpn → ipsec). Any protocol
	// missing from the list is appended at the end in enum order so
	// an older / partial list still produces a total order. Mirrors
	// the Android AppSettings.protocolFailoverOrder field.
	ProtocolFailoverOrder []string `json:"protocol_failover_order,omitempty"`
	// v1.0.0: encryption-at-rest. Set to true once
	// MigrateAppDataToEncrypted completes successfully on this
	// machine. Purely informational — the actual on-disk encryption
	// state is detected via the PVCE magic header (see
	// encrypted_file.go). UI surfaces this for the privacy banner.
	EncryptedAtRest bool `json:"encrypted_at_rest,omitempty"`
}

// ReconnectOnSystemWakeEnabled is the canonical accessor — falls back
// to ON when the *bool field is nil so existing settings.json files
// (pre-v0.9.14.64) inherit the new safe default without an explicit
// migration pass. nil = never-touched-by-user → default-on; explicit
// false = user-disabled; explicit true = user-enabled.
func (s *AppSettings) ReconnectOnSystemWakeEnabled() bool {
	if s.ReconnectOnSystemWake == nil {
		return true
	}
	return *s.ReconnectOnSystemWake
}

// LoadSettings reads settings from disk or returns defaults.
// Transparently handles pre-migration plaintext and v1.0.0+ encrypted
// settings.json via the EncryptedReadFile wrapper.
func LoadSettings() *AppSettings {
	path := filepath.Join(appDataDir(), "settings.json")
	data, err := EncryptedReadFile(path)
	if err != nil {
		return defaultSettings()
	}

	var settings AppSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		return defaultSettings()
	}

	// Migrate legacy auto_connect_on_start to connect_on_demand
	if settings.AutoConnectOnStart && !settings.ConnectOnDemand.Enabled {
		settings.ConnectOnDemand = ConnectOnDemandSettings{
			Enabled:  true,
			Trigger:  "any",
			SSIDMode: "all",
		}
		// Persist the migration
		SaveSettings(&settings)
	}

	// v0.9.15.30: pre-existing settings.json files have both
	// TunnelHealthPingIntervalSec + TunnelHealthDeadThreshold at
	// zero. Backfill from defaults so any code that reads the
	// settings struct sees the real cadence values, and so the
	// next SaveSettings (and backup) persists them explicitly.
	fillTunnelHealthDefaults(&settings)

	// (Removed: a one-shot trigger="wifi_mobile" -> "any" migration.
	// The migration was correct for users stuck on the legacy default,
	// but it also FIRED EVERY APP START - meaning a user who explicitly
	// chose "wifi_mobile" via the UI got their choice silently
	// reverted on next launch. Without a "migrated" flag in settings.
	// json there is no way to distinguish "default-never-touched" from
	// "explicit-via-UI", so we cannot safely auto-migrate. Fresh
	// installs get "any" as the default via defaultSettings(); existing
	// users who want "any" can pick it from the dropdown manually.)

	return &settings
}

// SaveSettings persists settings to disk. Uses EncryptedWriteFile so
// post-init writes land encrypted; pre-init writes (before
// initEncryptionAtRest wires the key provider) stay plaintext and get
// auto-encrypted by the next migration pass.
func SaveSettings(s *AppSettings) {
	path := filepath.Join(appDataDir(), "settings.json")
	os.MkdirAll(filepath.Dir(path), 0700)

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		log.Printf("Failed to marshal settings: %v", err)
		return
	}
	if err := EncryptedWriteFile(path, data, 0600); err != nil {
		log.Printf("Failed to save settings: %v", err)
	}
}

func defaultSettings() *AppSettings {
	return &AppSettings{
		ActiveProtocol:              "wireguard",
		Theme:                       "dark",
		LogLevel:                    "info",
		MinimizeToTray:              true,
		TunnelHealthPingIntervalSec: tunnelHealthPingIntervalSec, // 5
		TunnelHealthDeadThreshold:   tunnelHealthDeadThreshold,   // 2
	}
}

// fillTunnelHealthDefaults backfills the probe-cadence fields on a
// settings struct loaded from disk that pre-dates v0.9.15.30 (those
// settings.json files have both fields at their zero value 0/0). On
// the next SaveSettings the persisted file gets the populated values
// so backups carry them across.
func fillTunnelHealthDefaults(s *AppSettings) {
	if s.TunnelHealthPingIntervalSec <= 0 {
		s.TunnelHealthPingIntervalSec = tunnelHealthPingIntervalSec
	}
	if s.TunnelHealthDeadThreshold <= 0 {
		s.TunnelHealthDeadThreshold = tunnelHealthDeadThreshold
	}
}

// appDataDir returns the platform-specific app data directory
func appDataDir() string {
	var base string
	switch runtime.GOOS {
	case "windows":
		base = os.Getenv("LOCALAPPDATA")
		if base == "" {
			base = os.Getenv("APPDATA")
		}
	case "darwin":
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, "Library", "Application Support")
	default: // linux
		base = os.Getenv("XDG_DATA_HOME")
		if base == "" {
			home, _ := os.UserHomeDir()
			base = filepath.Join(home, ".local", "share")
		}
	}
	dir := filepath.Join(base, "privycs-vpn")
	os.MkdirAll(dir, 0700)
	return dir
}

// getRecentLogs returns the last N log lines from the log file
func getRecentLogs(n int) []string {
	logPath := filepath.Join(appDataDir(), "privycs-vpn.log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		return []string{
			fmt.Sprintf("Log file not found: %s", logPath),
			fmt.Sprintf("Error: %v", err),
			"Logs will appear here after the app writes log entries.",
		}
	}

	if len(data) == 0 {
		return []string{"Log file is empty. Logs will appear after activity."}
	}

	lines := splitLines(string(data))
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

// logSource describes one of several log files the app surfaces in the
// merged LogsView. Each entry contributes up to `perFile` lines, tagged
// with the source so the user can tell them apart.
type logSource struct {
	tag  string
	path string
}

// knownLogSources discovers every *.log file under appDataDir at call
// time. The app log "privycs-vpn.log" is always present; OpenVPN
// per-profile logs ("<connection-name>.log") appear after the first
// connect attempt for that profile. Any file that disappears between
// discovery and read is silently skipped by the callers.
//
// v1.0.5.28: was previously hard-coded to two static paths
// ("privycs-vpn.log" + "openvpn.log"). The second one never existed
// because protocol_openvpn.go writes per-profile <name>.log files;
// the LogsView and clearLogs both operated on a phantom path. Now
// the discovery is dynamic. The synthetic tag is the filename minus
// the .log suffix so the merged view labels each line correctly.
func knownLogSources() []logSource {
	dir := appDataDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []logSource{
			{"app", filepath.Join(dir, "privycs-vpn.log")},
		}
	}
	var sources []logSource
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		tag := strings.TrimSuffix(e.Name(), ".log")
		if tag == "privycs-vpn" {
			tag = "app"
		}
		sources = append(sources, logSource{
			tag:  tag,
			path: filepath.Join(dir, e.Name()),
		})
	}
	if len(sources) == 0 {
		sources = []logSource{
			{"app", filepath.Join(dir, "privycs-vpn.log")},
		}
	}
	return sources
}

// getMergedLogs returns the tail of every known log source, each line
// prefixed with "[tag] " so the UI can render a single merged stream.
// perFile caps per-source line count to prevent one chatty daemon from
// flooding the view.
//
// v1.0.5.28: chronologically sorts the merged stream by parsing the
// leading timestamp on each line. Lines without a parseable timestamp
// retain the order of their source file and are intermixed using a
// stable-sort keyed by the most recent preceding timestamp. The
// effect is one truly interleaved time-ordered stream instead of
// "all 100 app lines, then all 100 openvpn lines".
//
// Recognised timestamp formats (covers app Go-default log + OpenVPN
// default + strongSwan default):
//   - "2026/05/30 14:23:45"        (Go log package default)
//   - "Sat Mar 16 14:23:45 2026"   (OpenVPN default)
//   - "May 30 14:23:45"            (strongSwan default)
func getMergedLogs(perFile int) []string {
	type taggedLine struct {
		tag   string
		line  string
		ts    int64 // unix-nano; 0 = unparseable, kept stable
		order int   // tie-breaker so stable sort works
	}
	var collected []taggedLine
	order := 0
	for _, src := range knownLogSources() {
		data, err := os.ReadFile(src.path)
		if err != nil {
			continue
		}
		if len(data) == 0 {
			continue
		}
		lines := splitLines(string(data))
		if len(lines) > perFile {
			lines = lines[len(lines)-perFile:]
		}
		var lastTs int64
		for _, ln := range lines {
			ts := parseLogTimestamp(ln)
			if ts != 0 {
				lastTs = ts
			} else {
				ts = lastTs // continuation lines inherit prev timestamp
			}
			collected = append(collected, taggedLine{
				tag:   src.tag,
				line:  fmt.Sprintf("[%s] %s", src.tag, ln),
				ts:    ts,
				order: order,
			})
			order++
		}
	}
	if len(collected) == 0 {
		return []string{"No logs yet. Connect to a VPN or trigger an action to generate entries."}
	}
	// Stable sort by (ts, original-order). Unparseable lines (ts=0)
	// drift to the top but keep their source order; this is rare and
	// only happens for headerless log fragments.
	sort.SliceStable(collected, func(i, j int) bool {
		if collected[i].ts != collected[j].ts {
			return collected[i].ts < collected[j].ts
		}
		return collected[i].order < collected[j].order
	})
	out := make([]string, len(collected))
	for i, c := range collected {
		out[i] = c.line
	}
	return out
}

// parseLogTimestamp tries to extract a unix-nano timestamp from the
// start of a log line. Returns 0 when no recognised format matches.
// v1.0.5.28: feeds the chronological merge in getMergedLogs.
var logTimestampFormats = []string{
	"2006/01/02 15:04:05",
	"2006-01-02 15:04:05",
	"Mon Jan 2 15:04:05 2006",
	"Jan 2 15:04:05",
}

func parseLogTimestamp(line string) int64 {
	if len(line) < 8 {
		return 0
	}
	for _, layout := range logTimestampFormats {
		if len(line) < len(layout) {
			continue
		}
		candidate := line[:len(layout)]
		if t, err := time.Parse(layout, candidate); err == nil {
			// strongSwan's "Jan 2 15:04:05" has no year; assume current
			// year. Past-cycle log lines from December read at New Year
			// will be off by one day — acceptable for the merge view.
			if t.Year() == 0 {
				t = t.AddDate(time.Now().Year(), 0, 0)
			}
			return t.UnixNano()
		}
	}
	return 0
}

// clearLogs truncates every Privycs-owned log file. External daemon
// logs (e.g. /var/log/charon.log) are not touched — we don't own them.
//
// v1.0.5.28: routes failures through the privileged helper. The user-
// app process can read the helper-spawned OpenVPN per-profile *.log
// files (mode 0644) but cannot truncate them — they are owned by
// root/SYSTEM. First we try the direct Truncate (works for App-owned
// files like privycs-vpn.log); any file that fails with permission
// denied is bundled and sent to the helper for root-side truncation.
// Files that don't exist or are App-owned succeed without the helper
// roundtrip.
func clearLogs() error {
	var firstErr error
	var helperPaths []string
	for _, src := range knownLogSources() {
		if _, err := os.Stat(src.path); os.IsNotExist(err) {
			continue
		}
		if err := os.Truncate(src.path, 0); err != nil {
			if os.IsPermission(err) {
				helperPaths = append(helperPaths, src.path)
				continue
			}
			if firstErr == nil {
				firstErr = fmt.Errorf("truncate %s: %w", src.path, err)
			}
		}
	}
	if len(helperPaths) > 0 {
		client := NewHelperClient()
		if !client.IsHelperReachable() {
			return fmt.Errorf("helper unreachable — cannot clear %d helper-owned log file(s) (e.g. %s)",
				len(helperPaths), helperPaths[0])
		}
		resp, err := client.SendCommand("clear_logs", map[string]string{
			"paths": strings.Join(helperPaths, "\n"),
		})
		if err != nil {
			return fmt.Errorf("helper clear_logs failed: %w", err)
		}
		if !resp.Success {
			return fmt.Errorf("helper clear_logs: %s", resp.Error)
		}
	}
	return firstErr
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			line := s[start:i]
			if line != "" {
				lines = append(lines, line)
			}
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}
