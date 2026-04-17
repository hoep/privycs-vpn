package main

import (
	"context"
	encoding_base64 "encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// IPSecProtocol implements VPNProtocol for IKEv2/IPSec connections
type IPSecProtocol struct {
	connName    string
	connectedAt time.Time
	serverAddr  string
	localAddr   string
	configured  bool // true after first successful Configure()
}

// IPSecConfig holds IPSec-specific configuration
type IPSecConfig struct {
	ConnectionName string `json:"connection_name"`
	RemoteAddress  string `json:"remote_address"`
	RemoteID       string `json:"remote_id"`
	LocalID        string `json:"local_id"`
	IKEProposals   string `json:"ike_proposals"`
	ESPProposals   string `json:"esp_proposals"`
	CACertPEM      string `json:"ca_cert_pem"`
	ClientCertPEM  string `json:"client_cert_pem"`
	ClientKeyPEM   string `json:"client_key_pem"`
}

// NewIPSecProtocol creates a new IPSec protocol handler
func NewIPSecProtocol() *IPSecProtocol {
	return &IPSecProtocol{
		connName: "privycs-vpn",
	}
}

func (i *IPSecProtocol) Name() string { return "ipsec" }

func (i *IPSecProtocol) IsAvailable() bool {
	switch runtime.GOOS {
	case "linux":
		_, err := exec.LookPath("swanctl")
		return err == nil
	case "darwin":
		return true // macOS has built-in IKEv2
	case "windows":
		return true // Windows has built-in IKEv2
	default:
		return false
	}
}

func (i *IPSecProtocol) Up(ctx context.Context) error {
	if !i.IsAvailable() {
		return fmt.Errorf("IPSec not available on this system")
	}

	switch runtime.GOOS {
	case "linux":
		return i.upLinux(ctx)
	case "darwin":
		return i.upMacOS(ctx)
	case "windows":
		return i.upWindows(ctx)
	default:
		return fmt.Errorf("unsupported platform for IPSec")
	}
}

func (i *IPSecProtocol) Down(ctx context.Context) error {
	switch runtime.GOOS {
	case "linux":
		return i.downLinux(ctx)
	case "darwin":
		return i.downMacOS(ctx)
	case "windows":
		return i.downWindows(ctx)
	default:
		return nil
	}
}

func (i *IPSecProtocol) Status() ProtocolStatus {
	status := ProtocolStatus{
		Protocol:      "ipsec",
		ServerAddress: i.serverAddr,
		LocalAddress:  i.localAddr,
	}

	switch runtime.GOOS {
	case "linux":
		// Query via helper — avoids sudo prompt every 2 seconds.
		client := NewHelperClient()
		if !client.IsHelperReachable() {
			return status
		}
		resp, err := client.SendCommand("status", map[string]string{
			"protocol":  "ipsec",
			"interface": i.connName,
		})
		if err == nil && resp.Success && strings.Contains(resp.Output, "ESTABLISHED") {
			status.Connected = true
			status.ConnectedAt = i.connectedAt.Format(time.RFC3339)
		}
	case "darwin":
		out, err := execHidden("scutil", "--nc", "status", i.connName).CombinedOutput()
		if err == nil && strings.Contains(string(out), "Connected") {
			status.Connected = true
			status.ConnectedAt = i.connectedAt.Format(time.RFC3339)
		}
	case "windows":
		// Look up both per-user AND machine-wide VPN connections — the helper
		// creates with -AllUserConnection, direct-fallback creates per-user.
		psCmd := fmt.Sprintf(
			`$v = Get-VpnConnection -Name '%s' -AllUserConnection -ErrorAction SilentlyContinue; `+
				`if (-not $v) { $v = Get-VpnConnection -Name '%s' -ErrorAction SilentlyContinue }; `+
				`if ($v) { $v.ConnectionStatus } else { 'NotFound' }`,
			escapePowerShellString(i.connName), escapePowerShellString(i.connName))
		out, err := execHidden("powershell", "-NoProfile", "-Command", psCmd).CombinedOutput()
		if err == nil && strings.Contains(string(out), "Connected") {
			status.Connected = true
			status.ConnectedAt = i.connectedAt.Format(time.RFC3339)
			status.BytesRx, status.BytesTx = getWindowsTrafficStats(i.connName)
		}
	}

	// No timestamp fallback — only trust actual OS service/connection check.

	return status
}

func (i *IPSecProtocol) Configure(cfg []byte) error {
	// Try parsing as JSON (structured IPSec config)
	var ipsecCfg IPSecConfig
	if err := json.Unmarshal(cfg, &ipsecCfg); err == nil && ipsecCfg.RemoteAddress != "" {
		return i.configureFromStruct(&ipsecCfg)
	}

	// Try parsing as strongSwan .sswan profile
	content := string(cfg)
	if strings.Contains(content, "\"remote\"") || strings.Contains(content, "remote_addrs") {
		return i.configureFromSSwan(cfg)
	}

	return fmt.Errorf("unrecognized IPSec config format")
}

func (i *IPSecProtocol) configureFromStruct(cfg *IPSecConfig) error {
	if cfg.ConnectionName != "" {
		i.connName = cfg.ConnectionName
	}
	i.serverAddr = cfg.RemoteAddress

	switch runtime.GOOS {
	case "linux":
		return i.configureLinux(cfg)
	case "darwin":
		log.Println("macOS IPSec: use .mobileconfig for full integration")
		return nil
	case "windows":
		return i.configureWindows(cfg)
	default:
		return fmt.Errorf("unsupported platform for IPSec configuration")
	}
}

// sswanProfile represents a strongSwan .sswan profile (Android format)
type sswanProfile struct {
	UUID   string `json:"uuid"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	Remote struct {
		Addr string `json:"addr"`
		ID   string `json:"id"`
	} `json:"remote"`
	Local struct {
		ID          string `json:"id"`
		P12         string `json:"p12"`
		P12Password string `json:"p12-password"`
	} `json:"local"`
	SplitTunneling []string `json:"split-tunneling"`
	DNSServers     []string `json:"dns-servers"`
}

func (i *IPSecProtocol) configureFromSSwan(cfg []byte) error {
	var profile sswanProfile
	if err := json.Unmarshal(cfg, &profile); err != nil {
		return fmt.Errorf("failed to parse .sswan profile: %w", err)
	}

	if profile.Remote.Addr == "" {
		return fmt.Errorf("invalid .sswan profile: missing remote address")
	}

	if profile.Name != "" {
		i.connName = profile.Name
	}
	i.serverAddr = profile.Remote.Addr

	log.Printf("Parsed .sswan profile: %s -> %s", i.connName, i.serverAddr)

	// Save the raw .sswan for reference
	sswanPath := filepath.Join(appDataDir(), i.connName+".sswan")
	os.WriteFile(sswanPath, cfg, 0600)

	// Extract and save PKCS#12 bundle if present
	if profile.Local.P12 != "" {
		p12Path := filepath.Join(appDataDir(), i.connName+".p12")
		p12Data, err := base64Decode(profile.Local.P12)
		if err != nil {
			return fmt.Errorf("failed to decode PKCS#12 from .sswan: %w", err)
		}
		if err := os.WriteFile(p12Path, p12Data, 0600); err != nil {
			return fmt.Errorf("failed to write PKCS#12: %w", err)
		}
		log.Printf("PKCS#12 bundle saved to %s (%d bytes)", p12Path, len(p12Data))
	}

	switch runtime.GOOS {
	case "windows":
		return i.configureWindowsFromSSwan(&profile)
	case "linux":
		return i.configureLinuxFromSSwan(&profile)
	default:
		log.Printf("Platform %s: .sswan saved, manual configuration required", runtime.GOOS)
		return nil
	}
}

func (i *IPSecProtocol) configureWindowsFromSSwan(profile *sswanProfile) error {
	// Skip if already configured in this session
	if i.configured && i.serverAddr == profile.Remote.Addr {
		log.Printf("IPSec: '%s' already configured (cached), skipping", i.connName)
		return nil
	}

	p12Password := profile.Local.P12Password
	if p12Password == "" {
		// Empty p12-password field means the server used the default export password.
		p12Password = "privycs"
	}

	log.Printf("IPSec: creating Windows VPN connection '%s' -> %s", i.connName, profile.Remote.Addr)

	client := NewHelperClient()
	if client.IsHelperReachable() {
		// Helper-based path: no UAC prompt, cert goes to LocalMachine\My.
		resp, err := client.SendCommand("ipsec_configure", map[string]string{
			"conn_name":      i.connName,
			"server_address": profile.Remote.Addr,
			"p12_base64":     profile.Local.P12,
			"p12_password":   p12Password,
		})
		if err != nil {
			return fmt.Errorf("helper ipsec_configure failed: %w", err)
		}
		if !resp.Success {
			return fmt.Errorf("ipsec configure via helper: %s", resp.Error)
		}
		i.configured = true
		log.Printf("Windows IKEv2 VPN connection created via helper: %s -> %s", i.connName, profile.Remote.Addr)
		return nil
	}

	// Fallback: UAC-elevated setup (one-time admin prompt).
	p12Path := filepath.Join(appDataDir(), i.connName+".p12")
	// Write PKCS#12 locally (client writes in configureFromSSwan already, but
	// be defensive in case fallback is invoked from a different code path).
	if profile.Local.P12 != "" {
		if data, err := base64Decode(profile.Local.P12); err == nil {
			os.WriteFile(p12Path, data, 0600)
		}
	}

	psScript := fmt.Sprintf(`
$ErrorActionPreference = 'Stop'
$p12Password = ConvertTo-SecureString -String '%s' -AsPlainText -Force
try {
    Import-PfxCertificate -FilePath '%s' -CertStoreLocation Cert:\LocalMachine\My -Password $p12Password -ErrorAction Stop | Out-Null
} catch {
    Import-PfxCertificate -FilePath '%s' -CertStoreLocation Cert:\CurrentUser\My -Password $p12Password | Out-Null
}
Remove-VpnConnection -Name '%s' -Force -ErrorAction SilentlyContinue
Add-VpnConnection -Name '%s' -ServerAddress '%s' -TunnelType IKEv2 -AuthenticationMethod MachineCertificate -EncryptionLevel Required -RememberCredential -Force
`, escapePowerShellString(p12Password), escapePowerShellString(p12Path), escapePowerShellString(p12Path),
		escapePowerShellString(i.connName),
		escapePowerShellString(i.connName), escapePowerShellString(profile.Remote.Addr))

	cmd := execHidden("powershell", "-NoProfile", "-Command",
		fmt.Sprintf("Start-Process powershell -ArgumentList '-NoProfile','-Command','%s' -Verb RunAs -Wait -WindowStyle Hidden",
			escapePowerShellString(psScript)))
	setupOut, setupErr := cmd.CombinedOutput()
	if setupErr != nil {
		safeOutput := redactCredentials(string(setupOut), p12Password)
		return fmt.Errorf("failed to configure IPSec on Windows: %s: %w", safeOutput, setupErr)
	}

	i.configured = true
	log.Printf("Windows IKEv2 VPN connection created via UAC fallback: %s -> %s", i.connName, profile.Remote.Addr)
	return nil
}

func (i *IPSecProtocol) configureLinuxFromSSwan(profile *sswanProfile) error {
	// Convert .sswan to swanctl config
	ipsecCfg := &IPSecConfig{
		ConnectionName: i.connName,
		RemoteAddress:  profile.Remote.Addr,
		RemoteID:       profile.Remote.ID,
		LocalID:        profile.Local.ID,
	}
	return i.configureLinux(ipsecCfg)
}

// escapePowerShellString escapes single quotes for nested PowerShell execution
func escapePowerShellString(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

// base64Decode decodes standard or URL-safe base64
func base64Decode(s string) ([]byte, error) {
	// Try standard base64 first
	data, err := base64StdDecode(s)
	if err == nil {
		return data, nil
	}
	// Try URL-safe base64
	return base64StdDecode(strings.NewReplacer("-", "+", "_", "/").Replace(s))
}

func base64StdDecode(s string) ([]byte, error) {
	// Add padding if needed
	switch len(s) % 4 {
	case 2:
		s += "=="
	case 3:
		s += "="
	}
	return encoding_base64.StdEncoding.DecodeString(s)
}

// ============================================================================
// Linux: strongSwan swanctl
// ============================================================================

func (i *IPSecProtocol) configureLinux(cfg *IPSecConfig) error {
	ikeProposals := cfg.IKEProposals
	if ikeProposals == "" {
		ikeProposals = "aes256-sha256-modp2048"
	}
	espProposals := cfg.ESPProposals
	if espProposals == "" {
		espProposals = "aes256-sha256-modp2048"
	}

	swanctlConf := fmt.Sprintf(`connections {
    %s {
        version = 2
        remote_addrs = %s
        encap = yes
        mobike = yes
        dpd_delay = 60s
        local {
            auth = pubkey
            id = %s
            certs = privycs-client.pem
        }
        remote {
            auth = pubkey
            id = %s
        }
        children {
            %s {
                remote_ts = 0.0.0.0/0
                start_action = trap
                dpd_action = restart
                esp_proposals = %s
            }
        }
        proposals = %s
    }
}
`, cfg.ConnectionName, cfg.RemoteAddress, cfg.LocalID, cfg.RemoteID,
		cfg.ConnectionName, espProposals, ikeProposals)

	client := NewHelperClient()
	if !client.IsHelperReachable() {
		return fmt.Errorf("privileged helper not running — install it in Settings → Privileged Helper")
	}
	resp, err := client.SendCommand("ipsec_configure", map[string]string{
		"conn_name":    cfg.ConnectionName,
		"ca_cert":      cfg.CACertPEM,
		"client_cert":  cfg.ClientCertPEM,
		"client_key":   cfg.ClientKeyPEM,
		"swanctl_conf": swanctlConf,
	})
	if err != nil {
		return fmt.Errorf("helper ipsec_configure failed: %w", err)
	}
	if !resp.Success {
		return fmt.Errorf("ipsec configure failed: %s", resp.Error)
	}
	log.Printf("IPSec config installed and loaded via helper")
	return nil
}

func (i *IPSecProtocol) upLinux(ctx context.Context) error {
	client := NewHelperClient()
	if !client.IsHelperReachable() {
		return fmt.Errorf("privileged helper not running — install it in Settings → Privileged Helper")
	}
	resp, err := client.SendCommand("connect", map[string]string{
		"protocol":        "ipsec",
		"interface":       i.connName,
		"connection_name": i.connName,
	})
	if err != nil {
		return fmt.Errorf("helper connect failed: %w", err)
	}
	if !resp.Success {
		return fmt.Errorf("swanctl initiate failed: %s", resp.Error)
	}
	i.connectedAt = time.Now()
	log.Println("IPSec connected via helper")
	return nil
}

func (i *IPSecProtocol) downLinux(ctx context.Context) error {
	client := NewHelperClient()
	if client.IsHelperReachable() {
		client.SendCommand("disconnect", map[string]string{
			"protocol":        "ipsec",
			"interface":       i.connName,
			"connection_name": i.connName,
		})
	}
	i.connectedAt = time.Time{}
	log.Println("IPSec disconnected")
	return nil
}

// ============================================================================
// macOS: scutil (built-in IKEv2)
// ============================================================================

func (i *IPSecProtocol) upMacOS(ctx context.Context) error {
	out, err := execHiddenContext(ctx, "scutil", "--nc", "start", i.connName).CombinedOutput()
	if err != nil {
		return fmt.Errorf("scutil start failed: %s: %w", string(out), err)
	}
	i.connectedAt = time.Now()
	return nil
}

func (i *IPSecProtocol) downMacOS(ctx context.Context) error {
	execHiddenContext(ctx, "scutil", "--nc", "stop", i.connName).Run()
	i.connectedAt = time.Time{}
	return nil
}

// ============================================================================
// Windows: PowerShell Add-VpnConnection / rasdial
// ============================================================================

func (i *IPSecProtocol) configureWindows(cfg *IPSecConfig) error {
	// Fast path: already configured in this session
	if i.configured && i.serverAddr == cfg.RemoteAddress {
		return nil
	}
	// First time after app start: check if Windows already has this connection
	checkCmd := fmt.Sprintf(
		`(Get-VpnConnection -Name '%s' -ErrorAction SilentlyContinue).ServerAddress`,
		cfg.ConnectionName)
	chkOut, chkErr := execHidden("powershell", "-NoProfile", "-Command", checkCmd).CombinedOutput()
	if chkErr == nil && strings.TrimSpace(string(chkOut)) == cfg.RemoteAddress {
		i.configured = true
		log.Printf("IPSec: '%s' already exists in Windows, skipping create", cfg.ConnectionName)
		return nil
	}

	psScript := fmt.Sprintf(
		`Add-VpnConnection -Name '%s' -ServerAddress '%s' -TunnelType IKEv2 -AuthenticationMethod MachineCertificate -EncryptionLevel Required -Force`,
		cfg.ConnectionName, cfg.RemoteAddress)

	out, err := execHidden("powershell", "-Command", psScript).CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to create VPN connection: %s: %w", string(out), err)
	}
	i.configured = true
	log.Printf("Windows IKEv2 connection created: %s", cfg.ConnectionName)
	return nil
}

func (i *IPSecProtocol) upWindows(ctx context.Context) error {
	log.Printf("Connecting IPSec %s via rasdial...", i.connName)
	out, err := execHiddenContext(ctx, "rasdial", i.connName).CombinedOutput()
	if err != nil {
		log.Printf("rasdial failed: %s", string(out))
		return fmt.Errorf("rasdial failed: %s: %w", string(out), err)
	}
	i.connectedAt = time.Now()
	log.Printf("IPSec connected: %s", i.connName)
	return nil
}

func (i *IPSecProtocol) downWindows(ctx context.Context) error {
	log.Printf("Disconnecting IPSec %s...", i.connName)
	execHiddenContext(ctx, "rasdial", i.connName, "/disconnect").Run()
	i.connectedAt = time.Time{}
	log.Printf("IPSec disconnected: %s", i.connName)
	return nil
}

// ============================================================================
// Helpers
// ============================================================================

// redactCredentials replaces sensitive values in output strings for safe logging
func redactCredentials(output string, secrets ...string) string {
	for _, secret := range secrets {
		if secret != "" {
			output = strings.ReplaceAll(output, secret, "***REDACTED***")
		}
	}
	return output
}

// ProtocolError represents a protocol-specific error
type ProtocolError struct {
	Protocol string
	Message  string
}

func (e *ProtocolError) Error() string {
	return e.Protocol + ": " + e.Message
}
