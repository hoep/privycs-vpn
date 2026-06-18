package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
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
	configPath string
	// Per-profile log + PID paths, mirror configPath. Pre-fix builds
	// wrote every OpenVPN profile to a single shared "openvpn.log" /
	// "openvpn.pid", which made Status() read the wrong profile's
	// logs after a profile switch and Down() risk killing the wrong
	// process — fix from the multi-profile audit.
	logPath     string
	pidPath     string
	cmd         *exec.Cmd
	connectedAt time.Time
	serverAddr  string
	localAddr   string

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

	// macOS picks the utun device dynamically (utun0, utun1, ...) so we
	// can't hard-code it for traffic-stats lookups. Parsed from the
	// "Opened utun device utunN" log line on the first stats query
	// after Up() and cached for the session.
	tunIface string

	// v0.9.15.46: throttle log-read diagnostic emission. Polling is
	// 250 ms; without throttling the CONNECTING-tail log line would
	// fire 4×/s during the 30 s connect window and drown the app log.
	lastLogReadDiagAt time.Time

	// v0.9.15.53: Windows-only. Set true once we've applied the
	// gateway-pushed DNS to the tunnel adapter via the privileged
	// helper (we filter OpenVPN's own buggy netsh-DNS path with
	// `--pull-filter ignore "dhcp-option DNS"`). Reset on every Up()
	// so a fresh connect re-applies. Guards the once-only apply
	// against the 2 s Status() poll loop.
	windowsDNSApplied bool

	// v1.0.0 encryption-at-rest: the persistent configPath is
	// encrypted (PVCE magic), but the openvpn binary + privileged
	// helper read .ovpn files DIRECTLY from disk. So at Up() time we
	// decrypt into a sibling plaintext file at configPath+".runtime"
	// and hand THAT path to openvpn/helper; at Down() we remove it
	// again. Pre-v1.0 plaintext configs stay in-place (no runtime
	// copy needed) — prepareRuntimeConfig short-circuits in that case.
	runtimeConfigPath string
}

// NewOpenVPNProtocol creates a new OpenVPN protocol handler
func NewOpenVPNProtocol() *OpenVPNProtocol {
	return &OpenVPNProtocol{
		configPath: filepath.Join(appDataDir(), "privycs0.ovpn"),
		logPath:    filepath.Join(appDataDir(), "privycs0.log"),
		pidPath:    filepath.Join(appDataDir(), "privycs0.pid"),
	}
}

// prepareRuntimeConfig returns the .ovpn path the openvpn binary +
// privileged helper should consume. When the persistent configPath is
// encrypted-at-rest (PVCE magic, v1.0.0+), decrypt to a sibling
// "<name>.ovpn.runtime" plaintext file and return that path. The
// runtime file is removed by cleanupRuntimeConfig() at Down() time.
// Pre-v1.0 plaintext .ovpn files are passed through unchanged.
func (o *OpenVPNProtocol) prepareRuntimeConfig() (string, error) {
	if !IsEncryptedFile(o.configPath) {
		return o.configPath, nil
	}
	plain, err := EncryptedReadFile(o.configPath)
	if err != nil {
		return "", fmt.Errorf("decrypt .ovpn: %w", err)
	}
	runtimePath := o.configPath + ".runtime"
	if err := os.WriteFile(runtimePath, plain, 0600); err != nil {
		return "", fmt.Errorf("write runtime .ovpn: %w", err)
	}
	o.runtimeConfigPath = runtimePath
	return runtimePath, nil
}

// cleanupRuntimeConfig removes the plain-text runtime .ovpn produced
// by prepareRuntimeConfig. Idempotent and best-effort — a leftover
// file from a crashed run is overwritten on the next prepare.
func (o *OpenVPNProtocol) cleanupRuntimeConfig() {
	if o.runtimeConfigPath == "" {
		return
	}
	if err := os.Remove(o.runtimeConfigPath); err != nil && !os.IsNotExist(err) {
		log.Printf("cleanupRuntimeConfig: remove %s: %v", o.runtimeConfigPath, err)
	}
	o.runtimeConfigPath = ""
}

// SetTunnelName updates the per-profile paths to match the connection
// slot name (see setTunnelName + tunnelNameForSlot in app.go). Both the
// .ovpn config file and the .log file are kept distinct per profile so
// switching profiles doesn't overwrite or mis-read each other's state.
func (o *OpenVPNProtocol) SetTunnelName(name string) {
	if name == "" {
		return
	}
	o.configPath = filepath.Join(appDataDir(), name+".ovpn")
	o.logPath = filepath.Join(appDataDir(), name+".log")
	o.pidPath = filepath.Join(appDataDir(), name+".pid")
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

	// v1.0.0 encryption-at-rest: when configPath is encrypted (PVCE),
	// decrypt to a plain sibling so openvpn + the privileged helper
	// can read it directly. The runtime copy lives for the duration
	// of the tunnel and is removed in Down().
	cfgPath, err := o.prepareRuntimeConfig()
	if err != nil {
		return fmt.Errorf("prepare runtime config: %w", err)
	}

	logPath := o.logPath
	pidPath := o.pidPath

	// Reset any cached state from a previous run. Stale
	// "Initialization Sequence Completed" lines from earlier sessions
	// no longer cause false positives because readOpenVPNStateFromLog
	// scans for the LAST occurrence and checks for a terminal marker
	// after it.
	o.cachedState = ""
	o.cachedStateTime = time.Time{}
	o.tunIface = ""
	o.windowsDNSApplied = false // v0.9.15.53: re-apply tunnel DNS each fresh connect

	log.Printf("Starting OpenVPN via %s", ovpnExe)

	if runtime.GOOS == "windows" {
		// Windows: try privileged helper first (no UAC prompt).
		client := NewHelperClient()
		if client.IsHelperReachable() {
			log.Printf("Using privileged helper for OpenVPN connect")
			resp, err := client.SendCommand("connect", map[string]string{
				"protocol":    "openvpn",
				"config_path": cfgPath,
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
			escapePowerShellString(ovpnExe), escapePowerShellString(cfgPath), escapePowerShellString(logPath), ovpnMgmtHost, ovpnMgmtPort)
		o.cmd = execHiddenContext(ctx, "powershell", "-NoProfile", "-Command", psCmd)
	} else {
		// Linux/macOS: try privileged helper first (no sudo/password prompts)
		client := NewHelperClient()
		if client.IsHelperReachable() {
			log.Printf("Using privileged helper for OpenVPN connect")
			resp, err := client.SendCommand("connect", map[string]string{
				"protocol":    "openvpn",
				"config_path": cfgPath,
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
			"--config", cfgPath,
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
	// v1.0.0: remove the decrypted runtime config (no-op if Up never
	// ran or configPath was already plaintext). Deferred-style via
	// defer would be neater but Down has multiple return paths and
	// we want the cleanup unconditional + early.
	defer o.cleanupRuntimeConfig()

	if runtime.GOOS == "windows" {
		// Helper-first: kill by PID file (no UAC).
		client := NewHelperClient()
		if client.IsHelperReachable() {
			pidPath := o.pidPath
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
	pidPath := o.pidPath

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
//
// parsePushedDNSFromLog extracts the first server-pushed IPv4 DNS
// server from the most recent PUSH_REPLY line in openvpn.log.
// v0.9.15.53.
//
// We launch openvpn.exe with `--pull-filter ignore "dhcp-option DNS"`
// so OpenVPN never APPLIES the pushed DNS (its Windows-DCO netsh path
// is broken — see cmdWindowsDNSSet doc). `--pull-filter ignore`
// suppresses the option's effect but the raw PUSH_REPLY control
// message is still written to the log verbatim, so the intended DNS
// is recoverable here. Returns "" if no PUSH_REPLY / no DNS option /
// log unreadable — caller simply retries on the next Status() poll.
//
// Format in the log:
//
//	PUSH: Received control message: 'PUSH_REPLY,...,dhcp-option DNS 10.100.10.150,...'
func (o *OpenVPNProtocol) parsePushedDNSFromLog() string {
	logPath := o.logPath
	buf, err := os.ReadFile(logPath)
	if err != nil {
		return ""
	}
	content := string(buf)
	// Last PUSH_REPLY wins — a reconnect mid-session re-pushes.
	idx := strings.LastIndex(content, "PUSH_REPLY")
	if idx < 0 {
		return ""
	}
	// Bound the scan to that PUSH_REPLY's line so we don't pick up a
	// dhcp-option from a later unrelated log entry.
	seg := content[idx:]
	if nl := strings.IndexAny(seg, "\r\n"); nl >= 0 {
		seg = seg[:nl]
	}
	// PUSH_REPLY is a comma-separated list. Find the first
	// "dhcp-option DNS <ip>" entry and return a valid IPv4 literal.
	for _, part := range strings.Split(seg, ",") {
		f := strings.Fields(strings.TrimSpace(part))
		// f == ["dhcp-option", "DNS", "10.100.10.150"]
		if len(f) == 3 && f[0] == "dhcp-option" && f[1] == "DNS" {
			ip := net.ParseIP(f[2])
			if ip != nil && ip.To4() != nil {
				return f[2]
			}
		}
	}
	return ""
}

func (o *OpenVPNProtocol) readOpenVPNStateFromLog() string {
	logPath := o.logPath
	buf, err := os.ReadFile(logPath)
	if err != nil {
		// v0.9.15.46 diagnostic — fire at most every 5s so polling
		// (every 250ms) doesn't drown the log. Logs the actual error
		// so a permissions / path / locked-file scenario surfaces
		// instead of disappearing into a silent return.
		if time.Since(o.lastLogReadDiagAt) > 5*time.Second {
			log.Printf("OpenVPN status: read %s failed: %v", logPath, err)
			o.lastLogReadDiagAt = time.Now()
		}
		return ""
	}
	content := string(buf)

	// Find the LAST "Initialization Sequence Completed" — that line marks
	// the most recent successful tunnel bring-up. Reading by offset (the
	// previous approach) was unreliable: openvpn's --log truncates the
	// file at spawn, but the offset captured in Up() reflected the OLD
	// file size; once the new session's log grew past that offset, the
	// reader seeked PAST the init-complete line and never matched it.
	lastInit := strings.LastIndex(content, "Initialization Sequence Completed")
	if lastInit < 0 {
		// v0.9.15.46 diagnostic — surface the last 200 chars of the
		// log every 5s while we're still in CONNECTING. If openvpn
		// hit a fatal at startup but we never see init-complete,
		// this is the only place that knows about it.
		if time.Since(o.lastLogReadDiagAt) > 5*time.Second {
			tail := content
			if len(tail) > 200 {
				tail = "..." + tail[len(tail)-200:]
			}
			log.Printf("OpenVPN status: still CONNECTING after %d bytes of log. Tail: %q",
				len(content), strings.ReplaceAll(tail, "\n", " ⏎ "))
			o.lastLogReadDiagAt = time.Now()
		}
		return "CONNECTING"
	}

	// Anything AFTER the last init-complete marker that signals a
	// terminal state means the tunnel is down. SIGINT is included because
	// macOS openvpn logs "SIGINT[hard,] received, process exiting" on
	// helper-driven disconnect.
	after := content[lastInit:]
	if strings.Contains(after, "Exiting due to fatal error") ||
		strings.Contains(after, "SIGTERM received") ||
		strings.Contains(after, "SIGINT") ||
		strings.Contains(after, "process exiting") {
		return "EXITING"
	}

	return "CONNECTED"
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
		pidPath := o.pidPath
		if pidData, err := os.ReadFile(pidPath); err == nil {
			var pid int
			if _, err := fmt.Sscan(strings.TrimSpace(string(pidData)), &pid); err == nil && pid > 0 {
				// Liveness probe is the standard kill(pid, 0). The
				// previous implementation called proc.Signal(nil),
				// which Go always rejects with "unsupported signal
				// type" — processRunning was permanently false on
				// macOS, Status() bailed out before reading the log,
				// and the UI hung forever on the connecting spinner
				// even though the tunnel was up.
				processRunning = isProcessAlive(pid)
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
	// Resolve the OS interface this OpenVPN session uses so we can pull
	// both byte-counters AND the dynamically-assigned local IP off it.
	// Static `ifconfig-push` in the .ovpn already populated o.localAddr
	// at parse time (rare with provider-issued profiles); the typical
	// case is the server PUSHing an IP after TLS-handshake — the
	// localAddr starts empty and we read it from the live tun/utun.
	var ovpnIface string
	if runtime.GOOS == "windows" {
		// OpenVPN on Windows can use any of three transport drivers
		// each producing a different adapter friendly-name. Try all
		// the known patterns plus "Wintun" because some installs
		// share the WG driver. Order does not matter - we take the
		// first UP adapter that matches.
		status.BytesRx, status.BytesTx = getWindowsTrafficStats(
			"OpenVPN",     // OpenVPN Wintun / OpenVPN TAP-Windows6 / OpenVPN Data Channel Offload
			"ovpn-dco",    // standalone DCO driver naming
			"TAP-Windows", // legacy TAP driver
			"Wintun",      // shared wintun adapter
			"tap",         // catch-all for tap variants
		)
		ovpnIface = findFirstTunlikeInterface([]string{"OpenVPN", "ovpn", "TAP-Windows", "Wintun", "tap"})
		// v0.9.15.53: apply the gateway-pushed DNS to the tunnel
		// adapter ourselves. We launch openvpn.exe with
		// `--pull-filter ignore "dhcp-option DNS"` so it never runs
		// its own buggy `set dns`+`add dns` netsh sequence (OpenVPN
		// 2.7.1-DCO on Windows 26200 duplicates the single pushed
		// server and the redundant `add` fails fatally). The raw
		// PUSH_REPLY is still logged though, so we recover the
		// intended DNS from there and hand it to the privileged
		// helper for a single clean `netsh ... set dns`. Once-only
		// per connect (Status() polls every 2 s); flag reset in Up().
		if !o.windowsDNSApplied && ovpnIface != "" {
			if dns := o.parsePushedDNSFromLog(); dns != "" {
				client := NewHelperClient()
				if client.IsHelperReachable() {
					resp, err := client.SendCommand("windows_dns_set", map[string]string{
						"iface": ovpnIface,
						"dns":   dns,
					})
					if err == nil && resp.Success {
						o.windowsDNSApplied = true
						log.Printf("OpenVPN Windows: tunnel DNS %s applied to %q via helper", dns, ovpnIface)
					} else {
						errStr := ""
						if err != nil {
							errStr = err.Error()
						} else {
							errStr = resp.Error
						}
						log.Printf("OpenVPN Windows: helper windows_dns_set failed (will retry next poll): %s", errStr)
					}
				}
			}
		}
	} else if runtime.GOOS == "linux" {
		// OpenVPN auto-assigns tun0/tun1/… — a hardcoded "tun0" mis-reads stats
		// when another tun already exists. Detect the active tun (audit 2026-06-18).
		ovpnIface = findLinuxTunInterface()
		status.BytesRx, status.BytesTx = getLinuxInterfaceStats(ovpnIface)
	} else if runtime.GOOS == "darwin" {
		if iface := o.findUtunInterface(); iface != "" {
			ovpnIface = iface
			status.BytesRx, status.BytesTx = getDarwinInterfaceStats(iface)
		}
	}
	if ovpnIface != "" {
		// Always re-read live from the interface so a server that
		// rotated the assigned IP between sessions surfaces correctly.
		// Cheap call (sysctl-backed on darwin/linux, GetAdaptersAddresses
		// on windows) and only runs on Status() polls (every 2 s) when
		// the tunnel is up.
		if addrs := getInterfaceIPAddresses(ovpnIface); addrs != "" {
			o.localAddr = addrs
			status.LocalAddress = addrs
		}
	}
	return status
}

// getInterfaceIPAddresses returns the IPv4 + IPv6 unicast addresses
// assigned to `ifname`, formatted as a comma-separated CIDR string —
// e.g. "10.66.0.5/24, fd00:42::5/64". Mirrors the WireGuard config-
// file Address line so the UI's local-address renderer can apply the
// same splitAddresses() formatter without protocol-specific branching.
//
// Skips link-local (fe80::/10) and loopback addresses; those are
// noise for the user.
func getInterfaceIPAddresses(ifname string) string {
	iface, err := net.InterfaceByName(ifname)
	if err != nil {
		return ""
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return ""
	}
	var parts []string
	for _, a := range addrs {
		ipNet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		if ipNet.IP.IsLinkLocalUnicast() || ipNet.IP.IsLoopback() {
			continue
		}
		parts = append(parts, ipNet.String())
	}
	return strings.Join(parts, ", ")
}

// findFirstTunlikeInterface scans the system's network interfaces
// and returns the first UP interface whose name contains any of the
// given substrings. Used as the Windows-side iface lookup for the
// IP-address read since the OpenVPN adapter alias varies across
// driver flavours (TAP / Wintun / DCO). Returns "" if no match.
func findFirstTunlikeInterface(needles []string) string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		nameLower := strings.ToLower(iface.Name)
		for _, n := range needles {
			if strings.Contains(nameLower, strings.ToLower(n)) {
				return iface.Name
			}
		}
	}
	return ""
}

// findLinuxTunInterface returns the active OpenVPN tun device on Linux.
// OpenVPN auto-assigns tun0/tun1/…, so the previous hardcoded "tun0" read the
// wrong counters when another tun already existed. Pick the first up tun*
// interface that carries an address; fall back to "tun0". Audit 2026-06-18.
func findLinuxTunInterface() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "tun0"
	}
	for _, ifc := range ifaces {
		if !strings.HasPrefix(ifc.Name, "tun") || ifc.Flags&net.FlagUp == 0 {
			continue
		}
		if addrs, _ := ifc.Addrs(); len(addrs) > 0 {
			return ifc.Name
		}
	}
	return "tun0"
}

// findUtunInterface discovers the utun device assigned to the current
// OpenVPN session by scanning the log for the "Opened utun device utunN"
// line. Result is cached on the OpenVPNProtocol; cleared on Up().
func (o *OpenVPNProtocol) findUtunInterface() string {
	if o.tunIface != "" {
		return o.tunIface
	}
	logPath := o.logPath
	buf, err := os.ReadFile(logPath)
	if err != nil {
		return ""
	}
	// "Opened utun device utun8" — match the LAST occurrence so multi-
	// session log files resolve to the current session.
	content := string(buf)
	const marker = "Opened utun device "
	idx := strings.LastIndex(content, marker)
	if idx < 0 {
		return ""
	}
	rest := content[idx+len(marker):]
	end := strings.IndexAny(rest, " \r\n\t")
	if end < 0 {
		return ""
	}
	o.tunIface = rest[:end]
	return o.tunIface
}

// getDarwinInterfaceStats returns RX/TX byte counters for the named
// network interface by parsing `netstat -ibn`. Returns (0, 0) if the
// interface is not present in the output.
//
// netstat lines on macOS have variable leading columns: the AF_LINK row
// has no Address column (or has a MAC), AF_INET/INET6 rows have an
// Address column that may be one token (10.100.121.7) or two
// (fe80::1%utun8 with prefix). Counting from the START therefore picks
// up the wrong field. We count from the END instead — the trailing
// seven columns are always `Ipkts Ierrs Ibytes Opkts Oerrs Obytes Coll`.
func getDarwinInterfaceStats(ifname string) (int64, int64) {
	out, err := exec.Command("netstat", "-ibn").Output()
	if err != nil {
		return 0, 0
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 7 || fields[0] != ifname {
			continue
		}
		n := len(fields)
		// Trailing layout: ... Ibytes(-5) Opkts(-4) Oerrs(-3) Obytes(-2) Coll(-1)
		rx, errRx := strconv.ParseInt(fields[n-5], 10, 64)
		tx, errTx := strconv.ParseInt(fields[n-2], 10, 64)
		if errRx != nil || errTx != nil {
			continue
		}
		if rx != 0 || tx != 0 {
			return rx, tx
		}
	}
	return 0, 0
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

	if err := EncryptedWriteFile(o.configPath, cfg, 0600); err != nil {
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
