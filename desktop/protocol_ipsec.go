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
		out, err := execHidden("sudo", "swanctl", "--list-sas", "--ike", i.connName).CombinedOutput()
		if err == nil && strings.Contains(string(out), "ESTABLISHED") {
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
		// Check connection status AND get traffic stats in one PowerShell call
		psCmd := fmt.Sprintf(
			`$vpn = Get-VpnConnection -Name '%s' -ErrorAction SilentlyContinue; `+
				`if ($vpn) { $vpn.ConnectionStatus } else { 'NotFound' }`,
			i.connName)
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

	p12Path := filepath.Join(appDataDir(), i.connName+".p12")
	p12Password := profile.Local.P12Password
	if p12Password == "" {
		// Empty p12-password field means the server used the default export password.
		// This is set during certificate generation on the gateway.
		p12Password = "privycs"
	}

	// Never log the p12 password — it appears in PowerShell scripts below
	log.Printf("IPSec: creating Windows VPN connection '%s' -> %s (first-time setup)", i.connName, profile.Remote.Addr)

	// PowerShell script to:
	// 1. Import PKCS#12 certificate bundle into Windows cert store
	// 2. Create IKEv2 VPN connection
	psScript := fmt.Sprintf(`
$ErrorActionPreference = 'Stop'

# Import PKCS#12 certificate (includes CA + client cert + key)
$p12Path = '%s'
$p12Password = ConvertTo-SecureString -String '%s' -AsPlainText -Force
try {
    Import-PfxCertificate -FilePath $p12Path -CertStoreLocation Cert:\LocalMachine\My -Password $p12Password -ErrorAction Stop | Out-Null
    Write-Host 'Certificate imported to LocalMachine\My'
} catch {
    # Try CurrentUser if LocalMachine fails (no admin)
    Import-PfxCertificate -FilePath $p12Path -CertStoreLocation Cert:\CurrentUser\My -Password $p12Password | Out-Null
    Write-Host 'Certificate imported to CurrentUser\My'
}

# Remove existing VPN connection if exists
Remove-VpnConnection -Name '%s' -Force -ErrorAction SilentlyContinue

# Create IKEv2 VPN connection
Add-VpnConnection -Name '%s' -ServerAddress '%s' -TunnelType IKEv2 -AuthenticationMethod MachineCertificate -EncryptionLevel Required -RememberCredential -Force
Write-Host 'VPN connection created: %s -> %s'
`,
		p12Path, p12Password,
		i.connName,
		i.connName, profile.Remote.Addr,
		i.connName, profile.Remote.Addr,
	)

	// Run elevated
	cmd := execHidden("powershell", "-NoProfile", "-Command",
		fmt.Sprintf("Start-Process powershell -ArgumentList '-NoProfile','-Command','%s' -Verb RunAs -Wait -WindowStyle Hidden",
			escapePowerShellString(psScript)))
	setupOut, setupErr := cmd.CombinedOutput()
	if setupErr != nil {
		// Redact credentials from log output — the PowerShell script contains
		// the PKCS#12 password which must never appear in log files.
		safeOutput := redactCredentials(string(setupOut), p12Password)
		log.Printf("Windows IPSec setup output: %s", safeOutput)
		return fmt.Errorf("failed to configure IPSec on Windows: %s: %w", safeOutput, setupErr)
	}

	i.configured = true
	log.Printf("Windows IKEv2 VPN connection created: %s -> %s", i.connName, profile.Remote.Addr)
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
	certDir := "/etc/swanctl"

	// Write certificates with sudo
	if cfg.CACertPEM != "" {
		writeSudoFile(certDir+"/x509ca/privycs-ca.pem", cfg.CACertPEM, 0644)
	}
	if cfg.ClientCertPEM != "" {
		writeSudoFile(certDir+"/x509/privycs-client.pem", cfg.ClientCertPEM, 0644)
	}
	if cfg.ClientKeyPEM != "" {
		writeSudoFile(certDir+"/private/privycs-client.pem", cfg.ClientKeyPEM, 0600)
	}

	// Write swanctl.conf
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

	confPath := certDir + "/conf.d/privycs-vpn.conf"
	writeSudoFile(confPath, swanctlConf, 0644)

	// Reload swanctl
	execHidden("sudo", "swanctl", "--load-all").Run()
	log.Printf("IPSec config written: %s", confPath)
	return nil
}

func (i *IPSecProtocol) upLinux(ctx context.Context) error {
	out, err := execHiddenContext(ctx, "sudo", "swanctl", "--initiate", "--ike", i.connName, "--child", i.connName).CombinedOutput()
	if err != nil {
		return fmt.Errorf("swanctl initiate failed: %s: %w", string(out), err)
	}
	i.connectedAt = time.Now()
	log.Println("IPSec connected via swanctl")
	return nil
}

func (i *IPSecProtocol) downLinux(ctx context.Context) error {
	execHiddenContext(ctx, "sudo", "swanctl", "--terminate", "--ike", i.connName).Run()
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

// writeSudoFile writes content to a path using sudo tee
func writeSudoFile(path string, content string, mode os.FileMode) error {
	// Ensure parent directory exists
	dir := filepath.Dir(path)
	execHidden("sudo", "mkdir", "-p", dir).Run()

	cmd := execHidden("sudo", "tee", path)
	cmd.Stdin = strings.NewReader(content)
	cmd.Stdout = nil
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to write %s: %v - %s", path, err, string(output))
	}

	// Set permissions
	execHidden("sudo", "chmod", fmt.Sprintf("%o", mode), path).Run()
	return nil
}

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
