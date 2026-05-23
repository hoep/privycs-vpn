package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// RemoteConfigEntry represents a VPN config from the gateway API
type RemoteConfigEntry struct {
	ID            int    `json:"id"`
	PeerName      string `json:"peer_name"`
	Protocol      string `json:"protocol"`
	InterfaceName string `json:"interface_name"`
	AgentID       string `json:"agent_id"`
	VPNIP         string `json:"vpn_ip"`
	Status        string `json:"status"`
	LastHandshake string `json:"last_handshake,omitempty"`
	// ObfuscationEnabled is true when this WireGuard peer's parent
	// interface runs awg-quick (i.e. AmneziaWG). Server emits the
	// flag at /api/v1/connect/my-configs so the client can swap
	// the WireGuard badge for an AmneziaWG one without having to
	// fetch the full peer-config blob for each row. See
	// privycs/cmd/gateway/connect_my_configs_api.go:45.
	ObfuscationEnabled bool `json:"obfuscation_enabled,omitempty"`
}

// RemoteProfile represents the user profile returned by the gateway
type RemoteProfile struct {
	User    string              `json:"user"`
	Count   int                 `json:"count"`
	Configs []RemoteConfigEntry `json:"configs"`
}

// apiRequest makes an authenticated HTTP request to the gateway API
func (a *App) apiRequest(method, path string) ([]byte, error) {
	log.Printf("apiRequest: %s %s", method, path)
	settings := a.settings
	if settings.GatewayURL == "" || settings.APIKey == "" {
		log.Printf("apiRequest: missing gateway URL or API key")
		return nil, fmt.Errorf("gateway URL and API key must be configured in settings")
	}

	url := strings.TrimRight(settings.GatewayURL, "/") + path

	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		log.Printf("apiRequest: NewRequest FAILED: %v", err)
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+settings.APIKey)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		log.Printf("apiRequest: client.Do FAILED: %v", err)
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	log.Printf("apiRequest: HTTP %d", resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("apiRequest: ReadAll FAILED: %v", err)
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode == 401 {
		return nil, fmt.Errorf("authentication failed - check your API key")
	}
	if resp.StatusCode == 403 {
		return nil, fmt.Errorf("access denied")
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
	}

	return body, nil
}

// FetchMyProfile verifies the API key and returns the user's email and config count
func (a *App) FetchMyProfile() (*RemoteProfile, error) {
	if err := a.gateGatewayDownload(); err != nil {
		return nil, err
	}
	body, err := a.apiRequest("GET", "/api/v1/connect/my-configs")
	if err != nil {
		return nil, err
	}

	var result struct {
		Success bool                `json:"success"`
		User    string              `json:"user"`
		Count   int                 `json:"count"`
		Configs []RemoteConfigEntry `json:"configs"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("invalid response: %w", err)
	}

	if !result.Success {
		return nil, fmt.Errorf("API returned error")
	}

	log.Printf("API profile: user=%s, configs=%d", result.User, result.Count)

	return &RemoteProfile{
		User:    result.User,
		Count:   result.Count,
		Configs: result.Configs,
	}, nil
}

// FetchMyConfig downloads a specific config with full secrets from the gateway
func (a *App) FetchMyConfig(protocol string, configID int) (string, error) {
	log.Printf("FetchMyConfig: protocol=%s configID=%d", protocol, configID)
	path := fmt.Sprintf("/api/v1/connect/my-configs/%s-%d", protocol, configID)
	// IPSec: request .sswan (JSON) format. Default is iOS .mobileconfig (XML)
	// which the desktop client cannot parse.
	if protocol == "ipsec" {
		path += "?format=sswan"
	}
	body, err := a.apiRequest("GET", path)
	if err != nil {
		return "", err
	}

	if protocol == "wireguard" {
		// WireGuard: build .conf from JSON response
		return a.buildWireGuardConf(body)
	}

	if protocol == "openvpn" {
		// OpenVPN: extract config string from JSON
		var result struct {
			Success bool   `json:"success"`
			Config  string `json:"config"`
		}
		if err := json.Unmarshal(body, &result); err != nil {
			return "", fmt.Errorf("invalid response: %w", err)
		}
		return result.Config, nil
	}

	// IPSec: raw profile content
	return string(body), nil
}

// buildWireGuardConf generates a .conf file from the API JSON response
func (a *App) buildWireGuardConf(body []byte) (string, error) {
	var result struct {
		Success bool `json:"success"`
		Config  struct {
			PeerPrivateKey      string  `json:"peer_private_key"`
			PeerAddress         string  `json:"peer_address"`
			ServerPublicKey     string  `json:"server_public_key"`
			PresharedKey        *string `json:"preshared_key"`
			ServerEndpoint      string  `json:"server_endpoint"`
			ServerPort          int     `json:"server_port"`
			AllowedIPs          string  `json:"allowed_ips"`
			DNS                 string  `json:"dns"`
			MTU                 int     `json:"mtu"`
			PersistentKeepalive int     `json:"persistent_keepalive"`
			// ObfuscationConfigLines is the pre-rendered AmneziaWG
			// block (Jc/Jmin/Jmax/S1-4/H1-4/I1-5 keys ready to
			// append into [Interface]). Emitted by privycs server
			// v0.8.4.18+; empty/missing for vanilla WG configs.
			// Without this field the client falls back to plain
			// WireGuard and the AWG-magic-header server then
			// silently drops all traffic.
			ObfuscationConfigLines string `json:"obfuscation_config_lines"`
		} `json:"config"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("invalid WireGuard config response: %w", err)
	}

	if !result.Success || result.Config.PeerPrivateKey == "" {
		return "", fmt.Errorf("config not available (private key missing)")
	}

	c := result.Config
	var conf strings.Builder

	conf.WriteString("[Interface]\n")
	conf.WriteString(fmt.Sprintf("PrivateKey = %s\n", c.PeerPrivateKey))
	conf.WriteString(fmt.Sprintf("Address = %s\n", c.PeerAddress))
	if c.DNS != "" {
		conf.WriteString(fmt.Sprintf("DNS = %s\n", c.DNS))
	}
	if c.MTU > 0 {
		conf.WriteString(fmt.Sprintf("MTU = %d\n", c.MTU))
	}
	// AmneziaWG obfuscation block — append before [Peer] so the
	// keys land inside the [Interface] section where the AWG
	// parser expects them.
	if obf := strings.TrimSpace(c.ObfuscationConfigLines); obf != "" {
		conf.WriteString(obf)
		conf.WriteString("\n")
	}

	conf.WriteString("\n[Peer]\n")
	conf.WriteString(fmt.Sprintf("PublicKey = %s\n", c.ServerPublicKey))
	if c.PresharedKey != nil && *c.PresharedKey != "" {
		conf.WriteString(fmt.Sprintf("PresharedKey = %s\n", *c.PresharedKey))
	}
	conf.WriteString(fmt.Sprintf("Endpoint = %s:%d\n", c.ServerEndpoint, c.ServerPort))
	conf.WriteString(fmt.Sprintf("AllowedIPs = %s\n", c.AllowedIPs))

	keepalive := c.PersistentKeepalive
	if keepalive == 0 {
		keepalive = 25
	}
	conf.WriteString(fmt.Sprintf("PersistentKeepalive = %d\n", keepalive))

	return conf.String(), nil
}

// DownloadAndImportConfig downloads a config from gateway and imports it.
// If connectionID is empty, a new connection is created. Otherwise the config
// is added as an additional protocol to the existing connection.
func (a *App) DownloadAndImportConfig(protocol string, configID int, peerName string, connectionID string) error {
	log.Printf("DownloadAndImportConfig: protocol=%s configID=%d peerName=%q connID=%q", protocol, configID, peerName, connectionID)
	if err := a.gateGatewayDownload(); err != nil {
		return err
	}
	configContent, err := a.FetchMyConfig(protocol, configID)
	if err != nil {
		log.Printf("DownloadAndImportConfig: FetchMyConfig FAILED: %v", err)
		return fmt.Errorf("download failed: %w", err)
	}
	log.Printf("DownloadAndImportConfig: fetched %d bytes, calling ImportConfig", len(configContent))

	// v0.9.15.30: filename is purely the deterministic gateway-
	// stable ID, decoupled from the user-visible peerName.
	// Server-side name changes (peer rename, display-string churn)
	// no longer break the (protocol, filename) tuple that AddOrUpdate
	// uses to detect re-import. The same stable ID is mirrored into
	// pc.ID by ImportConfig so the upsert match hits at the ID stage
	// (1st priority in connection_registry.AddOrUpdate). Two peers
	// with the same display name from different gateway interfaces
	// have different configIDs → different filenames → coexist as
	// separate slots → protocol-pill picker shows the "2" badge.
	stableID := fmt.Sprintf("gw-%s-%d", protocol, configID)
	filename := stableID + ".conf"
	switch protocol {
	case "openvpn":
		filename = stableID + ".ovpn"
	case "ipsec":
		filename = stableID + ".sswan"
	}

	// Empty name when adding to existing connection — don't rename
	name := peerName
	if connectionID != "" {
		name = ""
	}

	return a.ImportConfig(protocol, configContent, filename, name, connectionID)
}
