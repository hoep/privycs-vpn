//go:build windows

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Microsoft/go-winio"
	awgtunnel "github.com/amnezia-vpn/amneziawg-windows/tunnel"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// AmneziaWG per-tunnel Windows Service.
//
// v0.9.15.42 onward: this file is largely a thin wrapper around
// github.com/amnezia-vpn/amneziawg-windows tunnel.Run. CREDIT and
// thanks to the Amnezia VPN team (https://github.com/amnezia-vpn,
// https://amnezia.org) and the upstream WireGuard LLC authors
// from whom they forked golang.zx2c4.com/wireguard/windows.
// Their amneziawg-windows project (Apache-2.0 + MIT-licensed code,
// see vendor go.mod for exact terms) handles the hard parts:
// Wintun adapter lifecycle, WFP firewall, privilege drop,
// interface watching, address/route/DNS config via winipcfg, UAPI
// listener — production-tested by the AmneziaVPN desktop client.
// We delegate to it from our --awg-tunnel CLI flag handler so we
// don't have to maintain a parallel implementation.
//
// === Original v0.9.15.30-era rationale below, kept for context ===
//
// Why this exists: the in-process wgWindowsUpAwg call from the helper
// (introduced in v0.9.15.4) hits a known wintun.dll race condition
// when "orphaned adapter" cleanup runs in parallel with the
// CreateTUN → NewDevice → IpcSet → Up sequence — observed on the
// user's Windows test machine as a helper-service crash with no Go
// runtime panic trace. The upstream WireGuard project explicitly
// recommends running each tunnel in its own service process rather
// than embedding wintun in the parent (see wireguard.com/embedding,
// February 2025 zx2c4 mailing list thread). The official
// amneziawg-windows project follows the same pattern:
// AmneziaWGTunnel$<name> per tunnel.
//
// Our implementation mirrors that:
//
//   1. The HELPER (still running as SYSTEM via PrivycsVPNHelper SCM
//      entry) no longer calls wgWindowsUpAwg directly. Instead it
//      installs a fresh service "PrivycsAWGTunnel$<iface>" via SCM
//      manager, pointed at our SAME binary with the new CLI flag
//      `--awg-tunnel <confPath> <ifaceName>`.
//
//   2. The per-tunnel service runs in its OWN process under
//      LocalSystem, so any wintun-state corruption or hung netsh
//      call stays isolated from the helper. CreateTUN + NewDevice
//      + IpcSet + Up + netsh configuration all happen here.
//
//   3. Status queries (rxBytes, txBytes, last_handshake_time) go
//      back to the helper over a per-tunnel named pipe at
//      \\.\pipe\PrivycsAWGTunnel.<iface>. Simple JSON one-shot
//      protocol — connect, read line, close.
//
//   4. On disconnect, the helper stops the service and deletes it
//      from SCM. A startup-sweep in the helper removes any
//      PrivycsAWGTunnel$* services that didn't get cleaned up due
//      to a helper crash.

// v0.9.15.42: service prefix matches what amnezia-vpn/amneziawg-
// windows derives via services.ServiceNameOfTunnel — we now
// delegate the entire tunnel-service runtime to their tunnel.Run
// (production-tested by the AmneziaVPN desktop client). Their
// svc.Run dispatcher matches against this name when SCM hands
// control to the service process.
const (
	awgTunnelServicePrefix = "AmneziaWGTunnel$"
	awgTunnelPipePrefix    = `\\.\pipe\PrivycsAWGTunnel.` // legacy, unused with new delegation
)

// awgTunnelStatus is the JSON payload returned by the per-tunnel
// service's status pipe.
type awgTunnelStatus struct {
	Connected            bool   `json:"connected"`
	UAPIDump             string `json:"uapi"`
	IfaceName            string `json:"iface"`
	StartedAtUnix        int64  `json:"started_at_unix"`
	Error                string `json:"error,omitempty"`
}

// awgServiceState is the runtime state of the per-tunnel service.
// Built when the service handler runs Execute and torn down on stop.
type awgServiceState struct {
	confPath   string
	ifaceName  string
	startedAt  time.Time
	tunnelUp   atomic.Bool
	tunnelDown chan struct{}

	// The actual in-process AWG device — re-uses
	// awgWindowsTunnelState helpers from wg_windows_awg.go. We don't
	// store a pointer here directly; the existing global
	// awgWinTunnels registry holds it and we look up by ifaceName.
	// That way wgWindowsDownAwg can tear down via the same path used
	// by the legacy helper-in-process code.
}

// runAWGTunnelService is invoked when privycs-vpn.exe is started
// with `--awg-tunnel <confPath> <ifaceName>` from the helper.
// v0.9.15.42: delegate entire tunnel-service runtime to the
// official amnezia-vpn/amneziawg-windows tunnel.Run — that path
// handles ringlogger init, conf parsing, watchInterface, CreateTUN
// with 5x retry, WFP firewall setup, privilege drop, NewDevice,
// UAPIListen (named pipe \\.\pipe\ProtectedPrefix\Administrators\
// AmneziaWG\<name>), IpcSet, dev.Up, address+route+DNS config via
// winipcfg (their interfaceWatcher.Configure), accept loop, plus
// stop+cleanup on SCM stop. Reproduces what the AmneziaVPN
// desktop client does. Replaces the entire previous per-tunnel-
// service code path (which evolved through v0.9.15.25-.40 chasing
// netsh races, pipe-SD problems, bind-disruption from DNS notifs
// etc.). All of those classes of bug are non-issues in their
// implementation.
//
// ifaceName parameter is unused now — tunnel.Run derives the
// service name internally from the conf filename. Kept in the
// signature so the main.go --awg-tunnel dispatcher doesn't need
// to change.
func runAWGTunnelService(confPath, ifaceName string) {
	logPath := awgTunnelLogPath(ifaceName)
	_ = os.MkdirAll(filepath.Dir(logPath), 0755)
	if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600); err == nil {
		log.SetOutput(f)
		os.Stderr = f
		os.Stdout = f
	}
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
	log.Printf("awg-tunnel-service[%s]: delegating to awgtunnel.Run(%s)", ifaceName, confPath)
	if err := awgtunnel.Run(confPath); err != nil {
		log.Printf("awg-tunnel-service[%s]: awgtunnel.Run failed: %v", ifaceName, err)
	}
	return
}

// runAWGTunnelServiceLegacy retains the previous in-process AWG
// tunnel implementation for emergency rollback. Not wired into
// the --awg-tunnel dispatcher anymore. Kept until the new
// delegation path is confirmed stable in production.
func runAWGTunnelServiceLegacy(confPath, ifaceName string) {
	logPath := awgTunnelLogPath(ifaceName)
	_ = os.MkdirAll(filepath.Dir(logPath), 0755)
	if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600); err == nil {
		log.SetOutput(f)
		os.Stderr = f
		os.Stdout = f
	}
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
	log.Printf("awg-tunnel-service[%s]: starting (conf=%s)", ifaceName, confPath)

	isService, err := svc.IsWindowsService()
	if err != nil || !isService {
		// Console mode — for dev/debug.
		log.Printf("awg-tunnel-service[%s]: console mode (not under SCM)", ifaceName)
		state := &awgServiceState{
			confPath:   confPath,
			ifaceName:  ifaceName,
			startedAt:  time.Now(),
			tunnelDown: make(chan struct{}),
		}
		if err := state.bringUp(); err != nil {
			log.Fatalf("awg-tunnel-service[%s]: bringUp: %v", ifaceName, err)
		}
		go state.serveStatusPipe()
		select {}
	}

	handler := &awgServiceHandler{confPath: confPath, ifaceName: ifaceName}
	if err := svc.Run(awgTunnelServicePrefix+ifaceName, handler); err != nil {
		log.Printf("awg-tunnel-service[%s]: svc.Run failed: %v", ifaceName, err)
	}
}

// awgServiceHandler implements svc.Handler for the per-tunnel service.
type awgServiceHandler struct {
	confPath, ifaceName string
	state               *awgServiceState
}

func (h *awgServiceHandler) Execute(args []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	changes <- svc.Status{State: svc.StartPending}

	h.state = &awgServiceState{
		confPath:   h.confPath,
		ifaceName:  h.ifaceName,
		startedAt:  time.Now(),
		tunnelDown: make(chan struct{}),
	}

	// v0.9.15.39: open the status pipe BEFORE bringUp. Rationale:
	// the helper polls the pipe at ~25 Hz from the moment the
	// install-service RPC returns, and expects to get *some*
	// response (even "tunnel not yet up") within ~30 s. If bringUp
	// hangs (we have seen netsh interface ipv4 set address freeze
	// on freshly-created Wintun adapters because the OS hasn't
	// fully registered the adapter in netsh's user-mode namespace
	// yet) the pipe was previously never opened, every helper
	// poll returned "file not found", and the app gave up and
	// failed over to vanilla WG. With the listener up-front,
	// queryAWGTunnelService can answer "not ready" rather than
	// "service unreachable" — gives the app time to observe a
	// successful bring-up rather than racing against the netsh
	// stalls.
	go h.state.serveStatusPipe()

	// Bring up the AWG tunnel. If this fails, abort the service
	// with a non-zero exit code so SCM marks it stopped.
	if err := h.state.bringUp(); err != nil {
		log.Printf("awg-tunnel-service[%s]: bringUp failed: %v", h.ifaceName, err)
		changes <- svc.Status{State: svc.Stopped}
		return false, 1
	}

	const accepts = svc.AcceptStop | svc.AcceptShutdown
	changes <- svc.Status{State: svc.Running, Accepts: accepts}
	log.Printf("awg-tunnel-service[%s]: running", h.ifaceName)

	for c := range r {
		switch c.Cmd {
		case svc.Interrogate:
			changes <- c.CurrentStatus
		case svc.Stop, svc.Shutdown:
			log.Printf("awg-tunnel-service[%s]: SCM requested stop", h.ifaceName)
			changes <- svc.Status{State: svc.StopPending}
			h.state.bringDown()
			changes <- svc.Status{State: svc.Stopped}
			return false, 0
		}
	}
	return false, 0
}

func (s *awgServiceState) bringUp() error {
	content, err := os.ReadFile(s.confPath)
	if err != nil {
		return fmt.Errorf("read conf %s: %w", s.confPath, err)
	}
	// Re-use the existing in-process bring-up implementation from
	// wg_windows_awg.go. wgWindowsUpAwg() does CreateTUN + NewDevice
	// + IpcSet + Up + netsh configuration. Running it inside this
	// per-tunnel service process gives the isolation that the
	// upstream-recommended embedding pattern requires; running it
	// inside the helper process was what triggered the v0.9.15.x
	// crash chain.
	if err := wgWindowsUpAwg(s.ifaceName, string(content)); err != nil {
		return fmt.Errorf("wgWindowsUpAwg: %w", err)
	}
	s.tunnelUp.Store(true)
	return nil
}

func (s *awgServiceState) bringDown() {
	if !s.tunnelUp.Load() {
		return
	}
	if err := wgWindowsDownAwg(s.ifaceName); err != nil {
		log.Printf("awg-tunnel-service[%s]: wgWindowsDownAwg: %v", s.ifaceName, err)
	}
	s.tunnelUp.Store(false)
	close(s.tunnelDown)
}

// serveStatusPipe listens on \\.\pipe\PrivycsAWGTunnel.<iface> and
// returns a JSON awgTunnelStatus payload for each connecting client.
// v0.9.15.37: use Microsoft/go-winio for proper Windows named-pipe
// support. Previous version used net.Listen("unix", ...) under the
// false assumption that Go's net package routes \\.\pipe\... paths
// to named pipes — it doesn't. AF_UNIX on Windows expects a real
// filesystem path; \\.\pipe\ is a separate Win32 namespace (named
// pipes via CreateNamedPipe/NamedPipeClient APIs). Consequence of
// the old code: the listener silently failed → every helper status
// poll returned "target machine actively refused it" → app gave up
// after ~30 s, falling over to vanilla WireGuard. AWG itself was
// fully online the whole time (handshake + keepalives confirmed in
// the per-tunnel trace log).
func (s *awgServiceState) serveStatusPipe() {
	pipePath := awgTunnelPipePrefix + s.ifaceName
	// v0.9.15.40: explicit SecurityDescriptor allowing SYSTEM and
	// Builtin Administrators full pipe access. Without it go-winio
	// uses an empty SD which on some Windows configurations falls
	// back to a restrictive per-creator ACL. The helper (running as
	// LocalSystem) was getting "file not found" on every DialPipe
	// even while this listener was active — symptom matches a SD
	// access-denied silently translated to ENOENT by Windows. The
	// SDDL form below grants Generic All to S-1-5-18 (LocalSystem)
	// and S-1-5-32-544 (Builtin Administrators) — same accessor
	// set the official wireguard-windows IPC uses.
	pipeCfg := &winio.PipeConfig{
		SecurityDescriptor: "D:P(A;;GA;;;SY)(A;;GA;;;BA)",
	}
	ln, err := winio.ListenPipe(pipePath, pipeCfg)
	if err != nil {
		log.Printf("awg-tunnel-service[%s]: pipe listen failed: %v", s.ifaceName, err)
		return
	}
	defer ln.Close()
	log.Printf("awg-tunnel-service[%s]: status pipe listening at %s", s.ifaceName, pipePath)

	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			log.Printf("awg-tunnel-service[%s]: pipe accept: %v", s.ifaceName, err)
			continue
		}
		go s.handleStatusConn(conn)
	}
}

func (s *awgServiceState) handleStatusConn(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))

	uapi, connected, err := wgWindowsStatusAwg(s.ifaceName)
	resp := awgTunnelStatus{
		IfaceName:     s.ifaceName,
		Connected:     connected,
		UAPIDump:      uapi,
		StartedAtUnix: s.startedAt.Unix(),
	}
	if err != nil {
		resp.Error = err.Error()
	}
	if err := json.NewEncoder(conn).Encode(resp); err != nil {
		log.Printf("awg-tunnel-service[%s]: pipe encode: %v", s.ifaceName, err)
	}
}

// awgTunnelLogPath returns the per-service log file path.
func awgTunnelLogPath(ifaceName string) string {
	programData := os.Getenv("PROGRAMDATA")
	if programData == "" {
		programData = `C:\ProgramData`
	}
	return filepath.Join(programData, "PrivycsVPN", "awg-tunnel-"+ifaceName+".log")
}

// ============================================================================
// Helper-side service management (called from privileged_helper.go)
// ============================================================================

// installAWGTunnelService creates a new SCM service entry for the
// given AWG interface and starts it. The service binary path is
// resolved from the helper's own executable path (so it works
// regardless of where Privycs VPN is installed). Replaces the
// previous in-helper wgWindowsUpAwg call.
//
// Idempotent: if a service with the same name already exists, it
// is stopped + deleted first so the new install always has a
// clean slate (otherwise the SCM-side service config might be
// stale, e.g. pointing at an old install path).
func installAWGTunnelService(ifaceName, confPath string) error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve helper exe: %w", err)
	}
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("scm connect: %w", err)
	}
	defer m.Disconnect()

	serviceName := awgTunnelServicePrefix + ifaceName

	// Remove any existing service with this name. Service-Manager
	// can be in inconsistent states after helper crashes — clean
	// slate every time.
	if existing, err := m.OpenService(serviceName); err == nil {
		_ = stopAndDeleteService(existing, serviceName)
		existing.Close()
		// Give SCM up to 2s to commit the deletion before the
		// fresh install. Without this gap, CreateService can
		// return ERROR_SERVICE_MARKED_FOR_DELETE.
		for i := 0; i < 20; i++ {
			if s, err := m.OpenService(serviceName); err != nil {
				break
			} else {
				s.Close()
				time.Sleep(100 * time.Millisecond)
			}
		}
	}

	// v0.9.15.43: mgr.Config aligned with what
	// amnezia-vpn/amneziawg-windows-client manager/install.go uses
	// for its InstallTunnel — see GitHub source. Three critical
	// fields that our previous mgr.Config was missing:
	//
	//   - SidType: SERVICE_SID_TYPE_UNRESTRICTED — gives the per-
	//     tunnel service its own SID. tunnel.Run's
	//     CopyConfigOwnerToIPCSecurityDescriptor and the IPC pipe
	//     SecurityDescriptor rely on this. Without it, awgtunnel.Run
	//     enters a code path that exits silently.
	//
	//   - Dependencies: Nsi + TcpIp — ensure the TCP/IP stack is
	//     up before the tunnel-service starts. Irrelevant for
	//     normal runtime starts but matters for boot-time auto-
	//     start.
	//
	//   - StartType: StartAutomatic — matches their pattern.
	//     We still call Start() explicitly below; the auto-start
	//     attribute is for SCM bookkeeping consistency.
	//
	// Also: removed our hand-built BinaryPathName + the explicit
	// "--awg-tunnel" + ifaceName args. CreateService builds the
	// service's command line from (exePath, args...) automatically
	// — our previous double-specification (BinaryPathName AND args)
	// was the wrong contract. Now we pass "--awg-tunnel" + confPath
	// + ifaceName as svc-args; CreateService appends them.
	displayName := "Privycs AWG Tunnel: " + ifaceName
	config := mgr.Config{
		ServiceType:  windows.SERVICE_WIN32_OWN_PROCESS,
		StartType:    mgr.StartAutomatic,
		ErrorControl: mgr.ErrorNormal,
		Dependencies: []string{"Nsi", "TcpIp"},
		DisplayName:  displayName,
		Description:  "Privycs VPN per-tunnel AmneziaWG worker service. Managed by PrivycsVPNHelper.",
		SidType:      windows.SERVICE_SID_TYPE_UNRESTRICTED,
	}

	svcInst, err := m.CreateService(serviceName, exePath, config,
		"--awg-tunnel", confPath, ifaceName)
	if err != nil {
		return fmt.Errorf("CreateService %s: %w", serviceName, err)
	}
	defer svcInst.Close()

	if err := svcInst.Start(); err != nil {
		// Best-effort cleanup so we don't leave a half-installed
		// service ghost behind on failure paths.
		_ = svcInst.Delete()
		return fmt.Errorf("Start %s: %w", serviceName, err)
	}

	log.Printf("awg-helper: installed + started service %s (conf=%s)", serviceName, confPath)
	return nil
}

// uninstallAWGTunnelService stops and removes the per-tunnel SCM
// service entry. Called from the helper when the user disconnects
// an AWG tunnel.
func uninstallAWGTunnelService(ifaceName string) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("scm connect: %w", err)
	}
	defer m.Disconnect()

	serviceName := awgTunnelServicePrefix + ifaceName
	s, err := m.OpenService(serviceName)
	if err != nil {
		// Already gone — that's the desired end-state, treat as success.
		return nil
	}
	defer s.Close()

	if err := stopAndDeleteService(s, serviceName); err != nil {
		return err
	}
	log.Printf("awg-helper: uninstalled service %s", serviceName)
	return nil
}

func stopAndDeleteService(s *mgr.Service, name string) error {
	// Best-effort stop. The service may already be stopped, in
	// which case Control(svc.Stop) returns an error we can ignore.
	if _, err := s.Control(svc.Stop); err != nil {
		// If the service was already stopped or never started,
		// the control call errors out — that's fine.
		log.Printf("awg-helper: Control(Stop) on %s: %v (continuing to Delete)", name, err)
	}

	// Poll until stopped or 5s elapsed.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status, err := s.Query()
		if err != nil {
			break
		}
		if status.State == svc.Stopped {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if err := s.Delete(); err != nil {
		return fmt.Errorf("Delete %s: %w", name, err)
	}
	return nil
}

// queryAWGTunnelService returns the AWG tunnel state by querying
// the SCM service registered for this interface. v0.9.15.42:
// drops the previous "talk JSON over a custom named pipe" approach
// since the new awgtunnel.Run-based service owns its own UAPI pipe
// (\\.\pipe\ProtectedPrefix\Administrators\AmneziaWG\<name>) which
// we don't need to parse — connected/disconnected via SCM state is
// sufficient for the helper's status-RPC consumers.
//
// uapi return value is always empty in the new path. The helper's
// status RPC used to surface this for fine-grained "show
// handshake / rxBytes" display in the app; with the migration that
// information would require talking the wireguard UAPI protocol
// to their pipe. Keeping the field present (empty) to preserve
// the function signature for callers.
func queryAWGTunnelService(ifaceName string) (uapi string, connected bool, err error) {
	serviceName := awgTunnelServicePrefix + ifaceName
	m, err := mgr.Connect()
	if err != nil {
		return "", false, fmt.Errorf("scm connect: %w", err)
	}
	defer m.Disconnect()
	s, err := m.OpenService(serviceName)
	if err != nil {
		return "", false, fmt.Errorf("open service %s: %w", serviceName, err)
	}
	defer s.Close()
	status, err := s.Query()
	if err != nil {
		return "", false, fmt.Errorf("query service %s: %w", serviceName, err)
	}
	return "", status.State == svc.Running, nil
}

// sweepOrphanedAWGTunnelServices removes any PrivycsAWGTunnel$*
// services left behind by a helper crash or unclean shutdown.
// Called once at helper start so we never inherit a half-broken
// AWG tunnel state from the previous session.
func sweepOrphanedAWGTunnelServices() {
	m, err := mgr.Connect()
	if err != nil {
		log.Printf("awg-helper: sweep mgr.Connect: %v", err)
		return
	}
	defer m.Disconnect()

	names, err := m.ListServices()
	if err != nil {
		log.Printf("awg-helper: sweep ListServices: %v", err)
		return
	}
	for _, n := range names {
		if !strings.HasPrefix(n, awgTunnelServicePrefix) {
			continue
		}
		s, err := m.OpenService(n)
		if err != nil {
			continue
		}
		if err := stopAndDeleteService(s, n); err != nil {
			log.Printf("awg-helper: sweep %s: %v", n, err)
		} else {
			log.Printf("awg-helper: sweep removed orphan %s", n)
		}
		s.Close()
	}
}
