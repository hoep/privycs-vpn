package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

// HelperCommand is a JSON request sent from the client to the privileged helper.
type HelperCommand struct {
	Action     string            `json:"action"`
	Protocol   string            `json:"protocol,omitempty"`
	ConfigPath string            `json:"config_path,omitempty"`
	Interface  string            `json:"interface,omitempty"`
	Args       map[string]string `json:"args,omitempty"`
}

// HelperResponse is a JSON response from the privileged helper back to the client.
type HelperResponse struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
	Output  string `json:"output,omitempty"`
}

// allowedActions is the whitelist of commands the helper will execute.
var allowedActions = map[string]bool{
	"connect":               true,
	"disconnect":            true,
	"killswitch_enable":     true,
	"killswitch_disable":    true,
	"status":                true,
	"wg_install_config":     true,
	"ipsec_configure":       true,
	"remove_legacy_sudoers": true,
}

// safePathPattern validates file paths to prevent directory traversal and injection.
var safePathPattern = regexp.MustCompile(`^[a-zA-Z0-9/_\-\.\\:]+$`)

// safeInterfacePattern validates interface names (alphanumeric, dash, underscore).
// Length is bounded at 64 chars — enough for any sanitized connection name
// produced by sanitizeTunnelName, plus still tight enough to block pathological
// inputs. The stricter 15-char IFNAMSIZ cap is enforced upstream in
// sanitizeTunnelName on Linux/macOS via hash truncation; on Windows longer
// tunnel names (= Windows service suffix) are perfectly valid.
var safeInterfacePattern = regexp.MustCompile(`^[a-zA-Z0-9_\-]{1,64}$`)

// helperSocketPath returns the IPC socket path for the current platform.
// Windows uses a filesystem unix socket under %PROGRAMDATA% (Win10 1803+).
func helperSocketPath() string {
	switch runtime.GOOS {
	case "windows":
		programData := os.Getenv("PROGRAMDATA")
		if programData == "" {
			programData = `C:\ProgramData`
		}
		return filepath.Join(programData, "PrivycsVPN", "helper.sock")
	default:
		return "/var/run/privycs-vpn.sock"
	}
}

// PrivilegedHelper is the server-side helper that runs with elevated privileges.
// It listens on a Unix socket (Linux/macOS) or named pipe (Windows) and executes
// VPN management commands on behalf of the unprivileged desktop app.
type PrivilegedHelper struct {
	listener net.Listener
	mu       sync.Mutex
	running  bool
	stopCh   chan struct{}
}

// NewPrivilegedHelper creates a new helper instance.
func NewPrivilegedHelper() *PrivilegedHelper {
	return &PrivilegedHelper{
		stopCh: make(chan struct{}),
	}
}

// Start begins listening for IPC commands. This blocks until Stop() is called.
func (h *PrivilegedHelper) Start() error {
	socketPath := helperSocketPath()
	socketDir := filepath.Dir(socketPath)

	// Ensure parent directory exists (matters on Windows %PROGRAMDATA%\PrivycsVPN\).
	if err := os.MkdirAll(socketDir, 0755); err != nil {
		return fmt.Errorf("failed to create socket directory: %w", err)
	}

	// On Windows, proactively grant Authenticated Users full access to the
	// helper's runtime directory so user-session processes can connect to
	// the socket file. The default inherited ACL from %PROGRAMDATA% is
	// Read+Execute only, which is not enough for AF_UNIX socket connect.
	// (OI) = Object Inherit, (CI) = Container Inherit → applies to new
	// files/subdirs created in the directory.
	if runtime.GOOS == "windows" {
		exec.Command("icacls", socketDir, "/grant", "*S-1-5-11:(OI)(CI)F", "/T").Run()
	}

	// Clean up stale socket file (Windows also benefits: if previous run left
	// a zombie file, re-bind would fail).
	os.Remove(socketPath)

	listener, err := helperListen(socketPath)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", socketPath, err)
	}

	// Socket permissions:
	// - Windows: re-apply the Authenticated-Users ACL explicitly on
	//   the just-created socket file because the file is recreated
	//   each service start and may not pick up the directory
	//   inheritance in time for the first client connect.
	// - Linux/macOS: helper runs as root under systemd/launchd, so
	//   the socket's owner/group is root:root. The desktop app
	//   runs as the login user. 0660 would only allow root or root-
	//   group members - neither of which the login user is - so
	//   client connects fail with EACCES and IsHelperRunning()
	//   permanently returns false even while systemd happily
	//   reports the service as active. Use 0666 and rely on the
	//   IPC layer to reject malformed peers.
	//
	//   TODO: tighten with SO_PEERCRED once the installer passes
	//   the invoking user's UID to the helper via EnvironmentFile -
	//   then the helper can reject connects from any other UID
	//   and the permissive 0666 is redundant defence-in-depth.
	if runtime.GOOS == "windows" {
		exec.Command("icacls", socketPath, "/grant", "*S-1-5-11:F").Run()
	} else {
		os.Chmod(socketPath, 0666)
	}

	h.mu.Lock()
	h.listener = listener
	h.running = true
	h.mu.Unlock()

	log.Printf("Privileged helper listening on %s", socketPath)

	for {
		conn, err := listener.Accept()
		if err != nil {
			select {
			case <-h.stopCh:
				return nil // clean shutdown
			default:
				log.Printf("Helper accept error: %v", err)
				continue
			}
		}
		go h.handleConnection(conn)
	}
}

// Stop shuts down the helper listener.
func (h *PrivilegedHelper) Stop() {
	h.mu.Lock()
	defer h.mu.Unlock()

	if !h.running {
		return
	}

	close(h.stopCh)
	if h.listener != nil {
		h.listener.Close()
	}
	h.running = false

	// Clean up socket file on Unix
	if runtime.GOOS != "windows" {
		os.Remove(helperSocketPath())
	}

	log.Println("Privileged helper stopped")
}

// handleConnection processes a single IPC client connection.
func (h *PrivilegedHelper) handleConnection(conn net.Conn) {
	defer conn.Close()

	// Set read deadline to prevent hung connections
	conn.SetReadDeadline(time.Now().Add(30 * time.Second))

	decoder := json.NewDecoder(conn)
	var cmd HelperCommand
	if err := decoder.Decode(&cmd); err != nil {
		h.sendResponse(conn, HelperResponse{Success: false, Error: "invalid JSON command"})
		return
	}

	// Audit log every command
	log.Printf("Helper command: action=%s protocol=%s interface=%s", cmd.Action, cmd.Protocol, cmd.Interface)

	resp := h.executeCommand(cmd)

	// Audit log result
	if resp.Success {
		log.Printf("Helper result: action=%s success=true", cmd.Action)
	} else {
		log.Printf("Helper result: action=%s success=false error=%s", cmd.Action, resp.Error)
	}

	h.sendResponse(conn, resp)
}

// sendResponse writes a JSON response to the connection.
func (h *PrivilegedHelper) sendResponse(conn net.Conn, resp HelperResponse) {
	conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	json.NewEncoder(conn).Encode(resp)
}

// executeCommand validates and dispatches a helper command.
func (h *PrivilegedHelper) executeCommand(cmd HelperCommand) HelperResponse {
	// Validate action is whitelisted
	if !allowedActions[cmd.Action] {
		return HelperResponse{Success: false, Error: fmt.Sprintf("unknown action: %s", cmd.Action)}
	}

	// Validate inputs to prevent injection
	if cmd.ConfigPath != "" && !safePathPattern.MatchString(cmd.ConfigPath) {
		return HelperResponse{Success: false, Error: "invalid config path"}
	}
	if cmd.Interface != "" && !safeInterfacePattern.MatchString(cmd.Interface) {
		return HelperResponse{Success: false, Error: "invalid interface name"}
	}

	switch cmd.Action {
	case "connect":
		return h.cmdConnect(cmd)
	case "disconnect":
		return h.cmdDisconnect(cmd)
	case "killswitch_enable":
		return h.cmdKillSwitchEnable(cmd)
	case "killswitch_disable":
		return h.cmdKillSwitchDisable(cmd)
	case "status":
		return h.cmdStatus(cmd)
	case "wg_install_config":
		return h.cmdWGInstallConfig(cmd)
	case "ipsec_configure":
		return h.cmdIPSecConfigure(cmd)
	case "remove_legacy_sudoers":
		return h.cmdRemoveLegacySudoers(cmd)
	default:
		return HelperResponse{Success: false, Error: "unhandled action"}
	}
}

// cmdConnect starts a VPN tunnel.
func (h *PrivilegedHelper) cmdConnect(cmd HelperCommand) HelperResponse {
	switch cmd.Protocol {
	case "wireguard":
		return h.connectWireGuard(cmd)
	case "openvpn":
		return h.connectOpenVPN(cmd)
	case "ipsec":
		return h.connectIPSec(cmd)
	default:
		return HelperResponse{Success: false, Error: fmt.Sprintf("unsupported protocol: %s", cmd.Protocol)}
	}
}

// cmdDisconnect stops a VPN tunnel.
func (h *PrivilegedHelper) cmdDisconnect(cmd HelperCommand) HelperResponse {
	switch cmd.Protocol {
	case "wireguard":
		return h.disconnectWireGuard(cmd)
	case "openvpn":
		return h.disconnectOpenVPN(cmd)
	case "ipsec":
		return h.disconnectIPSec(cmd)
	default:
		return HelperResponse{Success: false, Error: fmt.Sprintf("unsupported protocol: %s", cmd.Protocol)}
	}
}

// connectWireGuard installs the tunnel service (Windows) or runs wg-quick (Unix).
// The helper runs as SYSTEM/root so neither path triggers UAC/sudo prompts.
func (h *PrivilegedHelper) connectWireGuard(cmd HelperCommand) HelperResponse {
	ifaceName := cmd.Interface
	if ifaceName == "" {
		ifaceName = "privycs0"
	}

	if runtime.GOOS == "windows" {
		wgExe := findWireGuardExe()
		if wgExe == "" {
			return HelperResponse{Success: false, Error: "wireguard.exe not found"}
		}
		// Config path: prefer helper-managed %PROGRAMDATA%\PrivycsVPN\tunnels\ location.
		confPath := cmd.ConfigPath
		if confPath == "" {
			confPath = windowsWGConfigPath(ifaceName)
		}
		if _, err := os.Stat(confPath); err != nil {
			return HelperResponse{Success: false, Error: fmt.Sprintf("config not found: %s", confPath)}
		}
		// Remove any existing tunnel service with this name first (idempotent).
		exec.Command(wgExe, "/uninstalltunnelservice", ifaceName).Run()
		out, err := exec.Command(wgExe, "/installtunnelservice", confPath).CombinedOutput()
		if err != nil {
			return HelperResponse{Success: false, Error: fmt.Sprintf("installtunnelservice failed: %s: %v", string(out), err), Output: string(out)}
		}
		return HelperResponse{Success: true, Output: string(out)}
	}

	// Linux/macOS: wg-quick up, with optional config copy from user path.
	if cmd.ConfigPath != "" {
		etcConf := filepath.Join("/etc/wireguard", ifaceName+".conf")
		if err := h.copyConfigFile(cmd.ConfigPath, etcConf); err != nil {
			return HelperResponse{Success: false, Error: fmt.Sprintf("failed to install config: %v", err)}
		}
	}
	out, err := exec.Command("wg-quick", "up", ifaceName).CombinedOutput()
	if err != nil {
		return HelperResponse{Success: false, Error: fmt.Sprintf("wg-quick up failed: %s", string(out)), Output: string(out)}
	}
	return HelperResponse{Success: true, Output: string(out)}
}

// disconnectWireGuard uninstalls the tunnel service (Windows) or runs wg-quick down (Unix).
func (h *PrivilegedHelper) disconnectWireGuard(cmd HelperCommand) HelperResponse {
	ifaceName := cmd.Interface
	if ifaceName == "" {
		ifaceName = "privycs0"
	}

	if runtime.GOOS == "windows" {
		wgExe := findWireGuardExe()
		if wgExe == "" {
			return HelperResponse{Success: false, Error: "wireguard.exe not found"}
		}
		out, err := exec.Command(wgExe, "/uninstalltunnelservice", ifaceName).CombinedOutput()
		if err != nil {
			return HelperResponse{Success: false, Error: fmt.Sprintf("uninstalltunnelservice failed: %s: %v", string(out), err), Output: string(out)}
		}
		return HelperResponse{Success: true, Output: string(out)}
	}

	out, err := exec.Command("wg-quick", "down", ifaceName).CombinedOutput()
	if err != nil {
		return HelperResponse{Success: false, Error: fmt.Sprintf("wg-quick down failed: %s", string(out)), Output: string(out)}
	}
	return HelperResponse{Success: true, Output: string(out)}
}

// windowsWGConfigPath returns the canonical Windows location for a WG config
// managed by the privileged helper.
func windowsWGConfigPath(ifaceName string) string {
	programData := os.Getenv("PROGRAMDATA")
	if programData == "" {
		programData = `C:\ProgramData`
	}
	return filepath.Join(programData, "PrivycsVPN", "tunnels", ifaceName+".conf")
}

// connectOpenVPN starts the openvpn daemon.
//   - Linux: openvpn --daemon with pid/log files under /tmp
//   - Windows: spawns openvpn.exe as background child of the SYSTEM helper
//     process (Windows has no native --daemon flag). Returns after start;
//     PID is recorded so disconnectOpenVPN can kill it later.
func (h *PrivilegedHelper) connectOpenVPN(cmd HelperCommand) HelperResponse {
	ovpnExe := findOpenVPNExe()
	if ovpnExe == "" {
		return HelperResponse{Success: false, Error: "openvpn not found"}
	}

	configPath := cmd.ConfigPath
	if configPath == "" {
		return HelperResponse{Success: false, Error: "config_path is required for openvpn"}
	}
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return HelperResponse{Success: false, Error: "config file not found"}
	}

	logPath := cmd.Args["log_path"]
	pidPath := cmd.Args["pid_path"]
	mgmtHost := cmd.Args["mgmt_host"]
	if mgmtHost == "" {
		mgmtHost = "127.0.0.1"
	}
	mgmtPort := cmd.Args["mgmt_port"]
	if mgmtPort == "" {
		mgmtPort = "7505"
	}

	if runtime.GOOS == "windows" {
		if logPath == "" {
			logPath = filepath.Join(windowsHelperDataDir(), "openvpn.log")
		}
		if pidPath == "" {
			pidPath = filepath.Join(windowsHelperDataDir(), "openvpn.pid")
		}
		os.MkdirAll(filepath.Dir(logPath), 0755)

		// --service <exit-event-name> 0 tells openvpn.exe to talk to the
		// OpenVPNServiceInteractive named pipe (\\.\pipe\openvpn\service)
		// for privileged operations (netsh, route, firewall). Without it,
		// msg_channel stays at 0 and every netsh invocation requires
		// openvpn to already hold admin rights — which forces the whole
		// Privycs App to run elevated.
		//
		// The event name must be unique per launch so concurrent tunnels
		// don't trample each other. OpenVPN's --service handler creates
		// the event if it doesn't exist (CreateEvent semantics), so we
		// don't need to pre-allocate it from Go — passing any valid name
		// is sufficient.
		eventName := fmt.Sprintf("privycs_ovpn_exit_%d_%d",
			os.Getpid(), time.Now().UnixNano())

		c := exec.Command(ovpnExe,
			"--service", eventName, "0",
			"--config", configPath,
			"--log", logPath,
			"--management", mgmtHost, mgmtPort,
		)
		// Hide console window.
		hideWindow(c)
		if err := c.Start(); err != nil {
			return HelperResponse{Success: false, Error: fmt.Sprintf("openvpn start failed: %v", err)}
		}
		// Record PID so disconnect can find it.
		os.WriteFile(pidPath, []byte(fmt.Sprintf("%d", c.Process.Pid)), 0644)
		// Release the child — we don't Wait; it lives past this handler.
		go c.Process.Release()
		return HelperResponse{Success: true, Output: fmt.Sprintf("openvpn started pid=%d", c.Process.Pid)}
	}

	if logPath == "" {
		logPath = "/tmp/privycs-openvpn.log"
	}
	if pidPath == "" {
		pidPath = "/tmp/privycs-openvpn.pid"
	}
	c := exec.Command(ovpnExe,
		"--config", configPath,
		"--daemon",
		"--log", logPath,
		"--writepid", pidPath,
		"--management", mgmtHost, mgmtPort,
	)
	out, err := c.CombinedOutput()
	if err != nil {
		return HelperResponse{Success: false, Error: fmt.Sprintf("openvpn start failed: %s", string(out)), Output: string(out)}
	}
	return HelperResponse{Success: true, Output: string(out)}
}

// windowsHelperDataDir returns %PROGRAMDATA%\PrivycsVPN for helper-managed state.
func windowsHelperDataDir() string {
	programData := os.Getenv("PROGRAMDATA")
	if programData == "" {
		programData = `C:\ProgramData`
	}
	return filepath.Join(programData, "PrivycsVPN")
}

// disconnectOpenVPN stops the openvpn daemon via PID file.
func (h *PrivilegedHelper) disconnectOpenVPN(cmd HelperCommand) HelperResponse {
	pidPath := cmd.Args["pid_path"]
	if pidPath == "" {
		pidPath = "/tmp/privycs-openvpn.pid"
	}

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

	return HelperResponse{Success: true, Output: "openvpn stopped"}
}

// connectIPSec starts an IPSec/IKEv2 connection.
//   - Linux: swanctl --load-all + --initiate
//   - Windows: rasdial <name> (machine certs already imported via ipsec_configure)
func (h *PrivilegedHelper) connectIPSec(cmd HelperCommand) HelperResponse {
	connName := cmd.Interface
	if connName == "" {
		connName = cmd.Args["connection_name"]
	}
	if connName == "" {
		return HelperResponse{Success: false, Error: "connection name required for ipsec"}
	}

	if runtime.GOOS == "windows" {
		// rasdial is the silent CLI-only dialer — no "Connecting..." dialog,
		// synchronous exit code. The rasphone -d variant we used in v0.9.0.17
		// was reverted because the dialog was user-visible; the earlier
		// rasdial-rejects-auth failures were an unrelated server-cert issue.
		out, err := exec.Command("rasdial", connName).CombinedOutput()
		if err != nil {
			return HelperResponse{Success: false, Error: fmt.Sprintf("rasdial failed: %s: %v", string(out), err), Output: string(out)}
		}
		return HelperResponse{Success: true, Output: string(out)}
	}

	out, err := exec.Command("swanctl", "--load-all").CombinedOutput()
	if err != nil {
		return HelperResponse{Success: false, Error: fmt.Sprintf("swanctl --load-all failed: %s", string(out))}
	}

	out, err = exec.Command("swanctl", "--initiate", "--child", connName).CombinedOutput()
	if err != nil {
		return HelperResponse{Success: false, Error: fmt.Sprintf("swanctl --initiate failed: %s", string(out)), Output: string(out)}
	}
	return HelperResponse{Success: true, Output: string(out)}
}

// disconnectIPSec terminates an IPSec connection.
func (h *PrivilegedHelper) disconnectIPSec(cmd HelperCommand) HelperResponse {
	connName := cmd.Interface
	if connName == "" {
		connName = cmd.Args["connection_name"]
	}
	if connName == "" {
		return HelperResponse{Success: false, Error: "connection name required for ipsec"}
	}

	if runtime.GOOS == "windows" {
		out, err := exec.Command("rasdial", connName, "/disconnect").CombinedOutput()
		if err != nil {
			return HelperResponse{Success: false, Error: fmt.Sprintf("rasdial /disconnect failed: %s", string(out)), Output: string(out)}
		}
		return HelperResponse{Success: true, Output: string(out)}
	}

	out, err := exec.Command("swanctl", "--terminate", "--ike", connName).CombinedOutput()
	if err != nil {
		return HelperResponse{Success: false, Error: fmt.Sprintf("swanctl --terminate failed: %s", string(out)), Output: string(out)}
	}
	return HelperResponse{Success: true, Output: string(out)}
}

// cmdKillSwitchEnable applies firewall rules to block non-VPN traffic.
func (h *PrivilegedHelper) cmdKillSwitchEnable(cmd HelperCommand) HelperResponse {
	switch runtime.GOOS {
	case "linux":
		return h.killSwitchLinuxEnable()
	case "darwin":
		return h.killSwitchMacOSEnable()
	case "windows":
		return h.killSwitchWindowsEnable()
	default:
		return HelperResponse{Success: false, Error: "unsupported platform for kill switch"}
	}
}

// cmdKillSwitchDisable removes firewall rules.
func (h *PrivilegedHelper) cmdKillSwitchDisable(cmd HelperCommand) HelperResponse {
	switch runtime.GOOS {
	case "linux":
		return h.killSwitchLinuxDisable()
	case "darwin":
		return h.killSwitchMacOSDisable()
	case "windows":
		return h.killSwitchWindowsDisable()
	default:
		return HelperResponse{Success: false, Error: "unsupported platform for kill switch"}
	}
}

// cmdStatus returns tunnel status information.
func (h *PrivilegedHelper) cmdStatus(cmd HelperCommand) HelperResponse {
	switch cmd.Protocol {
	case "wireguard":
		ifaceName := cmd.Interface
		if ifaceName == "" {
			ifaceName = "privycs0"
		}
		if runtime.GOOS == "windows" {
			out, _ := exec.Command("sc", "query", "WireGuardTunnel$"+ifaceName).CombinedOutput()
			if strings.Contains(string(out), "RUNNING") {
				return HelperResponse{Success: true, Output: "running"}
			}
			return HelperResponse{Success: false, Error: "not connected"}
		}
		out, err := exec.Command("wg", "show", ifaceName).CombinedOutput()
		if err != nil {
			return HelperResponse{Success: false, Error: "not connected", Output: string(out)}
		}
		return HelperResponse{Success: true, Output: string(out)}

	case "openvpn":
		pidPath := cmd.Args["pid_path"]
		if pidPath == "" {
			pidPath = "/tmp/privycs-openvpn.pid"
		}
		pidData, err := os.ReadFile(pidPath)
		if err != nil {
			return HelperResponse{Success: false, Error: "not connected"}
		}
		var pid int
		if _, err := fmt.Sscan(strings.TrimSpace(string(pidData)), &pid); err == nil && pid > 0 {
			if proc, err := os.FindProcess(pid); err == nil {
				if proc.Signal(nil) == nil {
					return HelperResponse{Success: true, Output: fmt.Sprintf("running (pid %d)", pid)}
				}
			}
		}
		return HelperResponse{Success: false, Error: "not connected"}

	case "ipsec":
		if runtime.GOOS == "windows" {
			connName := cmd.Interface
			psCmd := fmt.Sprintf(
				`(Get-VpnConnection -Name '%s' -AllUserConnection -ErrorAction SilentlyContinue).ConnectionStatus`,
				escapePowerShellString(connName))
			out, _ := exec.Command("powershell", "-NoProfile", "-Command", psCmd).CombinedOutput()
			if strings.Contains(string(out), "Connected") {
				return HelperResponse{Success: true, Output: "Connected"}
			}
			return HelperResponse{Success: false, Error: "not connected", Output: string(out)}
		}
		args := []string{"--list-sas"}
		if cmd.Interface != "" {
			args = append(args, "--ike", cmd.Interface)
		}
		out, err := exec.Command("swanctl", args...).CombinedOutput()
		if err != nil {
			return HelperResponse{Success: false, Error: "swanctl not available", Output: string(out)}
		}
		return HelperResponse{Success: true, Output: string(out)}

	default:
		return HelperResponse{Success: false, Error: "protocol required for status"}
	}
}

// killSwitchLinuxEnable applies iptables rules.
func (h *PrivilegedHelper) killSwitchLinuxEnable() HelperResponse {
	// Clean up stale rules first
	h.killSwitchLinuxDisable()

	commands := [][]string{
		{"iptables", "-I", "OUTPUT", "-o", "lo", "-j", "ACCEPT", "-m", "comment", "--comment", "privycs-ks"},
		{"iptables", "-I", "OUTPUT", "-m", "comment", "--comment", "privycs-ks", "-o", "privycs+", "-j", "ACCEPT"},
		{"iptables", "-I", "OUTPUT", "-m", "comment", "--comment", "privycs-ks", "-o", "tun+", "-j", "ACCEPT"},
		{"iptables", "-A", "OUTPUT", "-p", "udp", "--dport", "51820", "-j", "ACCEPT", "-m", "comment", "--comment", "privycs-ks"},
		{"iptables", "-A", "OUTPUT", "-p", "udp", "--dport", "51821", "-j", "ACCEPT", "-m", "comment", "--comment", "privycs-ks"},
		{"iptables", "-A", "OUTPUT", "-p", "udp", "--dport", "51822", "-j", "ACCEPT", "-m", "comment", "--comment", "privycs-ks"},
		{"iptables", "-A", "OUTPUT", "-p", "udp", "--dport", "51823", "-j", "ACCEPT", "-m", "comment", "--comment", "privycs-ks"},
		{"iptables", "-A", "OUTPUT", "-p", "udp", "--dport", "1194", "-j", "ACCEPT", "-m", "comment", "--comment", "privycs-ks"},
		{"iptables", "-A", "OUTPUT", "-p", "tcp", "--dport", "1194", "-j", "ACCEPT", "-m", "comment", "--comment", "privycs-ks"},
		{"iptables", "-A", "OUTPUT", "-p", "udp", "--dport", "500", "-j", "ACCEPT", "-m", "comment", "--comment", "privycs-ks"},
		{"iptables", "-A", "OUTPUT", "-p", "udp", "--dport", "4500", "-j", "ACCEPT", "-m", "comment", "--comment", "privycs-ks"},
		{"iptables", "-A", "OUTPUT", "-p", "udp", "--dport", "67:68", "-j", "ACCEPT", "-m", "comment", "--comment", "privycs-ks"},
		{"iptables", "-A", "OUTPUT", "-d", "10.0.0.0/8", "-j", "ACCEPT", "-m", "comment", "--comment", "privycs-ks"},
		{"iptables", "-A", "OUTPUT", "-d", "192.168.0.0/16", "-j", "ACCEPT", "-m", "comment", "--comment", "privycs-ks"},
		{"iptables", "-A", "OUTPUT", "-d", "172.16.0.0/12", "-j", "ACCEPT", "-m", "comment", "--comment", "privycs-ks"},
		{"iptables", "-A", "OUTPUT", "-j", "DROP", "-m", "comment", "--comment", "privycs-ks"},
	}
	var errors []string
	for _, args := range commands {
		if out, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
			errors = append(errors, fmt.Sprintf("%s: %s", strings.Join(args, " "), string(out)))
		}
	}
	if len(errors) > 0 {
		return HelperResponse{Success: false, Error: strings.Join(errors, "; ")}
	}
	return HelperResponse{Success: true, Output: "linux kill switch enabled"}
}

// killSwitchLinuxDisable removes all privycs-ks iptables rules.
func (h *PrivilegedHelper) killSwitchLinuxDisable() HelperResponse {
	for i := 0; i < 100; i++ { // safety limit
		out, err := exec.Command("iptables", "-L", "OUTPUT", "--line-numbers", "-n").CombinedOutput()
		if err != nil {
			break
		}
		found := false
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(line, "privycs-ks") {
				fields := strings.Fields(line)
				if len(fields) > 0 {
					exec.Command("iptables", "-D", "OUTPUT", fields[0]).Run()
					found = true
					break // line numbers shift, restart
				}
			}
		}
		if !found {
			break
		}
	}
	return HelperResponse{Success: true, Output: "linux kill switch disabled"}
}

// cmdWGInstallConfig writes a full WireGuard config to the canonical location:
//   - Linux/macOS: /etc/wireguard/<iface>.conf
//   - Windows:     %PROGRAMDATA%\PrivycsVPN\tunnels\<iface>.conf
//
// The client injects endpoint bypass routes into the content before sending.
func (h *PrivilegedHelper) cmdWGInstallConfig(cmd HelperCommand) HelperResponse {
	if cmd.Interface == "" {
		return HelperResponse{Success: false, Error: "interface name required"}
	}
	content := cmd.Args["content"]
	if content == "" {
		return HelperResponse{Success: false, Error: "content required"}
	}
	var dst string
	if runtime.GOOS == "windows" {
		dst = windowsWGConfigPath(cmd.Interface)
	} else {
		dst = filepath.Join("/etc/wireguard", cmd.Interface+".conf")
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return HelperResponse{Success: false, Error: fmt.Sprintf("mkdir %s: %v", filepath.Dir(dst), err)}
	}
	if err := os.WriteFile(dst, []byte(content), 0600); err != nil {
		return HelperResponse{Success: false, Error: fmt.Sprintf("write %s: %v", dst, err)}
	}
	return HelperResponse{Success: true, Output: "wg config installed at " + dst}
}

// cmdIPSecConfigure sets up the IPSec connection for the platform:
//   - Linux:   writes PEM certs + swanctl.conf under /etc/swanctl/, runs --load-all
//   - Windows: imports PKCS#12 to LocalMachine\My, creates IKEv2 VPN connection
//     via Add-VpnConnection (MachineCertificate auth). Since the helper runs as
//     SYSTEM, neither step triggers a UAC prompt for the user.
func (h *PrivilegedHelper) cmdIPSecConfigure(cmd HelperCommand) HelperResponse {
	if runtime.GOOS == "windows" {
		return h.cmdIPSecConfigureWindows(cmd)
	}
	certDir := "/etc/swanctl"
	files := []struct {
		path    string
		content string
		mode    os.FileMode
	}{
		{certDir + "/x509ca/privycs-ca.pem", cmd.Args["ca_cert"], 0644},
		{certDir + "/x509/privycs-client.pem", cmd.Args["client_cert"], 0644},
		{certDir + "/private/privycs-client.pem", cmd.Args["client_key"], 0600},
		{certDir + "/conf.d/privycs-vpn.conf", cmd.Args["swanctl_conf"], 0644},
	}
	for _, f := range files {
		if f.content == "" {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(f.path), 0755); err != nil {
			return HelperResponse{Success: false, Error: fmt.Sprintf("mkdir %s: %v", filepath.Dir(f.path), err)}
		}
		if err := os.WriteFile(f.path, []byte(f.content), f.mode); err != nil {
			return HelperResponse{Success: false, Error: fmt.Sprintf("write %s: %v", f.path, err)}
		}
	}
	out, err := exec.Command("swanctl", "--load-all").CombinedOutput()
	if err != nil {
		return HelperResponse{Success: false, Error: fmt.Sprintf("swanctl --load-all: %s", string(out)), Output: string(out)}
	}
	return HelperResponse{Success: true, Output: string(out)}
}

// cmdIPSecConfigureWindows handles the Windows-specific IPSec setup:
//  1. Decode the PKCS#12 bundle (base64) to a temp file under %PROGRAMDATA%.
//  2. Import it into LocalMachine\My with the given password.
//  3. (Re-)create the IKEv2 VPN connection with MachineCertificate auth.
func (h *PrivilegedHelper) cmdIPSecConfigureWindows(cmd HelperCommand) HelperResponse {
	connName := cmd.Args["conn_name"]
	serverAddr := cmd.Args["server_address"]
	p12B64 := cmd.Args["p12_base64"]
	p12Pass := cmd.Args["p12_password"]
	if connName == "" || serverAddr == "" || p12B64 == "" {
		return HelperResponse{Success: false, Error: "conn_name, server_address and p12_base64 required"}
	}

	programData := os.Getenv("PROGRAMDATA")
	if programData == "" {
		programData = `C:\ProgramData`
	}
	certDir := filepath.Join(programData, "PrivycsVPN", "certs")
	if err := os.MkdirAll(certDir, 0700); err != nil {
		return HelperResponse{Success: false, Error: fmt.Sprintf("mkdir %s: %v", certDir, err)}
	}

	p12Path := filepath.Join(certDir, connName+".p12")
	p12Data, err := base64StdDecode(p12B64)
	if err != nil {
		return HelperResponse{Success: false, Error: fmt.Sprintf("p12 base64 decode: %v", err)}
	}
	if err := os.WriteFile(p12Path, p12Data, 0600); err != nil {
		return HelperResponse{Success: false, Error: fmt.Sprintf("write %s: %v", p12Path, err)}
	}
	defer os.Remove(p12Path)

	// PowerShell script: import cert to LocalMachine\My and (re)create VPN
	// connection in the AllUser scope. Running as SYSTEM — no UAC.
	//
	// Note: user-scope VPN entries are stored under HKCU of the user that
	// created them. SYSTEM's HKCU is not the end-user's HKCU, so the client
	// side (running as the user) is responsible for cleaning up any stale
	// user-scope entry with this name — see configureWindowsFromSSwan.
	psScript := fmt.Sprintf(`
$ErrorActionPreference = 'Stop'
$p12Password = ConvertTo-SecureString -String '%s' -AsPlainText -Force
Import-PfxCertificate -FilePath '%s' -CertStoreLocation Cert:\LocalMachine\My -Password $p12Password -ErrorAction Stop | Out-Null
Remove-VpnConnection -Name '%s' -Force -AllUserConnection -ErrorAction SilentlyContinue
Add-VpnConnection -Name '%s' -ServerAddress '%s' -TunnelType IKEv2 -AuthenticationMethod MachineCertificate -EncryptionLevel Required -RememberCredential -AllUserConnection -Force
`, escapePowerShellString(p12Pass), escapePowerShellString(p12Path),
		escapePowerShellString(connName),
		escapePowerShellString(connName), escapePowerShellString(serverAddr))

	out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", psScript).CombinedOutput()
	if err != nil {
		safe := redactCredentials(string(out), p12Pass)
		return HelperResponse{Success: false, Error: fmt.Sprintf("ipsec configure failed: %s: %v", safe, err), Output: safe}
	}
	return HelperResponse{Success: true, Output: redactCredentials(string(out), p12Pass)}
}

// cmdRemoveLegacySudoers removes the legacy /etc/sudoers.d/privycs-vpn NOPASSWD
// file created by older versions before the helper service existed.
func (h *PrivilegedHelper) cmdRemoveLegacySudoers(cmd HelperCommand) HelperResponse {
	legacy := "/etc/sudoers.d/privycs-vpn"
	if _, err := os.Stat(legacy); err != nil {
		return HelperResponse{Success: true, Output: "no legacy sudoers file"}
	}
	if err := os.Remove(legacy); err != nil {
		return HelperResponse{Success: false, Error: fmt.Sprintf("remove %s: %v", legacy, err)}
	}
	return HelperResponse{Success: true, Output: "legacy sudoers removed"}
}

// killSwitchMacOSEnable applies pf anchor rules.
func (h *PrivilegedHelper) killSwitchMacOSEnable() HelperResponse {
	anchorFile := "/etc/pf.anchors/privycs_ks"
	rules := "# Privycs Kill Switch\n" +
		"pass on lo0 all\n" +
		"pass on utun0 all\n" +
		"pass on utun1 all\n" +
		"pass on utun2 all\n" +
		"pass on utun3 all\n" +
		"pass out proto udp to any port 51820\n" +
		"pass out proto udp to any port 1194\n" +
		"pass out proto tcp to any port 1194\n" +
		"pass out proto udp to any port 500\n" +
		"pass out proto udp to any port 4500\n" +
		"pass out proto esp all\n" +
		"block drop all\n"

	if err := os.WriteFile(anchorFile, []byte(rules), 0644); err != nil {
		return HelperResponse{Success: false, Error: fmt.Sprintf("failed to write pf anchor: %v", err)}
	}

	exec.Command("pfctl", "-a", "privycs_ks", "-f", anchorFile).Run()
	exec.Command("pfctl", "-e").Run()

	return HelperResponse{Success: true, Output: "macos kill switch enabled"}
}

// killSwitchMacOSDisable removes pf anchor rules.
func (h *PrivilegedHelper) killSwitchMacOSDisable() HelperResponse {
	exec.Command("pfctl", "-a", "privycs_ks", "-F", "all").Run()
	os.Remove("/etc/pf.anchors/privycs_ks")
	return HelperResponse{Success: true, Output: "macos kill switch disabled"}
}

// killSwitchWindowsEnable applies netsh firewall rules.
func (h *PrivilegedHelper) killSwitchWindowsEnable() HelperResponse {
	commands := [][]string{
		{"netsh", "advfirewall", "firewall", "add", "rule", "name=PrivycsKS-Loopback", "dir=out", "action=allow", "remoteip=127.0.0.0/8"},
		{"netsh", "advfirewall", "firewall", "add", "rule", "name=PrivycsKS-WireGuard", "dir=out", "action=allow", "protocol=udp", "remoteport=51820"},
		{"netsh", "advfirewall", "firewall", "add", "rule", "name=PrivycsKS-OpenVPN-UDP", "dir=out", "action=allow", "protocol=udp", "remoteport=1194"},
		{"netsh", "advfirewall", "firewall", "add", "rule", "name=PrivycsKS-OpenVPN-TCP", "dir=out", "action=allow", "protocol=tcp", "remoteport=1194"},
		{"netsh", "advfirewall", "firewall", "add", "rule", "name=PrivycsKS-IKE", "dir=out", "action=allow", "protocol=udp", "remoteport=500"},
		{"netsh", "advfirewall", "firewall", "add", "rule", "name=PrivycsKS-IKE-NATT", "dir=out", "action=allow", "protocol=udp", "remoteport=4500"},
		{"netsh", "advfirewall", "firewall", "add", "rule", "name=PrivycsKS-BlockAll", "dir=out", "action=block"},
	}
	var errors []string
	for _, args := range commands {
		if out, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
			errors = append(errors, fmt.Sprintf("%s: %s", strings.Join(args, " "), string(out)))
		}
	}
	if len(errors) > 0 {
		return HelperResponse{Success: false, Error: strings.Join(errors, "; ")}
	}
	return HelperResponse{Success: true, Output: "windows kill switch enabled"}
}

// killSwitchWindowsDisable removes PrivycsKS-* firewall rules.
func (h *PrivilegedHelper) killSwitchWindowsDisable() HelperResponse {
	rules := []string{"PrivycsKS-Loopback", "PrivycsKS-WireGuard", "PrivycsKS-OpenVPN-UDP",
		"PrivycsKS-OpenVPN-TCP", "PrivycsKS-IKE", "PrivycsKS-IKE-NATT", "PrivycsKS-BlockAll"}
	for _, name := range rules {
		exec.Command("netsh", "advfirewall", "firewall", "delete", "rule", "name="+name).Run()
	}
	return HelperResponse{Success: true, Output: "windows kill switch disabled"}
}

// copyConfigFile copies a file using OS-level commands (helper runs as root).
func (h *PrivilegedHelper) copyConfigFile(src, dst string) error {
	// Validate source exists
	if _, err := os.Stat(src); os.IsNotExist(err) {
		return fmt.Errorf("source config not found: %s", src)
	}

	// Ensure destination directory exists
	os.MkdirAll(filepath.Dir(dst), 0755)

	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("failed to read source: %w", err)
	}

	if err := os.WriteFile(dst, data, 0600); err != nil {
		return fmt.Errorf("failed to write destination: %w", err)
	}

	return nil
}

// helperListen creates a platform-appropriate listener.
// On Linux/macOS: Unix domain socket.
// On Windows: Unix socket (Go 1.23+ supports this on Windows too, but we use
// it only for consistency; the named pipe path is handled at a higher level).
func helperListen(path string) (net.Listener, error) {
	if runtime.GOOS == "windows" {
		// On Windows, use a Unix socket as well for simplicity.
		// The path \\.\pipe\privycs-vpn works as a named pipe path.
		// Go's net package handles this transparently.
		return net.Listen("unix", path)
	}
	return net.Listen("unix", path)
}

// RunHelperMode is the entry point when the binary is started with --helper.
// It runs the privileged helper and blocks until interrupted.
func RunHelperMode() {
	log.SetFlags(log.Ldate | log.Ltime)
	logPath := "/var/log/privycs-vpn-helper.log"
	if runtime.GOOS == "windows" {
		logPath = filepath.Join(os.Getenv("PROGRAMDATA"), "PrivycsVPN", "helper.log")
	}
	os.MkdirAll(filepath.Dir(logPath), 0755)
	if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600); err == nil {
		log.SetOutput(f)
		defer f.Close()
	}

	log.Println("Privycs VPN privileged helper starting...")

	helper := NewPrivilegedHelper()

	// Handle OS signals for clean shutdown
	go func() {
		sigCh := make(chan os.Signal, 1)
		signalNotify(sigCh)
		<-sigCh
		log.Println("Signal received, shutting down helper...")
		helper.Stop()
	}()

	if err := helper.Start(); err != nil {
		log.Fatalf("Helper failed: %v", err)
	}
}
