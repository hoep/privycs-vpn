package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
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
	Theme              string                  `json:"theme"` // dark, light, system
	DNSOverride        string                  `json:"dns_override,omitempty"`
	RoutingMode        string                  `json:"routing_mode"` // full, split
	LogLevel           string                  `json:"log_level"`    // debug, info, warn, error
	GatewayURL         string                  `json:"gateway_url,omitempty"`
	APIKey             string                  `json:"api_key,omitempty"`
}

// LoadSettings reads settings from disk or returns defaults
func LoadSettings() *AppSettings {
	path := filepath.Join(appDataDir(), "settings.json")
	data, err := os.ReadFile(path)
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

	// Migrate stale trigger="wifi_mobile" to "any" for desktop users:
	// the old default never matched on wired/Ethernet machines (the
	// most common desktop case) so users who simply enabled COD on a
	// wired machine got silent no-ops. "any" is now the default.
	// Only applied if the user has not customized the trigger to one
	// of the explicit values that signal intent (wifi-only or
	// mobile-only).
	if settings.ConnectOnDemand.Trigger == "wifi_mobile" {
		settings.ConnectOnDemand.Trigger = "any"
		SaveSettings(&settings)
	}

	return &settings
}

// SaveSettings persists settings to disk
func SaveSettings(s *AppSettings) {
	path := filepath.Join(appDataDir(), "settings.json")
	os.MkdirAll(filepath.Dir(path), 0700)

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		log.Printf("Failed to marshal settings: %v", err)
		return
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		log.Printf("Failed to save settings: %v", err)
	}
}

func defaultSettings() *AppSettings {
	return &AppSettings{
		ActiveProtocol: "wireguard",
		Theme:          "dark",
		RoutingMode:    "full",
		LogLevel:       "info",
		MinimizeToTray: true,
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

// knownLogSources lists the files we read when the LogsView asks for
// a merged tail. Any file that doesn't exist (because e.g. OpenVPN has
// never been started) is silently skipped — no error shown to user.
func knownLogSources() []logSource {
	return []logSource{
		{"app", filepath.Join(appDataDir(), "privycs-vpn.log")},
		{"openvpn", filepath.Join(appDataDir(), "openvpn.log")},
		// WireGuard / IPSec don't have per-app log files on desktop;
		// their output is captured inside the app log via log.Printf.
	}
}

// getMergedLogs returns the tail of every known log source, each line
// prefixed with "[tag] " so the UI can render a single merged stream.
// perFile caps per-source line count to prevent one chatty daemon from
// flooding the view.
func getMergedLogs(perFile int) []string {
	var out []string
	for _, src := range knownLogSources() {
		data, err := os.ReadFile(src.path)
		if err != nil {
			continue // silently skip missing files
		}
		if len(data) == 0 {
			continue
		}
		lines := splitLines(string(data))
		if len(lines) > perFile {
			lines = lines[len(lines)-perFile:]
		}
		for _, ln := range lines {
			out = append(out, fmt.Sprintf("[%s] %s", src.tag, ln))
		}
	}
	if len(out) == 0 {
		return []string{"No logs yet. Connect to a VPN or trigger an action to generate entries."}
	}
	return out
}

// clearLogs truncates every Privycs-owned log file. External daemon
// logs (e.g. /var/log/charon.log) are not touched — we don't own them.
func clearLogs() error {
	var firstErr error
	for _, src := range knownLogSources() {
		if _, err := os.Stat(src.path); os.IsNotExist(err) {
			continue
		}
		if err := os.Truncate(src.path, 0); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("truncate %s: %w", src.path, err)
			}
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
