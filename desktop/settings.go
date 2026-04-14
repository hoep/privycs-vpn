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
			Trigger:  "wifi_mobile",
			SSIDMode: "all",
		}
		// Persist the migration
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
