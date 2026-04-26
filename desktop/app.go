package main

import (
	"context"
	"fmt"
	"hash/crc32"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/hoep/privycs/desktop/geoip"
	"github.com/hoep/privycs/desktop/selfip"
	"github.com/wailsapp/wails/v2/pkg/options"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// App is the main application struct. All public methods are callable from the Vue frontend
// via auto-generated TypeScript bindings in wailsjs/go/main/App.ts
type App struct {
	ctx context.Context
	mu  sync.RWMutex

	// VPN protocol management
	protocols      map[string]VPNProtocol
	activeProtocol string

	// Connection registry for multi-config support
	connections *ConnectionRegistry

	// Pool feature: virtual connections that wrap multiple endpoints
	// and pick one per Connect using a policy. activePoolID is set
	// when the user activates a Pool in the picker; activeMemberID
	// is the member currently connected (set after PickMember runs).
	pools          *PoolRegistry
	poolImporter   *PoolImporter
	poolRotator    *PoolRotator
	selfIPDetector *selfip.Detector
	activePoolID   string

	// Features
	killSwitch         *KillSwitch         // legacy - kept ONLY for one-time PrivycsKS-* cleanup at startup/shutdown
	ksManager          *KillSwitchManager  // new state machine (Phase 1)
	sinkholeController *SinkholeController // new platform driver bridge (Phase 2/3)
	pauseManager       *PauseManager       // user-initiated VPN pause (B4)
	autoConnect        *AutoConnectManager
	settings           *AppSettings

	// State
	connected     bool
	disconnecting bool // true while proto.Down() is running — blocks auto-detect
	connectedAt   time.Time
	stopStats     chan struct{}
	forceQuit     bool           // set by tray Quit to bypass minimize-to-tray
	logFile       *os.File       // log file handle for proper cleanup on shutdown
	wg            sync.WaitGroup // tracks background goroutines for clean shutdown
}

// NewApp creates a new App instance
func NewApp() *App {
	ks := NewKillSwitchManager()

	// Resolve the GeoIP reader once and share it between the Pool
	// importer (resolves endpoint hostnames at import time) and the
	// SelfIP detector (resolves the user's public IP at connect time).
	// A missing MMDB is non-fatal - downstream callers handle the
	// "country unknown" case by degrading Geo-Nearest to Random.
	geoR, geoErr := geoip.Default()
	if geoErr != nil {
		log.Printf("App: GeoIP DB unavailable (%v); Geo-Nearest will degrade to Random", geoErr)
	}
	var geoForImport CountryResolverIF
	var geoForSelfIP selfip.CountryResolver
	if geoR != nil {
		geoForImport = geoR
		geoForSelfIP = geoR
	}

	return &App{
		protocols:          make(map[string]VPNProtocol),
		connections:        NewConnectionRegistry(),
		pools:              NewPoolRegistry(),
		poolImporter:       NewPoolImporter(geoForImport),
		poolRotator:        NewPoolRotator(),
		selfIPDetector:     selfip.New(geoForSelfIP),
		killSwitch:         NewKillSwitch(),
		ksManager:          ks,
		sinkholeController: NewSinkholeController(ks, NewPlatformSinkhole()),
		pauseManager:       NewPauseManager(),
		autoConnect:        NewAutoConnectManager(),
		settings:           LoadSettings(),
		stopStats:          make(chan struct{}),
	}
}

// startup is called by Wails when the application starts
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx

	// Set up file logging — write to file always, stderr if available.
	// Store handle for proper cleanup in shutdown() to prevent file handle leaks.
	logPath := filepath.Join(appDataDir(), "privycs-vpn.log")
	// Rotate-on-startup safety net: if the existing log is huge (e.g.
	// from a runaway loop in a prior version - a user reported a 9 GB
	// privycs-vpn.log produced in minutes by the network-monitor
	// spin-loop bug), rename to .old (replacing any prior .old) and
	// start fresh. Single-depth rotation is enough to give the user
	// the prior session's tail without ever ballooning the disk.
	if info, statErr := os.Stat(logPath); statErr == nil && info.Size() > 10*1024*1024 {
		oldPath := logPath + ".old"
		_ = os.Remove(oldPath)
		_ = os.Rename(logPath, oldPath)
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err == nil {
		a.logFile = logFile
		// On Windows GUI apps, stderr may not be available
		// Always write to log file; try stderr as bonus
		writers := []io.Writer{logFile}
		if os.Stderr != nil {
			writers = append(writers, os.Stderr)
		}
		log.SetOutput(io.MultiWriter(writers...))
	} else {
		// Fallback: try to log the error itself
		fmt.Fprintf(os.Stderr, "Failed to open log file %s: %v\n", logPath, err)
	}
	log.SetFlags(log.Ldate | log.Ltime)

	log.Println("Privycs VPN starting...")

	// Always clean up stale kill switch rules from previous crash/kill.
	// If the app was killed without graceful shutdown, iptables DROP rules
	// remain and block all traffic.
	NewKillSwitch().Deactivate()

	// Ensure sudoers is configured for passwordless VPN commands (Linux only, shows pkexec prompt once)
	ensureSudoers()

	// Register protocol handlers
	a.protocols["wireguard"] = NewWireGuardProtocol()
	a.protocols["openvpn"] = NewOpenVPNProtocol()
	a.protocols["ipsec"] = NewIPSecProtocol()

	// Load last active protocol
	a.activeProtocol = a.settings.ActiveProtocol
	if a.activeProtocol == "" {
		a.activeProtocol = "wireguard"
	}

	// Restore selection from last session. Order of precedence:
	//   1. Persisted pool ActiveID  -> activate that pool
	//   2. Persisted single ActiveID -> already loaded by registry
	//   3. Neither set, but saved connections exist -> auto-select
	//      the most-recently-used single (highest LastConnected)
	// Step 3 is the user-facing fix: cold-start should never land on
	// the empty Welcome screen when there ARE saved connections, the
	// user just hasn't pinned one as active.
	if a.pools != nil && a.pools.ActiveID != "" && a.pools.Get(a.pools.ActiveID) != nil {
		a.activePoolID = a.pools.ActiveID
		log.Printf("Restored active pool: %s", a.pools.Get(a.activePoolID).Name)
	} else if a.connections.Active() == nil && len(a.connections.List()) > 0 {
		mru := mostRecentlyUsedConnection(a.connections.List())
		if mru != nil {
			a.connections.SetActive(mru.ID)
			log.Printf("Auto-selected last-used connection: %s", mru.Name)
		}
	}

	// Load saved connections and configure the active protocol handler
	// so status checks and disconnect work even after app restart
	if conn := a.connections.Active(); conn != nil {
		if cfg := conn.GetActiveConfig(); cfg != nil {
			if proto, ok := a.protocols[cfg.Protocol]; ok {
				setTunnelName(proto, sanitizeTunnelName(conn.Name))
				proto.Configure([]byte(cfg.ConfigContent))
				a.activeProtocol = cfg.Protocol
				log.Printf("Restored active connection: %s (%s, tunnel: %s)", conn.Name, cfg.Protocol, sanitizeTunnelName(conn.Name))
			}
		}
	}

	// Wire pool rotator with the restored active pool, if any.
	if a.activePoolID != "" && a.pools != nil && a.poolRotator != nil {
		if p := a.pools.Get(a.activePoolID); p != nil {
			a.poolRotator.SetActivePool(p)
		}
	}

	// Detect if a tunnel is already running (e.g. after app restart)
	if proto, ok := a.protocols[a.activeProtocol]; ok {
		s := proto.Status()
		if s.Connected {
			a.connected = true
			a.connectedAt = time.Now() // approximate, we don't know exact start time
			log.Printf("Detected running %s tunnel", a.activeProtocol)
		}
	}

	// Start periodic status emitter for real-time UI updates
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		a.statusEmitter()
	}()

	// Start system tray icon
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		a.startTray()
	}()

	// Auto-connect: prefer connect-on-demand if enabled, fall back to legacy.
	if a.settings.ConnectOnDemand.Enabled {
		// COD owns the connect/disconnect lifecycle whenever enabled,
		// regardless of whether a tunnel is already up at startup. The
		// monitor's first evaluation will either confirm the connect
		// is wanted (rules match) or tear it down (rules do not match).
		a.startOnDemandMonitoring()
	} else if a.settings.AutoConnectOnStart && !a.connected {
		a.autoConnect.Start(func() { a.Connect(a.activeProtocol) }, func() bool {
			a.mu.RLock()
			defer a.mu.RUnlock()
			return a.connected
		})
	}

	// MIGRATION: clean up legacy PrivycsKS-* rules from prior versions.
	// The new sinkhole system uses Privycs-Sinkhole-* names so old rules
	// would otherwise persist forever after upgrading. killSwitch.Disable
	// is idempotent (delete-not-found is silently ignored).
	a.killSwitch.Disable()

	// Start the new sinkhole controller. It first runs RecoverFromCrash
	// (cleans up any Privycs-Sinkhole-* leftovers from a previous crashed
	// run via the snapshot file) then subscribes to ksManager and
	// engages/releases the OS firewall on state transitions.
	go a.sinkholeController.Run(a.ctx)

	// Drive the new state machine according to settings.
	if a.settings.KillSwitchEnabled {
		switch {
		case a.connected:
			// Tunnel up at startup (we restored a running session).
			// Arm so an unexpected drop engages the sinkhole.
			a.ksManager.Arm()
		case a.connections.Active() != nil:
			// KS on, no tunnel, but a configured connection exists.
			// Hardcore semantics: traffic must be blocked NOW.
			a.ksManager.ForceSinkhole("KS enabled at startup, no active tunnel")
		default:
			// KS on but no connections configured. Nothing to protect
			// against; leave at IDLE until the user adds a connection.
		}
	}

	log.Println("Privycs VPN ready")
}

// shutdown is called when the application is closing
func (a *App) shutdown(ctx context.Context) {
	log.Println("Privycs VPN shutting down...")

	// Stop stats emitter (safe to call multiple times)
	select {
	case <-a.stopStats:
		// already closed
	default:
		close(a.stopStats)
	}

	// Stop auto-connect / network monitor
	a.autoConnect.Stop()

	// CRITICAL ORDER: release the sinkhole BEFORE we tear down the tunnel
	// or shut the app. If we exit while the sinkhole is engaged the user
	// is locked out of the network until next launch (or until they run
	// the emergency PowerShell cleanup - see EMERGENCY_RECOVERY.md). The
	// controller's Stop() releases idempotently.
	a.sinkholeController.Stop()

	// Disconnect tunnel if still connected
	if a.connected {
		log.Println("Disconnecting tunnel on shutdown...")
		// Call Down() directly to avoid mutex issues during shutdown
		if proto, ok := a.protocols[a.activeProtocol]; ok {
			proto.Down(context.Background())
		}
		a.connected = false
	}

	// Final safety net: legacy PrivycsKS-* cleanup AND a best-effort
	// Privycs-Sinkhole-* cleanup in case the controller missed something.
	// Both are idempotent, log warnings only on failure.
	a.killSwitch.Disable()

	// Wait for background goroutines (status emitter, tray) to exit.
	// They listen on stopStats which was closed above.
	a.wg.Wait()

	log.Println("Privycs VPN stopped")

	// Close log file handle to prevent file handle leak.
	// Must be last — no logging after this point.
	if a.logFile != nil {
		a.logFile.Close()
		a.logFile = nil
	}
}

// beforeClose is called before the window closes — minimize to tray if enabled.
// If forceQuit is set (from tray Quit), always allow close.
func (a *App) beforeClose(ctx context.Context) (prevent bool) {
	if a.forceQuit {
		log.Println("Force quit — closing app")
		return false
	}
	if runtime.GOOS == "darwin" {
		return false
	}
	if a.settings.MinimizeToTray {
		log.Println("Minimizing to tray")
		wailsRuntime.WindowHide(ctx)
		return true
	}
	return false
}

// onSecondInstance is called when a second instance tries to start
func (a *App) onSecondInstance(data options.SecondInstanceData) {
	// Bring existing window to front
	wailsRuntime.WindowShow(a.ctx)
	wailsRuntime.WindowSetAlwaysOnTop(a.ctx, true)
	wailsRuntime.WindowSetAlwaysOnTop(a.ctx, false)

	// Handle deep links (privycs://enroll?gateway=X&token=Y)
	if len(data.Args) > 1 {
		wailsRuntime.EventsEmit(a.ctx, "deeplink", data.Args[1])
	}
}

// ============================================================================
// CONNECTION METHODS (callable from Vue frontend)
// ============================================================================

// StatusResponse is the full status returned to the frontend
type StatusResponse struct {
	Connected           bool     `json:"connected"`
	ActiveProtocol      string   `json:"active_protocol"`
	AvailableProtocols  []string `json:"available_protocols"`
	ServerAddress       string   `json:"server_address,omitempty"`
	LocalAddress        string   `json:"local_address,omitempty"`
	BytesRx             int64    `json:"bytes_rx"`
	BytesTx             int64    `json:"bytes_tx"`
	ConnectedAt         string   `json:"connected_at,omitempty"`
	LastHandshake       string   `json:"last_handshake,omitempty"`
	LatencyMs           float64  `json:"latency_ms,omitempty"`
	Uptime              string   `json:"uptime,omitempty"`
	KillSwitchEnabled   bool     `json:"kill_switch_enabled"`
	KillSwitchState     string   `json:"kill_switch_state"` // "IDLE" / "ARMED" / "SINKHOLE"
	AutoConnectEnabled  bool     `json:"auto_connect_enabled"`
	PauseRemainingSec   int      `json:"pause_remaining_sec"` // 0 when not paused
	ConnectionName      string   `json:"connection_name,omitempty"`
	ConnectionID        string   `json:"connection_id,omitempty"`
	ConnectionProtocols []string `json:"connection_protocols,omitempty"` // protocols available for this connection
	Error               string   `json:"error,omitempty"`
}

// Status returns the current VPN connection status.
// IMPORTANT: Does NOT hold the mutex during proto.Status() calls because
// those run external commands (PowerShell, sc query) that can take seconds.
// Holding the RLock during those calls blocks Disconnect() from getting
// the write lock, causing the disconnect button to hang.
func (a *App) Status() *StatusResponse {
	// 1. Read app state quickly under lock
	a.mu.RLock()
	connected := a.connected
	activeProtocol := a.activeProtocol
	connectedAt := a.connectedAt
	var connName, connID string
	var connProtocols []string
	if conn := a.connections.Active(); conn != nil {
		connName = conn.Name
		connID = conn.ID
		connProtocols = conn.AvailableProtocols()
	}
	a.mu.RUnlock()

	// 2. Query protocol status WITHOUT holding the lock (slow on Windows).
	//
	// CRITICAL: skip the proto.Status() call entirely when the app is
	// not in the connected state. There is nothing meaningful to query
	// (no tunnel, no traffic, no server) and on Windows every protocol
	// implements Status() by spawning an external process:
	//   - IPSec: powershell.exe Get-VpnConnection (~300-500ms, allocates
	//     handles + briefly creates conhost child)
	//   - OpenVPN: tasklist.exe
	//   - WireGuard: sc.exe query
	// statusEmitter polls every 2s, so disconnected idle was spawning a
	// PowerShell child every 2s for the user's active session. Over
	// hours this leaks handles; on multiple Windows test machines this
	// has been correlated with system instability (handle-table
	// pressure on npfs.sys) and BSOD when combined with Wintun/WFP
	// activity. The simplest, safest mitigation is to not poll when
	// there is nothing to poll.
	var protoStatus ProtocolStatus
	if connected {
		if proto, ok := a.protocols[activeProtocol]; ok {
			protoStatus = proto.Status()
		}
	}

	// No auto-detection here. a.connected is managed exclusively by:
	// - startup() for detecting tunnels running from a previous session
	// - Connect() when user explicitly connects
	// - disconnectInternal() when user explicitly disconnects
	// Running auto-detect in the poll loop caused disconnect to "reconnect"
	// because the process was still briefly visible after SIGTERM.

	// 3. Defensive Kill Switch arming. When the user has KS enabled
	// AND the tunnel is up AND ksManager is not yet ARMED, arm it.
	// Mirrors Android v0.9.10.5+ defensive-arming pattern: covers the
	// edge case where the user toggles KS on while already connected
	// (settings flip alone does not transition the state machine
	// because the connect path already finished). arm() is idempotent
	// across all three states so re-checking on every status tick is
	// cheap.
	if connected && a.settings.KillSwitchEnabled && a.ksManager != nil {
		if a.ksManager.State() != KSStateArmed {
			a.ksManager.Arm()
		}
	}

	// 4. Build response
	pauseRem := 0
	if a.pauseManager != nil {
		pauseRem = int(a.pauseManager.Remaining().Seconds())
	}
	ksState := "IDLE"
	if a.ksManager != nil {
		ksState = a.ksManager.State().String()
	}
	resp := &StatusResponse{
		Connected:           connected,
		ActiveProtocol:      activeProtocol,
		AvailableProtocols:  a.availableProtocols(),
		KillSwitchEnabled:   a.settings.KillSwitchEnabled,
		KillSwitchState:     ksState,
		AutoConnectEnabled:  a.autoConnect.IsRunning(),
		PauseRemainingSec:   pauseRem,
		ServerAddress:       protoStatus.ServerAddress,
		LocalAddress:        protoStatus.LocalAddress,
		BytesRx:             protoStatus.BytesRx,
		BytesTx:             protoStatus.BytesTx,
		LastHandshake:       protoStatus.LastHandshake,
		LatencyMs:           protoStatus.LatencyMs,
		ConnectionName:      connName,
		ConnectionID:        connID,
		ConnectionProtocols: connProtocols,
	}

	if protoStatus.Error != "" {
		resp.Error = protoStatus.Error
	}

	if connected && !connectedAt.IsZero() {
		resp.ConnectedAt = connectedAt.Format(time.RFC3339)
		resp.Uptime = formatDuration(time.Since(connectedAt))
	}

	return resp
}

// Connect establishes a VPN connection using the active protocol
func (a *App) Connect(protocol string) (*StatusResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Hardcore Kill Switch lock: refuse every connect attempt while
	// the sinkhole is engaged. The ONLY release path is the user
	// toggling KS off in Settings (which transitions ksManager to
	// IDLE and lets the controller release the firewall block).
	// Mirrors the Android v0.9.10.6 hardcore lock behaviour.
	if a.ksManager != nil && a.ksManager.IsSinkholeActive() {
		return nil, fmt.Errorf("kill switch active — toggle Kill Switch off in Settings to release the sinkhole")
	}

	// Pause guard: refuse non-explicit connect attempts while a
	// user-initiated pause is active. NetworkMonitor's pause check
	// already filters COD-driven calls before they ever reach here,
	// but this is the defense-in-depth: any other code path that
	// calls Connect (status emitter, autostart, helper IPC) gets
	// blocked too. The user's manual click via the UI toggleConnection
	// path also goes through here; we deliberately block that to keep
	// "Pause means pause" semantics. The user can hit "Resume now"
	// in the pause banner to release the pause - that path calls
	// CancelPause first, then a normal Connect succeeds.
	if a.pauseManager != nil && a.pauseManager.IsPaused() {
		return nil, fmt.Errorf("VPN paused — click Resume now in the pause banner to release")
	}

	// Switch protocol if specified
	if protocol != "" && protocol != a.activeProtocol {
		if _, ok := a.protocols[protocol]; !ok {
			return nil, fmt.Errorf("unknown protocol: %s", protocol)
		}
		a.activeProtocol = protocol
		a.settings.ActiveProtocol = protocol
		SaveSettings(a.settings)
	}

	proto, ok := a.protocols[a.activeProtocol]
	if !ok {
		return nil, fmt.Errorf("no active protocol configured")
	}

	if !proto.IsAvailable() {
		return nil, fmt.Errorf("%s is not available on this system", a.activeProtocol)
	}

	// Check if already connected
	currentStatus := proto.Status()
	if currentStatus.Connected {
		log.Printf("Tunnel already running via %s", a.activeProtocol)
		a.connected = true
		if a.connectedAt.IsZero() {
			a.connectedAt = time.Now()
		}
		return a.statusLocked(), nil
	}

	// NOTE: pre-Phase-3 we activated the kill switch BEFORE Up() and
	// rolled back on Up() failure. The new sinkhole system does NOT
	// pre-block during connect attempts - if Up() succeeds the
	// MarkConnected hook below transitions ksManager to ARMED; if Up()
	// fails the firewall stays open and the user keeps their internet.
	// Trade-off: a brief window where KS-armed-yet-disconnected user
	// could leak DNS during a connect retry. Mitigation is the
	// SINKHOLE state engaging on user-initiated disconnect (see the
	// disconnect path), which is how the user enters the protected
	// state in the first place after their initial successful connect.

	log.Printf("Connecting via %s...", a.activeProtocol)
	wailsRuntime.EventsEmit(a.ctx, "vpn:connecting", a.activeProtocol)

	// Run Up() in a goroutine so the UI stays responsive.
	// Configs with many AllowedIPs can take 60+ seconds for wg-quick.
	activeProto := a.activeProtocol
	appCtx := a.ctx
	a.mu.Unlock() // Release lock during long operation

	upErr := proto.Up(appCtx)

	a.mu.Lock() // Re-acquire lock
	if upErr != nil {
		// New sinkhole model: we did NOT pre-activate, so there is
		// nothing to roll back here - the firewall stayed open during
		// the failed connect attempt. User retains internet
		// automatically. Just surface the error.
		wailsRuntime.EventsEmit(appCtx, "vpn:error", upErr.Error())
		Notify("VPN connection failed",
			fmt.Sprintf("%s tunnel could not be started: %s", activeProto, upErr.Error()),
			NotifyError)
		return nil, fmt.Errorf("connection failed: %w", upErr)
	}

	// Wait for the tunnel to actually be up before reporting Connected=true.
	// proto.Up() returns as soon as the daemon/process is kicked off, but
	// that is not the same as "tunnel is routing traffic". OpenVPN on
	// Windows previously crashed at NETSH a few seconds after Up() returned
	// yet the UI happily reported "connected" because we set the flag
	// unconditionally. Now we poll proto.Status() — which consults the
	// OpenVPN management interface / wg-show / swanctl for the actual
	// state — and only transition to connected after it reports true.
	// Timeout must fit realistic worst case:
	//   - OpenVPN TLS handshake on slow links: up to ~15s
	//   - WireGuard: <1s (handshake is lazy; we consider established when
	//     the wg-quick call returns, which already blocks that long)
	//   - IPSec: IKE_SA + CHILD_SA negotiation: up to ~20s
	const connectTimeout = 30 * time.Second
	const pollInterval = 3 * time.Second
	a.mu.Unlock() // release during blocking poll so status emitter can read state
	deadline := time.Now().Add(connectTimeout)
	tunnelUp := false
	for time.Now().Before(deadline) {
		if proto.Status().Connected {
			tunnelUp = true
			break
		}
		time.Sleep(pollInterval)
	}
	a.mu.Lock()

	if !tunnelUp {
		// Tunnel never actually came up. Just kill the daemon. New
		// sinkhole model: no firewall rollback needed because we did
		// not pre-activate. User retains internet.
		a.mu.Unlock()
		_ = proto.Down(appCtx)
		a.mu.Lock()
		errMsg := fmt.Sprintf("%s tunnel did not come up within %v — check logs", activeProto, connectTimeout)
		wailsRuntime.EventsEmit(appCtx, "vpn:error", errMsg)
		Notify("VPN connection timed out", errMsg, NotifyError)
		return nil, fmt.Errorf("%s", errMsg)
	}

	a.connected = true
	a.connectedAt = time.Now()

	// Tunnel verified up: arm the kill switch state machine so an
	// unexpected drop engages the sinkhole. Idempotent across all
	// states (IDLE -> ARMED, SINKHOLE -> ARMED, ARMED no-op). Only
	// triggered when the user has KS enabled in settings - if they
	// have it off, ksManager stays at IDLE and never engages.
	if a.settings.KillSwitchEnabled {
		a.ksManager.Arm()
	}

	// Update connection last-used timestamp
	if conn := a.connections.Active(); conn != nil {
		conn.LastConnected = time.Now()
		a.connections.Save()
	}

	wailsRuntime.EventsEmit(appCtx, "vpn:connected", activeProto)
	log.Printf("Connected via %s", protocol)

	// Notify user — matches Android foreground notification behaviour
	// (see PrivycsVpnService buildNotification). Desktop users had no
	// feedback when the tunnel came up in the background via auto-connect.
	connName := ""
	if conn := a.connections.Active(); conn != nil {
		connName = conn.Name
	}
	notifyBody := fmt.Sprintf("%s tunnel is active", strings.ToUpper(activeProto))
	if connName != "" {
		notifyBody = fmt.Sprintf("%s connected via %s", connName, strings.ToUpper(activeProto))
	}
	Notify("VPN connected", notifyBody, NotifyInfo)

	return a.statusLocked(), nil
}

// Disconnect tears down the active VPN connection. User-initiated
// (clicked the disconnect button, called via tray, etc.) - cancels
// any pending Pause auto-reconnect because the user has explicitly
// said "I want to be off". PauseFor uses disconnectInternal directly
// to keep its scheduled reconnect alive.
func (a *App) Disconnect() error {
	if a.pauseManager != nil {
		a.pauseManager.Cancel()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.disconnectInternal()
}

// disconnectInternal tears down the tunnel without locking (caller must hold lock)
func (a *App) disconnectInternal() error {
	proto, ok := a.protocols[a.activeProtocol]
	if !ok {
		return nil
	}

	// Block auto-detection while disconnecting. Do NOT set
	// a.connected=false yet - that depends on proto.Down succeeding.
	// If Down returns an error (helper unreachable on Windows etc.)
	// the tunnel is still running at the OS level and the UI must
	// keep showing connected, otherwise the user thinks they're
	// off-VPN while their traffic is actually still being routed
	// through the tunnel - the bug pattern reported as "tunnel was
	// active without UI showing it".
	a.disconnecting = true
	a.connectedAt = time.Time{}

	log.Printf("Disconnecting %s...", a.activeProtocol)

	downErr := proto.Down(a.ctx)
	a.disconnecting = false

	if downErr != nil {
		log.Printf("Disconnect error: %v", downErr)
		// Surface to the user so they can act (e.g. restart helper).
		// Keep a.connected=true because the OS-level tunnel is in
		// fact still up.
		wailsRuntime.EventsEmit(a.ctx, "vpn:error", downErr.Error())
		Notify("VPN disconnect failed",
			fmt.Sprintf("%s tunnel could not be torn down: %s",
				strings.ToUpper(a.activeProtocol), downErr.Error()),
			NotifyError)
		return downErr
	}

	a.connected = false

	// Hardcore Kill Switch: a user-initiated disconnect with KS
	// enabled engages the sinkhole - traffic stays blocked until
	// the user reconnects or toggles KS off. CRITICAL on Windows:
	// engage with a settle delay so the WireGuard NDIS teardown
	// (wintun.sys cleanup, async after proto.Down returns) is
	// COMPLETE before netsh modifies WFP filters. The two
	// operations racing in kernel mode is what BSOD'd v0.9.10.29.
	//
	// 3 seconds is empirically enough on Windows for wintun.sys
	// to fully release. On Linux/macOS the delay is harmless
	// (iptables/pf changes are atomic and not racing anything).
	//
	// Engagement is via goroutine so the disconnect path returns
	// immediately to the UI - the user does not wait 3s for the
	// disconnect to finish, the sinkhole just slides in
	// underneath afterwards.
	if a.settings.KillSwitchEnabled && a.ksManager != nil {
		go func() {
			time.Sleep(3 * time.Second)
			a.ksManager.ForceSinkhole("user-initiated disconnect with KS enabled (delayed for NDIS settle)")
		}()
	}

	wailsRuntime.EventsEmit(a.ctx, "vpn:disconnected", a.activeProtocol)
	log.Println("Disconnected")
	Notify("VPN disconnected", fmt.Sprintf("%s tunnel closed", strings.ToUpper(a.activeProtocol)), NotifyInfo)

	return nil
}

// ============================================================================
// PROTOCOL METHODS
// ============================================================================

// SelectProtocol changes the selected protocol for the active connection
// WITHOUT disconnecting or connecting. The user must press Connect to apply.
func (a *App) SelectProtocol(protocol string) error {
	conn := a.connections.Active()
	if conn == nil {
		return fmt.Errorf("no active connection")
	}
	if !conn.HasProtocol(protocol) {
		return fmt.Errorf("protocol %s not available for %s", protocol, conn.Name)
	}

	a.mu.Lock()
	conn.ActiveProtocol = protocol
	a.activeProtocol = protocol
	a.settings.ActiveProtocol = protocol
	a.connections.Save()
	SaveSettings(a.settings)

	// Snapshot config while holding lock — Configure() may be slow (Windows PowerShell)
	// and we don't want to block other goroutines reading state
	var configContent []byte
	var tunnelName string
	if cfg := conn.GetProtocol(protocol); cfg != nil {
		configContent = []byte(cfg.ConfigContent)
		tunnelName = sanitizeTunnelName(conn.Name)
	}
	a.mu.Unlock()

	// Configure the protocol handler so Connect() is ready
	if configContent != nil {
		if proto, ok := a.protocols[protocol]; ok {
			setTunnelName(proto, tunnelName)
			proto.Configure(configContent)
		}
	}

	log.Printf("Selected protocol: %s for %s", protocol, conn.Name)
	wailsRuntime.EventsEmit(a.ctx, "vpn:protocol_changed", protocol)
	return nil
}

// SetProtocol switches the active VPN protocol (disconnects first if connected)
func (a *App) SetProtocol(protocol string) error {
	if _, ok := a.protocols[protocol]; !ok {
		return fmt.Errorf("unknown protocol: %s", protocol)
	}

	// Disconnect current if connected
	if a.connected {
		if err := a.Disconnect(); err != nil {
			return fmt.Errorf("failed to disconnect before switching: %w", err)
		}
	}

	a.mu.Lock()
	a.activeProtocol = protocol
	a.settings.ActiveProtocol = protocol
	SaveSettings(a.settings)
	a.mu.Unlock()

	wailsRuntime.EventsEmit(a.ctx, "vpn:protocol_changed", protocol)
	log.Printf("Active protocol changed to: %s", protocol)

	return nil
}

// GetAvailableProtocols returns which protocols are usable on this system
func (a *App) GetAvailableProtocols() []ProtocolInfo {
	var protocols []ProtocolInfo
	for name, proto := range a.protocols {
		protocols = append(protocols, ProtocolInfo{
			Name:        name,
			Available:   proto.IsAvailable(),
			DisplayName: protocolDisplayName(name),
			Description: protocolDescription(name),
		})
	}
	return protocols
}

// ProtocolInfo describes a protocol for the frontend
type ProtocolInfo struct {
	Name        string `json:"name"`
	Available   bool   `json:"available"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
}

// ============================================================================
// CONFIG IMPORT METHODS
// ============================================================================

// ImportConfig imports a VPN configuration file.
// If connectionID is provided, the config is added as an additional protocol
// to an existing connection. Otherwise a new connection is created.
func (a *App) ImportConfig(protocol string, content string, filename string, connectionName string, connectionID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Auto-detect protocol if not specified
	if protocol == "" {
		protocol = detectProtocol(content, filename)
		if protocol == "" {
			return fmt.Errorf("cannot detect protocol from file content")
		}
	}

	proto, ok := a.protocols[protocol]
	if !ok {
		return fmt.Errorf("unsupported protocol: %s", protocol)
	}

	// Set tunnel name from connection name so each connection
	// gets its own config file and Windows service name
	tunnelName := sanitizeTunnelName(connectionName)
	if tunnelName == "" {
		tunnelName = sanitizeTunnelName(filename)
	}
	setTunnelName(proto, tunnelName)

	// Validate config by configuring the protocol handler
	if err := proto.Configure([]byte(content)); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}

	// Build protocol config entry
	pc := &ProtocolConfig{
		Protocol:      protocol,
		ConfigContent: content,
		Filename:      filename,
		AddedAt:       time.Now().Format(time.RFC3339),
	}
	// Extract server address from protocol handler
	status := proto.Status()
	if status.ServerAddress != "" {
		pc.ServerAddress = status.ServerAddress
	}
	if status.LocalAddress != "" {
		pc.LocalAddress = status.LocalAddress
	}

	// Add to existing connection or create new one
	name := connectionName
	if name == "" {
		name = filename
	}
	conn, err := a.connections.AddOrUpdate(connectionID, name, pc)
	if err != nil {
		return err
	}

	a.connections.SetActive(conn.ID)
	a.activeProtocol = protocol
	a.settings.ActiveProtocol = protocol
	SaveSettings(a.settings)

	log.Printf("Config imported: %s (%s, %d bytes) -> connection %s", name, protocol, len(content), conn.ID)
	wailsRuntime.EventsEmit(a.ctx, "vpn:config_imported", map[string]string{
		"connection_id": conn.ID,
		"name":          conn.Name,
		"protocol":      protocol,
	})

	return nil
}

// ============================================================================
// CONNECTION REGISTRY METHODS
// ============================================================================

// ListConnections returns all saved VPN connections
func (a *App) ListConnections() []*SavedConnection {
	return a.connections.List()
}

// ActivateConnection switches to a saved connection and optionally a specific protocol
func (a *App) ActivateConnection(id string, protocol string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	conn := a.connections.Get(id)
	if conn == nil {
		return fmt.Errorf("connection not found: %s", id)
	}

	// Mutual exclusion with the Pool layer: activating a single
	// clears any active pool, just like ActivatePool clears the
	// singles' activeID. The picker UI assumes only one of (pool,
	// single) drives the connection at a time. Persist the cleared
	// pool selection so a restart does not revive it.
	if a.activePoolID != "" {
		a.activePoolID = ""
		if a.poolRotator != nil {
			a.poolRotator.SetActivePool(nil)
		}
		if a.pools != nil {
			_ = a.pools.SetActiveID("")
		}
	}

	// Disconnect current tunnel if connected
	if a.connected {
		if err := a.disconnectInternal(); err != nil {
			log.Printf("Warning: disconnect failed during activate: %v", err)
		}
	}

	// Select protocol
	selectedProtocol := protocol
	if selectedProtocol == "" {
		selectedProtocol = conn.ActiveProtocol
	}
	if selectedProtocol == "" && len(conn.Protocols) > 0 {
		selectedProtocol = conn.Protocols[0].Protocol
	}

	cfg := conn.GetProtocol(selectedProtocol)
	if cfg == nil {
		return fmt.Errorf("protocol %s not configured for connection %s", selectedProtocol, conn.Name)
	}

	protoHandler, ok := a.protocols[selectedProtocol]
	if !ok {
		return fmt.Errorf("protocol handler not available: %s", selectedProtocol)
	}

	setTunnelName(protoHandler, sanitizeTunnelName(conn.Name))

	if err := protoHandler.Configure([]byte(cfg.ConfigContent)); err != nil {
		return fmt.Errorf("failed to configure: %w", err)
	}

	a.connections.SetActive(id)
	conn.ActiveProtocol = selectedProtocol
	a.activeProtocol = selectedProtocol
	a.settings.ActiveProtocol = selectedProtocol
	a.connections.Save()
	SaveSettings(a.settings)

	log.Printf("Activated connection: %s (%s)", conn.Name, selectedProtocol)
	return nil
}

// SwitchActiveConnection switches the active connection AND - if the
// tunnel was up at switch time, or COD says it should be up on the
// current network - automatically reconnects with the new connection.
// Returns true if a reconnect will be attempted (caller should
// surface a Kill-Switch warning toast in that case), false when the
// call is purely a setActive.
//
// Mirrors the Android v0.9.10.10 switchActiveConnection contract.
// The KillSwitch interaction is identical: if KS is armed and a
// reconnect is attempted, the disconnect engages forceSinkhole and
// the next Connect call is refused by the hardcore-lock guard. The
// new active connection id is persisted regardless so that toggling
// KS off later resumes with the right connection.
func (a *App) SwitchActiveConnection(id string, protocol string) (bool, error) {
	a.mu.Lock()
	wasConnected := a.connected
	prevID := ""
	if act := a.connections.Active(); act != nil {
		prevID = act.ID
	}
	a.mu.Unlock()

	if id == prevID {
		return false, nil
	}

	// ActivateConnection already disconnects-on-active and reconfigures
	// the protocol handler. Reuse it.
	if err := a.ActivateConnection(id, protocol); err != nil {
		return false, err
	}

	// Decide whether to fire a fresh Connect.
	willReconnect := wasConnected
	if !willReconnect && a.settings.ConnectOnDemand.Enabled {
		nm := a.autoConnect.NetworkMonitor()
		if nm != nil {
			ns := nm.CurrentState()
			willReconnect = ns.RuleMatch
		}
	}
	if !willReconnect {
		return false, nil
	}

	// Detach from the caller's goroutine so the slow tunnel-up path
	// does not block the UI's pickConnection click handler.
	go func() {
		// Brief settle delay so the disconnect-on-active inside
		// ActivateConnection has time to complete its native-side
		// teardown before we fire a new Up().
		time.Sleep(1500 * time.Millisecond)
		if _, err := a.Connect(""); err != nil {
			log.Printf("SwitchActiveConnection reconnect: %v", err)
		}
	}()
	return true, nil
}

// PauseFor schedules a user-initiated VPN pause for the given number
// of seconds. While the pause is active, the network monitor's pause
// guard suppresses COD reconnects and App.Connect rejects new
// intents (including the user clicking the connect button). When the
// pause expires, if the VPN was connected at pause-start time, this
// auto-reconnects to whatever was active. The auto-reconnect intent
// is cancelled if the user explicitly disconnects during the pause
// (via the public Disconnect method, which calls pauseManager.Cancel).
func (a *App) PauseFor(seconds int) error {
	if a.pauseManager == nil {
		return fmt.Errorf("pause manager not initialised")
	}
	if seconds <= 0 {
		a.pauseManager.Cancel()
		return nil
	}

	a.mu.Lock()
	wasConnected := a.connected
	a.mu.Unlock()

	a.pauseManager.PauseFor(time.Duration(seconds) * time.Second)

	if wasConnected {
		// Disconnect via the internal path so we do NOT clear our
		// own pause (public Disconnect cancels pause as part of
		// "user wants off" semantics).
		go func() {
			a.mu.Lock()
			err := a.disconnectInternal()
			a.mu.Unlock()
			if err != nil {
				log.Printf("PauseFor: internal disconnect failed: %v", err)
			}
		}()
		// Schedule the auto-reconnect watcher.
		go a.pauseExpiryReconnectWatcher(time.Duration(seconds) * time.Second)
	}
	return nil
}

// pauseExpiryReconnectWatcher waits for the pause to elapse and then
// fires Connect if the pause was not explicitly cancelled and the
// VPN is still down. The cancellation case (user clicked the
// disconnect button during the pause, or hit Resume Now which calls
// CancelPause) results in pauseManager.IsPaused() returning false
// before our timer fires - we then check whether the user already
// hit Resume (in which case Connect was kicked off elsewhere) or
// Disconnect (in which case we should NOT auto-reconnect).
func (a *App) pauseExpiryReconnectWatcher(pauseDuration time.Duration) {
	// Wait the full pause duration plus a small grace window so the
	// PauseManager's wall-clock check has definitely flipped to
	// expired by the time we check.
	time.Sleep(pauseDuration + 250*time.Millisecond)

	if a.pauseManager == nil {
		return
	}
	// If pause is still considered active here, the user must have
	// extended it via another PauseFor call. The newer call has its
	// own watcher; this one bows out.
	if a.pauseManager.IsPaused() {
		return
	}
	// If we are already connected (e.g. user hit Resume Now and the
	// reconnect already ran), nothing to do.
	a.mu.RLock()
	connected := a.connected
	a.mu.RUnlock()
	if connected {
		return
	}
	log.Println("Pause expired - auto-reconnecting (was connected at pause start)")
	if _, err := a.Connect(""); err != nil {
		log.Printf("Pause-expiry reconnect failed: %v", err)
	}
}

// CancelPause clears any active pause. If the user invoked this via
// "Resume now" in the pause banner, also fire an immediate
// reconnect - they explicitly asked to be back on the VPN.
func (a *App) CancelPause() error {
	if a.pauseManager == nil {
		return nil
	}
	wasPaused := a.pauseManager.IsPaused()
	a.pauseManager.Cancel()
	if !wasPaused {
		return nil
	}
	a.mu.RLock()
	connected := a.connected
	a.mu.RUnlock()
	if connected {
		return nil
	}
	// Resume Now intent: reconnect immediately. Detached so the
	// IPC call returns fast.
	go func() {
		if _, err := a.Connect(""); err != nil {
			log.Printf("Resume-now reconnect failed: %v", err)
		}
	}()
	return nil
}

// SwitchConnectionProtocol switches protocol within the active connection
func (a *App) SwitchConnectionProtocol(protocol string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	conn := a.connections.Active()
	if conn == nil {
		return fmt.Errorf("no active connection")
	}

	if !conn.HasProtocol(protocol) {
		return fmt.Errorf("protocol %s not configured for %s", protocol, conn.Name)
	}

	// Disconnect current tunnel first
	if a.connected {
		if err := a.disconnectInternal(); err != nil {
			log.Printf("Warning: disconnect failed during protocol switch: %v", err)
		}
	}

	// Configure and switch
	cfg := conn.GetProtocol(protocol)
	protoHandler, ok := a.protocols[protocol]
	if !ok {
		return fmt.Errorf("protocol handler not available: %s", protocol)
	}

	setTunnelName(protoHandler, sanitizeTunnelName(conn.Name))

	if err := protoHandler.Configure([]byte(cfg.ConfigContent)); err != nil {
		return fmt.Errorf("failed to configure: %w", err)
	}

	conn.ActiveProtocol = protocol
	a.activeProtocol = protocol
	a.settings.ActiveProtocol = protocol
	a.connections.Save()
	SaveSettings(a.settings)

	wailsRuntime.EventsEmit(a.ctx, "vpn:protocol_changed", protocol)
	log.Printf("Switched connection %s to protocol %s", conn.Name, protocol)
	return nil
}

// RenameConnection changes the display name of a saved connection
func (a *App) RenameConnection(id string, newName string) error {
	conn := a.connections.Get(id)
	if conn == nil {
		return fmt.Errorf("connection not found: %s", id)
	}
	conn.Name = newName
	a.connections.Save()
	log.Printf("Connection renamed to: %s", newName)
	return nil
}

// DeleteConnection removes a saved connection
func (a *App) DeleteConnection(id string) error {
	return a.connections.Delete(id)
}

// RemoveProtocolFromConnection removes a single protocol config from a connection
func (a *App) RemoveProtocolFromConnection(connectionID string, protocol string) error {
	return a.connections.RemoveProtocol(connectionID, protocol)
}

// ============================================================================
// SETTINGS METHODS
// ============================================================================

// GetSettings returns current app settings
func (a *App) GetSettings() *AppSettings {
	return a.settings
}

// UpdateSettings saves updated settings.
//
// Side-effects (KS apply, autostart write) are gated on actual value
// CHANGE rather than firing every call. The Vue UI was observed
// calling this method ~14 times in 8 seconds while the user was
// editing an on-demand network entry, and each call previously fired
// SetAutostart which spawns a registry-write subprocess on Windows -
// the spawn loop produced visible console-window flashing on the
// user's screen.
func (a *App) UpdateSettings(settings *AppSettings) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	prevAutostart := a.settings != nil && a.settings.AutostartEnabled
	prevKS := a.settings != nil && a.settings.KillSwitchEnabled
	prevCOD := a.settings != nil && a.settings.ConnectOnDemand.Enabled

	a.settings = settings
	SaveSettings(settings)

	// Connect-on-Demand transitions:
	//
	//   off -> on : start the network monitor; a tick later it will
	//               connect or disconnect according to current rules.
	//   on -> off : stop the monitor AND immediately disconnect any
	//               active tunnel. The user toggling COD off is an
	//               explicit "I do not want auto-management AND I do
	//               not want VPN right now" intent (per user feedback).
	//   on -> on  : push the new ConnectOnDemand pointer into the
	//               monitor so settings changes (trigger, ssid_list)
	//               take effect immediately rather than on next 60s
	//               poll. Otherwise the monitor would still hold the
	//               pointer to the OLD settings struct that
	//               `a.settings = settings` just orphaned.
	switch {
	case !prevCOD && settings.ConnectOnDemand.Enabled:
		a.startOnDemandMonitoring()
	case prevCOD && !settings.ConnectOnDemand.Enabled:
		a.autoConnect.Stop()
		if a.connected {
			// Detach disconnect from the locked UpdateSettings path -
			// proto.Down can be slow and we hold a.mu here.
			go func() {
				if err := a.Disconnect(); err != nil {
					log.Printf("UpdateSettings: COD-off triggered disconnect failed: %v", err)
				}
			}()
		}
	case prevCOD && settings.ConnectOnDemand.Enabled:
		nm := a.autoConnect.NetworkMonitor()
		nm.UpdateSettings(&a.settings.ConnectOnDemand)
		// Force an immediate re-eval so the change takes effect
		// within ~1s rather than up to 60s (the safety-poll
		// interval). Common case: user just changed the trigger
		// from "any" to "wifi_mobile" while on Ethernet - they
		// expect the VPN to disconnect now, not in a minute.
		nm.Reevaluate()
	}

	// Apply kill switch setting via the new state machine, but only
	// when it actually changed - applyKillSwitchSetting calls
	// ksManager.Arm/Disarm/ForceSinkhole which fire transitions and
	// trigger the SinkholeController. Calling them on every settings
	// touch (even with the same value) generates extra log noise and
	// pointless transition events.
	if settings.KillSwitchEnabled != prevKS {
		a.applyKillSwitchSetting(settings.KillSwitchEnabled)
	}

	// Apply autostart setting ONLY if it actually changed. SetAutostart
	// spawns a registry-write subprocess on Windows; calling it on
	// every UpdateSettings (which the UI hits repeatedly while the
	// user is e.g. editing an on-demand network entry) produced a
	// visible console-window flash storm.
	if settings.AutostartEnabled != prevAutostart {
		if err := SetAutostart(settings.AutostartEnabled); err != nil {
			log.Printf("Failed to set autostart: %v", err)
		}
	}

	wailsRuntime.EventsEmit(a.ctx, "vpn:settings_changed", settings)
	return nil
}

// GetConnectOnDemandStatus returns the current network state and rule evaluation
// for the connect-on-demand feature. Used by the frontend to display live status.
func (a *App) GetConnectOnDemandStatus() map[string]interface{} {
	nm := a.autoConnect.NetworkMonitor()
	state := nm.CurrentState()

	a.mu.RLock()
	vpnConnected := a.connected
	a.mu.RUnlock()

	return map[string]interface{}{
		"network_type":  state.NetworkType,
		"ssid":          state.SSID,
		"rule_match":    state.RuleMatch,
		"vpn_connected": vpnConnected,
		"monitoring":    nm.IsRunning(),
	}
}

// SetKillSwitch toggles the kill switch.
func (a *App) SetKillSwitch(enabled bool) error {
	a.applyKillSwitchSetting(enabled)
	a.settings.KillSwitchEnabled = enabled
	SaveSettings(a.settings)
	return nil
}

// startOnDemandMonitoring builds the connect/disconnect/isConnected
// closures that the network monitor needs and starts the monitor
// against the current ConnectOnDemand settings.
//
// Used from:
//   - startup() when COD is enabled at app launch.
//   - UpdateSettings() when the user toggles COD from off to on.
//
// The monitor's first evaluation runs ~3s after Start to give the app
// time to finish initialising; subsequent evaluations are driven by
// platform network events plus a 60s safety poll.
func (a *App) startOnDemandMonitoring() {
	connectFn := func() {
		a.Connect(a.activeProtocol)
	}
	disconnectFn := func() {
		// Detach: proto.Down can take seconds (especially IPSec) and
		// we do not want to stall the network-monitor goroutine.
		go func() {
			if err := a.Disconnect(); err != nil {
				log.Printf("On-demand disconnect: %v", err)
			}
		}()
	}
	isConnectedFn := func() bool {
		a.mu.RLock()
		defer a.mu.RUnlock()
		return a.connected
	}
	a.autoConnect.StartWithOnDemand(&a.settings.ConnectOnDemand, connectFn, disconnectFn, isConnectedFn)
	// Wire the pause guard so a user-initiated pause suppresses COD
	// auto-reconnect attempts. Without this, trigger="any" would fire
	// connect() within seconds of pausing because the very network
	// event that made the user pause (settling on a wired desk?) is
	// still sitting in the platform watcher's queue.
	if nm := a.autoConnect.NetworkMonitor(); nm != nil && a.pauseManager != nil {
		nm.SetPauseCheck(a.pauseManager.IsPaused)
	}

	// Wire SelfIP cache invalidation to network-roam events. The
	// NetworkMonitor's OnChange fan-out delivers each subscriber
	// asynchronously - cheap to add and the detector handles
	// re-probing lazily on the next CountryFor / Detect call.
	if nm := a.autoConnect.NetworkMonitor(); nm != nil && a.selfIPDetector != nil {
		a.selfIPDetector.SubscribeNetworkChanges(nm)
	}

	// Wire the Pool rotator. Traffic-bytes are consumed for the
	// idle-aware decision; the active-state callback prevents rotation
	// from firing while the VPN is down. onRotate -> PickAndConnectActivePool
	// pulls a new member, tears the current tunnel, and reconnects.
	if a.poolRotator != nil {
		a.poolRotator.Start(
			func(poolID string) {
				if err := a.PickAndConnectActivePool(); err != nil {
					log.Printf("PoolRotator: rotation for %s failed: %v", poolID, err)
				}
			},
			a.poolTrafficSnapshot,
			func() bool {
				a.mu.RLock()
				defer a.mu.RUnlock()
				return a.connected
			},
		)
	}
}

// poolTrafficSnapshot reads the current tunnel's RX/TX byte counters
// for the rotator's idle-aware detection. Returns (0, 0) when no
// active protocol or when the protocol has no stats - either case is
// indistinguishable from "user is idle" which is the safe default
// (rotation may fire). When a real number comes back, the rotator can
// detect deltas across ticks.
func (a *App) poolTrafficSnapshot() (int64, int64) {
	a.mu.RLock()
	activeProto := a.activeProtocol
	proto, ok := a.protocols[activeProto]
	a.mu.RUnlock()
	if !ok {
		return 0, 0
	}
	st := proto.Status()
	return st.BytesRx, st.BytesTx
}

// applyKillSwitchSetting drives the new state machine in response to
// the user toggling KS. Used by both UpdateSettings and SetKillSwitch
// so both paths apply identical semantics:
//
//   - Enabled with active tunnel -> Arm (state ARMED, no firewall change
//     yet; an unexpected drop will EngageSinkhole automatically).
//   - Enabled with no tunnel BUT a configured connection -> ForceSinkhole
//     (state SINKHOLE, controller engages firewall NOW). Hardcore
//     semantics: user said block, we block.
//   - Enabled with no tunnel AND no configured connection -> nothing
//     to protect against. Stay IDLE. (User should configure a
//     connection before enabling KS.)
//   - Disabled (any state) -> Disarm. Transitions to IDLE which the
//     controller observes and Release()s. THIS IS THE NETWORK-RESTORE
//     PATH the user explicitly demanded must always work.
func (a *App) applyKillSwitchSetting(enabled bool) {
	if !enabled {
		a.ksManager.Disarm()
		return
	}
	switch {
	case a.connected:
		a.ksManager.Arm()
	case a.connections.Active() != nil:
		a.ksManager.ForceSinkhole("user enabled KS while disconnected")
	default:
		// No connections configured - nothing to enforce.
	}
}

// ============================================================================
// PRIVILEGED HELPER METHODS
// ============================================================================

// HelperStatus describes the current state of the privileged helper service.
type HelperStatus struct {
	Installed bool   `json:"installed"`
	Running   bool   `json:"running"`
	Platform  string `json:"platform"`
}

// GetHelperStatus returns the current privileged helper service state.
func (a *App) GetHelperStatus() *HelperStatus {
	return &HelperStatus{
		Installed: IsHelperInstalled(),
		Running:   IsHelperRunning(),
		Platform:  runtime.GOOS,
	}
}

// EnsurePrivilegedHelper checks if the helper is installed and running.
// If not installed, triggers a one-time admin prompt to install it.
func (a *App) EnsurePrivilegedHelper() error {
	if err := EnsureHelper(); err != nil {
		return fmt.Errorf("failed to ensure privileged helper: %w", err)
	}
	wailsRuntime.EventsEmit(a.ctx, "vpn:helper_status", a.GetHelperStatus())
	return nil
}

// InstallPrivilegedHelper installs the helper as a system service (one-time admin prompt).
func (a *App) InstallPrivilegedHelper() error {
	if err := InstallHelper(); err != nil {
		return fmt.Errorf("failed to install helper: %w", err)
	}
	log.Println("Privileged helper installed successfully")
	wailsRuntime.EventsEmit(a.ctx, "vpn:helper_status", a.GetHelperStatus())
	return nil
}

// UninstallPrivilegedHelper removes the helper system service.
func (a *App) UninstallPrivilegedHelper() error {
	if err := UninstallHelper(); err != nil {
		return fmt.Errorf("failed to uninstall helper: %w", err)
	}
	log.Println("Privileged helper uninstalled")
	wailsRuntime.EventsEmit(a.ctx, "vpn:helper_status", a.GetHelperStatus())
	return nil
}

// ============================================================================
// CONFIG EDITOR
// ============================================================================

// GetActiveConfigContent returns the raw config content for the active protocol
// of the active connection. Used by the frontend config editor.
func (a *App) GetActiveConfigContent() (string, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	conn := a.connections.Active()
	if conn == nil {
		return "", fmt.Errorf("no active connection")
	}

	cfg := conn.GetActiveConfig()
	if cfg == nil {
		return "", fmt.Errorf("no config for active protocol")
	}

	return cfg.ConfigContent, nil
}

// SaveActiveConfigContent saves edited config content for the active protocol.
// If connected, disconnects first, saves, then reconnects.
func (a *App) SaveActiveConfigContent(content string) error {
	a.mu.Lock()

	conn := a.connections.Active()
	if conn == nil {
		a.mu.Unlock()
		return fmt.Errorf("no active connection")
	}

	cfg := conn.GetActiveConfig()
	if cfg == nil {
		a.mu.Unlock()
		return fmt.Errorf("no config for active protocol")
	}

	wasConnected := a.connected
	protocol := a.activeProtocol
	a.mu.Unlock()

	// Disconnect if currently connected
	if wasConnected {
		log.Println("Config editor: disconnecting before config change")
		a.Disconnect()
	}

	// Update the config
	a.mu.Lock()
	cfg.ConfigContent = content

	// Re-configure the protocol handler with new content
	if proto, ok := a.protocols[cfg.Protocol]; ok {
		if err := proto.Configure([]byte(content)); err != nil {
			a.mu.Unlock()
			return fmt.Errorf("invalid config: %w", err)
		}
	}

	a.connections.Save()
	a.mu.Unlock()

	log.Printf("Config editor: saved %d bytes for %s", len(content), protocol)

	// Reconnect if was connected
	if wasConnected {
		log.Println("Config editor: reconnecting with new config")
		a.Connect(protocol)
	}

	return nil
}

// ============================================================================
// LOGS & DIAGNOSTICS
// ============================================================================

// GetLogs returns the merged tail of every log file the app writes,
// prefixed with a source tag so the user can tell at a glance which
// daemon produced the line. Mirrors Android LogsScreen which merges
// app-event log + charon (strongSwan) log into one view.
func (a *App) GetLogs() []string {
	return getMergedLogs(500)
}

// ClearLogs truncates all Privycs-written log files. Does NOT touch
// external daemon logs we don't own (charon, wg, etc.). Called from the
// LogsView "Clear" button.
func (a *App) ClearLogs() error {
	return clearLogs()
}

// PickBackupSavePath asks the OS for a save-file dialog and returns the
// path the user chose. Empty string means the user cancelled — caller
// MUST treat that as "no-op" rather than "error" so the UI doesn't
// flash an error toast on cancel. Default name "privycs-backup.json"
// matches the Android convention so cross-device users see familiar
// filenames.
func (a *App) PickBackupSavePath() (string, error) {
	return wailsRuntime.SaveFileDialog(a.ctx, wailsRuntime.SaveDialogOptions{
		Title:           "Export Privycs Backup",
		DefaultFilename: "privycs-backup.json",
		Filters: []wailsRuntime.FileFilter{
			{DisplayName: "Privycs Backup (*.json)", Pattern: "*.json"},
		},
	})
}

// PickBackupOpenPath asks the OS for an open-file dialog. Same semantics
// as PickBackupSavePath: empty = cancelled, not an error.
func (a *App) PickBackupOpenPath() (string, error) {
	return wailsRuntime.OpenFileDialog(a.ctx, wailsRuntime.OpenDialogOptions{
		Title: "Import Privycs Backup",
		Filters: []wailsRuntime.FileFilter{
			{DisplayName: "Privycs Backup (*.json)", Pattern: "*.json"},
			{DisplayName: "All Files", Pattern: "*"},
		},
	})
}

// GetVersion returns the app version
func (a *App) GetVersion() string {
	return AppVersion
}

// PlatformFeatures describes which features are available on the current OS.
// The frontend uses this to disable toggles for unimplemented platform features
// so users aren't misled into thinking their traffic is protected when it isn't.
type PlatformFeatures struct {
	KillSwitchSupported  bool   `json:"kill_switch_supported"`
	AutoConnectSupported bool   `json:"auto_connect_supported"`
	AutostartSupported   bool   `json:"autostart_supported"`
	TraySupported        bool   `json:"tray_supported"`
	Platform             string `json:"platform"`
}

// GetPlatformFeatures returns which features are supported on this OS.
func (a *App) GetPlatformFeatures() *PlatformFeatures {
	return &PlatformFeatures{
		KillSwitchSupported:  true, // implemented for Linux (iptables), macOS (pf), Windows (netsh)
		AutoConnectSupported: true, // auto-connect on app start
		AutostartSupported:   true, // Linux (.desktop), macOS (LaunchAgent), Windows (Registry)
		TraySupported:        runtime.GOOS != "darwin",
		Platform:             runtime.GOOS,
	}
}

// ============================================================================
// INTERNAL HELPERS
// ============================================================================

func (a *App) availableProtocols() []string {
	var names []string
	for name, proto := range a.protocols {
		if proto.IsAvailable() {
			names = append(names, name)
		}
	}
	return names
}

// statusLocked returns status while already holding the lock.
// Must include all fields that the frontend needs to render correctly,
// especially ConnectionName (controls welcome vs connection screen).
func (a *App) statusLocked() *StatusResponse {
	resp := &StatusResponse{
		Connected:          a.connected,
		ActiveProtocol:     a.activeProtocol,
		AvailableProtocols: a.availableProtocols(),
		KillSwitchEnabled:  a.settings.KillSwitchEnabled,
		AutoConnectEnabled: a.autoConnect.IsRunning(),
	}
	if proto, ok := a.protocols[a.activeProtocol]; ok {
		s := proto.Status()
		resp.Connected = s.Connected || a.connected
		resp.ServerAddress = s.ServerAddress
		resp.LocalAddress = s.LocalAddress
		resp.BytesRx = s.BytesRx
		resp.BytesTx = s.BytesTx
		resp.LastHandshake = s.LastHandshake
	}
	if a.connected && !a.connectedAt.IsZero() {
		resp.ConnectedAt = a.connectedAt.Format(time.RFC3339)
		resp.Uptime = formatDuration(time.Since(a.connectedAt))
	}
	if conn := a.connections.Active(); conn != nil {
		resp.ConnectionName = conn.Name
		resp.ConnectionID = conn.ID
		resp.ConnectionProtocols = conn.AvailableProtocols()
	}
	return resp
}

// statusEmitter periodically sends status updates to the frontend via events.
// Includes panic recovery so a single Status() failure doesn't crash the emitter
// goroutine (which would leave the UI frozen without updates).
func (a *App) statusEmitter() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-a.stopStats:
			return
		case <-ticker.C:
			func() {
				defer func() {
					if r := recover(); r != nil {
						log.Printf("Status emitter recovered from panic: %v", r)
					}
				}()
				status := a.Status()
				wailsRuntime.EventsEmit(a.ctx, "vpn:status", status)
			}()
		}
	}
}

func formatDuration(d time.Duration) string {
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
}

func protocolDisplayName(name string) string {
	switch name {
	case "wireguard":
		return "WireGuard"
	case "openvpn":
		return "OpenVPN"
	case "ipsec":
		return "IPSec/IKEv2"
	default:
		return name
	}
}

func protocolDescription(name string) string {
	switch name {
	case "wireguard":
		return "Fast, modern VPN protocol with state-of-the-art cryptography"
	case "openvpn":
		return "Flexible protocol with TCP/UDP support, works behind restrictive firewalls"
	case "ipsec":
		return "Native OS support, enterprise standard IKEv2 protocol"
	default:
		return ""
	}
}

// sanitizeTunnelName converts a connection name into a safe tunnel/filename.
// "Office VPN" -> "office-vpn", "My Server (prod)" -> "my-server-prod"
// On Linux/macOS, interface names are limited to 15 characters (IFNAMSIZ).
// If truncation is needed, a 4-char hash suffix ensures uniqueness:
// "privycs-shielded" -> "pv-shielde-a1b2" (10 chars base + 4 char hash + dash)
func sanitizeTunnelName(name string) string {
	original := strings.ToLower(strings.TrimSpace(name))
	// Remove file extensions
	for _, ext := range []string{".conf", ".ovpn", ".sswan", ".mobileconfig"} {
		original = strings.TrimSuffix(original, ext)
	}
	// Replace spaces and unsafe chars with hyphens
	var result []byte
	for i := 0; i < len(original); i++ {
		c := original[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			result = append(result, c)
		} else if c == ' ' || c == '.' || c == '(' || c == ')' {
			if len(result) > 0 && result[len(result)-1] != '-' {
				result = append(result, '-')
			}
		}
	}
	// Trim trailing hyphens
	s := strings.TrimRight(string(result), "-")
	if s == "" {
		return "privycs0"
	}
	// Linux/macOS interface names are limited to 15 characters (IFNAMSIZ).
	// Append a 4-char hash of the full name to prevent collisions when truncating.
	if runtime.GOOS != "windows" && len(s) > 15 {
		hash := fmt.Sprintf("%x", crc32.ChecksumIEEE([]byte(s)))
		if len(hash) > 4 {
			hash = hash[:4]
		}
		// 15 - 1 (dash) - 4 (hash) = 10 chars for base name
		base := s[:10]
		base = strings.TrimRight(base, "-")
		s = base + "-" + hash
	}
	return s
}

// setTunnelName calls SetTunnelName on protocol handlers that support it.
func setTunnelName(proto VPNProtocol, name string) {
	type tunnelNamer interface {
		SetTunnelName(string)
	}
	if tn, ok := proto.(tunnelNamer); ok {
		tn.SetTunnelName(name)
	}
}
