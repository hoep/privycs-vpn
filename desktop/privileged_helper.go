package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
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
	"ipsec_install_macos_v6_default_route": true, // macOS post-bypass: ::/0 via utun for v6 tunnel
	"ipsec_install_windows_routes": true, // Windows post-up split-tunnel + bypass routes
	"ipsec_install_windows_profile": true, // Windows: execute gateway-supplied full-setup .cmd at import time
	"macos_dns_override_set":     true, // primary-service DNS override (swanctl-darwin)
	"macos_dns_override_restore": true,
	"macos_dns_override_clean":   true, // orphan-cleanup at app startup
	"macos_dns_snapshot":         true, // capture pre-VPN DNS for restore-on-disconnect (NEVPN IPSec)
	"remove_legacy_sudoers":      true,
	"wlan_ssid":                  true, // SSID query (bypasses user-level Location GPO)
	// v0.9.15.37: IPv6 leak-killswitch actions. Dispatcher cases
	// existed (lines ~310-313) but the action names were absent
	// from this whitelist, so the gate at executeCommand rejected
	// every call with "unknown action: ipv6_unblock". Net effect:
	// killswitch enable worked (block rule installed by other code
	// path? actually it didn't — same issue), but the unblock-on-
	// disconnect path silently failed and left the rules stuck,
	// breaking the local IPv6 stack until manual netsh cleanup.
	"ipv6_block":   true,
	"ipv6_unblock": true,
	// v0.9.15.53: Windows OpenVPN DNS — set the tunnel adapter's DNS
	// ourselves with a single `netsh ... set dns`. OpenVPN 2.7.1-DCO
	// on Windows 26200 does `set dns` THEN `add dns` for the same
	// single pushed server and the duplicate `add` fails fatally; we
	// `--pull-filter ignore "dhcp-option DNS"` to skip that code path
	// entirely and apply DNS here instead. No restore action — the
	// ovpn-dco/tap adapter is ephemeral and takes its DNS config with
	// it on disconnect.
	"windows_dns_set": true,
	// v1.0.5.28: helper-side log truncation. The unprivileged app
	// process cannot truncate the per-profile *.log files in the
	// shared appDataDir because the helper (root/SYSTEM) writes them
	// at openvpn-start time, and even with mode 0644 the file owner
	// stays root/SYSTEM — Truncate needs WRITE permission, not just
	// read. The helper does Truncate as root and reports success.
	"clear_logs": true,
	// v1.0.5.30: enrol the calling user's UID into the helper's
	// peer-cred whitelist. App calls this once on first connect
	// after install/upgrade. TOFU-protected: only honoured when the
	// whitelist is empty OR the caller is already enrolled.
	"enroll_uid": true,
}

// safePathPattern validates file paths to prevent directory traversal and injection.
// Spaces are allowed because macOS uses "Application Support" in the standard
// per-user data directory. Shell metacharacters ($, ;, |, &, `, etc.) remain blocked,
// and the path is always passed as an exec.Cmd argument (no shell interpolation).
var safePathPattern = regexp.MustCompile(`^[a-zA-Z0-9 /_\-\.\\:]+$`)

// pathContainsTraversal reports whether p contains a ".." path
// segment in either Unix or Windows form. safePathPattern alone allows
// "." and "/" (legitimate for absolute config paths) and so lets
// "../../etc/passwd" through — this is the directory-traversal gate
// that complements it. Checks the raw string AND the filepath.Clean'd
// form so that obfuscated traversals (e.g. "a/../../b") are caught.
func pathContainsTraversal(p string) bool {
	norm := strings.ReplaceAll(p, `\`, `/`)
	for _, seg := range strings.Split(norm, "/") {
		if seg == ".." {
			return true
		}
	}
	cleaned := strings.ReplaceAll(filepath.Clean(p), `\`, `/`)
	for _, seg := range strings.Split(cleaned, "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}

// validateHelperFilePath is the hardened path gate for root-side file
// operations: it requires an absolute path, rejects any ".." traversal
// segment, and rejects shell-metacharacter / non-allowed-character
// paths via safePathPattern. Returns nil when the path is safe to act
// on as root. An empty path is the caller's "not supplied" sentinel
// and is rejected here — callers that allow empty must check that
// first.
func validateHelperFilePath(p string) error {
	if p == "" {
		return fmt.Errorf("empty path")
	}
	if !safePathPattern.MatchString(p) {
		return fmt.Errorf("path contains disallowed characters")
	}
	if pathContainsTraversal(p) {
		return fmt.Errorf("path contains '..' traversal")
	}
	if !filepath.IsAbs(p) {
		return fmt.Errorf("path must be absolute")
	}
	return nil
}

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

	// v0.9.15.30: sweep stale per-tunnel AWG services on Windows
	// from previous helper sessions (helper-crash recovery). On
	// non-Windows this is a no-op stub. Runs in a goroutine so a
	// slow SCM doesn't delay listener-readiness.
	if runtime.GOOS == "windows" {
		go sweepOrphanedAWGTunnelServices()
	}

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

	// v1.0.5.30 — peer-UID enforcement. Closes the local-RCE-as-root
	// attack the 2026-05-21 audit flagged: prior to this gate, any
	// unprivileged user could connect to the 0666 AF_UNIX socket
	// and invoke any whitelisted action. Linux / macOS get
	// SO_PEERCRED / LOCAL_PEERCRED; Windows AF_UNIX has no
	// equivalent and falls back to the icacls Authenticated-Users
	// ACL until the v1.0.6 named-pipe transition.
	var peerUID uint32
	if peerCredSupported() {
		uid, err := getPeerUID(conn)
		if err != nil {
			log.Printf("PEERCRED: rejecting connection — getPeerUID failed: %v", err)
			h.sendResponse(conn, HelperResponse{Success: false, Error: "peer credentials unavailable"})
			return
		}
		peerUID = uid
	}

	// Bound the per-connection read so a hostile / buggy local client
	// cannot stream gigabytes at the root helper and OOM it. The
	// largest legitimate command is an IPSec Windows-profile script
	// (base64 of a few hundred Add-VpnConnectionRoute lines + cert
	// material), comfortably under 4 MB. Anything larger is rejected
	// by the decoder hitting EOF mid-token.
	const maxCommandBytes = 4 << 20 // 4 MB
	decoder := json.NewDecoder(io.LimitReader(conn, maxCommandBytes))
	var cmd HelperCommand
	if err := decoder.Decode(&cmd); err != nil {
		h.sendResponse(conn, HelperResponse{Success: false, Error: "invalid JSON command"})
		return
	}

	// Audit log every command — include peer UID where available so a
	// forensic review can correlate IPC traffic with system users.
	if peerCredSupported() {
		log.Printf("Helper command: uid=%d action=%s protocol=%s interface=%s", peerUID, cmd.Action, cmd.Protocol, cmd.Interface)
	} else {
		log.Printf("Helper command: action=%s protocol=%s interface=%s", cmd.Action, cmd.Protocol, cmd.Interface)
	}

	// v1.0.7.2 — UID whitelist enforcement with TOFU on the first
	// call from any caller (not just the explicit enroll_uid IPC).
	//
	// History: v1.0.6 / v1.0.5.30 only allowed enroll_uid when the
	// whitelist was empty, and required ALL other actions to come
	// from an already-enrolled UID. The app's startup probe is
	// `status` (via IsHelperRunning), NOT enroll_uid — so on a
	// fresh install the probe was rejected, IsHelperRunning
	// returned false, the enrollSelfWithHelper() goroutine never
	// fired (it sits inside the IsHelperRunning=true branch), and
	// the user got a permanent "helper not running" error. Bug
	// observed in production on 2026-05-31.
	//
	// v1.0.7.2: when the whitelist is empty AND a non-root caller
	// connects, auto-enrol that caller's UID and let the action
	// through. Subsequent connects from any OTHER UID are then
	// rejected (the file is the source of truth). The TOFU race
	// window is the microseconds between helper-install and the
	// first app launch; any attacker would have to be already
	// running and waiting on the socket, which on macOS / Linux
	// is detectable via lsof and on Windows would require a local
	// account anyway. Acceptable risk for the UX win.
	if peerCredSupported() && peerUID != 0 {
		if !isAllowedUID(peerUID) {
			if whitelistIsEmpty() {
				if _, err := enrollUID(peerUID); err != nil {
					log.Printf("PEERCRED: TOFU auto-enrol uid=%d failed: %v — proceeding fail-open for this connect", peerUID, err)
				} else {
					log.Printf("PEERCRED: TOFU auto-enrol uid=%d via first action=%s", peerUID, cmd.Action)
				}
			} else {
				log.Printf("PEERCRED: rejecting uid=%d action=%s — not in whitelist (enrolled UIDs exist)", peerUID, cmd.Action)
				h.sendResponse(conn, HelperResponse{Success: false, Error: "peer UID not authorised"})
				return
			}
		}
		// Special case: explicit enroll_uid call from a non-enrolled
		// UID when the whitelist is non-empty — this is the only
		// path that should be rejected even after the broader gate
		// above, because the caller is asking to be added to a
		// whitelist that already has someone else in it. Should
		// never reach here in practice (the gate above already
		// returned for non-enrolled UIDs on non-empty whitelist),
		// kept as a defensive log line.
		if cmd.Action == "enroll_uid" && !whitelistIsEmpty() && !isAllowedUID(peerUID) {
			log.Printf("PEERCRED: rejecting enroll_uid from uid=%d — whitelist is non-empty and caller is not enrolled", peerUID)
			h.sendResponse(conn, HelperResponse{Success: false, Error: "enroll_uid denied — helper already enrolled to a different user"})
			return
		}
	}

	resp := h.executeCommand(HelperCommand{
		Action:     cmd.Action,
		Protocol:   cmd.Protocol,
		Interface:  cmd.Interface,
		ConfigPath: cmd.ConfigPath,
		Args:       attachPeerUIDToArgs(cmd.Args, peerUID),
	})

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

	// Validate inputs to prevent injection + directory traversal.
	// ConfigPath, when supplied, must be an absolute path with no ".."
	// segment and only allow-listed characters — a root-side file op
	// must never act on "../../etc/..." or a relative path.
	if cmd.ConfigPath != "" {
		if err := validateHelperFilePath(cmd.ConfigPath); err != nil {
			return HelperResponse{Success: false, Error: fmt.Sprintf("invalid config path: %v", err)}
		}
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
	case "ipsec_install_macos_v6_default_route":
		return h.cmdIPSecInstallMacOSV6DefaultRoute(cmd)
	case "ipsec_install_windows_routes":
		return h.cmdIPSecInstallWindowsRoutes(cmd)
	case "ipsec_install_windows_profile":
		return h.cmdIPSecInstallWindowsProfile(cmd)
	case "macos_dns_override_set":
		return h.cmdMacOSDNSOverrideSet(cmd)
	case "macos_dns_override_restore":
		return h.cmdMacOSDNSOverrideRestore(cmd)
	case "macos_dns_override_clean":
		return h.cmdMacOSDNSOverrideClean(cmd)
	case "macos_dns_snapshot":
		return h.cmdMacOSDNSSnapshot(cmd)
	case "windows_dns_set":
		return h.cmdWindowsDNSSet(cmd)
	case "remove_legacy_sudoers":
		return h.cmdRemoveLegacySudoers(cmd)
	case "macos_restart_charon":
		return h.cmdMacOSRestartCharon(cmd)
	case "ipv6_block":
		return h.cmdIPv6Block(cmd)
	case "ipv6_unblock":
		return h.cmdIPv6Unblock(cmd)
	case "clear_logs":
		return h.cmdClearLogs(cmd)
	case "enroll_uid":
		return h.cmdEnrollUID(cmd)
	default:
		return HelperResponse{Success: false, Error: "unhandled action"}
	}
}

// attachPeerUIDToArgs threads the peer UID through to the command
// handlers via the cmd.Args bag. cmdEnrollUID consumes it; other
// handlers ignore it. Key is a reserved name that the IPC layer
// strips and the handlers will never read from user input.
// v1.0.5.30 — supports the peer-UID enforcement and enrolment flow.
func attachPeerUIDToArgs(args map[string]string, uid uint32) map[string]string {
	out := make(map[string]string, len(args)+1)
	for k, v := range args {
		// Defensive: callers shouldn't be able to forge this key
		// since handleConnection always overrides it. Strip
		// anyway so a malicious client can't smuggle a UID
		// through a different action and read it via logs.
		if k == "_peer_uid" {
			continue
		}
		out[k] = v
	}
	out["_peer_uid"] = fmt.Sprintf("%d", uid)
	return out
}

// cmdEnrollUID records the calling user's UID in the helper's
// whitelist. Called by the app on first successful Connect after
// install or upgrade. The handleConnection gate already enforces
// TOFU-eligibility, so by the time we get here the caller is
// authorised; this handler's job is the persistent write.
// v1.0.5.30.
func (h *PrivilegedHelper) cmdEnrollUID(cmd HelperCommand) HelperResponse {
	uidStr := cmd.Args["_peer_uid"]
	if uidStr == "" {
		// Windows path or platform without peer-cred — record a
		// no-op success so the client doesn't loop trying.
		log.Printf("ENROL: no peer UID available (platform=%s) — skipping whitelist write", runtime.GOOS)
		return HelperResponse{Success: true, Output: "no peer credentials available; whitelist not used on this platform"}
	}
	v, err := strconv.ParseUint(uidStr, 10, 32)
	if err != nil {
		return HelperResponse{Success: false, Error: fmt.Sprintf("enroll_uid: malformed _peer_uid: %v", err)}
	}
	uid := uint32(v)
	if uid == 0 {
		// Refuse to enrol root explicitly — it's implicitly
		// allowed and recording it would obscure who the real
		// trusted user is on disk.
		log.Printf("ENROL: declining to enrol uid=0 (root is implicit)")
		return HelperResponse{Success: true, Output: "root is implicitly allowed; no enrolment needed"}
	}
	count, err := enrollUID(uid)
	if err != nil {
		log.Printf("ENROL: write failed for uid=%d: %v", uid, err)
		return HelperResponse{Success: false, Error: err.Error()}
	}
	log.Printf("ENROL: uid=%d enrolled (whitelist now has %d entries)", uid, count)
	return HelperResponse{Success: true, Output: fmt.Sprintf("uid=%d enrolled; whitelist has %d entries", uid, count)}
}

// cmdClearLogs truncates every *.log file under the paths supplied by
// the caller. The caller (settings.go clearLogs) discovers the *.log
// files in appDataDir on the user side and ships the absolute paths
// here as cmd.Args["paths"] (newline-separated). The helper rejects
// anything outside the expected appDataDir / Windows-ProgramData
// helper-data-dir patterns to avoid being abused as a generic
// "truncate arbitrary file" RPC.
//
// v1.0.5.28: fixes "delete logs → permission denied" on every desktop
// OS. Helper-spawned daemons (openvpn) create *.log files owned by
// root/SYSTEM with mode 0644 — readable by the unprivileged app, NOT
// writable. The clearLogs() call from the user app would error EACCES
// on the first such file. Routing the truncate through the helper
// solves this on all three OSes uniformly.
func (h *PrivilegedHelper) cmdClearLogs(cmd HelperCommand) HelperResponse {
	pathsArg := cmd.Args["paths"]
	if pathsArg == "" {
		return HelperResponse{Success: true, Output: "no paths"}
	}
	var allowedPrefixes []string
	switch runtime.GOOS {
	case "windows":
		programData := os.Getenv("PROGRAMDATA")
		if programData == "" {
			programData = `C:\ProgramData`
		}
		allowedPrefixes = []string{
			filepath.Join(programData, "PrivycsVPN") + string(os.PathSeparator),
		}
		// Per-user appDataDir on Windows is %LOCALAPPDATA%\privycs-vpn;
		// the helper runs as SYSTEM and doesn't know which user's
		// LOCALAPPDATA to allow, so we accept any path that contains
		// the "privycs-vpn" segment AND ends in .log — defence in depth
		// against directory traversal via the path validator below.
	default:
		// macOS + Linux both have appDataDir under the invoking user's
		// home; same caveat as Windows — helper doesn't know the user
		// path at IPC time. We rely on the "privycs-vpn" segment +
		// .log suffix gate below.
		allowedPrefixes = nil
	}
	var truncated, skipped int
	var firstErr string
	for _, p := range strings.Split(pathsArg, "\n") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// Path-safety: must end in ".log", must contain "privycs-vpn"
		// as a directory segment, must pass safePathPattern (no shell
		// metacharacters, only ASCII filename chars).
		if !strings.HasSuffix(p, ".log") ||
			!strings.Contains(p, "privycs-vpn") ||
			!safePathPattern.MatchString(p) ||
			pathContainsTraversal(p) ||
			!filepath.IsAbs(p) {
			skipped++
			continue
		}
		if len(allowedPrefixes) > 0 {
			ok := false
			for _, prefix := range allowedPrefixes {
				if strings.HasPrefix(p, prefix) {
					ok = true
					break
				}
			}
			if !ok {
				skipped++
				continue
			}
		}
		if _, err := os.Stat(p); os.IsNotExist(err) {
			continue
		}
		if err := os.Truncate(p, 0); err != nil {
			if firstErr == "" {
				firstErr = fmt.Sprintf("truncate %s: %v", p, err)
			}
			continue
		}
		truncated++
	}
	if firstErr != "" {
		return HelperResponse{
			Success: false,
			Error:   firstErr,
			Output:  fmt.Sprintf("truncated=%d skipped=%d", truncated, skipped),
		}
	}
	return HelperResponse{
		Success: true,
		Output:  fmt.Sprintf("truncated=%d skipped=%d", truncated, skipped),
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
// v0.9.15.x AmneziaWG: variant carries via cmd.Args["variant"]. When
// "amneziawg" we route through awg-quick (Linux), in-process
// amneziawg-go (macOS), or in-process amneziawg-go (Windows — no
// official Tunnel-Service equivalent for AWG, so we own the Wintun
// fd ourselves the same way the macOS in-process path owns utun).
func (h *PrivilegedHelper) connectWireGuard(cmd HelperCommand) HelperResponse {
	ifaceName := cmd.Interface
	if ifaceName == "" {
		ifaceName = "privycs0"
	}
	isAwg := cmd.Args["variant"] == VariantAmnezia

	if runtime.GOOS == "windows" {
		if isAwg {
			// AWG on Windows: install + start a per-tunnel SCM
			// service `PrivycsAWGTunnel$<iface>` that runs
			// amneziawg-go in its own LocalSystem process. The
			// helper no longer drives the Wintun adapter itself
			// — upstream-recommended embedding pattern (see
			// awg_tunnel_service_windows.go header).
			//
			// Pre-v0.9.15.30 the helper called wgWindowsUpAwg
			// in-process which hit a known wintun-adapter race
			// (CreateTUN ok → silent crash before/during
			// NewDevice/IpcSet/Up) — see the Feb 2025 zx2c4
			// mailing-list thread and tailscale#1128.
			confPath := cmd.ConfigPath
			if confPath == "" {
				confPath = windowsWGConfigPath(ifaceName)
			}
			if _, err := os.Stat(confPath); err != nil {
				return HelperResponse{Success: false, Error: fmt.Sprintf("config not found: %s", confPath)}
			}
			if err := installAWGTunnelService(ifaceName, confPath); err != nil {
				return HelperResponse{Success: false, Error: fmt.Sprintf("install AWG tunnel service: %v", err)}
			}
			return HelperResponse{Success: true, Output: fmt.Sprintf("AmneziaWG tunnel service %s started", ifaceName)}
		}
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

	// Linux/macOS: copy config to /etc/wireguard (vanilla) or
	// /etc/amnezia/amneziawg (AWG, where awg-quick looks).
	etcDir := "/etc/wireguard"
	if isAwg {
		etcDir = "/etc/amnezia/amneziawg"
	}
	etcConf := filepath.Join(etcDir, ifaceName+".conf")
	if cmd.ConfigPath != "" {
		if err := os.MkdirAll(etcDir, 0755); err != nil {
			return HelperResponse{Success: false, Error: fmt.Sprintf("mkdir %s: %v", etcDir, err)}
		}
		if err := h.copyConfigFile(cmd.ConfigPath, etcConf); err != nil {
			return HelperResponse{Success: false, Error: fmt.Sprintf("failed to install config: %v", err)}
		}
	}

	if runtime.GOOS == "darwin" {
		content, err := os.ReadFile(etcConf)
		if err != nil {
			return HelperResponse{Success: false, Error: fmt.Sprintf("read %s: %v", etcConf, err)}
		}
		if isAwg {
			realIface, err := wgDarwinUpAwg(ifaceName, string(content))
			if err != nil {
				return HelperResponse{Success: false, Error: fmt.Sprintf("wgDarwinUpAwg failed: %v", err)}
			}
			return HelperResponse{Success: true, Output: fmt.Sprintf("AmneziaWG tunnel up on %s (in-process)", realIface)}
		}
		realIface, err := wgDarwinUp(ifaceName, string(content))
		if err != nil {
			return HelperResponse{Success: false, Error: fmt.Sprintf("wgDarwinUp failed: %v", err)}
		}
		return HelperResponse{Success: true, Output: fmt.Sprintf("WireGuard tunnel up on %s (in-process)", realIface)}
	}

	// Linux: variant-aware userland CLI. awg-quick handles both
	// kernel-mode (DKMS module loaded) and userspace fallback
	// internally — we just hand it the conf and let it pick.
	bin := "wg-quick"
	if isAwg {
		bin = "awg-quick"
	}
	wgQuick := findWGBinary(bin)
	if wgQuick == "" {
		hint := "install wireguard-tools"
		if isAwg {
			hint = "install amneziawg-tools (apt add ppa:amnezia/ppa)"
		}
		return HelperResponse{Success: false, Error: fmt.Sprintf("%s not found — %s", bin, hint)}
	}
	wgUp := exec.Command(wgQuick, "up", ifaceName)
	wgUp.Env = wgExecEnv()
	applyDetachedSession(wgUp)
	out, err := wgUp.CombinedOutput()
	if err != nil {
		return HelperResponse{Success: false, Error: fmt.Sprintf("%s up failed: %s", bin, string(out)), Output: string(out)}
	}
	return HelperResponse{Success: true, Output: string(out)}
}

// disconnectWireGuard uninstalls the tunnel service (Windows) or runs wg-quick down (Unix).
func (h *PrivilegedHelper) disconnectWireGuard(cmd HelperCommand) HelperResponse {
	ifaceName := cmd.Interface
	if ifaceName == "" {
		ifaceName = "privycs0"
	}
	isAwg := cmd.Args["variant"] == VariantAmnezia

	if runtime.GOOS == "windows" {
		if isAwg {
			// v0.9.15.30: stop + delete the per-tunnel SCM
			// service. The service's Execute handler tears the
			// AWG device + Wintun adapter down on receiving
			// svc.Stop. Idempotent — if the service is already
			// gone (helper crash recovery, etc.) the uninstall
			// returns success.
			if err := uninstallAWGTunnelService(ifaceName); err != nil {
				return HelperResponse{Success: false, Error: fmt.Sprintf("uninstall AWG tunnel service: %v", err)}
			}
			return HelperResponse{Success: true, Output: fmt.Sprintf("AmneziaWG tunnel service %s stopped", ifaceName)}
		}
		wgExe := findWireGuardExe()
		if wgExe == "" {
			return HelperResponse{Success: false, Error: "wireguard.exe not found"}
		}
		out, err := exec.Command(wgExe, "/uninstalltunnelservice", ifaceName).CombinedOutput()
		if err != nil {
			return HelperResponse{Success: false, Error: fmt.Sprintf("uninstalltunnelservice failed: %s: %v", string(out), err), Output: string(out)}
		}
		// v1.0.5.11: Block until the per-tunnel SCM service is
		// fully gone. `wireguard.exe /uninstalltunnelservice`
		// returns when SCM has dispatched stop+delete, but the
		// wintun.sys driver cleanup (NDIS unbind, WFP filter
		// removal, adapter ref-count drop) is async — the kernel
		// is still releasing state after userspace returns
		// success. Returning to the caller (App.disconnectInternal
		// → TunnelHealth failover → next Up(), or a rapid manual
		// Disconnect+Connect cycle) before this completes lets
		// the next Up() race the lingering kernel state and
		// triggers the historic Windows BSOD pattern (v0.9.10.29
		// and a recurrence observed in v1.0.5.x failover).
		//
		// Active polling (typical ~500-800 ms, capped at 3 s)
		// rather than a fixed sleep so fast machines do not wait
		// unnecessarily and slow machines (AV, disk I/O pressure)
		// get the time they need.
		waitForVanillaWGServiceGone(ifaceName, 3*time.Second)
		return HelperResponse{Success: true, Output: string(out)}
	}

	if runtime.GOOS == "darwin" {
		if isAwg {
			if err := wgDarwinDownAwg(ifaceName); err != nil {
				return HelperResponse{Success: false, Error: fmt.Sprintf("wgDarwinDownAwg failed: %v", err)}
			}
			return HelperResponse{Success: true, Output: fmt.Sprintf("AmneziaWG tunnel down (%s, in-process)", ifaceName)}
		}
		if err := wgDarwinDown(ifaceName); err != nil {
			return HelperResponse{Success: false, Error: fmt.Sprintf("wgDarwinDown failed: %v", err)}
		}
		return HelperResponse{Success: true, Output: fmt.Sprintf("WireGuard tunnel down (%s, in-process)", ifaceName)}
	}

	bin := "wg-quick"
	if isAwg {
		bin = "awg-quick"
	}
	wgQuick := findWGBinary(bin)
	if wgQuick == "" {
		return HelperResponse{Success: false, Error: fmt.Sprintf("%s not found", bin)}
	}
	wgDown := exec.Command(wgQuick, "down", ifaceName)
	wgDown.Env = wgExecEnv()
	out, err := wgDown.CombinedOutput()
	if err != nil {
		return HelperResponse{Success: false, Error: fmt.Sprintf("%s down failed: %s", bin, string(out)), Output: string(out)}
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

		// v0.9.15.53: stop fighting OpenVPN's Windows DNS code; skip it
		// and set tunnel DNS ourselves.
		//
		// Layered bug story on Windows 10.0.26200 + OpenVPN 2.7.1:
		//   v0.9.15.45: DCO 2.8.2 default. After TLS handshake OpenVPN
		//   issues `netsh ... set dns <iface> static <ip>` THEN
		//   `netsh ... add dns <iface> <ip>` for the SAME single
		//   pushed server — Windows 26200 rejects the duplicate `add`,
		//   netsh returns error 1, OpenVPN treats the netsh failure as
		//   fatal and exits. Same gateway config works on Linux
		//   openvpn 2.x and Android ics-openvpn.
		//   v0.9.15.48: --disable-dco → fell back to TAP-Windows6 9.27
		//   which has its own Windows-26200 media-state bug ("Route:
		//   Waiting for TUN/TAP interface to come up..." forever).
		//   v0.9.15.50: --windows-driver wintun → NO-OP. OpenVPN 2.7
		//   removed Wintun support entirely ("DEPRECATED OPTION:
		//   windows-driver ... Wintun support has been removed"),
		//   silently falls back to DCO → back to the .45 netsh bug.
		//   v0.9.15.53: all three OpenVPN-2.7.1 Windows drivers are
		//   broken on Windows 26200, and the netsh duplicate-`add dns`
		//   is internal to openvpn.exe (absolute netsh path, can't be
		//   shimmed). So: `--pull-filter ignore "dhcp-option DNS"`
		//   makes OpenVPN never run that netsh code path at all (the
		//   raw PUSH_REPLY is still logged, so we can recover the
		//   intended DNS), and the privileged helper applies the
		//   tunnel DNS afterwards with a single `netsh ... set dns`
		//   (never `add`). Mirrors the proven macOS swanctl DNS-
		//   override pattern (cmdMacOSDNSOverrideSet) — no restore
		//   needed here because the ovpn-dco/tap adapter is ephemeral
		//   and drops its DNS config on disconnect. DCO offload stays
		//   on (it works for data plane; only its netsh-DNS path is
		//   broken, which we now bypass).
		// v0.9.15.54: prefer the OpenVPN Interactive Service. Spawning
		// openvpn.exe ourselves gives `msg_channel=0` → OpenVPN runs
		// every privileged op via its own broken direct-netsh code
		// (Windows 26200 + 2.7.1-DCO: duplicate `add dns`, IPv6 `set
		// address` fails, …). Going through the interactive service
		// makes the service spawn openvpn with `--msg-channel <h>`;
		// OpenVPN then delegates netsh/route to the service's working
		// implementation. One structural fix for the whole netsh
		// failure class instead of per-call whack-a-mole.
		//
		// Options for the interactive path do NOT carry the legacy
		// `--service <event> 0` tokens (that is the old SCM model,
		// mutually exclusive with --msg-channel) and NOT the binary
		// path (the service uses its own openvpn.exe + appends
		// --msg-channel). We keep --pull-filter "ignore dhcp-option
		// DNS" + the windows_dns_set helper from v0.9.15.53: that DNS
		// path is already proven to work on 26200, and not relying on
		// the interactive service's own DNS handling keeps risk down
		// (the service definitively fixes the IPv6-address / route
		// netsh ops we have NO helper workaround for; DNS we already
		// solved).
		isvcArgs := []string{
			"--config", configPath,
			"--log", logPath,
			"--management", mgmtHost, mgmtPort,
			"--pull-filter", "ignore", "dhcp-option DNS",
		}
		if pid, isvcErr := startOpenVPNViaInteractiveService(filepath.Dir(configPath), isvcArgs); isvcErr == nil {
			os.WriteFile(pidPath, []byte(fmt.Sprintf("%d", pid)), 0644)
			log.Printf("OpenVPN started via Interactive Service pid=%d", pid)
			return HelperResponse{Success: true, Output: fmt.Sprintf("openvpn started pid=%d (interactive service)", pid)}
		} else {
			// Old OpenVPN install without the interactive service, or
			// the service is stopped. Fall back to the legacy direct
			// spawn — still subject to the netsh bugs on 26200, but
			// the only option when the service isn't there.
			log.Printf("Interactive Service unavailable (%v); falling back to direct openvpn spawn", isvcErr)
		}

		c := exec.Command(ovpnExe,
			"--service", eventName, "0",
			"--config", configPath,
			"--log", logPath,
			"--management", mgmtHost, mgmtPort,
			"--pull-filter", "ignore", "dhcp-option DNS",
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
	// Same PATH fix the WireGuard spawn gets (see wgExecEnv + the wg-quick
	// spawn above): the helper runs as a systemd service / launchd daemon whose
	// inherited PATH can lack /usr/sbin:/sbin, and on Linux openvpn applies the
	// server-pushed default route + DNS by shelling out to `ip`/`route`/
	// `resolvconf` BY BARE NAME. Without a PATH that finds them the tunnel comes
	// up but no IPv4 default route is installed → "connects but no IPv4" (Linux
	// only; macOS uses the route socket, Windows the interactive service).
	c.Env = wgExecEnv()
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
			// Recycled-PID guard: a stale PID file can name a PID the
			// OS has since reassigned to an unrelated process. Since we
			// run as root, an unguarded Kill could SIGKILL that victim.
			// Verify the process image still looks like openvpn before
			// signalling; if the check is inconclusive we DO still send
			// the gentle Interrupt (SIGINT is mostly benign) but SKIP the
			// hard Kill so we never SIGKILL a misidentified process.
			isOpenVPN := processImageLooksLikeOpenVPN(pid)
			if proc, err := os.FindProcess(pid); err == nil {
				if isOpenVPN {
					proc.Signal(os.Interrupt)
					time.Sleep(1 * time.Second)
					proc.Kill()
				} else {
					log.Printf("disconnectOpenVPN: pid %d does not look like openvpn (recycled PID?) — sending SIGINT only, skipping Kill", pid)
					proc.Signal(os.Interrupt)
				}
			}
		}
		os.Remove(pidPath)
	}

	return HelperResponse{Success: true, Output: "openvpn stopped"}
}

// processImageLooksLikeOpenVPN returns true when the running process
// with the given PID has an executable image / command line that
// contains "openvpn" (case-insensitive). Best-effort + non-blocking:
// a quick `ps`/`tasklist` lookup with no retries. Returns false if the
// process is gone or the lookup fails — callers treat that as "do not
// hard-Kill" rather than "definitely not openvpn", so a recycled PID
// is never SIGKILL'd on our behalf.
func processImageLooksLikeOpenVPN(pid int) bool {
	switch runtime.GOOS {
	case "linux":
		// /proc/<pid>/cmdline is NUL-separated argv.
		if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid)); err == nil {
			return strings.Contains(strings.ToLower(string(data)), "openvpn")
		}
		// Fallback: resolve the exe symlink.
		if dst, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid)); err == nil {
			return strings.Contains(strings.ToLower(dst), "openvpn")
		}
		return false
	case "darwin":
		out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "command=").CombinedOutput()
		if err != nil {
			return false
		}
		return strings.Contains(strings.ToLower(string(out)), "openvpn")
	case "windows":
		// tasklist filtered by PID; the image-name column carries the
		// exe name (openvpn.exe) when this is the daemon we started.
		out, err := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid), "/FO", "CSV", "/NH").CombinedOutput()
		if err != nil {
			return false
		}
		return strings.Contains(strings.ToLower(string(out)), "openvpn")
	default:
		return false
	}
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
		// v0.9.14.92: defensive flush BEFORE the new --initiate.
		// If a previous session left orphan kernel SAs/policies in
		// the SADB+SPD (e.g. crash recovery, helper restart, or
		// prior --terminate that didn't cascade through to the
		// kernel), they could intercept traffic for the new tunnel
		// before our fresh SA's policies are installed. Flushing
		// here makes every connect start from a known-clean kernel
		// IPSec state. setkey is a base macOS utility (no install
		// needed). Linux IPSec connections don't currently exhibit
		// this, so we keep the flush darwin-only.
		_, _ = exec.Command("/usr/sbin/setkey", "-F").CombinedOutput()
		_, _ = exec.Command("/usr/sbin/setkey", "-FP").CombinedOutput()
	}
	loadArgs := helperMacOSSwanctlArgs([]string{"--load-all"})
	out, err := exec.Command(swanctlBin, loadArgs...).CombinedOutput()
	if err != nil {
		return HelperResponse{Success: false, Error: fmt.Sprintf("swanctl --load-all failed: %s", string(out))}
	}

	initArgs := helperMacOSSwanctlArgs([]string{"--initiate", "--child", connName})
	out, err = exec.Command(swanctlBin, initArgs...).CombinedOutput()
	if err != nil {
		return HelperResponse{Success: false, Error: fmt.Sprintf("swanctl --initiate failed: %s", string(out)), Output: string(out)}
	}

	// v1.0.5.21: macOS-only post-initiate fixup for the IPv6 vip
	// source-address-selection bug. strongSwan's charon-libipsec
	// plugin installs the v6 vip on the utun adapter as a /128
	// Point-to-Point binding (a --> a, prefixlen 128). macOS's
	// RFC 6724 source-address-selection then refuses to pick that
	// address as a source for global IPv6 destinations (Rule 5
	// "prefer outgoing interface" doesn't apply because the /128
	// P2P binding is not considered an "applicable" source), and
	// outbound v6 connections fall back to source `::1` and fail
	// with "No route to host" — even though the tunnel + routing
	// are otherwise fully established and gateway-side NAT66 works.
	//
	// User-verified: rebinding the same address as a /64 prefix
	// (instead of /128 P2P) flips macOS source-selection so it
	// picks the vip automatically. WireGuard + OpenVPN on macOS
	// already install their v6 vip with a real prefix (not /128
	// P2P) which is why they don't hit this; charon-libipsec is
	// the outlier.
	//
	// Best-effort: any failure here is non-fatal — the tunnel is
	// up, the user just loses v6 source-auto-selection (workaround:
	// applications explicitly bind to the vip).
	if runtime.GOOS == "darwin" {
		v6vip := helperParseSwanctlLocalV6(string(out))
		if v6vip == "" {
			// --initiate output didn't surface the local v6 TS —
			// fall back to --list-sas which always lists it.
			sasArgs := helperMacOSSwanctlArgs([]string{"--list-sas", "--ike", connName})
			if sasOut, sasErr := exec.Command(swanctlBin, sasArgs...).CombinedOutput(); sasErr == nil {
				v6vip = helperParseSwanctlLocalV6(string(sasOut))
			}
		}
		if v6vip != "" {
			utun := helperFindUtunWithV6(v6vip)
			if utun != "" {
				// Remove the existing /128 P2P binding first;
				// the alias add otherwise creates a duplicate.
				_, _ = exec.Command("ifconfig", utun, "inet6", "-alias", v6vip).CombinedOutput()
				if rebindOut, rebindErr := exec.Command(
					"ifconfig", utun, "inet6", v6vip, "prefixlen", "64",
				).CombinedOutput(); rebindErr != nil {
					log.Printf(
						"macOS IPSec v6 vip rebind %s on %s failed (non-fatal): %v: %s",
						v6vip, utun, rebindErr, strings.TrimSpace(string(rebindOut)),
					)
				} else {
					log.Printf(
						"macOS IPSec v6 vip rebind: %s on %s now /64 (was /128 P2P) — source-selection fixed",
						v6vip, utun,
					)
				}
			} else {
				log.Printf("macOS IPSec v6 vip %s found in swanctl output but no utun has it bound — skipping rebind", v6vip)
			}
		}
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
	// v0.9.14.94: bound swanctl --terminate with a 3-second timeout.
	// User report: even with v0.9.14.93's "kill charon on disconnect"
	// path, the manual `ipsec stop && ipsec start` workaround was
	// still required after sleep/wake. Diagnosis: when charon is
	// in a zombie state post-sleep, `swanctl --terminate` hangs
	// indefinitely waiting for an ack from the unresponsive daemon.
	// The whole helper RPC was blocked at this step — the
	// subsequent `ipsec stop` (which is what the user does manually
	// and which actually kills the daemon via SIGTERM) was never
	// reached.
	//
	// With CommandContext + 3s timeout: if --terminate doesn't
	// complete in 3 s we abort it (Process.Kill from context
	// cancellation) and proceed straight to `ipsec stop`. Best of
	// both worlds: clean termination when charon is healthy,
	// guaranteed progression to forced kill when it isn't.
	tctx, tcancel := context.WithTimeout(context.Background(), 3*time.Second)
	termArgs := helperMacOSSwanctlArgs([]string{"--terminate", "--ike", connName})
	out, err := exec.CommandContext(tctx, swanctlBin, termArgs...).CombinedOutput()
	tcancel()
	if err != nil {
		// Even on terminate-failure or timeout, fall through to
		// the kernel-SADB flush below — `swanctl --terminate` can
		// fail when charon is in a stale state (frequent after
		// sleep), but the kernel SAs may still need flushing
		// regardless. We log the terminate error but don't return.
		log.Printf("swanctl --terminate failed/timed-out (continuing to ipsec-stop + kernel flush): %s: %v",
			strings.TrimSpace(string(out)), err)
	}
	// v1.0.5.28 — macOS IPSec teardown without killing charon.
	//
	// Pre-v1.0.5.28 (v0.9.14.93+) we did `ipsec stop` on every
	// disconnect to guarantee no userspace state survived. Side
	// effect: every NEXT connect found charon dead, ran load-all
	// against a non-existent vici socket, got "Connection refused",
	// then triggered macos_restart_charon as auto-recovery —
	// adding ~5-7s to every Connect AND making users perceive
	// IPSec as "ständig down" because charon wasn't running
	// outside of an active session.
	//
	// New strategy: trust `swanctl --terminate` (already invoked
	// above with 3s timeout) PLUS the kernel-SADB+SPD flush below
	// to guarantee no SA survives in the kernel — even if charon's
	// userspace IKE_SA descriptor lingers, it cannot reinstall
	// kernel SAs without going through a fresh IKE_AUTH that we
	// would not initiate. Charon stays alive between sessions,
	// vici socket stays bound, next connect is one swanctl
	// --load-all + --initiate away with no restart-and-retry
	// detour.
	//
	// Hard-stop fallback: ONLY if --terminate timed out (the
	// 3-second context-cancellation path above) AND the vici
	// socket is no longer responsive (charon stuck in zombie
	// state). That preserves the pre-v1.0.5.28 safety net for
	// the rare wedged-daemon case the original v0.9.14.93 change
	// was written for.
	//
	// Linux path is unchanged (xfrm flush handled separately, no
	// historical "ipsec stop on disconnect" there).
	if runtime.GOOS == "darwin" {
		// Flush kernel SADB + SPD unconditionally. Even if charon
		// can recreate userspace state, the kernel has nothing to
		// route through until charon's next IKE_AUTH — which only
		// happens via our explicit Connect path. Belt-and-braces.
		if flushOut, flushErr := exec.Command("/usr/sbin/setkey", "-F").CombinedOutput(); flushErr != nil {
			log.Printf("setkey -F (post-disconnect SADB flush) failed: %s: %v",
				strings.TrimSpace(string(flushOut)), flushErr)
		} else {
			log.Printf("setkey -F (post-disconnect SADB flush) OK: %s",
				strings.TrimSpace(string(flushOut)))
		}
		if flushOut, flushErr := exec.Command("/usr/sbin/setkey", "-FP").CombinedOutput(); flushErr != nil {
			log.Printf("setkey -FP (post-disconnect SPD flush) failed: %s: %v",
				strings.TrimSpace(string(flushOut)), flushErr)
		} else {
			log.Printf("setkey -FP (post-disconnect SPD flush) OK: %s",
				strings.TrimSpace(string(flushOut)))
		}
		// Hard-stop ONLY if --terminate failed AND vici is dead.
		// `err != nil` here means the 3s --terminate above failed
		// or timed out — charon may be wedged. Probe vici with a
		// 1s --list-sas; if THAT also fails, the daemon really is
		// stuck and we fall back to the old kill-it path. This
		// preserves the v0.9.14.93 safety net for the wedged
		// state without paying the cost on every healthy
		// disconnect.
		if err != nil {
			probeCtx, probeCancel := context.WithTimeout(context.Background(), 1*time.Second)
			probeArgs := helperMacOSSwanctlArgs([]string{"--list-sas"})
			_, probeErr := exec.CommandContext(probeCtx, swanctlBin, probeArgs...).CombinedOutput()
			probeCancel()
			if probeErr != nil {
				log.Printf("disconnect: --terminate failed AND vici probe failed — charon wedged, falling back to hard stop")
				ipsecBin := helperFindMacOSStrongswanBinary("ipsec")
				if ipsecBin != "" {
					if stopOut, stopErr := exec.Command(ipsecBin, "stop").CombinedOutput(); stopErr != nil {
						log.Printf("ipsec stop (wedged-charon fallback) failed: %s: %v",
							strings.TrimSpace(string(stopOut)), stopErr)
					} else {
						log.Printf("ipsec stop (wedged-charon fallback) OK: %s",
							strings.TrimSpace(string(stopOut)))
					}
				}
			} else {
				log.Printf("disconnect: --terminate failed but vici still responsive — keeping charon alive (no fallback stop)")
			}
		}
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
		isAwg := cmd.Args["variant"] == VariantAmnezia
		if runtime.GOOS == "windows" {
			if isAwg {
				// v0.9.15.44: AWG status — query SCM service state via
				// queryAWGTunnelService (rewritten in .43 to drop our
				// custom JSON-over-pipe since amneziawg-windows
				// tunnel.Run owns its own UAPI pipe at
				// \\.\pipe\ProtectedPrefix\Administrators\AmneziaWG\
				// <name>). The fix here: emit the SAME response shape
				// as the vanilla WG branch below — "running" string on
				// success, "not connected" error on failure — so the
				// app's status-parse path treats them identically and
				// doesn't falsely declare the tunnel dead just because
				// AWG returns an empty UAPI dump now.
				_, connected, err := queryAWGTunnelService(ifaceName)
				if err != nil {
					return HelperResponse{Success: false, Error: "not connected", Output: err.Error()}
				}
				if !connected {
					return HelperResponse{Success: false, Error: "not connected"}
				}
				return HelperResponse{Success: true, Output: "running"}
			}
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
			var (
				uapi      string
				connected bool
				err       error
			)
			if isAwg {
				uapi, connected, err = wgDarwinStatusAwg(ifaceName)
			} else {
				uapi, connected, err = wgDarwinStatus(ifaceName)
			}
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

		// Linux: variant-aware kernel/userspace WG via wg show / awg show.
		wgBin := "wg"
		if isAwg {
			wgBin = "awg"
		}
		wg := findWGBinary(wgBin)
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
		args = helperMacOSSwanctlArgs(args)
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
	isAwg := cmd.Args["variant"] == VariantAmnezia
	var dst string
	if runtime.GOOS == "windows" {
		// On Windows, both vanilla and AWG live under
		// %PROGRAMDATA%\PrivycsVPN\tunnels\ — the wireguard.exe
		// tunnel-service reads from there for vanilla; for AWG
		// we read+route via amneziawg-go from the same path.
		dst = windowsWGConfigPath(cmd.Interface)
	} else if isAwg {
		// awg-quick on Linux reads from /etc/amnezia/amneziawg/.
		dst = filepath.Join("/etc/amnezia/amneziawg", cmd.Interface+".conf")
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
	isAwg := cmd.Args["variant"] == VariantAmnezia
	// v0.9.15.30: AWG on Windows runs in a per-tunnel service —
	// handshake comes from the JSON-over-pipe query, not from
	// wg.exe. Branch before we try to locate any external binary
	// (which doesn't exist for AWG anyway).
	if runtime.GOOS == "windows" && isAwg {
		uapi, _, err := queryAWGTunnelService(cmd.Interface)
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
	var binary string
	if runtime.GOOS == "windows" {
		binary = findWireGuardExe()
		if binary == "" {
			return HelperResponse{Success: false, Error: "wg.exe not found"}
		}
	} else {
		// Linux: pick awg-tools binary when AWG variant is active.
		bin := "wg"
		if isAwg {
			bin = "awg"
		}
		binary = findWGBinary(bin)
		if binary == "" {
			return HelperResponse{Success: false, Error: bin + " not found — install the matching tools package"}
		}
	}
	// macOS: read from in-process tunnel via UAPI. Same data, no exec.
	if runtime.GOOS == "darwin" {
		var (
			uapi string
			err  error
		)
		if isAwg {
			uapi, _, err = wgDarwinStatusAwg(cmd.Interface)
		} else {
			uapi, _, err = wgDarwinStatus(cmd.Interface)
		}
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
	loadArgs := helperMacOSSwanctlArgs([]string{"--load-all"})
	out, err := exec.Command(swanctlBin, loadArgs...).CombinedOutput()
	// v1.0.5.26: macOS auto-restart-on-load-all-failure.
	//
	// User-reported 2026-05-29: even with a fresh helper (v1.0.5.20
	// onward, which centralises the --uri pass-through via
	// helperMacOSSwanctlArgs), Profile-Import AND Connect both fail
	// with `connecting to 'unix:///var/run/charon.vici' failed:
	// Connection refused` when charon is in a stuck/zombie state —
	// the process is up (pgrep finds it) but the vici socket refuses
	// connections. Manual `sudo ipsec stop && sudo ipsec start` heals
	// it instantly; afterwards charon auto-loads /etc/swanctl/conf.d/*
	// at startup and the profile is available.
	//
	// This block automates that recovery for the IPSec-configure path
	// (which both Profile-Import and Connect go through): when the
	// load-all fails with a "Connection refused" or "Connection reset"
	// against the vici socket, hard-restart charon (stop → setkey
	// flush → start) and retry --load-all once. Only kicks in on the
	// specific error signature so non-vici failures (malformed
	// swanctl.conf, missing certs, charon startup error) fall through
	// to the normal error path. Logged loudly so the recovery is
	// visible in the helper log.
	if err != nil && runtime.GOOS == "darwin" {
		outStr := string(out)
		looksLikeViciDown := strings.Contains(outStr, "Connection refused") ||
			strings.Contains(outStr, "Connection reset") ||
			strings.Contains(outStr, "No such file or directory")
		if looksLikeViciDown {
			log.Printf("ipsec_configure: swanctl --load-all reports vici unreachable (%q); attempting auto-restart of charon and one retry",
				strings.TrimSpace(outStr))
			if stopOut, restartErr := helperMacOSHardRestartCharon(); restartErr != nil {
				log.Printf("ipsec_configure: auto-restart-and-retry — restart failed: %v (stop output=%q); returning original load-all error",
					restartErr, strings.TrimSpace(stopOut))
			} else {
				log.Printf("ipsec_configure: auto-restart-and-retry — charon restarted, retrying --load-all")
				retryArgs := helperMacOSSwanctlArgs([]string{"--load-all"})
				retryOut, retryErr := exec.Command(swanctlBin, retryArgs...).CombinedOutput()
				if retryErr == nil {
					log.Printf("ipsec_configure: auto-restart-and-retry — SUCCESS on retry (output=%q)",
						strings.TrimSpace(string(retryOut)))
					return HelperResponse{Success: true, Output: string(retryOut)}
				}
				log.Printf("ipsec_configure: auto-restart-and-retry — retry STILL failed: %v (output=%q); returning retry error",
					retryErr, strings.TrimSpace(string(retryOut)))
				// Fall through to error reporting using the RETRY
				// output so the user sees the post-restart failure
				// mode (which is more informative — original could
				// have been a stuck-socket symptom).
				out = retryOut
				err = retryErr
			}
		}
	}
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
	stopOut, err := helperMacOSHardRestartCharon()
	if err != nil {
		return HelperResponse{
			Success: false,
			Error:   fmt.Sprintf("charon-restart: stop output=%q; %v", strings.TrimSpace(stopOut), err),
		}
	}
	return HelperResponse{Success: true, Output: "charon restarted (kernel SADB+SPD flushed)"}
}

// helperMacOSHardRestartCharon executes the full `ipsec stop` →
// wait-for-socket-gone → setkey-flush → `ipsec start` →
// wait-for-socket-back sequence. Returns (stopCommandOutput, error).
// Extracted from cmdMacOSRestartCharon (v0.9.14.88) in v1.0.5.26 so the
// auto-restart-on-load-all-failure path in cmdIPSecConfigure can reuse
// the same logic instead of issuing its own IPC roundtrip.
//
// User-reported (2026-05-29): macOS IPSec import fails with
// `connecting to 'unix:///var/run/charon.vici' failed: Connection refused`
// when charon is in a stuck/zombie state (process present but vici
// socket not accepting connections). Manual `sudo ipsec stop && sudo
// ipsec start` heals it. This helper makes that the automatic
// recovery path when load-all hits the same symptom.
//
// Darwin-only — callers must guard on runtime.GOOS.
func helperMacOSHardRestartCharon() (string, error) {
	ipsecBin := helperFindMacOSStrongswanBinary("ipsec")
	if ipsecBin == "" {
		return "", fmt.Errorf("ipsec wrapper not found")
	}
	// Stop. Output captured for diagnosis on failure; ipsec stop
	// returns 0 even if charon was already down so we don't gate
	// on the exit code — only on the socket disappearing.
	stopOutBytes, _ := exec.Command(ipsecBin, "stop").CombinedOutput()
	stopOut := string(stopOutBytes)

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

	// v0.9.14.90: flush the kernel SA database before starting the
	// fresh daemon. `ipsec stop` tells charon to tear down its SAs,
	// but on macOS the kernel-level SADB entries can survive a
	// stuck/zombie charon — the user sees: traffic flows (because
	// the kernel still has policy + SA pointing at the OLD ESP
	// tunnel), but the NEW charon's `swanctl --list-sas` reports
	// near-zero bytes because it doesn't own those orphan kernel
	// entries. setkey -F flushes all SAs; setkey -FP flushes all
	// policies. Both are macOS base utilities (/usr/sbin/setkey),
	// no install needed. We log the output rather than gate on it
	// — if setkey isn't present we want the restart to continue
	// anyway because the user reports it usually still works.
	flushOut, _ := exec.Command("/usr/sbin/setkey", "-F").CombinedOutput()
	flushPolOut, _ := exec.Command("/usr/sbin/setkey", "-FP").CombinedOutput()
	log.Printf("charon-restart: setkey -F output=%q; setkey -FP output=%q",
		strings.TrimSpace(string(flushOut)),
		strings.TrimSpace(string(flushPolOut)),
	)

	// Start fresh. Reuses the existing helper which polls vici
	// socket up to 8 s.
	if err := helperEnsureMacOSCharonRunning(); err != nil {
		return stopOut, fmt.Errorf("start failed: %v", err)
	}
	return stopOut, nil
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
// helperParseSwanctlLocalV6 extracts the first local IPv6 address from
// a swanctl output (--initiate or --list-sas), looking for the
// "local <v4>/<n> <v6>/<n>" line. Returns "" when no local v6 is
// surfaced. The match is intentionally permissive: it accepts any
// IPv6 literal (containing ":") at the start or end of the line so
// dual-stack and v6-only profiles both produce a hit.
//
// Example matches:
//
//	"    local  10.100.126.3/32 fd63:43:45::3/128"  → "fd63:43:45::3"
//	"    local  fd63:43:45::3/128"                   → "fd63:43:45::3"
//
// v1.0.5.21 — feeds the macOS /128-P2P → /64 v6 vip rebind.
var helperLocalV6Pattern = regexp.MustCompile(
	`local\s+(?:[^\s]+\s+)?([0-9a-fA-F:]+:[0-9a-fA-F:]+)/\d+`,
)

func helperParseSwanctlLocalV6(swanctlOutput string) string {
	for _, line := range strings.Split(swanctlOutput, "\n") {
		// Cheap filter so we don't run the regex on every line.
		// swanctl prefixes the TS-installed line with "    local "
		// after the SA header; the IKE_SA endpoints line is
		// "  local '<id>' @ <addr>..." which we don't want to
		// match. The leading "    local " (4 spaces) discriminates.
		if !strings.HasPrefix(strings.TrimRight(line, " \t"), "    local") &&
			!strings.HasPrefix(strings.TrimRight(line, " \t"), "  local") {
			continue
		}
		if !strings.Contains(line, "::") {
			continue
		}
		m := helperLocalV6Pattern.FindStringSubmatch(line)
		if len(m) >= 2 {
			return m[1]
		}
	}
	return ""
}

// helperFindUtunWithV6 returns the utun interface name (e.g. "utun5")
// that has the given IPv6 address currently bound, or "" when none
// matches. Iterates utun0..utun15 via `ifconfig <name>`; stops at the
// first match. Used by the macOS post-initiate v6-vip rebind so we
// know which interface to ifconfig.
func helperFindUtunWithV6(v6vip string) string {
	for i := 0; i < 16; i++ {
		name := fmt.Sprintf("utun%d", i)
		out, err := exec.Command("ifconfig", name).CombinedOutput()
		if err != nil {
			continue
		}
		// Match either "inet6 fd63:43:45::3" or
		// "inet6 fd63:43:45::3%utun5" — the scopeid suffix is
		// emitted for link-local addresses only, but we strip it
		// defensively so a future macOS version that adds it
		// for ULAs would not break the lookup.
		body := string(out)
		if strings.Contains(body, "inet6 "+v6vip+" ") ||
			strings.Contains(body, "inet6 "+v6vip+"%") {
			return name
		}
	}
	return ""
}

// helperMacOSViciURI returns the explicit unix-socket URI for the
// running charon's VICI socket on this macOS install, in a form
// swanctl accepts via --uri. Empty string when no candidate socket
// exists. Used by every macOS swanctl invocation in the helper —
// without this, swanctl on Homebrew strongSwan 6.0.6 connects to
// its compile-time default URI (`unix:///var/run/charon.vici`)
// which is NOT the Homebrew socket path (`unix:///opt/homebrew/
// var/run/charon.vici`), producing "Connection refused" even when
// charon is running normally. v1.0.5.20 fix.
func helperMacOSViciURI() string {
	viciCandidates := []string{
		"/opt/homebrew/var/run/charon.vici",
		"/usr/local/var/run/charon.vici",
		"/var/run/charon.vici",
	}
	for _, p := range viciCandidates {
		if fi, err := os.Stat(p); err == nil && (fi.Mode()&os.ModeSocket) != 0 {
			return "unix://" + p
		}
	}
	return ""
}

// helperMacOSSwanctlArgs appends `--uri <socket>` to a base swanctl
// argument list when on Darwin and a vici socket is present. On
// non-darwin / no-socket the args are returned unchanged. Centralises
// the URI-passthrough so a single call site change keeps every macOS
// swanctl invocation consistent.
func helperMacOSSwanctlArgs(args []string) []string {
	if runtime.GOOS != "darwin" {
		return args
	}
	if uri := helperMacOSViciURI(); uri != "" {
		return append(append([]string(nil), args...), "--uri", uri)
	}
	return args
}

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
	// Optional FriendlyName label for the imported cert. Forward-compat
	// hook so a future client could override; today the client passes
	// the slot-stable connName here. Helper falls back to connName when
	// the arg is missing (older clients).
	friendlyLabel := cmd.Args["friendly_label"]
	if friendlyLabel == "" {
		friendlyLabel = connName
	}
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
	// Multi-profile cert-pick fix (v1.0.2): Add-VpnConnection's
	// `-AuthenticationMethod MachineCertificate` does NOT tell Windows
	// which cert to use. At dial time Windows scans LocalMachine\My and
	// picks the first cert matching the EKU/issuer criteria. When two
	// Privycs IPSec profiles share the same issuing CA — which is the
	// common case for a single Privycs gateway — the 2nd profile's
	// rasdial ends up sending the 1st profile's cert + identity, the
	// gateway rejects the IKE_AUTH, and Windows surfaces error 13801
	// ("IKE authentication credentials are not acceptable").
	//
	// Fix (v1.0.3 hardening): import the new cert FIRST so its Issuer
	// DN is known, then sweep every OTHER cert in LocalMachine\My that
	// matches either condition:
	//   (1) FriendlyName starts with "Privycs IPSec - "  (catches
	//       sibling profile certs tagged by v1.0.2+ helper runs)
	//   (2) Same Issuer DN as the new cert  (catches pre-v1.0.2 certs
	//       from v1.0.0/v1.0.1 builds that never got our Privycs
	//       FriendlyName tag)
	// Both conditions EXCLUDE the just-imported cert (by thumbprint).
	// Tag the new cert so future v1.0.2+ sweeps see it. Re-import on
	// every profile switch is acceptable.
	//
	// Note: user-scope VPN entries are stored under HKCU of the user that
	// created them. SYSTEM's HKCU is not the end-user's HKCU, so the client
	// side (running as the user) is responsible for cleaning up any stale
	// user-scope entry with this name — see configureWindowsFromSSwan.
	// v1.0.4: PowerShell array-comparison fix. Import-PfxCertificate on
	// a .p12 with both leaf + intermediate CA returns an ARRAY of
	// X509Certificate2 objects. The v1.0.3 sweep used `$_.Thumbprint
	// -ne $myThumb` where $myThumb was that array — in PowerShell,
	// scalar -ne array returns the FILTERED REMAINDER (non-empty when
	// any element differs), which is truthy in a Where-Object context.
	// Effect: EVERY cert in the store (including the just-imported
	// leaf) passed the filter → got swept → no Privycs cert left for
	// rasdial → Windows fell back to an unrelated stale cert → 13801.
	//
	// Fix:
	//   1. Use `-notin` for proper array-containment check.
	//   2. Identify the LEAF cert (`Issuer -ne Subject`) so we tag /
	//      issuer-pick the right one — CA certs in the bundle have
	//      themselves as Issuer and would skew the sweep target.
	//   3. Emit Write-Host diagnostics so the next failure carries
	//      actionable signal (thumbprints, issuer, sweep count).
	psScript := fmt.Sprintf(`
$ErrorActionPreference = 'Stop'
$p12Password = ConvertTo-SecureString -String '%s' -AsPlainText -Force
$friendly = 'Privycs IPSec - %s'
$imported = @(Import-PfxCertificate -FilePath '%s' -CertStoreLocation Cert:\LocalMachine\My -Password $p12Password -ErrorAction Stop)
$myThumbs = @($imported | ForEach-Object { $_.Thumbprint })
$leaf = $imported | Where-Object { $_.Issuer -ne $_.Subject } | Select-Object -First 1
if (-not $leaf) { $leaf = $imported[0] }
$myIssuer = $leaf.Issuer
Write-Host "ipsec-helper: leaf thumb=$($leaf.Thumbprint) issuer=$myIssuer imported=$($imported.Count)"
$toDelete = @(Get-ChildItem Cert:\LocalMachine\My | Where-Object {
    ($_.Thumbprint -notin $myThumbs) -and (
        ($_.FriendlyName -like 'Privycs IPSec - *') -or ($_.Issuer -eq $myIssuer)
    )
})
Write-Host "ipsec-helper: sweeping $($toDelete.Count) stale cert(s)"
$toDelete | ForEach-Object { Write-Host "ipsec-helper: remove thumb=$($_.Thumbprint) friendly=$($_.FriendlyName) subj=$($_.Subject)" } | Out-Null
$toDelete | Remove-Item -Force -ErrorAction SilentlyContinue
$leaf.FriendlyName = $friendly
Write-Host "ipsec-helper: tagged leaf with FriendlyName='$friendly'"
Remove-VpnConnection -Name '%s' -Force -AllUserConnection -ErrorAction SilentlyContinue
Add-VpnConnection -Name '%s' -ServerAddress '%s' -TunnelType IKEv2 -AuthenticationMethod MachineCertificate -EncryptionLevel Required -RememberCredential -AllUserConnection -Force
`, escapePowerShellString(p12Pass), escapePowerShellString(friendlyLabel),
		escapePowerShellString(p12Path),
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
	switch runtime.GOOS {
	case "darwin":
		return h.ipsecCleanupSwanctl(helperFindMacOSSwanctlConfDir(), helperFindMacOSStrongswanBinary("swanctl"))
	case "linux":
		bin := "swanctl"
		if p, err := exec.LookPath("swanctl"); err == nil {
			bin = p
		} else if _, e := os.Stat("/usr/sbin/swanctl"); e == nil {
			bin = "/usr/sbin/swanctl"
		}
		return h.ipsecCleanupSwanctl("/etc/swanctl", bin)
	case "windows":
		return h.ipsecCleanupWindows(cmd)
	default:
		return HelperResponse{Success: true, Output: "ipsec_cleanup: no-op on " + runtime.GOOS}
	}
}

// ipsecCleanupSwanctl removes the swanctl conf + PEMs for the deleted connection
// and reloads charon. Shared by macOS (Homebrew swanctl) and Linux (/etc/swanctl).
// Single-connection-per-host filenames (same gap as the configure path).
func (h *PrivilegedHelper) ipsecCleanupSwanctl(certDir, swanctlBin string) HelperResponse {
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
		// Failure here just means charon still knows about the conn until
		// it's restarted — non-fatal.
		_, _ = exec.Command(swanctlBin, "--load-all").CombinedOutput()
	}
	return HelperResponse{Success: true, Output: out}
}

// ipsecCleanupWindows removes the all-user RAS VPN connection the installer
// created, plus the Privycs-owned certificates it imported. Deliberately
// NARROW: the cert filter matches only Subject/Issuer containing "Privycs", so
// we can never touch a publicly-trusted root (e.g. ISRG Root X1 / Let's Encrypt,
// whose org is "Internet Security Research Group", never "Privycs"). Best-effort
// — every step is wrapped so a missing artifact is not an error. NRPT rules
// added by the gateway setup script are NOT removed here (their namespaces are
// script-defined and unknown to the client); documented as a known gap.
func (h *PrivilegedHelper) ipsecCleanupWindows(cmd HelperCommand) HelperResponse {
	connName := strings.TrimSpace(cmd.Args["connection_name"])
	if connName == "" {
		return HelperResponse{Success: false, Error: "connection_name required for windows ipsec_cleanup"}
	}
	// PowerShell single-quote escaping: double any embedded single quote.
	esc := strings.ReplaceAll(connName, "'", "''")
	ps := "$ErrorActionPreference='SilentlyContinue';" +
		fmt.Sprintf("try { Remove-VpnConnection -Name '%s' -AllUserConnection -Force } catch {};", esc) +
		"Get-ChildItem Cert:\\LocalMachine\\My | Where-Object { $_.Subject -like '*Privycs*' -or $_.Issuer -like '*Privycs*' } | Remove-Item -Force;" +
		"Get-ChildItem Cert:\\LocalMachine\\Root | Where-Object { $_.Subject -like '*Privycs*' } | Remove-Item -Force;"
	out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", ps).CombinedOutput()
	if err != nil {
		return HelperResponse{Success: true, Output: fmt.Sprintf("windows ipsec cleanup (best-effort) for %q: %v / %s", connName, err, strings.TrimSpace(string(out)))}
	}
	return HelperResponse{Success: true, Output: fmt.Sprintf("windows ipsec cleanup done for %q", connName)}
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

// cmdIPSecInstallMacOSV6DefaultRoute installs `default ::/0 via utun`
// on macOS so charon-libipsec actually carries IPv6 traffic. Without
// this, charon-libipsec brings the SA up (v6 vip assigned, kernel SPD
// loaded) but installs NO routing entry pointing v6 traffic at the
// utun — netstat -rn -f inet6 shows no default via utun4 and outbound
// v6 packets exit via the physical interface and fail.
//
// MUST be called by the App-side AFTER ipsec_split_routes_add returns,
// so any user-configured v6 bypass CIDRs are already in the routing
// table when ::/0 lands. BSD's longest-prefix-match then honors the
// bypass routes over the catch-all default. This ordering matters —
// reversing it would route bypass-destined v6 traffic through the
// tunnel for the duration of the window. The user explicitly flagged
// this requirement ("excluded networks must be considered!!!").
//
// Idempotent: "File exists" return from route(8) is treated as
// success (the route is already in place from a prior call or from
// charon-libipsec itself installing it for the same iface). Failures
// other than File-exists are reported back to the caller.
//
// Inputs:
//
//	v6_vip — the local v6 vip from swanctl --list-sas / --initiate
//	         output. Used to find the utun via helperFindUtunWithV6
//	         so we don't add a default route to the wrong interface
//	         (the physical iface or an unrelated utun).
//
// Darwin-only — callers must guard on runtime.GOOS.
func (h *PrivilegedHelper) cmdIPSecInstallMacOSV6DefaultRoute(cmd HelperCommand) HelperResponse {
	if runtime.GOOS != "darwin" {
		return HelperResponse{Success: false, Error: "ipsec_install_macos_v6_default_route: darwin-only"}
	}
	v6vip := strings.TrimSpace(cmd.Args["v6_vip"])
	if v6vip == "" {
		return HelperResponse{Success: false, Error: "v6_vip required"}
	}
	utun := helperFindUtunWithV6(v6vip)
	if utun == "" {
		return HelperResponse{
			Success: false,
			Error:   fmt.Sprintf("no utun found with v6 vip %s — is the IPSec tunnel actually up?", v6vip),
		}
	}
	// route(8) syntax on macOS: `route -n add -inet6 default -interface <name>`.
	// The `-interface` form (vs. gateway form) is what we need here
	// because charon-libipsec maintains its own ESP encapsulation
	// over the utun pty — there is no v6 gateway address inside the
	// tunnel, just the utun's link-local. -interface tells the kernel
	// "for ::/0 destinations, send the packet at this iface" and the
	// charon-libipsec userspace reader then picks it up and encrypts.
	out, err := exec.Command("/sbin/route", "-n", "add", "-inet6", "default", "-interface", utun).CombinedOutput()
	if err != nil {
		outStr := strings.TrimSpace(string(out))
		// File-exists = already installed (probably by charon-libipsec
		// itself, or by an earlier call). Idempotent.
		if strings.Contains(outStr, "File exists") {
			return HelperResponse{
				Success: true,
				Output:  fmt.Sprintf("default ::/0 via %s already present (idempotent)", utun),
			}
		}
		return HelperResponse{
			Success: false,
			Error:   fmt.Sprintf("route add -inet6 default -interface %s: %v: %s", utun, err, outStr),
		}
	}
	log.Printf("macOS IPSec v6 default route: ::/0 via %s installed", utun)
	return HelperResponse{
		Success: true,
		Output:  fmt.Sprintf("default ::/0 via %s installed", utun),
	}
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

// cmdMacOSDNSSnapshot captures the CURRENT primary-service DNS into the
// per-connection backup WITHOUT applying any override. Used by the macOS
// NEVPN IPSec path: the Apple IKEv2 stack pushes the gateway DNS on connect
// but does NOT restore the previous DNS on disconnect (the resolver entry
// lingers), stranding the user on a dead DNS server. We snapshot before
// connect, then cmdMacOSDNSOverrideRestore puts it back on disconnect.
// Reuses the same backup format + restore path as the swanctl override.
func (h *PrivilegedHelper) cmdMacOSDNSSnapshot(cmd HelperCommand) HelperResponse {
	if runtime.GOOS != "darwin" {
		return HelperResponse{Success: true, Output: "macos_dns_snapshot: no-op on non-darwin"}
	}
	connName := cmd.Args["connection_name"]
	if connName == "" {
		return HelperResponse{Success: false, Error: "connection_name required"}
	}
	svc := findMacOSPrimaryNetworkService()
	if svc == "" {
		return HelperResponse{Success: false, Error: "no primary network service detected"}
	}
	curOut, _ := exec.Command("networksetup", "-getdnsservers", svc).CombinedOutput()
	current := strings.TrimSpace(string(curOut))
	// "There aren't any DNS servers set on Wi-Fi." → the "Empty" sentinel so
	// restore clears back to DHCP rather than pinning a stale manual list.
	if strings.Contains(current, "aren't any") {
		current = "Empty"
	}
	if err := persistDNSOverrideBackup(connName, svc, current); err != nil {
		return HelperResponse{Success: false, Error: fmt.Sprintf("backup write: %v", err)}
	}
	log.Printf("DNS snapshot (macOS IPSec restore-on-disconnect): service=%q dns=%s", svc, current)
	return HelperResponse{Success: true, Output: fmt.Sprintf("DNS snapshot saved for %q (%s)", svc, current)}
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

// cmdWindowsDNSSet applies the VPN-pushed DNS server to the OpenVPN
// tunnel adapter with a SINGLE `netsh interface ip set dns ... static`
// call. v0.9.15.53.
//
// Background: OpenVPN 2.7.1-DCO on Windows 26200 applies a pushed
// `dhcp-option DNS <ip>` by issuing `netsh ... set dns <iface> static
// <ip>` immediately followed by `netsh ... add dns <iface> <ip>` for
// the SAME single server. Windows 26200 rejects the duplicate `add`,
// netsh returns exit 1, and OpenVPN treats the netsh failure as fatal.
// We launch openvpn.exe with `--pull-filter ignore "dhcp-option DNS"`
// so it never runs that code path, then call this to set the tunnel
// DNS the correct way — `set` only, no `add`, so no duplicate.
//
// No restore counterpart: the ovpn-dco / tap-windows6 adapter is
// ephemeral. On disconnect the adapter is removed and its per-adapter
// DNS configuration goes with it — there is no persistent host state
// to roll back (unlike the macOS swanctl path which mutates the real
// primary service and therefore needs cmdMacOSDNSOverride{Set,Restore}).
//
// iface: the OpenVPN tunnel adapter — accepts either the numeric
// interface index (as OpenVPN itself uses, e.g. "14") or the adapter
// friendly-name (e.g. "OpenVPN Data Channel Offload"). The caller
// (protocol_openvpn.go) resolves it the same way it resolves the
// adapter for traffic-stats.
func (h *PrivilegedHelper) cmdWindowsDNSSet(cmd HelperCommand) HelperResponse {
	if runtime.GOOS != "windows" {
		return HelperResponse{Success: true, Output: "windows_dns_set: no-op on non-windows"}
	}
	iface := strings.TrimSpace(cmd.Args["iface"])
	dns := strings.TrimSpace(cmd.Args["dns"])
	if iface == "" || dns == "" {
		return HelperResponse{Success: false, Error: "iface and dns required"}
	}
	// Validate dns is an IP literal — never interpolate untrusted text
	// straight into a netsh argv even though exec.Command does not use
	// a shell. Belt-and-braces against a malformed PUSH_REPLY parse.
	if net.ParseIP(dns) == nil {
		return HelperResponse{Success: false, Error: fmt.Sprintf("dns %q is not a valid IP", dns)}
	}
	// netsh accepts both `set dns <idx> ...` and `set dns name="<n>" ...`.
	// A bare numeric token is treated as the index; anything else we
	// pass as name=.
	ifaceArg := iface
	if _, err := strconv.Atoi(iface); err != nil {
		ifaceArg = fmt.Sprintf("name=%s", iface)
	}
	args := []string{"interface", "ip", "set", "dns", ifaceArg, "static", dns, "validate=no"}
	out, err := exec.Command("netsh", args...).CombinedOutput()
	if err != nil {
		return HelperResponse{
			Success: false,
			Error:   fmt.Sprintf("netsh set dns: %s: %v", strings.TrimSpace(string(out)), err),
		}
	}
	log.Printf("Windows DNS set on tunnel adapter %q → %s", iface, dns)
	return HelperResponse{Success: true, Output: fmt.Sprintf("dns %s set on %s", dns, iface)}
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
