package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
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

	// Management-interface state cache. OpenVPN 2.7.1 Windows has a
	// bug where rapid connect/disconnect cycles on the TCP management
	// port trigger an internal assertion
	// ("Assertion failed at win32.c:332 (!socket_defined(ne->sd))")
	// that kills openvpn.exe after ~13 seconds. Status() is called
	// every 500 ms during the connect-poll loop and every 2 s by the
	// UI poll — without caching that sums to 3+ new TCP connections
	// per second to 127.0.0.1:7505, which is far above the cadence
	// the buggy management loop can tolerate. A short-lived cache
	// (3 s) collapses the worst case to 1 query every 3 s.
	cachedState     string
	cachedStateTime time.Time
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
		// Windows: try privileged helper first (no UAC prompt).
		client := NewHelperClient()
		if client.IsHelperReachable() {
			log.Printf("Using privileged helper for OpenVPN connect")
			resp, err := client.SendCommand("connect", map[string]string{
				"protocol":    "openvpn",
				"config_path": o.configPath,
				"log_path":    logPath,
				"pid_path":    pidPath,
				"mgmt_host":   ovpnMgmtHost,
				"mgmt_port":   ovpnMgmtPort,
			})
			if err == nil {
				if resp.Success {
					o.connectedAt = time.Now()
					log.Printf("OpenVPN started via helper: %s", resp.Output)
					return nil
				}
				return fmt.Errorf("openvpn start via helper failed: %s", resp.Error)
			}
			log.Printf("Helper communication failed, falling back to UAC: %v", err)
		}

		// Fallback: UAC-elevated Start-Process (prompt per connect).
		// DNS is handled server-side via push "dhcp-option DNS" which uses
		// NRPT on Windows instead of netsh (avoids DNS bugs).
		psCmd := fmt.Sprintf(
			"Start-Process -FilePath '%s' -ArgumentList '--config','%s','--log','%s','--management','%s','%s' -Verb RunAs -WindowStyle Hidden",
			escapePowerShellString(ovpnExe), escapePowerShellString(o.configPath), escapePowerShellString(logPath), ovpnMgmtHost, ovpnMgmtPort)
		o.cmd = execHiddenContext(ctx, "powershell", "-NoProfile", "-Command", psCmd)
	} else {
		// Linux/macOS: try privileged helper first (no sudo/password prompts)
		client := NewHelperClient()
		if client.IsHelperReachable() {
			log.Printf("Using privileged helper for OpenVPN connect")
			resp, err := client.SendCommand("connect", map[string]string{
				"protocol":    "openvpn",
				"config_path": o.configPath,
				"log_path":    logPath,
				"pid_path":    pidPath,
				"mgmt_host":   ovpnMgmtHost,
				"mgmt_port":   ovpnMgmtPort,
			})
			if err == nil {
				if resp.Success {
					o.connectedAt = time.Now()
					log.Printf("OpenVPN started via helper")
					return nil
				}
				return fmt.Errorf("openvpn start via helper failed: %s", resp.Error)
			}
			log.Printf("Helper communication failed, falling back to sudo: %v", err)
		}

		// Fallback: direct sudo execution
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
		// Helper-first: kill by PID file (no UAC).
		client := NewHelperClient()
		if client.IsHelperReachable() {
			pidPath := filepath.Join(appDataDir(), "openvpn.pid")
			resp, err := client.SendCommand("disconnect", map[string]string{
				"protocol": "openvpn",
				"pid_path": pidPath,
			})
			if err == nil && resp.Success {
				log.Println("OpenVPN stopped via helper")
			} else {
				log.Printf("Helper disconnect returned err=%v success=%v", err, resp.Success)
			}
		} else {
			// Fallback: UAC-elevated Stop-Process.
			psCmd := `Stop-Process -Name openvpn -Force -ErrorAction SilentlyContinue`
			execHiddenContext(ctx, "powershell", "-NoProfile", "-Command",
				fmt.Sprintf("Start-Process powershell -ArgumentList '-NoProfile','-Command','%s' -Verb RunAs -Wait -WindowStyle Hidden", psCmd)).Run()
			log.Println("OpenVPN: process killed via UAC fallback")
		}
	} else {
		o.downUnixOpenVPN(ctx)
	}

	o.connectedAt = time.Time{}
	log.Println("OpenVPN stopped")
	return nil
}

// downUnixOpenVPN stops OpenVPN on Linux/macOS, using the helper if available.
func (o *OpenVPNProtocol) downUnixOpenVPN(ctx context.Context) {
	pidPath := filepath.Join(appDataDir(), "openvpn.pid")

	// Try privileged helper first (no sudo/password prompts)
	client := NewHelperClient()
	if client.IsHelperReachable() {
		log.Printf("Using privileged helper for OpenVPN disconnect")
		resp, err := client.SendCommand("disconnect", map[string]string{
			"protocol": "openvpn",
			"pid_path": pidPath,
		})
		if err == nil && resp.Success {
			log.Println("OpenVPN stopped via helper")
			o.cmd = nil
			return
		}
		log.Printf("Helper disconnect failed, falling back to direct: %v / %s", err, resp.Error)
	}

	// Fallback: try PID file first, then tracked process
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

// queryOpenVPNState opens a short-lived TCP connection to the OpenVPN
// management interface and returns the current tunnel state string. The
// management protocol replies to `state` with a line prefixed by ">STATE:"
// whose second comma-separated field is the state name — CONNECTED means
// the handshake completed, routes are installed, and the tunnel is truly
// up. Any earlier state (CONNECTING, WAIT, AUTH, GET_CONFIG, ASSIGN_IP,
// ADD_ROUTES) means "still working"; RECONNECTING/EXITING means failure.
// Returns an empty string on any I/O error — caller treats that as
// "cannot determine, assume not connected".
func queryOpenVPNState(host, port string) string {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), 1*time.Second)
	if err != nil {
		return ""
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(1 * time.Second))

	if _, err := conn.Write([]byte("state\nquit\n")); err != nil {
		return ""
	}

	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil && err != io.EOF {
		return ""
	}

	for _, line := range strings.Split(string(buf[:n]), "\n") {
		line = strings.TrimSpace(line)
		// OpenVPN prefixes async state messages with ">STATE:", sync
		// responses with an unprefixed epoch,<state>,... line. Match both.
		line = strings.TrimPrefix(line, ">STATE:")
		parts := strings.Split(line, ",")
		if len(parts) >= 2 {
			// Epoch timestamp is parts[0] — it's all digits for real
			// state lines; everything else (OK, help text) fails this
			// check and is silently ignored.
			if len(parts[0]) >= 8 && isAllDigits(parts[0]) {
				return parts[1]
			}
		}
	}
	return ""
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func (o *OpenVPNProtocol) Status() ProtocolStatus {
	status := ProtocolStatus{
		Protocol:      "openvpn",
		ServerAddress: o.serverAddr,
		LocalAddress:  o.localAddr,
	}

	// Process liveness is a necessary but NOT sufficient signal — openvpn.exe
	// may be stuck in a RESOLVE-retry loop or crashing repeatedly at NETSH,
	// which previously made the UI falsely report "connected" for ~10s until
	// the user noticed traffic wasn't flowing. Management-interface "state"
	// query is the authoritative source.
	processRunning := false
	if runtime.GOOS == "windows" {
		out, err := execHidden("tasklist", "/FI", "IMAGENAME eq openvpn.exe", "/NH").CombinedOutput()
		processRunning = err == nil && strings.Contains(string(out), "openvpn.exe")
	} else {
		pidPath := filepath.Join(appDataDir(), "openvpn.pid")
		if pidData, err := os.ReadFile(pidPath); err == nil {
			var pid int
			if _, err := fmt.Sscan(strings.TrimSpace(string(pidData)), &pid); err == nil && pid > 0 {
				if proc, err := os.FindProcess(pid); err == nil {
					processRunning = proc.Signal(nil) == nil
				}
			}
		}
	}

	if !processRunning {
		return status
	}

	// Query management interface for the actual tunnel state. Only CONNECTED
	// counts as "truly up" — any transient state (CONNECTING, WAIT, AUTH,
	// GET_CONFIG, ASSIGN_IP, ADD_ROUTES) means setup is still in progress.
	// If the management socket isn't reachable at all, we fall back to
	// "process is running but state unknown" — report not connected, since
	// the user-facing guarantee is that "connected" means traffic flows.
	//
	// Cache the result for 3 s to prevent hammering the management TCP
	// socket. OpenVPN 2.7.1 on Windows 11 26200 has a crash bug
	// (win32.c:332 assertion) triggered by rapid connect/disconnect
	// cycles on the management port, which happens if our 500 ms
	// connect-poll and 2 s UI-status-poll both hit it uncached.
	var state string
	if time.Since(o.cachedStateTime) < 3*time.Second {
		state = o.cachedState
	} else {
		state = queryOpenVPNState(ovpnMgmtHost, ovpnMgmtPort)
		o.cachedState = state
		o.cachedStateTime = time.Now()
	}
	if state != "CONNECTED" {
		return status
	}

	status.Connected = true
	status.ConnectedAt = o.connectedAt.Format(time.RFC3339)
	if runtime.GOOS == "windows" {
		status.BytesRx, status.BytesTx = getWindowsTrafficStats("OpenVPN")
	} else if runtime.GOOS == "linux" {
		status.BytesRx, status.BytesTx = getLinuxInterfaceStats("tun0")
	}
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
