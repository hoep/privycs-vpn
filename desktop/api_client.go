package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"runtime"
	"strings"
	"time"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
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

// FetchMyConfigWindowsRoutes downloads the Windows-specific PowerShell
// .cmd companion file for an IPSec connection. Returns "" + nil if the
// gateway has no such endpoint configured (older gateway) or returns
// 404 — the caller treats the empty string as "no extra routes needed"
// and the connection still works (just without the bypass-routes /
// IPv6 routing workaround that the .cmd encodes).
//
// Workaround for: Windows IKEv2 with MachineCertificate only honours
// the FIRST traffic-selector from the server (a 0.0.0.0/0 default),
// so IPv6 routing + bypass-network exceptions from the Apple-NE-style
// ExcludedRoutes/IncludedRoutes blocks in the .mobileconfig are lost.
// The companion endpoint serves a generated .cmd containing explicit
// Add-VpnConnectionRoute directives that feed those routes back in
// after Add-VpnConnection.
func (a *App) FetchMyConfigWindowsRoutes(configID int) (string, error) {
	log.Printf("FetchMyConfigWindowsRoutes: configID=%d", configID)
	path := fmt.Sprintf("/api/v1/ipsec/connections/%d/profile/windows", configID)
	body, err := a.apiRequest("GET", path)
	if err != nil {
		// 404 from older gateways without this endpoint is expected;
		// return empty string + nil so the caller proceeds without
		// routes (degraded mode — IPv4 default route still works,
		// just no IPv6 / no bypass).
		if strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "not found") {
			log.Printf("FetchMyConfigWindowsRoutes: endpoint not available on gateway (404) — continuing without routes")
			return "", nil
		}
		return "", err
	}
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

	if err := a.ImportConfig(protocol, configContent, filename, name, connectionID); err != nil {
		return err
	}

	// v1.0.5.16: for IPSec, additionally fetch the Windows-companion
	// .cmd that carries explicit Add-VpnConnectionRoute directives for
	// bypass / IPv6 routing. Stored on the just-imported ProtocolConfig
	// so the Windows connect path can parse + install routes after
	// rasdial succeeds. Workaround for Windows IKEv2 honouring only
	// the FIRST traffic-selector from the server (a 0.0.0.0/0 default),
	// which loses every ExcludedRoutes/IncludedRoutes entry from the
	// Apple-NE-style .mobileconfig.
	//
	// Non-fatal on failure: a missing endpoint (older gateway → 404)
	// or transient network problem leaves WindowsRoutesScript empty
	// and the connection still works (without v6 / without bypass).
	if protocol == "ipsec" {
		winRoutes, wErr := a.FetchMyConfigWindowsRoutes(configID)
		if wErr != nil {
			log.Printf("DownloadAndImportConfig: FetchMyConfigWindowsRoutes non-fatal failure: %v — continuing without Windows routes", wErr)
		}
		if winRoutes != "" {
			stableID := fmt.Sprintf("gw-%s-%d", protocol, configID)
			a.mu.Lock()
			updated := false
			for _, conn := range a.connections.List() {
				for _, pc := range conn.Protocols {
					if pc.ID == stableID {
						pc.WindowsRoutesScript = winRoutes
						updated = true
						break
					}
				}
				if updated {
					break
				}
			}
			if updated {
				a.connections.Save()
				log.Printf("DownloadAndImportConfig: stored Windows routes script (%d bytes) on config %s", len(winRoutes), stableID)
			} else {
				log.Printf("DownloadAndImportConfig: could not locate freshly-imported config %s for Windows routes attachment", stableID)
			}
			a.mu.Unlock()

			// v1.0.5.19: on Windows ONLY, send the script to the
			// privileged helper for full RAS VPN provisioning at
			// import time — PKCS#12 + CA import to LocalMachine
			// cert store, Add-VpnConnection with MachineCertificate
			// + SplitTunneling, ~300 Add-VpnConnectionRoute calls
			// for bypass-complement + IPv6 routes, rasphone.pbk
			// patching for DisableClassBasedDefaultRoute +
			// IpInterfaceMetric, DNS-pin /32 host routes. The
			// helper runs the script via cmd.exe /c and cleans it
			// up afterwards (script contains cert material).
			//
			// Effect: after gateway-pull on Windows, the Windows
			// RAS VPN is fully ready — user clicks Connect, the
			// existing configureWindows path sees the connection
			// already exists and skips, rasdial connects, and
			// the post-rasdial route install (the v1.0.5.16 path)
			// becomes an idempotent no-op since the routes are
			// already in place.
			//
			// Failure is non-fatal: import already succeeded, the
			// connection still works via the existing client-side
			// Add-VpnConnection + post-rasdial routes fallback.
			// Error is logged + event-emitted for the UI to surface.
			if runtime.GOOS == "windows" {
				client := NewHelperClient()
				if !client.IsHelperReachable() {
					log.Printf("DownloadAndImportConfig: Windows IPSec auto-setup skipped — helper unreachable")
				} else {
					// Use the connection's display name (or the
					// stable ID as fallback) for the temp-script
					// filename component. The helper sanitises it.
					connectionDisplayName := name
					if connectionDisplayName == "" {
						connectionDisplayName = stableID
					}
					// Adapt the per-user server script to ALL-USER so the SYSTEM
					// helper runs it WITHOUT UAC, yet the connection is visible/
					// dialable by the logged-in user AND named to the app's slot
					// (gw-ipsec-N) — so the script's Remove+Add REPLACES the app's
					// own bare all-user create instead of duplicating it (the
					// "two connections, one without bypass" bug). All-user creation
					// needs admin, so it must run as SYSTEM — we deliberately do
					// NOT pass target_user (dropping to the unelevated user would
					// make -AllUserConnection fail). See rewriteWindowsSetupScriptAllUser.
					connName := sanitizeTunnelName(stableID)
					allUserScript := rewriteWindowsSetupScriptAllUser(winRoutes, connName)
					scriptB64 := base64.StdEncoding.EncodeToString([]byte(allUserScript))
					resp, ierr := client.SendCommand("ipsec_install_windows_profile", map[string]string{
						"connection_name": connectionDisplayName,
						"script_b64":      scriptB64,
					})
					if ierr != nil {
						log.Printf("DownloadAndImportConfig: Windows IPSec auto-setup IPC failed: %v", ierr)
						wailsRuntime.EventsEmit(a.ctx, "vpn:warning", map[string]string{
							"key":    "ipsec_windows_auto_setup_failed",
							"detail": ierr.Error(),
						})
					} else if !resp.Success {
						log.Printf("DownloadAndImportConfig: Windows IPSec auto-setup helper reported failure: %s", resp.Error)
						wailsRuntime.EventsEmit(a.ctx, "vpn:warning", map[string]string{
							"key":    "ipsec_windows_auto_setup_failed",
							"detail": resp.Error,
						})
					} else {
						log.Printf("DownloadAndImportConfig: Windows IPSec auto-setup: %s", resp.Output)
						wailsRuntime.EventsEmit(a.ctx, "vpn:info", map[string]string{
							"key":    "ipsec_windows_auto_setup_ok",
							"detail": resp.Output,
						})
					}
				}
			}
		}
	}

	return nil
}
