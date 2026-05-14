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

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// AmneziaWG per-tunnel Windows Service (v0.9.15.30).
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

const (
	awgTunnelServicePrefix = "PrivycsAWGTunnel$"
	awgTunnelPipePrefix    = `\\.\pipe\PrivycsAWGTunnel.`
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
// Hooks into Windows SCM as the service named
// PrivycsAWGTunnel$<ifaceName>.
func runAWGTunnelService(confPath, ifaceName string) {
	// Log to a per-service file under %ProgramData% so each tunnel
	// has its own debugging trail. Helps when the user has two AWG
	// tunnels up at once and only one is misbehaving.
	logPath := awgTunnelLogPath(ifaceName)
	_ = os.MkdirAll(filepath.Dir(logPath), 0755)
	if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600); err == nil {
		log.SetOutput(f)
		// Redirect stderr + stdout to the same file so Go runtime
		// panic stacktraces (which bypass the log package) and any
		// fmt.Print from inside amneziawg-go land in the per-service
		// log instead of vanishing. Under SCM stderr is normally
		// detached — without this redirect a goroutine panic shows
		// up to us as ERROR_PROCESS_ABORTED (1067) with no trace.
		os.Stderr = f
		os.Stdout = f
	}
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
	log.Printf("awg-tunnel-service[%s]: starting (conf=%s)", ifaceName, confPath)

	isService, err := svc.IsWindowsService()
	if err != nil || !isService {
		// Console mode — for dev/debug. Bring tunnel up, block on
		// stdin, then bring down. Useful when invoking directly
		// with `privycs-vpn.exe --awg-tunnel ... --console`.
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
		// Block forever — console mode, kill via Ctrl+C
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

	// Bring up the AWG tunnel. If this fails, abort the service
	// with a non-zero exit code so SCM marks it stopped.
	if err := h.state.bringUp(); err != nil {
		log.Printf("awg-tunnel-service[%s]: bringUp failed: %v", h.ifaceName, err)
		changes <- svc.Status{State: svc.Stopped}
		return false, 1
	}

	// Start the status-pipe server in the background.
	go h.state.serveStatusPipe()

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
// On Windows we use net.Listen("unix", path) — Go's net package
// transparently routes \\.\pipe\... names to a named pipe under the
// hood when the path matches.
func (s *awgServiceState) serveStatusPipe() {
	pipePath := awgTunnelPipePrefix + s.ifaceName
	ln, err := net.Listen("unix", pipePath)
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

	// CreateService args are positional. Display name is what
	// shows up in services.msc; we use the friendly per-tunnel
	// name so the user can see which tunnel is which.
	displayName := "Privycs AWG Tunnel: " + ifaceName
	config := mgr.Config{
		ServiceType:      0x10, // SERVICE_WIN32_OWN_PROCESS
		StartType:        mgr.StartManual,
		ErrorControl:     mgr.ErrorNormal,
		BinaryPathName:   fmt.Sprintf(`"%s" --awg-tunnel "%s" "%s"`, exePath, confPath, ifaceName),
		DisplayName:      displayName,
		Description:      "Privycs VPN per-tunnel AmneziaWG worker service. Managed by PrivycsVPNHelper.",
		ServiceStartName: "LocalSystem",
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

// queryAWGTunnelService dials the per-tunnel service's status pipe
// and returns the latest status payload. Used by the helper's
// status-action handler when variant=amneziawg on Windows.
func queryAWGTunnelService(ifaceName string) (uapi string, connected bool, err error) {
	pipePath := awgTunnelPipePrefix + ifaceName
	conn, err := net.DialTimeout("unix", pipePath, 2*time.Second)
	if err != nil {
		return "", false, fmt.Errorf("dial status pipe %s: %w", pipePath, err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))

	var status awgTunnelStatus
	if err := json.NewDecoder(conn).Decode(&status); err != nil {
		return "", false, fmt.Errorf("decode status: %w", err)
	}
	if status.Error != "" {
		return "", false, fmt.Errorf("service reported error: %s", status.Error)
	}
	return status.UAPIDump, status.Connected, nil
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
