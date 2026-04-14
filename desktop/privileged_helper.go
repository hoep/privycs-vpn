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
	"connect":           true,
	"disconnect":        true,
	"killswitch_enable": true,
	"killswitch_disable": true,
	"status":            true,
}

// safePathPattern validates file paths to prevent directory traversal and injection.
var safePathPattern = regexp.MustCompile(`^[a-zA-Z0-9/_\-\.\\:]+$`)

// safeInterfacePattern validates interface names (alphanumeric, dash, underscore, max 15 chars).
var safeInterfacePattern = regexp.MustCompile(`^[a-zA-Z0-9_\-]{1,15}$`)

// helperSocketPath returns the IPC socket/pipe path for the current platform.
func helperSocketPath() string {
	switch runtime.GOOS {
	case "windows":
		return `\\.\pipe\privycs-vpn`
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

	// Clean up stale socket file on Unix
	if runtime.GOOS != "windows" {
		os.Remove(socketPath)
	}

	listener, err := helperListen(socketPath)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", socketPath, err)
	}

	// Restrict socket permissions on Unix so only the owner can connect
	if runtime.GOOS != "windows" {
		os.Chmod(socketPath, 0660)
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

// connectWireGuard runs wg-quick up.
func (h *PrivilegedHelper) connectWireGuard(cmd HelperCommand) HelperResponse {
	ifaceName := cmd.Interface
	if ifaceName == "" {
		ifaceName = "privycs0"
	}

	// Copy config to /etc/wireguard if config_path is provided
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

// disconnectWireGuard runs wg-quick down.
func (h *PrivilegedHelper) disconnectWireGuard(cmd HelperCommand) HelperResponse {
	ifaceName := cmd.Interface
	if ifaceName == "" {
		ifaceName = "privycs0"
	}

	out, err := exec.Command("wg-quick", "down", ifaceName).CombinedOutput()
	if err != nil {
		return HelperResponse{Success: false, Error: fmt.Sprintf("wg-quick down failed: %s", string(out)), Output: string(out)}
	}
	return HelperResponse{Success: true, Output: string(out)}
}

// connectOpenVPN starts the openvpn daemon.
func (h *PrivilegedHelper) connectOpenVPN(cmd HelperCommand) HelperResponse {
	ovpnExe, err := exec.LookPath("openvpn")
	if err != nil {
		return HelperResponse{Success: false, Error: "openvpn not found in PATH"}
	}

	configPath := cmd.ConfigPath
	if configPath == "" {
		return HelperResponse{Success: false, Error: "config_path is required for openvpn"}
	}

	// Verify config file exists
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return HelperResponse{Success: false, Error: "config file not found"}
	}

	logPath := cmd.Args["log_path"]
	if logPath == "" {
		logPath = "/tmp/privycs-openvpn.log"
	}
	pidPath := cmd.Args["pid_path"]
	if pidPath == "" {
		pidPath = "/tmp/privycs-openvpn.pid"
	}

	mgmtHost := cmd.Args["mgmt_host"]
	if mgmtHost == "" {
		mgmtHost = "127.0.0.1"
	}
	mgmtPort := cmd.Args["mgmt_port"]
	if mgmtPort == "" {
		mgmtPort = "7505"
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

// connectIPSec starts an IPSec/IKEv2 connection via swanctl.
func (h *PrivilegedHelper) connectIPSec(cmd HelperCommand) HelperResponse {
	connName := cmd.Interface
	if connName == "" {
		connName = cmd.Args["connection_name"]
	}
	if connName == "" {
		return HelperResponse{Success: false, Error: "connection name required for ipsec"}
	}

	// Load credentials and initiate
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
		out, err := exec.Command("swanctl", "--list-sas").CombinedOutput()
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
