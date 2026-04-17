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
}

// RemoteProfile represents the user profile returned by the gateway
type RemoteProfile struct {
	User    string              `json:"user"`
	Count   int                 `json:"count"`
	Configs []RemoteConfigEntry `json:"configs"`
}

// apiRequest makes an authenticated HTTP request to the gateway API
func (a *App) apiRequest(method, path string) ([]byte, error) {
	settings := a.settings
	if settings.GatewayURL == "" || settings.APIKey == "" {
		return nil, fmt.Errorf("gateway URL and API key must be configured in settings")
	}

	url := strings.TrimRight(settings.GatewayURL, "/") + path

	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+settings.APIKey)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
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
	configContent, err := a.FetchMyConfig(protocol, configID)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}

	filename := fmt.Sprintf("%s.conf", peerName)
	switch protocol {
	case "openvpn":
		filename = fmt.Sprintf("%s.ovpn", peerName)
	case "ipsec":
		filename = fmt.Sprintf("%s.sswan", peerName)
	}

	// Empty name when adding to existing connection — don't rename
	name := peerName
	if connectionID != "" {
		name = ""
	}

	return a.ImportConfig(protocol, configContent, filename, name, connectionID)
}
