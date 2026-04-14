package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// OpenVPN management interface constants
const (
	ovpnMgmtHost = "127.0.0.1"
	ovpnMgmtPort = "7505"
)

// OpenVPNProtocol implements VPNProtocol for OpenVPN connections
type OpenVPNProtocol struct {
	configPath  string
	cmd         *exec.Cmd
	connectedAt time.Time
	serverAddr  string
	localAddr   string
}

// NewOpenVPNProtocol creates a new OpenVPN protocol handler
func NewOpenVPNProtocol() *OpenVPNProtocol {
	return &OpenVPNProtocol{
		configPath: filepath.Join(appDataDir(), "privycs0.ovpn"),
	}
}

// SetTunnelName updates the config file path to match the connection name.
func (o *OpenVPNProtocol) SetTunnelName(name string) {
	if name == "" {
		return
	}
	o.configPath = filepath.Join(appDataDir(), name+".ovpn")
}

func (o *OpenVPNProtocol) Name() string { return "openvpn" }

func (o *OpenVPNProtocol) IsAvailable() bool {
	return findOpenVPNExe() != ""
}

// findOpenVPNExe locates the OpenVPN executable
func findOpenVPNExe() string {
	// Check PATH first
	if p, err := exec.LookPath("openvpn"); err == nil {
		return p
	}
	if runtime.GOOS == "windows" {
		// Standard Windows install paths
		paths := []string{
			`C:\Program Files\OpenVPN\bin\openvpn.exe`,
			`C:\Program Files (x86)\OpenVPN\bin\openvpn.exe`,
			`C:\Program Files\OpenVPN Connect\openvpn.exe`,
		}
		for _, p := range paths {
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}
	return ""
}

func (o *OpenVPNProtocol) Up(ctx context.Context) error {
	ovpnExe := findOpenVPNExe()
	if ovpnExe == "" {
		return fmt.Errorf("openvpn not found — install OpenVPN from https://openvpn.net/community-downloads/")
	}

	if _, err := os.Stat(o.configPath); os.IsNotExist(err) {
		return fmt.Errorf("no OpenVPN config file found — import a .ovpn file first")
	}

	logPath := filepath.Join(appDataDir(), "openvpn.log")
	pidPath := filepath.Join(appDataDir(), "openvpn.pid")

	log.Printf("Starting OpenVPN via %s", ovpnExe)

	if runtime.GOOS == "windows" {
		// Windows: run elevated via Start-Process -Verb RunAs
		// No extra driver flags — OpenVPN 2.7+ uses DCO by default.
		// DNS is handled server-side via push "dhcp-option DNS"
		// which uses NRPT on Windows instead of netsh (avoids DNS bugs).
		// Escape paths for PowerShell single-quote context to prevent command injection.
		psCmd := fmt.Sprintf(
			"Start-Process -FilePath '%s' -ArgumentList '--config','%s','--log','%s','--management','%s','%s' -Verb RunAs -WindowStyle Hidden",
			escapePowerShellString(ovpnExe), escapePowerShellString(o.configPath), escapePowerShellString(logPath), ovpnMgmtHost, ovpnMgmtPort)
		o.cmd = execHiddenContext(ctx, "powershell", "-NoProfile", "-Command", psCmd)
	} else {
		// Linux/macOS: use sudo
		o.cmd = execHiddenContext(ctx, "sudo", ovpnExe,
			"--config", o.configPath,
			"--daemon",
			"--log", logPath,
			"--writepid", pidPath,
			"--management", ovpnMgmtHost, ovpnMgmtPort,
		)
	}

	if err := o.cmd.Start(); err != nil {
		return fmt.Errorf("failed to start openvpn: %w", err)
	}

	o.connectedAt = time.Now()
	log.Printf("OpenVPN started")
	return nil
}

func (o *OpenVPNProtocol) Down(ctx context.Context) error {
	log.Println("Stopping OpenVPN...")

	// Reset connectedAt FIRST — the status fallback uses this to report
	// "still connected" even when the process is gone. Must be cleared
	// before SIGTERM so the status emitter doesn't flip back to connected.
	o.connectedAt = time.Time{}

	if runtime.GOOS == "windows" {
		// Force kill OpenVPN process (elevated).
		// Management interface SIGTERM doesn't work with DCO in OpenVPN 2.7.
		psCmd := `Stop-Process -Name openvpn -Force -ErrorAction SilentlyContinue`
		execHiddenContext(ctx, "powershell", "-NoProfile", "-Command",
			fmt.Sprintf("Start-Process powershell -ArgumentList '-NoProfile','-Command','%s' -Verb RunAs -Wait -WindowStyle Hidden", psCmd)).Run()
		log.Println("OpenVPN: process killed")
	} else {
		// Unix: try PID file first, then tracked process
		pidPath := filepath.Join(appDataDir(), "openvpn.pid")
		pidData, err := os.ReadFile(pidPath)
		if err == nil {
			var pid int
			if _, err := fmt.Sscan(strings.TrimSpace(string(pidData)), &pid); err == nil && pid > 0 {
				if proc, err := os.FindProcess(pid); err == nil {
					proc.Signal(os.Interrupt)
					time.Sleep(1 * time.Second)
					proc.Kill()
				}
			}
			os.Remove(pidPath)
		}

		if o.cmd != nil && o.cmd.Process != nil {
			o.cmd.Process.Kill()
			o.cmd = nil
		}
	}

	o.connectedAt = time.Time{}
	log.Println("OpenVPN stopped")
	return nil
}

func (o *OpenVPNProtocol) Status() ProtocolStatus {
	status := ProtocolStatus{
		Protocol:      "openvpn",
		ServerAddress: o.serverAddr,
		LocalAddress:  o.localAddr,
	}

	// Check if OpenVPN process is running
	if runtime.GOOS == "windows" {
		// On Windows, proc.Signal(nil) doesn't work. Use tasklist instead.
		out, err := execHidden("tasklist", "/FI", "IMAGENAME eq openvpn.exe", "/NH").CombinedOutput()
		if err == nil && strings.Contains(string(out), "openvpn.exe") {
			status.Connected = true
			status.ConnectedAt = o.connectedAt.Format(time.RFC3339)
			// OpenVPN TAP/TUN adapter — search by common adapter names
			status.BytesRx, status.BytesTx = getWindowsTrafficStats("OpenVPN")
		}
	} else {
		// On Unix, check PID file + signal
		pidPath := filepath.Join(appDataDir(), "openvpn.pid")
		pidData, err := os.ReadFile(pidPath)
		if err == nil {
			var pid int
			if _, err := fmt.Sscan(strings.TrimSpace(string(pidData)), &pid); err == nil && pid > 0 {
				if proc, err := os.FindProcess(pid); err == nil {
					if proc.Signal(nil) == nil {
						status.Connected = true
						status.ConnectedAt = o.connectedAt.Format(time.RFC3339)
						// Read traffic stats from tun interface
						if runtime.GOOS == "linux" {
							status.BytesRx, status.BytesTx = getLinuxInterfaceStats("tun0")
						}
					}
				}
			}
		}
	}

	// No fallback — only trust actual process detection, not timestamps.
	// The connectedAt fallback caused disconnect to appear to "reconnect"
	// because the status emitter would see connectedAt set and report connected.

	return status
}

func (o *OpenVPNProtocol) Configure(cfg []byte) error {
	os.MkdirAll(filepath.Dir(o.configPath), 0700)

	if err := os.WriteFile(o.configPath, cfg, 0600); err != nil {
		return fmt.Errorf("failed to write OpenVPN config: %w", err)
	}

	// Extract server address and local IP for display
	for _, line := range strings.Split(string(cfg), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "remote ") {
			parts := strings.Fields(line)
			if len(parts) >= 3 {
				o.serverAddr = parts[1] + ":" + parts[2]
			} else if len(parts) == 2 {
				o.serverAddr = parts[1]
			}
		}
		// ifconfig-push or ifconfig line contains the assigned VPN IP
		if strings.HasPrefix(line, "ifconfig ") || strings.HasPrefix(line, "ifconfig-push ") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				o.localAddr = parts[1]
			}
		}
	}

	log.Printf("OpenVPN config written to %s", o.configPath)
	return nil
}
