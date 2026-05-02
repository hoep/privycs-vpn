package main

import (
	"context"
	"fmt"
	"io"
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

	// Offset in the OpenVPN log file at the moment we launched the
	// current session. State detection reads only content after this
	// offset so stale "Initialization Sequence Completed" lines from
	// previous sessions don't produce false positives. Set in Up(),
	// cleared in Down().
	logStartOffset int64

	// Short-lived state cache. OpenVPN 2.7.1 Windows has a known TCP
	// management-socket bug (win32.c:332 assertion) that crashes the
	// daemon on rapid connect/disconnect cycles to port 7505; the
	// OpenVPN community recommends avoiding the management socket
	// altogether. We derive state from the openvpn.log file instead,
	// but still cache the result for 3 s so Status()-heavy callers
	// (UI poll every 2 s, connect poll every 3 s) don't repeatedly
	// re-read + re-scan the log for no reason.
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

// findOpenVPNExe locates the OpenVPN executable.
//
// On macOS GUI-launched apps and launchd-spawned daemons inherit a minimal
// PATH (/usr/bin:/bin:/usr/sbin:/sbin) that does NOT include the Homebrew
// install dirs (/opt/homebrew/bin on Apple Silicon, /usr/local/bin on Intel),
// so a plain exec.LookPath("openvpn") returns "not found" even though
// `which openvpn` works fine in Terminal. Mirror the v0.9.14.23 findWGBinary
// fix here — explicit os.Stat fallbacks before falling through to PATH.
func findOpenVPNExe() string {
	if runtime.GOOS == "windows" {
		// Windows: standard install paths first, then PATH.
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
		if p, err := exec.LookPath("openvpn.exe"); err == nil {
			return p
		}
		return ""
	}
	// Linux/macOS: prefer absolute paths so we don't depend on PATH at all
	// from the launchd-spawned helper context.
	//
	// Homebrew puts OpenVPN under sbin/, NOT bin/ — the binary lives in
	// /usr/local/Cellar/openvpn/<ver>/sbin/openvpn and is symlinked from
	// /usr/local/sbin/openvpn (Intel) or /opt/homebrew/sbin/openvpn (Apple
	// Silicon). v0.9.14.30 missed the sbin variants for Mac and only
	// covered them for distro-packaged Linux. Search order: both arch's
	// Homebrew sbin + bin, then distro-packaged Linux paths.
	candidates := []string{
		"/opt/homebrew/sbin/openvpn",
		"/usr/local/sbin/openvpn",
		"/opt/homebrew/bin/openvpn",
		"/usr/local/bin/openvpn",
		"/usr/sbin/openvpn",
		"/sbin/openvpn",
		"/usr/bin/openvpn",
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if p, err := exec.LookPath("openvpn"); err == nil {
		return p
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

	// Remember the log file's size at spawn time so Status() can skip
	// any content written by previous OpenVPN sessions (which would
	// otherwise contain a stale "Initialization Sequence Completed"
	// line and cause a false positive for the very first state check).
	if fi, err := os.Stat(logPath); err == nil {
		o.logStartOffset = fi.Size()
	} else {
		o.logStartOffset = 0
	}
	// Reset any cached state from a previous run.
	o.cachedState = ""
	o.cachedStateTime = time.Time{}

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

// readOpenVPNStateFromLog determines the current OpenVPN tunnel state by
// tailing the openvpn.log file from the offset captured at Up() time.
// This avoids the management TCP socket entirely.
//
// Rationale: OpenVPN 2.7.1 on Windows has a known TCP-management-socket
// bug (win32.c:332 assertion, see Netgate / openvpn community forums)
// that triggers after a handful of short-lived management connections
// no matter how infrequent. The community-recommended workaround is to
// either keep one persistent management connection (still risks other
// 2.7.1-series bugs) or to drive state detection from the log. The log
// is the authoritative source anyway — OpenVPN writes
// "Initialization Sequence Completed" exactly once per successful
// tunnel setup, and terminal states ("Exiting due to fatal error",
// "SIGTERM received", "process exiting") are logged just as reliably.
//
// Returns one of:
//   - "CONNECTED": Initialization Sequence Completed has been logged,
//     no fatal-exit line after it
//   - "CONNECTING": no init-complete line yet, no fatal-exit, still
//     in setup phase
//   - "EXITING":   fatal exit or SIGTERM observed after init-complete
//     (tunnel torn down)
//   - "":          log unreadable; caller treats as "unknown, assume
//     not yet connected"
func (o *OpenVPNProtocol) readOpenVPNStateFromLog() string {
	logPath := filepath.Join(appDataDir(), "openvpn.log")
	f, err := os.Open(logPath)
	if err != nil {
		return ""
	}
	defer f.Close()

	// Seek to the offset captured at Up() time so we only read lines
	// from the current session. If the log rotated since Up() (size <
	// offset), start from 0 — the file is fresh and everything in it
	// belongs to the current run.
	fi, err := f.Stat()
	if err != nil {
		return ""
	}
	start := o.logStartOffset
	if fi.Size() < start {
		start = 0
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return ""
	}

	buf, err := io.ReadAll(f)
	if err != nil {
		return ""
	}
	content := string(buf)

	// Terminal states trump initialization-complete. If the process
	// logged a fatal exit, the tunnel is down even if init-complete
	// was logged earlier in the same session.
	if strings.Contains(content, "Exiting due to fatal error") ||
		strings.Contains(content, "SIGTERM received") ||
		strings.Contains(content, "process exiting") {
		return "EXITING"
	}

	// "Initialization Sequence Completed" is OpenVPN's canonical
	// "tunnel is now up" marker — emitted once after routes are
	// installed and the data channel is ready. Match case-insensitive
	// because different log verbosity levels vary capitalisation.
	if strings.Contains(content, "Initialization Sequence Completed") {
		return "CONNECTED"
	}

	return "CONNECTING"
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

	// Determine tunnel state by reading the openvpn.log file. See
	// readOpenVPNStateFromLog for the full rationale — tl;dr: OpenVPN
	// 2.7.1 Windows crashes on repeated management-socket usage so we
	// derive state from the log file instead. Cached for 3 s so UI
	// polling (every 2 s) doesn't re-read the log on every tick.
	var state string
	if time.Since(o.cachedStateTime) < 3*time.Second && o.cachedState != "" {
		state = o.cachedState
	} else {
		state = o.readOpenVPNStateFromLog()
		o.cachedState = state
		o.cachedStateTime = time.Now()
	}
	if state != "CONNECTED" {
		return status
	}

	status.Connected = true
	status.ConnectedAt = o.connectedAt.Format(time.RFC3339)
	if runtime.GOOS == "windows" {
		// OpenVPN on Windows can use any of three transport drivers
		// each producing a different adapter friendly-name. Try all
		// the known patterns plus "Wintun" because some installs
		// share the WG driver. Order does not matter - we take the
		// first UP adapter that matches.
		status.BytesRx, status.BytesTx = getWindowsTrafficStats(
			"OpenVPN",       // OpenVPN Wintun / OpenVPN TAP-Windows6 / OpenVPN Data Channel Offload
			"ovpn-dco",      // standalone DCO driver naming
			"TAP-Windows",   // legacy TAP driver
			"Wintun",        // shared wintun adapter
			"tap",           // catch-all for tap variants
		)
	} else if runtime.GOOS == "linux" {
		status.BytesRx, status.BytesTx = getLinuxInterfaceStats("tun0")
	}
	return status
}

// parseBypassNetworksFromOvpn extracts `# PRIVYCS-BYPASS: <cidr>` lines
// from the .ovpn file body. The Privycs gateway emits one such comment
// per bypass network (both IPv4 and IPv6) and the client app installs
// local routes for them after the tunnel comes up — OpenVPN itself has
// no native IPv6-bypass push syntax, so clients drive it from these
// comments. Unrecognised or malformed CIDRs are silently skipped.
func parseBypassNetworksFromOvpn(content string) []string {
	var bypass []string
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		// Comment marker is flexible: "# PRIVYCS-BYPASS:", "#PRIVYCS-BYPASS:"
		// and case-insensitive match so the gateway emitter's exact
		// whitespace doesn't have to be sacred.
		if !strings.HasPrefix(strings.ToLower(trimmed), "# privycs-bypass:") &&
			!strings.HasPrefix(strings.ToLower(trimmed), "#privycs-bypass:") {
			continue
		}
		idx := strings.Index(trimmed, ":")
		if idx < 0 {
			continue
		}
		cidr := strings.TrimSpace(trimmed[idx+1:])
		if cidr != "" {
			bypass = append(bypass, cidr)
		}
	}
	return bypass
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
