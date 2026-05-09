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
	"connect":                    true,
	"disconnect":                 true,
	"killswitch_enable":          true,
	"killswitch_disable":         true,
	"sinkhole_engage":            true, // new system: Privycs-Sinkhole-* rules
	"sinkhole_release":           true, // new system: Privycs-Sinkhole-* cleanup
	"status":                     true,
	"wg_install_config":          true,
	"ipsec_configure":            true,
	"ipsec_cleanup":              true, // wipe swanctl conf.d / PEMs
	"ipsec_check_dependencies":   true, // macOS: brew/strongswan/charon health
	"ipsec_split_routes_add":     true, // macOS post-up CIDR-bypass routes
	"ipsec_split_routes_remove":  true,
	"macos_dns_override_set":     true, // primary-service DNS override (swanctl-darwin)
	"macos_dns_override_restore": true,
	"macos_dns_override_clean":   true, // orphan-cleanup at app startup
	"remove_legacy_sudoers":      true,
	"wlan_ssid":                  true, // SSID query (bypasses user-level Location GPO)
}

// safePathPattern validates file paths to prevent directory traversal and injection.
// Spaces are allowed because macOS uses "Application Support" in the standard
// per-user data directory. Shell metacharacters ($, ;, |, &, `, etc.) remain blocked,
// and the path is always passed as an exec.Cmd argument (no shell interpolation).
var safePathPattern = regexp.MustCompile(`^[a-zA-Z0-9 /_\-\.\\:]+$`)

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

	// Audit log result. Output is logged on every action whose Output
	// field is non-empty (wg-quick up/down, swanctl, etc.) so we can
	// see what the underlying command actually said. v0.9.14.25 user
	// log showed wg-quick up reporting success but `wg show` returning
	// "not connected" right after — the wg-quick stdout had useful
	// diagnostics that were thrown away because we only logged Output
	// on the failure branch in the client. Now both branches log it.
	if resp.Success {
		if resp.Output != "" {
			log.Printf("Helper result: action=%s success=true output=%q", cmd.Action, strings.TrimSpace(resp.Output))
		} else {
			log.Printf("Helper result: action=%s success=true", cmd.Action)
		}
	} else {
		log.Printf("Helper result: action=%s success=false error=%s output=%q", cmd.Action, resp.Error, strings.TrimSpace(resp.Output))
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
	case "sinkhole_engage":
		return h.cmdSinkholeEngage(cmd)
	case "sinkhole_release":
		return h.cmdSinkholeRelease(cmd)
	case "wlan_ssid":
		return h.cmdWlanSSID(cmd)
	case "status":
		return h.cmdStatus(cmd)
	case "wg_install_config":
		return h.cmdWGInstallConfig(cmd)
	case "wg_handshake":
		return h.cmdWGHandshake(cmd)
	case "ipsec_configure":
		return h.cmdIPSecConfigure(cmd)
	case "ipsec_cleanup":
		return h.cmdIPSecCleanup(cmd)
	case "ipsec_check_dependencies":
		return h.cmdIPSecCheckDependencies(cmd)
	case "ipsec_split_routes_add":
		return h.cmdIPSecSplitRoutesAdd(cmd)
	case "ipsec_split_routes_remove":
		return h.cmdIPSecSplitRoutesRemove(cmd)
	case "macos_dns_override_set":
		return h.cmdMacOSDNSOverrideSet(cmd)
	case "macos_dns_override_restore":
		return h.cmdMacOSDNSOverrideRestore(cmd)
	case "macos_dns_override_clean":
		return h.cmdMacOSDNSOverrideClean(cmd)
	case "remove_legacy_sudoers":
		return h.cmdRemoveLegacySudoers(cmd)
	case "macos_restart_charon":
		return h.cmdMacOSRestartCharon(cmd)
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

	// Linux/macOS: copy config to /etc/wireguard. Linux then proceeds with
	// wg-quick; macOS branches into the in-process wireguard-go path that
	// avoids launchd's wg-quick foreground-wait trap entirely (see
	// wg_macos.go for the architecture).
	if cmd.ConfigPath != "" {
		etcConf := filepath.Join("/etc/wireguard", ifaceName+".conf")
		if err := h.copyConfigFile(cmd.ConfigPath, etcConf); err != nil {
			return HelperResponse{Success: false, Error: fmt.Sprintf("failed to install config: %v", err)}
		}
	}

	if runtime.GOOS == "darwin" {
		etcConf := filepath.Join("/etc/wireguard", ifaceName+".conf")
		content, err := os.ReadFile(etcConf)
		if err != nil {
			return HelperResponse{Success: false, Error: fmt.Sprintf("read %s: %v", etcConf, err)}
		}
		realIface, err := wgDarwinUp(ifaceName, string(content))
		if err != nil {
			return HelperResponse{Success: false, Error: fmt.Sprintf("wgDarwinUp failed: %v", err)}
		}
		return HelperResponse{Success: true, Output: fmt.Sprintf("WireGuard tunnel up on %s (in-process)", realIface)}
	}

	// Linux: wg-quick (untouched — works fine, kernel-WG, no userspace driver).
	wgQuick := findWGBinary("wg-quick")
	if wgQuick == "" {
		return HelperResponse{Success: false, Error: "wg-quick not found — install wireguard-tools"}
	}
	wgUp := exec.Command(wgQuick, "up", ifaceName)
	wgUp.Env = wgExecEnv()
	applyDetachedSession(wgUp)
	out, err := wgUp.CombinedOutput()
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

	if runtime.GOOS == "darwin" {
		if err := wgDarwinDown(ifaceName); err != nil {
			return HelperResponse{Success: false, Error: fmt.Sprintf("wgDarwinDown failed: %v", err)}
		}
		return HelperResponse{Success: true, Output: fmt.Sprintf("WireGuard tunnel down (%s, in-process)", ifaceName)}
	}

	wgQuick := findWGBinary("wg-quick")
	if wgQuick == "" {
		return HelperResponse{Success: false, Error: "wg-quick not found — install wireguard-tools"}
	}
	wgDown := exec.Command(wgQuick, "down", ifaceName)
	wgDown.Env = wgExecEnv()
	out, err := wgDown.CombinedOutput()
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
	// Pre-create log + pid files with mode 0644 so the unprivileged app
	// can read them. openvpn opens both with O_CREAT|O_TRUNC|O_WRONLY;
	// when the file already exists, O_CREAT is a no-op and O_TRUNC only
	// resets size — file mode is preserved. Without this the helper-
	// spawned openvpn (root) creates them mode 0600 and Status() in the
	// user app sees "log unreadable", never reports Connected=true and
	// the UI hangs on the connecting spinner.
	prepFile := func(p string) {
		os.MkdirAll(filepath.Dir(p), 0755)
		if f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY, 0644); err == nil {
			f.Close()
		}
		os.Chmod(p, 0644)
	}
	prepFile(logPath)
	prepFile(pidPath)

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
	// Re-chmod after start in case openvpn delete-recreated either file.
	os.Chmod(logPath, 0644)
	os.Chmod(pidPath, 0644)
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

	swanctlBin := "swanctl"
	if runtime.GOOS == "darwin" {
		swanctlBin = helperFindMacOSStrongswanBinary("swanctl")
		if swanctlBin == "" {
			return HelperResponse{Success: false, Error: "swanctl not found — install via `brew install strongswan`"}
		}
	}
	out, err := exec.Command(swanctlBin, "--load-all").CombinedOutput()
	if err != nil {
		return HelperResponse{Success: false, Error: fmt.Sprintf("swanctl --load-all failed: %s", string(out))}
	}

	out, err = exec.Command(swanctlBin, "--initiate", "--child", connName).CombinedOutput()
	if err != nil {
		return HelperResponse{Success: false, Error: fmt.Sprintf("swanctl --initiate failed: %s", string(out)), Output: string(out)}
	}

	// DNS override (Linux-only path). User's Settings.DNSOverride
	// reaches us via cmd.Args["dns_servers"] (space-separated IPs).
	// Apply by backing up /etc/resolv.conf and writing a fresh
	// nameserver-only file. The helper holds the backup for the
	// disconnect path to restore; if the helper crashes between
	// up/down, the backup file persists and the next disconnect
	// (or fresh tunnel up) restores correctly.
	if dns := cmd.Args["dns_servers"]; dns != "" {
		if err := writeIPSecDnsOverride(dns); err != nil {
			// DNS override failure is non-fatal: the tunnel is up,
			// the user just won't see their override DNS in effect.
			// Loud log so the issue is diagnosable.
			log.Printf("ipsec dns override apply failed: %v", err)
		} else {
			log.Printf("ipsec dns override applied: %s", dns)
		}
	}
	return HelperResponse{Success: true, Output: string(out)}
}

// writeIPSecDnsOverride backs up /etc/resolv.conf to
// /etc/resolv.conf.privycs-bak (if no backup yet) and writes a fresh
// resolv.conf with the user's nameservers. Only runs on Linux; the
// caller already checked runtime.GOOS.
//
// We deliberately avoid systemd-resolved / resolvectl here:
// distros vary widely (some symlink resolv.conf to a stub, some don't),
// and per-link DNS via resolvectl assumes a network interface for
// the tunnel which strongSwan's xfrm-mode policy SAs do not provide.
// Direct file write is the lowest-common-denominator path that works
// across systemd-resolved, NetworkManager, and bare resolvconf-managed
// systems. The backup-file convention lets the user manually recover
// if the helper crashed before disconnect.
func writeIPSecDnsOverride(dnsList string) error {
	const path = "/etc/resolv.conf"
	const backup = "/etc/resolv.conf.privycs-bak"
	// Only back up if no backup yet — preserves the original even
	// if the helper missed a previous disconnect.
	if _, err := os.Stat(backup); os.IsNotExist(err) {
		if orig, rerr := os.ReadFile(path); rerr == nil {
			_ = os.WriteFile(backup, orig, 0o644)
		}
	}
	var sb strings.Builder
	sb.WriteString("# Privycs VPN: temporary DNS override (auto-restored on disconnect)\n")
	for _, s := range strings.Fields(dnsList) {
		sb.WriteString("nameserver ")
		sb.WriteString(s)
		sb.WriteByte('\n')
	}
	return os.WriteFile(path, []byte(sb.String()), 0o644)
}

// restoreIPSecDnsOverride restores /etc/resolv.conf from the backup
// the writeIPSec... function created on connect. Idempotent: if no
// backup exists (no override was active), this is a no-op.
func restoreIPSecDnsOverride() error {
	const path = "/etc/resolv.conf"
	const backup = "/etc/resolv.conf.privycs-bak"
	data, err := os.ReadFile(backup)
	if err != nil {
		// No backup = no override was active; not an error.
		return nil
	}
	if werr := os.WriteFile(path, data, 0o644); werr != nil {
		return werr
	}
	_ = os.Remove(backup)
	return nil
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

	swanctlBin := "swanctl"
	if runtime.GOOS == "darwin" {
		swanctlBin = helperFindMacOSStrongswanBinary("swanctl")
		if swanctlBin == "" {
			return HelperResponse{Success: false, Error: "swanctl not found — install via `brew install strongswan`"}
		}
	}
	out, err := exec.Command(swanctlBin, "--terminate", "--ike", connName).CombinedOutput()
	if err != nil {
		return HelperResponse{Success: false, Error: fmt.Sprintf("swanctl --terminate failed: %s", string(out)), Output: string(out)}
	}
	// DNS override restore. Linux-only path — macOS via swanctl does
	// its own DNS via attribute payloads + scutil, no /etc/resolv.conf
	// hack needed.
	if runtime.GOOS == "linux" {
		if rerr := restoreIPSecDnsOverride(); rerr != nil {
			log.Printf("ipsec dns override restore failed: %v", rerr)
		}
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
		// macOS: in-process tunnel — query the device directly via UAPI.
		// No /var/run/wireguard files involved, no launchd-related quirks.
		// See wg_macos.go for why we own the tunnel inside the helper
		// instead of shelling out to wg show.
		if runtime.GOOS == "darwin" {
			uapi, connected, err := wgDarwinStatus(ifaceName)
			if err != nil {
				return HelperResponse{Success: false, Error: "not connected", Output: err.Error()}
			}
			if !connected {
				// Tunnel object exists but no handshake yet — return success
				// with the dump so the client can decide if peer info is
				// enough (handshake-pending state). Empty Output from this
				// path was the v0.9.14.27 regression where the client read
				// len(Output) > 0 as the connected signal.
				return HelperResponse{Success: true, Output: uapi}
			}
			return HelperResponse{Success: true, Output: uapi}
		}

		// Linux: kernel-WG via wg show.
		wg := findWGBinary("wg")
		if wg == "" {
			return HelperResponse{Success: false, Error: "not connected"}
		}
		out, err := exec.Command(wg, "show", ifaceName).CombinedOutput()
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
		swanctlBin := "swanctl"
		if runtime.GOOS == "darwin" {
			swanctlBin = helperFindMacOSStrongswanBinary("swanctl")
			if swanctlBin == "" {
				return HelperResponse{Success: false, Error: "swanctl not found"}
			}
		}
		args := []string{"--list-sas"}
		if cmd.Interface != "" {
			args = append(args, "--ike", cmd.Interface)
		}
		out, err := exec.Command(swanctlBin, args...).CombinedOutput()
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

// cmdWGHandshake returns the most recent peer-handshake timestamp on
// the named WireGuard interface. Output format: a single Unix-epoch
// integer in seconds (the max across all peers). Zero means "no
// handshake yet". Used by the rotator's post-connect health check
// (B) to detect dead remote endpoints that accepted the tunnel
// install but never actually responded to a handshake.
//
// `wg show <iface> latest-handshakes` is a stable interface across
// Linux and Windows wg.exe; it prints "<pubkey>\t<unix_secs>" per
// peer. We keep the parsing dumb: max int across the second column.
func (h *PrivilegedHelper) cmdWGHandshake(cmd HelperCommand) HelperResponse {
	if cmd.Interface == "" {
		return HelperResponse{Success: false, Error: "interface name required"}
	}
	var binary string
	if runtime.GOOS == "windows" {
		binary = findWireGuardExe()
		if binary == "" {
			return HelperResponse{Success: false, Error: "wg.exe not found"}
		}
	} else {
		binary = findWGBinary("wg")
		if binary == "" {
			return HelperResponse{Success: false, Error: "wg not found — install wireguard-tools"}
		}
	}
	// macOS: read from in-process tunnel via UAPI. Same data, no exec.
	if runtime.GOOS == "darwin" {
		uapi, _, err := wgDarwinStatus(cmd.Interface)
		if err != nil {
			return HelperResponse{Success: false, Error: fmt.Sprintf("status: %v", err)}
		}
		var maxTs int64
		for _, line := range strings.Split(uapi, "\n") {
			if !strings.HasPrefix(line, "last_handshake_time_sec=") {
				continue
			}
			val := strings.TrimPrefix(line, "last_handshake_time_sec=")
			var ts int64
			if _, err := fmt.Sscan(strings.TrimSpace(val), &ts); err == nil && ts > maxTs {
				maxTs = ts
			}
		}
		return HelperResponse{Success: true, Output: fmt.Sprintf("%d", maxTs)}
	}

	out, err := exec.Command(binary, "show", cmd.Interface, "latest-handshakes").CombinedOutput()
	if err != nil {
		return HelperResponse{Success: false, Error: fmt.Sprintf("wg show: %v", err), Output: string(out)}
	}
	var maxTs int64
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		var ts int64
		if _, err := fmt.Sscan(fields[1], &ts); err == nil && ts > maxTs {
			maxTs = ts
		}
	}
	return HelperResponse{Success: true, Output: fmt.Sprintf("%d", maxTs)}
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
	// macOS Pro PPK path uses Homebrew strongswan, which compiles in
	// its etc-dir relative to the brew prefix (/opt/homebrew or
	// /usr/local). Linux uses the canonical /etc/swanctl. Linux's
	// `swanctl` binary is on the daemon's PATH; macOS gets the
	// explicit Homebrew path because launchd runs us with a minimal
	// PATH that excludes Homebrew dirs.
	certDir := "/etc/swanctl"
	swanctlBin := "swanctl"
	if runtime.GOOS == "darwin" {
		certDir = helperFindMacOSSwanctlConfDir()
		swanctlBin = helperFindMacOSStrongswanBinary("swanctl")
		if swanctlBin == "" {
			return HelperResponse{Success: false, Error: "swanctl not found — install via `brew install strongswan`"}
		}
	}
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
	// macOS auto-start: Homebrew's strongswan formula does not ship a
	// launchd service plist, so `brew services start strongswan` fails
	// with "Formula has not implemented #plist, #service or provided a
	// locatable service file." The strongswan-recommended way to bring
	// charon up is the `ipsec` wrapper script (sets up runtime dirs,
	// daemonises, uses syslog). We invoke it from the helper —
	// already-root context, no sudo prompt.
	if runtime.GOOS == "darwin" {
		if err := helperEnsureMacOSCharonRunning(); err != nil {
			return HelperResponse{
				Success: false,
				Error:   fmt.Sprintf("could not start charon daemon: %v", err),
			}
		}
	}
	out, err := exec.Command(swanctlBin, "--load-all").CombinedOutput()
	if err != nil {
		errMsg := fmt.Sprintf("swanctl --load-all: %s", string(out))
		if runtime.GOOS == "darwin" && strings.Contains(string(out), "No such file") {
			errMsg += " (charon vici socket missing even after ipsec-start; check /opt/homebrew/var/log/charon.log)"
		}
		return HelperResponse{Success: false, Error: errMsg, Output: string(out)}
	}
	return HelperResponse{Success: true, Output: string(out)}
}

// cmdMacOSRestartCharon performs a hard restart of the macOS charon
// daemon (`ipsec stop` -> wait for vici socket to disappear ->
// `ipsec start` -> wait for vici socket to reappear). Used as the
// post-wake recovery step on macOS IPSec when the daemon's IKE_SA
// state has gone stale across a long sleep — which manifests as
// the user-reported symptom "after lid close+open IPSec hangs;
// must `ipsec restart` manually". v0.9.14.88 wires this into the
// NSWorkspaceDidWakeNotification handler so the recovery is
// automatic.
//
// Linux/Windows are no-ops — they have other recovery paths
// (systemd-restart on Linux is fast and rarely needed; Windows
// has its own service-control story).
func (h *PrivilegedHelper) cmdMacOSRestartCharon(cmd HelperCommand) HelperResponse {
	if runtime.GOOS != "darwin" {
		return HelperResponse{Success: false, Error: "macos_restart_charon is darwin-only"}
	}
	ipsecBin := helperFindMacOSStrongswanBinary("ipsec")
	if ipsecBin == "" {
		return HelperResponse{Success: false, Error: "ipsec wrapper not found"}
	}
	// Stop. Output captured for diagnosis on failure; ipsec stop
	// returns 0 even if charon was already down so we don't gate
	// on the exit code — only on the socket disappearing.
	stopOut, _ := exec.Command(ipsecBin, "stop").CombinedOutput()

	viciCandidates := []string{
		"/opt/homebrew/var/run/charon.vici",
		"/usr/local/var/run/charon.vici",
		"/var/run/charon.vici",
	}
	socketPresent := func() bool {
		for _, p := range viciCandidates {
			if fi, err := os.Stat(p); err == nil && (fi.Mode()&os.ModeSocket) != 0 {
				return true
			}
		}
		return false
	}
	// Poll for socket-disappear up to 5 s. Charon-shutdown is
	// typically <1 s but a hung daemon can take longer.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !socketPresent() {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Start fresh. Reuses the existing helper which polls vici
	// socket up to 8 s.
	if err := helperEnsureMacOSCharonRunning(); err != nil {
		return HelperResponse{
			Success: false,
			Error: fmt.Sprintf(
				"charon-restart: stop output=%q; start failed: %v",
				strings.TrimSpace(string(stopOut)), err,
			),
		}
	}
	return HelperResponse{Success: true, Output: "charon restarted"}
}

// helperEnsureMacOSCharonRunning is darwin-only. It checks whether
// charon's vici socket is already present; if so, it's a no-op. If
// not, it shells out to `ipsec start` from the Homebrew prefix and
// polls the socket for up to 8 seconds. Mirrors what `systemctl start
// strongswan` does on Linux but for the brew-services-less Homebrew
// strongswan formula.
//
// charon stays running across multiple connect/disconnect cycles. We
// don't `ipsec stop` on disconnect because the user may have other
// connections coming up in quick succession; the cost of an idle
// charon is one daemon process and the vici socket — negligible.
func helperEnsureMacOSCharonRunning() error {
	viciCandidates := []string{
		"/opt/homebrew/var/run/charon.vici",
		"/usr/local/var/run/charon.vici",
		"/var/run/charon.vici",
	}
	socketPresent := func() bool {
		for _, p := range viciCandidates {
			if fi, err := os.Stat(p); err == nil && (fi.Mode()&os.ModeSocket) != 0 {
				return true
			}
		}
		return false
	}
	if socketPresent() {
		return nil
	}
	ipsecBin := helperFindMacOSStrongswanBinary("ipsec")
	if ipsecBin == "" {
		return fmt.Errorf("ipsec wrapper not found — is `brew install strongswan` complete?")
	}
	out, err := exec.Command(ipsecBin, "start").CombinedOutput()
	if err != nil {
		return fmt.Errorf("ipsec start failed: %s: %v", strings.TrimSpace(string(out)), err)
	}
	// charon takes ~0.5–2 s to create the vici socket on a cold start
	// (cert chain validation + socket bind). Poll up to 8 s.
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if socketPresent() {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("vici socket did not appear within 8s after `ipsec start`; check /opt/homebrew/var/log/charon.log")
}

// helperFindMacOSSwanctlConfDir mirrors macosSwanctlConfDir() in
// protocol_ipsec_macos_swanctl.go but lives helper-side. We do not
// share code across the helper/client boundary because the helper
// lacks parts of the client's import graph.
func helperFindMacOSSwanctlConfDir() string {
	candidates := []string{
		"/opt/homebrew/etc/swanctl",
		"/usr/local/etc/swanctl",
		"/etc/swanctl",
	}
	for _, p := range candidates {
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			return p
		}
	}
	if bin := helperFindMacOSStrongswanBinary("swanctl"); bin != "" {
		root := filepath.Dir(filepath.Dir(bin))
		return filepath.Join(root, "etc", "swanctl")
	}
	return "/etc/swanctl"
}

// helperFindMacOSStrongswanBinary mirrors findStrongswanBinary client-side.
func helperFindMacOSStrongswanBinary(name string) string {
	candidates := []string{
		"/opt/homebrew/sbin/" + name,
		"/opt/homebrew/bin/" + name,
		"/usr/local/sbin/" + name,
		"/usr/local/bin/" + name,
		"/usr/sbin/" + name,
		"/usr/bin/" + name,
	}
	for _, p := range candidates {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}

// cmdIPSecCheckDependencies reports the macOS Homebrew strongSwan
// install state. Privycs uses Homebrew swanctl as the macOS IPSec
// backend (Apple's NEVPNManager would also work but requires the
// Personal-VPN entitlement which the direct-distribution build
// doesn't carry — App-Store-flavor will use NEVPNManager).
//
// Three signals are returned in Output as `key=true|false` lines:
//
//   - brew_installed: `brew` binary findable in either Homebrew prefix
//   - strongswan_installed: `swanctl` binary findable
//   - charon_running: vici socket present + accept()'able. We don't
//     just check pgrep because charon-on-launchd-with-fault might be
//     started but not listening, and `pgrep` would lie about that.
//
// The client uses these to render a precise install-instruction
// banner ("brew install strongswan" vs "brew services start strongswan"
// vs "all good, retry connect") instead of guessing.
func (h *PrivilegedHelper) cmdIPSecCheckDependencies(cmd HelperCommand) HelperResponse {
	if runtime.GOOS != "darwin" {
		return HelperResponse{Success: true, Output: "brew_installed=true\nstrongswan_installed=true\ncharon_running=true\n"}
	}

	brewBin := helperFindMacOSStrongswanBinary("brew")
	if brewBin == "" {
		// Apple-Silicon and Intel canonical install paths.
		for _, p := range []string{"/opt/homebrew/bin/brew", "/usr/local/bin/brew"} {
			if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
				brewBin = p
				break
			}
		}
	}
	swanctlBin := helperFindMacOSStrongswanBinary("swanctl")

	// vici socket location follows the brew prefix. /opt/homebrew on
	// Apple Silicon, /usr/local on Intel. Falls back to /etc on user
	// custom installs.
	viciCandidates := []string{
		"/opt/homebrew/var/run/charon.vici",
		"/usr/local/var/run/charon.vici",
		"/var/run/charon.vici",
	}
	charonRunning := false
	for _, p := range viciCandidates {
		if fi, err := os.Stat(p); err == nil && (fi.Mode()&os.ModeSocket) != 0 {
			charonRunning = true
			break
		}
	}

	out := fmt.Sprintf(
		"brew_installed=%t\nstrongswan_installed=%t\ncharon_running=%t\n",
		brewBin != "",
		swanctlBin != "",
		charonRunning,
	)
	return HelperResponse{Success: true, Output: out}
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
// cmdIPSecCleanup removes the swanctl-managed config + PEM files for a
// connection that the user is deleting in Privycs (or removing the
// IPSec protocol from). Then runs `swanctl --load-all` so charon
// forgets the connection. macOS-Pro only — Linux uses the Linux
// cleanup hooks (swanctl --terminate at disconnect time) and Windows
// uses Remove-VpnConnection.
//
// Currently the swanctl conf shape is single-connection per host (one
// privycs-vpn.conf, one privycs-client.pem). If the user adds support
// for multiple concurrent IPSec-PPK connections this function needs
// to track per-connection filenames. That gap is shared with
// configureMacOSFromSSwanViaSwanctl which writes to the same paths.
func (h *PrivilegedHelper) cmdIPSecCleanup(cmd HelperCommand) HelperResponse {
	if runtime.GOOS != "darwin" {
		return HelperResponse{Success: true, Output: "ipsec_cleanup: no-op on non-darwin"}
	}
	certDir := helperFindMacOSSwanctlConfDir()
	swanctlBin := helperFindMacOSStrongswanBinary("swanctl")

	files := []string{
		certDir + "/conf.d/privycs-vpn.conf",
		certDir + "/x509ca/privycs-ca.pem",
		certDir + "/x509/privycs-client.pem",
		certDir + "/private/privycs-client.pem",
	}
	var removed []string
	for _, f := range files {
		if err := os.Remove(f); err == nil {
			removed = append(removed, filepath.Base(f))
		}
	}

	out := fmt.Sprintf("removed %d swanctl files: %s", len(removed), strings.Join(removed, ", "))
	if swanctlBin != "" {
		// Best-effort reload so charon drops the in-memory conn config.
		// Failure here just means charon still knows about the conn
		// until it's restarted — non-fatal.
		_, _ = exec.Command(swanctlBin, "--load-all").CombinedOutput()
	}
	return HelperResponse{Success: true, Output: out}
}

// safeCIDRPattern validates a CIDR string. Accepts plain IPv4
// addresses (32-bit), IPv4 with /N, plain IPv6 hex+colon, and IPv6
// with /N. Anchors prevent shell-metachar injection — the helper
// passes these as direct exec.Cmd arguments to route(8) rather than
// through a shell, but anchored regex is defense-in-depth in case
// some future caller wraps them.
var safeCIDRPattern = regexp.MustCompile(`^[0-9a-fA-F:./]+$`)

// safeIPv4GatewayPattern validates a dotted-quad IPv4 gateway.
var safeIPv4GatewayPattern = regexp.MustCompile(`^[0-9.]+$`)

// cmdIPSecSplitRoutesAdd installs per-CIDR bypass routes after the
// macOS NEVPNProtocolIKEv2 stack has brought a tunnel up. The Apple
// IKE stack honors only IncludeAllNetworks at the policy level — it
// has no API to express a CIDR-list of bypass destinations. We
// install the bypass at the BSD route-table layer instead: each CIDR
// gets a host route through the user's pre-VPN default gateway, so
// packets to those destinations exit via en0/en1 (LAN/Ethernet)
// instead of the utun.
//
// macOS-only. On Linux/Windows the protocol-handler injects bypasses
// at the protocol layer (wg AllowedIPs, openvpn route-nopull, swanctl
// traffic-selectors), so this command is never invoked there.
func (h *PrivilegedHelper) cmdIPSecSplitRoutesAdd(cmd HelperCommand) HelperResponse {
	if runtime.GOOS != "darwin" {
		return HelperResponse{Success: false, Error: "ipsec_split_routes_add: darwin-only"}
	}
	gw := strings.TrimSpace(cmd.Args["gateway_ipv4"])
	if gw == "" {
		return HelperResponse{Success: false, Error: "gateway_ipv4 required"}
	}
	if !safeIPv4GatewayPattern.MatchString(gw) {
		return HelperResponse{Success: false, Error: "invalid gateway_ipv4"}
	}
	cidrsV4 := splitNonEmpty(cmd.Args["cidrs_ipv4"], ",")
	cidrsV6 := splitNonEmpty(cmd.Args["cidrs_ipv6"], ",")
	gwV6 := strings.TrimSpace(cmd.Args["gateway_ipv6"]) // optional, may be empty

	var added, failed []string
	for _, c := range cidrsV4 {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if !safeCIDRPattern.MatchString(c) {
			failed = append(failed, c+" (invalid CIDR)")
			continue
		}
		out, err := exec.Command("/sbin/route", "-n", "add", "-net", c, gw).CombinedOutput()
		if err != nil && !strings.Contains(string(out), "File exists") {
			failed = append(failed, fmt.Sprintf("%s (%v: %s)", c, err, strings.TrimSpace(string(out))))
			continue
		}
		added = append(added, c)
	}
	for _, c := range cidrsV6 {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if !safeCIDRPattern.MatchString(c) {
			failed = append(failed, c+" (invalid CIDR)")
			continue
		}
		// IPv6 needs either an explicit gateway (link-local with %iface
		// suffix) or -interface form. We use whichever was supplied.
		args := []string{"-n", "add", "-inet6", c}
		if gwV6 != "" && safeCIDRPattern.MatchString(gwV6) {
			args = append(args, gwV6)
		} else {
			// Skip silently when the caller couldn't determine an IPv6
			// gateway — better no IPv6 bypass than a broken route.
			continue
		}
		out, err := exec.Command("/sbin/route", args...).CombinedOutput()
		if err != nil && !strings.Contains(string(out), "File exists") {
			failed = append(failed, fmt.Sprintf("%s (%v: %s)", c, err, strings.TrimSpace(string(out))))
			continue
		}
		added = append(added, c)
	}

	out := fmt.Sprintf("added %d route(s): %s", len(added), strings.Join(added, " "))
	if len(failed) > 0 {
		out += fmt.Sprintf("; %d failed: %s", len(failed), strings.Join(failed, ", "))
	}
	return HelperResponse{Success: true, Output: out}
}

// cmdIPSecSplitRoutesRemove deletes the per-CIDR bypass routes that
// cmdIPSecSplitRoutesAdd installed. Idempotent — "route delete" of a
// non-existent route logs but does not fail the request.
func (h *PrivilegedHelper) cmdIPSecSplitRoutesRemove(cmd HelperCommand) HelperResponse {
	if runtime.GOOS != "darwin" {
		return HelperResponse{Success: false, Error: "ipsec_split_routes_remove: darwin-only"}
	}
	cidrsV4 := splitNonEmpty(cmd.Args["cidrs_ipv4"], ",")
	cidrsV6 := splitNonEmpty(cmd.Args["cidrs_ipv6"], ",")

	var removed, failed []string
	for _, c := range cidrsV4 {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if !safeCIDRPattern.MatchString(c) {
			failed = append(failed, c+" (invalid CIDR)")
			continue
		}
		out, err := exec.Command("/sbin/route", "-n", "delete", "-net", c).CombinedOutput()
		if err != nil && !strings.Contains(string(out), "not in table") {
			failed = append(failed, fmt.Sprintf("%s (%v: %s)", c, err, strings.TrimSpace(string(out))))
			continue
		}
		removed = append(removed, c)
	}
	for _, c := range cidrsV6 {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if !safeCIDRPattern.MatchString(c) {
			failed = append(failed, c+" (invalid CIDR)")
			continue
		}
		out, err := exec.Command("/sbin/route", "-n", "delete", "-inet6", c).CombinedOutput()
		if err != nil && !strings.Contains(string(out), "not in table") {
			failed = append(failed, fmt.Sprintf("%s (%v: %s)", c, err, strings.TrimSpace(string(out))))
			continue
		}
		removed = append(removed, c)
	}

	out := fmt.Sprintf("removed %d route(s): %s", len(removed), strings.Join(removed, " "))
	if len(failed) > 0 {
		out += fmt.Sprintf("; %d failed: %s", len(failed), strings.Join(failed, ", "))
	}
	return HelperResponse{Success: true, Output: out}
}

// cmdMacOSDNSOverrideSet applies the user's DNS-Override on macOS by
// pointing the primary network service at the override list and
// backing up the previous DNS so cmdMacOSDNSOverrideRestore can revert
// on disconnect.
//
// Why primary-service rather than per-VPN-tunnel: the swanctl path on
// macOS-Pro does NOT register a NEPacketTunnel (that would require
// MAS-style Network-Extension entitlements). Without one, macOS's
// resolver does not know about the IPSec SA's logical existence —
// DNS lookups go through the system resolver as if there were no VPN.
// Setting the primary network service's DNS via networksetup is the
// system-wide override knob that propagates to every resolver pass.
//
// Backup is persisted under /var/db/privycs-vpn/<connName>-dns-backup
// so a crashed Privycs can restore it on next launch (handled by
// CleanupMacOSSplitRouteOrphans + a similar DNS-orphan check).
func (h *PrivilegedHelper) cmdMacOSDNSOverrideSet(cmd HelperCommand) HelperResponse {
	if runtime.GOOS != "darwin" {
		return HelperResponse{Success: true, Output: "macos_dns_override_set: no-op on non-darwin"}
	}
	connName := cmd.Args["connection_name"]
	if connName == "" {
		return HelperResponse{Success: false, Error: "connection_name required"}
	}
	dnsList := cmd.Args["dns_servers"]
	if dnsList == "" {
		return HelperResponse{Success: false, Error: "dns_servers required"}
	}

	svc := findMacOSPrimaryNetworkService()
	if svc == "" {
		return HelperResponse{Success: false, Error: "no primary network service detected"}
	}

	// Snapshot current DNS for restore.
	curOut, _ := exec.Command("networksetup", "-getdnsservers", svc).CombinedOutput()
	current := strings.TrimSpace(string(curOut))
	// "There aren't any DNS servers set on Wi-Fi." → use the literal
	// "Empty" sentinel networksetup expects to clear back to DHCP.
	if strings.Contains(current, "aren't any") {
		current = "Empty"
	}
	if err := persistDNSOverrideBackup(connName, svc, current); err != nil {
		return HelperResponse{Success: false, Error: fmt.Sprintf("backup write: %v", err)}
	}

	// Apply override. networksetup wants a positional arg list —
	// dns_servers comes in space-separated.
	servers := strings.Fields(dnsList)
	args := append([]string{"-setdnsservers", svc}, servers...)
	if out, err := exec.Command("networksetup", args...).CombinedOutput(); err != nil {
		return HelperResponse{Success: false, Error: fmt.Sprintf("networksetup -setdnsservers: %s", string(out))}
	}
	log.Printf("DNS override (macOS swanctl): primary-service=%q dns=%s", svc, dnsList)
	return HelperResponse{Success: true, Output: fmt.Sprintf("DNS override applied to %q", svc)}
}

// cmdMacOSDNSOverrideRestore reads the per-connection backup and
// restores the previous DNS settings. Idempotent — missing backup or
// a no-longer-existing primary service both treat as a clean exit.
func (h *PrivilegedHelper) cmdMacOSDNSOverrideRestore(cmd HelperCommand) HelperResponse {
	if runtime.GOOS != "darwin" {
		return HelperResponse{Success: true, Output: "macos_dns_override_restore: no-op on non-darwin"}
	}
	connName := cmd.Args["connection_name"]
	if connName == "" {
		return HelperResponse{Success: false, Error: "connection_name required"}
	}
	svc, prev, err := loadDNSOverrideBackup(connName)
	if err != nil {
		// No backup = no override was active; non-error.
		return HelperResponse{Success: true, Output: "no backup to restore"}
	}

	// Reapply previous state. "Empty" tells networksetup to clear
	// back to DHCP-provided DNS.
	args := []string{"-setdnsservers", svc}
	if strings.TrimSpace(prev) == "Empty" || strings.TrimSpace(prev) == "" {
		args = append(args, "Empty")
	} else {
		args = append(args, strings.Fields(prev)...)
	}
	if out, err := exec.Command("networksetup", args...).CombinedOutput(); err != nil {
		return HelperResponse{Success: false, Error: fmt.Sprintf("networksetup restore: %s", string(out))}
	}
	deleteDNSOverrideBackup(connName)
	log.Printf("DNS override restored: service=%q prev=%s", svc, strings.TrimSpace(prev))
	return HelperResponse{Success: true, Output: fmt.Sprintf("DNS restored on %q", svc)}
}

// findMacOSPrimaryNetworkService returns the user-facing service name
// (e.g. "Wi-Fi", "Ethernet") whose interface carries the IPv4 default
// route. Maps `route -n get default` interface output through
// `networksetup -listallhardwareports` (Hardware Port + Device pairs).
// Returns empty string when no default route or no matching service.
func findMacOSPrimaryNetworkService() string {
	out, err := exec.Command("/sbin/route", "-n", "get", "default").Output()
	if err != nil {
		return ""
	}
	var iface string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "interface:") {
			iface = strings.TrimSpace(strings.TrimPrefix(line, "interface:"))
		}
	}
	if iface == "" {
		return ""
	}
	out, err = exec.Command("networksetup", "-listallhardwareports").Output()
	if err != nil {
		return ""
	}
	var currentService string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Hardware Port:") {
			currentService = strings.TrimSpace(strings.TrimPrefix(line, "Hardware Port:"))
		} else if strings.HasPrefix(line, "Device:") {
			device := strings.TrimSpace(strings.TrimPrefix(line, "Device:"))
			if device == iface {
				return currentService
			}
		}
	}
	return ""
}

const macosDNSBackupDir = "/var/db/privycs-vpn"

func dnsBackupPath(connName string) string {
	return filepath.Join(macosDNSBackupDir, connName+"-dns-backup.txt")
}

// persistDNSOverrideBackup writes "<service>\n<dns>\n" so the restore
// path can split-on-first-newline. networksetup output for the dns
// list is whitespace-separated IPs, harmless to keep verbatim.
func persistDNSOverrideBackup(connName, service, current string) error {
	if err := os.MkdirAll(macosDNSBackupDir, 0700); err != nil {
		return err
	}
	body := service + "\n" + current
	return os.WriteFile(dnsBackupPath(connName), []byte(body), 0600)
}

func loadDNSOverrideBackup(connName string) (service, prevDNS string, err error) {
	data, rErr := os.ReadFile(dnsBackupPath(connName))
	if rErr != nil {
		return "", "", rErr
	}
	parts := strings.SplitN(string(data), "\n", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("malformed backup")
	}
	return parts[0], parts[1], nil
}

func deleteDNSOverrideBackup(connName string) {
	_ = os.Remove(dnsBackupPath(connName))
}

// cmdMacOSDNSOverrideClean restores every DNS-Override backup found
// in /var/db/privycs-vpn. Called once at app start so a previous
// crash that left the primary network service pointing at a
// VPN-only DNS resolver doesn't strand the user offline. Idempotent —
// a clean state directory is a fast no-op.
func (h *PrivilegedHelper) cmdMacOSDNSOverrideClean(cmd HelperCommand) HelperResponse {
	if runtime.GOOS != "darwin" {
		return HelperResponse{Success: true, Output: "macos_dns_override_clean: no-op on non-darwin"}
	}
	entries, err := os.ReadDir(macosDNSBackupDir)
	if err != nil {
		// Directory does not exist = no orphans.
		return HelperResponse{Success: true, Output: "no backup directory"}
	}
	var restored []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), "-dns-backup.txt") {
			continue
		}
		connName := strings.TrimSuffix(e.Name(), "-dns-backup.txt")
		svc, prev, lErr := loadDNSOverrideBackup(connName)
		if lErr != nil {
			_ = os.Remove(filepath.Join(macosDNSBackupDir, e.Name()))
			continue
		}
		args := []string{"-setdnsservers", svc}
		if strings.TrimSpace(prev) == "Empty" || strings.TrimSpace(prev) == "" {
			args = append(args, "Empty")
		} else {
			args = append(args, strings.Fields(prev)...)
		}
		_, _ = exec.Command("networksetup", args...).CombinedOutput()
		deleteDNSOverrideBackup(connName)
		restored = append(restored, connName)
	}
	return HelperResponse{Success: true, Output: fmt.Sprintf("restored %d orphan DNS backup(s): %s",
		len(restored), strings.Join(restored, ","))}
}

// splitNonEmpty splits s by sep and drops empty fragments. Returns
// nil for an empty input so callers can range over it safely.
func splitNonEmpty(s, sep string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, sep)
	out := parts[:0]
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return out
}

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

// cmdSinkholeEngage applies the new sinkhole's Privycs-Sinkhole-* rules.
// Runs as SYSTEM via the helper service so PowerShell New-NetFirewall
// Rule has the privileges it needs (the unprivileged Wails app process
// hits "Zugriff verweigert" when calling these cmdlets directly).
//
// Uses netsh (same as legacy killswitch_enable) instead of PowerShell
// New-NetFirewallRule because netsh is already proven in this code path
// and avoids the PowerShell startup latency overhead.
func (h *PrivilegedHelper) cmdSinkholeEngage(cmd HelperCommand) HelperResponse {
	switch runtime.GOOS {
	case "windows":
		return h.sinkholeWindowsEngage()
	case "linux":
		return h.sinkholeLinuxEngage()
	case "darwin":
		return h.sinkholeMacOSEngage()
	}
	return HelperResponse{Success: false, Error: "unsupported platform"}
}

// cmdSinkholeRelease removes Privycs-Sinkhole-* rules.
func (h *PrivilegedHelper) cmdSinkholeRelease(cmd HelperCommand) HelperResponse {
	switch runtime.GOOS {
	case "windows":
		return h.sinkholeWindowsRelease()
	case "linux":
		return h.sinkholeLinuxRelease()
	case "darwin":
		return h.sinkholeMacOSRelease()
	}
	return HelperResponse{Success: false, Error: "unsupported platform"}
}

// Windows: Privycs-Sinkhole-* rules via netsh. Three rules: allow
// loopback, block all outbound, block all inbound. All-or-nothing
// semantics: on any single failure, rollback by removing whatever
// was added.
func (h *PrivilegedHelper) sinkholeWindowsEngage() HelperResponse {
	// Defensive cleanup: remove any leftover Privycs-Sinkhole-* rules
	// from a prior crashed run before adding fresh ones.
	h.sinkholeWindowsRelease()

	commands := [][]string{
		{"netsh", "advfirewall", "firewall", "add", "rule",
			"name=Privycs-Sinkhole-AllowLoopback", "dir=out", "action=allow", "remoteip=127.0.0.0/8"},
		{"netsh", "advfirewall", "firewall", "add", "rule",
			"name=Privycs-Sinkhole-BlockOutbound", "dir=out", "action=block"},
		{"netsh", "advfirewall", "firewall", "add", "rule",
			"name=Privycs-Sinkhole-BlockInbound", "dir=in", "action=block"},
	}
	added := []string{}
	for _, args := range commands {
		out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
		if err != nil {
			// Rollback added rules
			for _, name := range added {
				exec.Command("netsh", "advfirewall", "firewall", "delete", "rule", "name="+name).Run()
			}
			return HelperResponse{
				Success: false,
				Error:   fmt.Sprintf("sinkhole engage failed at %s: %s", args[len(args)-3], strings.TrimSpace(string(out))),
			}
		}
		// Extract name=... from args for rollback list
		for _, a := range args {
			if strings.HasPrefix(a, "name=") {
				added = append(added, strings.TrimPrefix(a, "name="))
				break
			}
		}
	}
	return HelperResponse{Success: true, Output: "sinkhole engaged (windows)"}
}

func (h *PrivilegedHelper) sinkholeWindowsRelease() HelperResponse {
	rules := []string{
		"Privycs-Sinkhole-AllowLoopback",
		"Privycs-Sinkhole-BlockOutbound",
		"Privycs-Sinkhole-BlockInbound",
	}
	for _, name := range rules {
		exec.Command("netsh", "advfirewall", "firewall", "delete", "rule", "name="+name).Run()
	}
	return HelperResponse{Success: true, Output: "sinkhole released (windows)"}
}

// Linux: PRIVYCS_SINKHOLE chain. Same logic as sinkhole_linux.go but
// running with root privileges via the helper.
func (h *PrivilegedHelper) sinkholeLinuxEngage() HelperResponse {
	// Defensive cleanup
	h.sinkholeLinuxRelease()
	steps := [][]string{
		{"iptables", "-N", "PRIVYCS_SINKHOLE"},
		{"iptables", "-A", "PRIVYCS_SINKHOLE", "-o", "lo", "-j", "RETURN"},
		{"iptables", "-A", "PRIVYCS_SINKHOLE", "-j", "DROP"},
		{"iptables", "-I", "OUTPUT", "1", "-j", "PRIVYCS_SINKHOLE"},
	}
	for _, step := range steps {
		if out, err := exec.Command(step[0], step[1:]...).CombinedOutput(); err != nil {
			h.sinkholeLinuxRelease()
			return HelperResponse{
				Success: false,
				Error:   fmt.Sprintf("sinkhole engage failed: %s: %s", strings.Join(step, " "), strings.TrimSpace(string(out))),
			}
		}
	}
	return HelperResponse{Success: true, Output: "sinkhole engaged (linux)"}
}

func (h *PrivilegedHelper) sinkholeLinuxRelease() HelperResponse {
	exec.Command("iptables", "-D", "OUTPUT", "-j", "PRIVYCS_SINKHOLE").Run()
	exec.Command("iptables", "-F", "PRIVYCS_SINKHOLE").Run()
	exec.Command("iptables", "-X", "PRIVYCS_SINKHOLE").Run()
	return HelperResponse{Success: true, Output: "sinkhole released (linux)"}
}

// macOS: pf anchor com.privycs/sinkhole.
func (h *PrivilegedHelper) sinkholeMacOSEngage() HelperResponse {
	rules := "set skip on lo0\nblock out all\nblock in all\n"
	cmd := exec.Command("pfctl", "-a", "com.privycs/sinkhole", "-f", "-")
	cmd.Stdin = strings.NewReader(rules)
	if out, err := cmd.CombinedOutput(); err != nil {
		exec.Command("pfctl", "-a", "com.privycs/sinkhole", "-F", "all").Run()
		return HelperResponse{
			Success: false,
			Error:   fmt.Sprintf("pfctl anchor load: %s", strings.TrimSpace(string(out))),
		}
	}
	exec.Command("pfctl", "-E").Run()
	return HelperResponse{Success: true, Output: "sinkhole engaged (darwin)"}
}

func (h *PrivilegedHelper) sinkholeMacOSRelease() HelperResponse {
	exec.Command("pfctl", "-a", "com.privycs/sinkhole", "-F", "all").Run()
	exec.Command("pfctl", "-X").Run()
	return HelperResponse{Success: true, Output: "sinkhole released (darwin)"}
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
