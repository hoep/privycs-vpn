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

	// connectMu serializes connect attempts — taken with TryLock at
	// the entry of connectActiveTarget so a network-monitor flood
	// (Windows fires WLAN notification + address-change events
	// every 1-2 seconds) cannot spawn parallel Up() calls.
	// v0.9.13.6's mutex-split removed the implicit serialization
	// that a.mu provided in v0.9.11.7, exposing this race: each
	// concurrent Up() attempted to install a different
	// WireGuardTunnel$ Windows service against the same proto
	// singleton, racing on proto.ifaceName / proto.confPath via
	// setTunnelName. Symptom: "Tunnel already installed and running"
	// errors and 6+ orphan service-wait timeouts. v0.9.13.7 fix.
	connectMu sync.Mutex

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
	poolStates     *PoolStateRegistry
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
	// lastUserDisconnectAt tracks the wall-clock time of the most
	// recent App.Disconnect() call (= user clicked the big disconnect
	// button or the tray menu). poolKeepaliveLoop consults this so
	// a manual disconnect is not undone by the next 30s tick.
	// Zero value means "no manual disconnect since process start".
	// Mirrors Android's AlwaysOnDetector.lastUserDisconnect stamp,
	// just in-memory because desktop processes do not need to
	// persist this across restarts (a fresh app start is implicitly
	// "user wants to start fresh").
	lastUserDisconnectAt time.Time
	stopStats            chan struct{}
	forceQuit            bool           // set by tray Quit to bypass minimize-to-tray
	logFile              *os.File       // log file handle for proper cleanup on shutdown
	wg                   sync.WaitGroup // tracks background goroutines for clean shutdown
	// Periodic tunnel-liveness probe. Started after a successful
	// Connect, stopped from Disconnect / disconnectInternal. Closes
	// the "tunnel up but no traffic" gap that OpenVPN / IPSec do
	// not detect themselves.
	tunnelHealth *TunnelHealthMonitor
	// Per-network auto-tunnel rule list (Phase 2). Walked on every
	// NetworkMonitor tick; first matching rule drives the connect
	// lifecycle (overrides COD trigger/SSID logic when at least
	// one rule exists). Empty list = legacy COD behaviour.
	networkRules *NetworkRulesRegistry
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

	poolStates := NewPoolStateRegistry()
	return &App{
		protocols:          make(map[string]VPNProtocol),
		connections:        NewConnectionRegistry(),
		poolStates:         poolStates,
		pools:              NewPoolRegistry(poolStates),
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
		tunnelHealth:       NewTunnelHealthMonitor(),
		networkRules:       NewNetworkRulesRegistry(),
	}
}

// startup is called by Wails when the application starts
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx

	// Wire tunnel-health state events to the Vue frontend so the
	// ConnectionView traffic-light pill updates live without
	// polling. Vue listens via wailsRuntime.EventsOn.
	if a.tunnelHealth != nil {
		a.tunnelHealth.SetOnStateChange(func(s TunnelHealthState) {
			if a.ctx != nil {
				wailsRuntime.EventsEmit(a.ctx, "tunnelHealth:state", string(s))
			}
		})
	}

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

	log.Printf("Privycs VPN starting... version=%s os=%s arch=%s", AppVersion, runtime.GOOS, runtime.GOARCH)

	// Always clean up stale kill switch rules from previous crash/kill.
	// If the app was killed without graceful shutdown, iptables DROP rules
	// remain and block all traffic.
	NewKillSwitch().Deactivate()

	// Same idea for macOS-IPSec split-tunnel bypass routes installed
	// via /sbin/route. A crashed Privycs leaves them in the kernel
	// route table until reboot — scan our state files and ask the
	// helper to remove any whose connection is no longer Connected.
	// No-op on Linux/Windows.
	CleanupMacOSSplitRouteOrphans()

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
	{
		poolActiveID := ""
		poolCount := 0
		if a.pools != nil {
			poolActiveID = a.pools.ActiveID
			poolCount = len(a.pools.List())
		}
		singleActive := a.connections.Active()
		singleActiveName := ""
		if singleActive != nil {
			singleActiveName = singleActive.Name
		}
		log.Printf("Startup state: pools=%d active_pool_id=%q | connections=%d active_single=%q",
			poolCount, poolActiveID, len(a.connections.List()), singleActiveName)

		if a.pools != nil && a.pools.ActiveID != "" && a.pools.Get(a.pools.ActiveID) != nil {
			a.activePoolID = a.pools.ActiveID
			log.Printf("Startup: restored active pool %q", a.pools.Get(a.activePoolID).Name)

			// Mutual-exclusion guard: pool and single-connection
			// active flags must never both be set on disk. If they
			// are (corrupt persisted state from a prior failed
			// switch), the picker UI gets confused — vpn.status's
			// connection_id is non-empty AND poolStore.activePoolId
			// is non-empty, the listbox's `selectConnection` sees
			// `isSelected(connId)` returning true, and the early-
			// return short-circuits before the switch dispatches.
			// User v0.9.14.36 hit exactly this state. Pool wins
			// because it owns the running rotator + tunnel; we
			// silently clear the single's active marker so the
			// picker UI is back in a consistent state.
			if singleActive != nil {
				log.Printf("Startup: WARNING — both pool %q and single %q marked active on disk (corrupt mutual-exclusion state). Pool wins; clearing single's active marker.", a.activePoolID, singleActive.Name)
				a.connections.SetActive("")
			}
		} else if a.pools != nil && a.pools.ActiveID != "" && a.pools.Get(a.pools.ActiveID) == nil {
			// Stale ActiveID points at a deleted pool. Clear it so
			// next time this branch does not block the MRU fallback.
			log.Printf("Startup: pools.active_id %q is stale, clearing", a.pools.ActiveID)
			_ = a.pools.SetActiveID("")
		}
		if a.activePoolID == "" && a.connections.Active() == nil && len(a.connections.List()) > 0 {
			mru := mostRecentlyUsedConnection(a.connections.List())
			if mru != nil {
				a.connections.SetActive(mru.ID)
				log.Printf("Startup: auto-selected last-used connection %q", mru.Name)
			}
		}
	}

	// Load saved connections and configure the active protocol handler
	// so status checks and disconnect work even after app restart
	if conn := a.connections.Active(); conn != nil {
		if cfg := conn.GetActiveConfig(); cfg != nil {
			if proto, ok := a.protocols[cfg.Protocol]; ok {
				setTunnelName(proto, sanitizeTunnelName(conn.Name))
				proto.Configure(a.applyDnsOverride([]byte(cfg.ConfigContent), proto.Name()))
				a.activeProtocol = cfg.Protocol
				log.Printf("Restored active connection: %s (%s, tunnel: %s)", conn.Name, cfg.Protocol, sanitizeTunnelName(conn.Name))
			}
		}
	}

	// Start the pool rotator goroutine. Must run unconditionally
	// (NOT gated on COD-enabled) so Round-Robin pools rotate even
	// when the user has Connect-on-Demand off. Earlier versions
	// piggybacked the rotator on COD startup and rotation silently
	// never fired for COD-disabled users.
	a.startPoolRotator()

	// Pool keepalive loop. Process-lifetime goroutine that checks
	// every 30s whether the user has a pool active but no tunnel
	// up - if so, fires a reconnect via connectActiveTarget. Closes
	// the "pool stopped overnight" hole on desktop for users who
	// don't have COD enabled. The COD path already does this via
	// NetworkMonitor's platform-watcher subscriptions; the keepalive
	// loop is the no-COD fallback. 30s tick is cheap and matches the
	// existing safety-poll cadence.
	// Pool keepalive ticker DISABLED in v0.9.13.4 stability patch.
	// Was suspected as one of the cascade sources during the
	// "4 windows opening / closing" / "Connect doesn't work"
	// reports. With autoConnect / Connect-on-Demand / network
	// monitor / tunnel health all firing in parallel, an extra
	// 30s ticker that fires reconnect intents added to the
	// noise. Manual reconnect via Connect button is the user's
	// recovery path until we re-enable with rate-limiting.
	if false {
		go a.poolKeepaliveLoop()
	}

	// Pre-warm the SelfIP cache in the background so the first
	// user-facing operation (ActivatePool, Connect, picker switch)
	// does not stall behind the DoH probe chain (up to 3-8s on a
	// cold cache before the first endpoint responds). By the time
	// the user clicks anything, a.selfIPDetector.Cached() returns
	// a populated result and downstream paths read it instantly.
	//
	// Frontend toast: emit app:loading begin/done so the user sees
	// "Detecting your location..." instead of staring at a blank-ish
	// screen for the few seconds the DoH chain takes on cold cache.
	if a.selfIPDetector != nil {
		go func() {
			a.emitLoading("geo-detect", 0, 0, "")
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			c := a.selfIPDetector.CountryFor(ctx)
			if c != "" {
				log.Printf("App: SelfIP pre-warm complete, country=%s", c)
			}
			a.emitLoading("geo-detect-done", 0, 0, c)
		}()
	}

	// Backfill empty pool-member country fields from the MMDB. Pools
	// imported before v0.9.11.9 (when the MMDB schema bug shipped)
	// have country="" for every member; this background pass repairs
	// them in place so flag rendering, Geo-Nearest, and Pool-Detail
	// country lookups all start working without re-import.
	go a.backfillPoolCountries()

	// Wire pool rotator with the restored active pool, if any.
	// Also backfill RestrictRegions for pools created before v0.9.11.13
	// landed - any pool with an empty restriction list gets pinned
	// to the user's home region so the next Connect does not pinball
	// across continents. This runs in a goroutine so the SelfIP DoH
	// probe (up to 3-8s on slow networks) does not block startup.
	if a.activePoolID != "" && a.pools != nil && a.poolRotator != nil {
		if p := a.pools.Get(a.activePoolID); p != nil {
			a.poolRotator.SetActivePool(p)
			if len(p.RestrictRegions) == 0 {
				go a.autoRestrictRoundRobinToHomeRegion(p)
			}
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
		a.autoConnect.Start(func() { a.connectActiveTarget() }, func() bool {
			a.mu.RLock()
			defer a.mu.RUnlock()
			return a.connected
		})
	}

	// macOS sleep/wake awareness. NSWorkspace dispatches will-sleep
	// and did-wake to us so we can force-reconnect after the system
	// returns from suspend. Without this, on wake the IKE_SA stays in
	// ESTABLISHED state but the upstream NAT mapping has expired, so
	// packets black-hole through stuck routes until charon's DPD
	// (~90 s) or tunnel-health ICMP (~60 s) catches up. With this hook
	// the recovery starts within ~1 s of wake.
	//
	// The handlers fire even when no tunnel is up (idempotent — the
	// reconnect path no-ops on a disconnected app). Toggle is
	// AppSettings.ReconnectOnSystemWakeEnabled (default ON, *bool nil
	// → on). No-op on Linux/Windows.
	RegisterMacOSPowerEvents(
		func() { a.handleSystemWillSleep() },
		func() { a.handleSystemDidWake() },
	)

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

	// Emit the bootstrap snapshot so the frontend renders pool /
	// connection state on its very first frame, without waiting for
	// the four-IPC poolStore.refresh() round-trip. Both ConnectionView
	// and the early-mount listener in main.ts subscribe to this -
	// stale-event safety: BootstrapState() is also exposed as a
	// callable so a late-mounted view that missed the event fetches
	// the same payload synchronously.
	if a.ctx != nil {
		wailsRuntime.EventsEmit(a.ctx, "pool:bootstrap", a.BootstrapState())
	}

	// v0.9.14.8: clear out leaked WireGuardTunnel$pool-* services from
	// previous sessions. User found 15 such services accumulated in
	// their Windows Services list — each one leaks a wintun adapter
	// slot and after enough leaks every new install fails with
	// EXIT_CODE 5010 (DLL_INIT_FAILED). Detached so startup is not
	// blocked on the helper IPC roundtrip.
	go a.cleanupOrphanPoolServices("")

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

	// v0.9.14.11 — force-teardown ALL pool-* services on shutdown.
	// proto.Down() above only kills proto.ifaceName (the LAST iface
	// configured); leftover services from failed-attempts and
	// earlier rotations stayed up across Quit, blocking the next
	// App-start's connect attempts. User reported "beim quit wurde
	// der tunnel nicht beendet" with WhatsMyIP showing an orphan
	// Mullvad UK IP after a session that had cycled through many
	// pool members. forceUninstallAllPoolServices uninstalls every
	// WireGuardTunnel$pool-* regardless of state (RUNNING included).
	// Single-connection services (`WireGuardTunnel$<conn-name>`)
	// are NOT touched — the prefix filter guarantees that.
	a.forceUninstallAllPoolServices()

	// Final safety net: legacy PrivycsKS-* cleanup AND a best-effort
	// Privycs-Sinkhole-* cleanup in case the controller missed something.
	// Both are idempotent, log warnings only on failure.
	a.killSwitch.Disable()

	// Wait for background goroutines (status emitter, tray) to exit.
	// They listen on stopStats which was closed above.
	a.wg.Wait()

	// Stop the pool-state flusher and force a final save so any
	// in-flight runtime mutations (active member, pending member,
	// unreachable flags) hit disk before exit. Without this a
	// shutdown within the 500ms debounce window loses the latest
	// state.
	if a.poolStates != nil {
		a.poolStates.Stop()
	}

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
//
// Return value semantics: prevent=true blocks the close, prevent=false lets it
// proceed.
//
// macOS: both the red close button and the AppMenu's ⌘Q route through
// this same beforeClose hook in Wails v2. v0.9.14.20 attempted to make
// the red button hide-only and reserve ⌘Q for real quit, but the AppMenu
// Quit goes through the same Wails Quit path and re-enters beforeClose,
// so the hide branch caught BOTH and the user could not actually quit
// ("beenden beendet die app NICHT", reported on v0.9.14.23). Reverted
// to "any close attempt quits" which matches the convention of every
// production Mac VPN app (Mullvad, Tailscale, ProtonVPN). There is no
// system tray on macOS (TraySupported=false) so a hidden-but-running
// app would have no UI handle anyway.
func (a *App) beforeClose(ctx context.Context) (prevent bool) {
	if a.forceQuit {
		log.Println("Force quit — closing app")
		return false
	}
	if runtime.GOOS == "darwin" {
		log.Println("Mac: closing app on window close / ⌘Q")
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
	ServerCountryCode   string   `json:"server_country_code,omitempty"` // ISO 3166-1 alpha-2 (e.g. "IT")
	ServerCity          string   `json:"server_city,omitempty"`         // best-effort from pool-member name pattern
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
		ServerCountryCode:   a.resolveServerCountry(protoStatus.ServerAddress),
		ServerCity:          a.resolveServerCity(connName),
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
		log.Printf("Connect: REFUSED - kill switch sinkhole active")
		return nil, fmt.Errorf("kill switch active — toggle Kill Switch off in Settings to release the sinkhole")
	}
	log.Printf("Connect: entry (protocol arg=%q, current active=%q)", protocol, a.activeProtocol)

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
		log.Printf("Connect: REFUSED - pause active")
		return nil, fmt.Errorf("VPN paused — click Resume now in the pause banner to release")
	}

	// Switch protocol if specified
	if protocol != "" && protocol != a.activeProtocol {
		if _, ok := a.protocols[protocol]; !ok {
			return nil, fmt.Errorf("unknown protocol: %s", protocol)
		}
		// Tear down the currently-active protocol BEFORE switching the
		// pointer. Without this two protocols can run in parallel: the
		// previous Up()'s native session (e.g. ics-openvpn UDP socket
		// + management thread) keeps sending keepalives to the server,
		// the new Up() establishes its own tunnel, and the server's
		// connection list shows BOTH as connected forever. Observed
		// 2026-05-07 with Peter-Android-Shielded reporting connected
		// via OpenVPN AND IPSec simultaneously even though only one
		// was actively routing user traffic — the other one was a
		// zombie native session leftover from a previous Connect()
		// with a different protocol arg. SwitchConnectionProtocol
		// (further down in this file) already does the right thing
		// via disconnectInternal(); the missing piece was here.
		if a.connected && a.activeProtocol != "" {
			if prevProto, ok := a.protocols[a.activeProtocol]; ok {
				downCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				if err := prevProto.Down(downCtx); err != nil {
					log.Printf("Connect: warning during pre-switch teardown of %s: %v", a.activeProtocol, err)
				}
				cancel()
				a.connected = false
			}
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
		log.Printf("Connect: REFUSED - protocol %s not available on this system", a.activeProtocol)
		return nil, fmt.Errorf("%s is not available on this system", a.activeProtocol)
	}

	// Check if already connected. Stale Windows-WG service state
	// from a previous run can produce a false-positive "Connected"
	// here that short-circuits the new connect attempt - tunnel
	// "running" per status query but no traffic actually flowing.
	// Logged explicitly so user-reports of "Connect doesn't work"
	// can be diagnosed via the log line.
	currentStatus := proto.Status()
	log.Printf("Connect: pre-check Status().Connected=%v for %s", currentStatus.Connected, a.activeProtocol)
	if currentStatus.Connected {
		log.Printf("Connect: SHORT-CIRCUIT - Tunnel already running via %s, treating as connected (no new Up() call)", a.activeProtocol)
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
		// Log the full error chain BEFORE emitting frontend events —
		// the previous code went straight to EventsEmit/Notify, which
		// surfaced the error to the user but never wrote it to the log
		// file. That made post-mortem debugging impossible: tail -f
		// privycs-vpn.log saw "Using privileged helper for wg-quick
		// up X" and then nothing, while the actual wg-quick stderr
		// (passed back via helper IPC) was discarded.
		log.Printf("Connect: %s.Up FAILED: %v", activeProto, upErr)

		// Multi-protocol failover: if the connection has alternate
		// protocols configured, try them in order before surfacing
		// the failure to the user. This implements the user-expected
		// "if one protocol fails, try the next" behaviour that was
		// missing pre-v0.9.14.66. Failover only fires for connections
		// with >1 protocol; single-protocol connections still get the
		// original error path. tryFailoverProtocol commits the new
		// activeProtocol on success and returns the protocol name.
		if successProto, ferr := a.tryFailoverProtocol(activeProto); ferr == nil {
			log.Printf("Connect: failover succeeded via %q after %q failed", successProto, activeProto)
			Notify("VPN connection failed over",
				fmt.Sprintf("%s could not start; connected via %s instead", activeProto, successProto),
				NotifyInfo)
			return a.statusLocked(), nil
		} else {
			log.Printf("Connect: failover unsuccessful: %v — surfacing original %s error", ferr, activeProto)
		}

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
	//
	// Poll interval was 3s, dropped to 250ms in v0.9.11.38 because the
	// 3s gap between checks meant a healthy WireGuard tunnel that came
	// up in 200 ms still got at minimum 3 s of "connected=false" leaking
	// to the statusEmitter, which the frontend rendered as "00:00:00
	// disconnected" for that whole window. 250 ms keeps the syscall
	// load trivial (max 4 calls/s) and detects a healthy connect within
	// half a second average.
	const connectTimeout = 30 * time.Second
	const pollInterval = 250 * time.Millisecond
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

		// Failover on tunnel-didn't-come-up: same logic as the Up()
		// error path above. The protocol's daemon either started but
		// stalled (OpenVPN TLS hang, IPSec SA negotiation timeout) or
		// the kernel side never registered. Try alternate protocols
		// before giving up.
		if successProto, ferr := a.tryFailoverProtocol(activeProto); ferr == nil {
			log.Printf("Connect: failover succeeded via %q after %q timed out", successProto, activeProto)
			Notify("VPN connection failed over",
				fmt.Sprintf("%s timed out; connected via %s instead", activeProto, successProto),
				NotifyInfo)
			return a.statusLocked(), nil
		} else {
			log.Printf("Connect: failover unsuccessful after timeout: %v", ferr)
		}

		errMsg := fmt.Sprintf("%s tunnel did not come up within %v — check logs", activeProto, connectTimeout)
		wailsRuntime.EventsEmit(appCtx, "vpn:error", errMsg)
		Notify("VPN connection timed out", errMsg, NotifyError)
		return nil, fmt.Errorf("%s", errMsg)
	}

	a.connected = true
	a.connectedAt = time.Now()

	// Persist the runtime-assigned VPN IP back to the connection
	// registry so the Configs page can show it after reload, even
	// before the next connect. WireGuard's Address is static (parsed
	// from the .conf at import) and was already persisted; OpenVPN +
	// IPSec only learn their inner IP after IKE_AUTH/TLS so we update
	// here. Empty status.LocalAddress (no virtual IP pushed by
	// server) leaves the previous value intact rather than wiping it.
	if status := proto.Status(); status.LocalAddress != "" {
		if conn := a.connections.Active(); conn != nil {
			for idx, pc := range conn.Protocols {
				if pc.Protocol == activeProto && pc.LocalAddress != status.LocalAddress {
					conn.Protocols[idx].LocalAddress = status.LocalAddress
					a.connections.Save()
					break
				}
			}
		}
	}

	// Tunnel-liveness monitor: 60s ICMP probe to a known reliable
	// target. Mode resolution (matches Android):
	//   - "off":    never run
	//   - "always": run + recovery (auto disconnect/reconnect)
	//   - "auto":   run + recovery for pool AND single (default;
	//               recovery for single is also a disconnect+
	//               reconnect via connectActiveTarget below)
	if a.tunnelHealth != nil {
		mode := a.settings.TunnelHealthMode
		if mode == "" {
			mode = "auto"
		}
		isPool := a.activePoolID != ""
		shouldRun := mode != "off"
		// v0.9.14.2: explicit gating-decision log so the user can
		// diagnose "no healthy dot" without code-tracing. The three
		// inputs uniquely determine the decision; if shouldRun=false
		// you read the line and immediately know whether to flip mode
		// off "off".
		log.Printf("TunnelHealth: gating mode=%q isPool=%v shouldRun=%v target=%q",
			mode, isPool, shouldRun, a.settings.TunnelHealthTarget)
		if shouldRun {
			target := a.settings.TunnelHealthTarget
			// v0.9.14.7: Recovery callback re-enabled. v0.9.13.4
			// disabled it because of a cascade interaction with
			// poolKeepaliveLoop + notify-on-transition that
			// produced the "4 modal windows opening and closing"
			// symptom. Two of those guards are now in place:
			//   - poolKeepaliveLoop is `if false` disabled
			//     (separate ticker that double-fired connects)
			//   - connectMu in connectActiveTarget serialises
			//     concurrent connect intents (v0.9.13.7) so
			//     recovery cannot race with a network-monitor
			//     trigger or a manual user tap
			// User-reported overnight symptom: pool shows "connected"
			// but tunnel is actually dead because the no-op recovery
			// never reset a.connected when ICMP probes died, and
			// PickAndConnectActivePool's "wasConnected" path then
			// no-ops on a phantom-running tunnel. Re-enabling the
			// real recovery closes that hole.
			a.tunnelHealth.Start(target, func() {
				log.Printf("TunnelHealth: recovery triggered — tunnel dead per ICMP probe, disconnecting + trying failover")
				// Tear down the stale connected state so the
				// reconnect goes through the normal Up path
				// instead of short-circuiting on Status().Connected.
				a.mu.Lock()
				deadProto := a.activeProtocol
				if err := a.disconnectInternal(); err != nil {
					log.Printf("TunnelHealth: recovery disconnect: %v", err)
				}
				a.mu.Unlock()
				// Brief settle delay so wintun.sys/NDIS teardown
				// completes before the new Up() races with the
				// release. 2s is empirically the minimum on
				// Windows for a clean restart.
				time.Sleep(2 * time.Second)

				// Failover-first recovery: blindly reconnecting with
				// the same protocol that just died is rarely the
				// right move — if ICMP through the tunnel is broken,
				// the protocol's transport (or the server's exit) is
				// likely the problem. Try an alternate protocol
				// first; only if the connection is single-protocol or
				// failover itself fails do we fall back to the
				// original-protocol reconnect via connectActiveTarget.
				a.mu.Lock()
				if successProto, ferr := a.tryFailoverProtocol(deadProto); ferr == nil {
					log.Printf("TunnelHealth: recovery via failover %s → %s", deadProto, successProto)
					a.mu.Unlock()
					return
				} else {
					log.Printf("TunnelHealth: failover not viable (%v), falling back to same-protocol reconnect", ferr)
				}
				a.mu.Unlock()

				// connectActiveTarget respects connectMu, COD pause,
				// kill switch sinkhole, and the configured rule
				// resolution, so this single call is the right
				// drop-in for "redo whatever the user's intent
				// currently is".
				a.connectActiveTarget()
			})
		}
	}

	// Pool rotator: reset the rotation countdown to start fresh from
	// THIS moment. The countdown the user sees on the Connect screen
	// should begin at connect-time, not at pool-activation-time. Only
	// has an effect when activePoolID is set and the rotator has a
	// pool wired (i.e. Round-Robin with a known interval).
	if a.activePoolID != "" && a.poolRotator != nil {
		a.poolRotator.ResetSchedule()
	}

	// Tunnel verified up: arm the kill switch state machine so an
	// unexpected drop engages the sinkhole. Idempotent across all
	// states (IDLE -> ARMED, SINKHOLE -> ARMED, ARMED no-op). Only
	// triggered when the user has KS enabled in settings - if they
	// have it off, ksManager stays at IDLE and never engages.
	if a.settings.KillSwitchEnabled {
		a.ksManager.Arm()
	}

	// Prevent display + idle sleep while tunnel is up — opt-in via
	// PreventDisplaySleep. macOS-only; no-op shim on other platforms.
	// caffeinate child process gets reaped on stopCaffeinate() in
	// disconnectInternal or implicitly when the parent process exits.
	if a.settings.PreventDisplaySleep {
		startCaffeinate()
	}

	// Update connection last-used timestamp
	if conn := a.connections.Active(); conn != nil {
		conn.LastConnected = time.Now()
		a.connections.Save()
	}

	wailsRuntime.EventsEmit(appCtx, "vpn:connected", activeProto)
	log.Printf("Connected via %s", activeProto)

	// Immediately push a fresh status to the frontend instead of
	// waiting up to 2 s for the statusEmitter ticker. Pool rotations
	// in particular benefit: between disconnect (a.connected=false)
	// and the first post-connect tick, the UI used to render "00:00
	// disconnected" for up to 2 s after the OS tunnel was already up.
	wailsRuntime.EventsEmit(appCtx, "vpn:status", a.statusLocked())

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
	// Stamp the wall-clock so poolKeepaliveLoop can suppress the
	// next 30s tick. Without this, a user who clicks Disconnect
	// while a pool is the active selection saw the tunnel come
	// back up within ~10-30s as the keepalive ticker rediscovered
	// "pool active, not connected -> reconnect" - blind to the
	// fact that this disconnect was user-initiated, not a
	// drop-recovery scenario. Mirrors Android's
	// AlwaysOnDetector.stampUserDisconnect.
	a.lastUserDisconnectAt = time.Now()
	return a.disconnectInternal()
}

// disconnectInternal tears down the tunnel without locking (caller must hold lock)
func (a *App) disconnectInternal() error {
	proto, ok := a.protocols[a.activeProtocol]
	if !ok {
		return nil
	}

	// Stop the tunnel-liveness monitor first thing. The ping loop
	// would otherwise generate spurious failures against the
	// torn-down tunnel and the recovery callback would fire
	// disconnectInternal recursively (idempotent at the proto
	// level but log-noise we can avoid).
	if a.tunnelHealth != nil {
		a.tunnelHealth.Stop()
	}

	// Release the prevent-display-sleep assertion. Idempotent —
	// no-op if PreventDisplaySleep was off or caffeinate was never
	// started. Killed BEFORE the proto.Down so display can wake/sleep
	// normally during the brief teardown window even if Down hangs.
	stopCaffeinate()

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

	// Snapshot fields we need without the lock during the slow Down call.
	appCtx := a.ctx
	activeProtocol := a.activeProtocol

	log.Printf("Disconnecting %s...", activeProtocol)

	// Release a.mu around proto.Down. Down is slow on every protocol
	// (Windows: subprocess to privileged helper + NDIS teardown for
	// WireGuard, IKE-SA delete for IPSec, management-socket close for
	// OpenVPN — up to 5+ seconds in the worst case) and holding the
	// global app mutex during it freezes every UI IPC handler that
	// needs the lock (Status, ListConnections, GetSettings, ...).
	// proto.Down touches OS state via the protocol handler / privileged
	// helper; it does not touch App state. Re-acquire after.
	a.mu.Unlock()
	downErr := proto.Down(appCtx)
	a.mu.Lock()

	a.disconnecting = false

	if downErr != nil {
		log.Printf("Disconnect error: %v", downErr)
		// Earlier behaviour kept a.connected=true here on the theory
		// that the OS tunnel might still be up. Result was the user
		// got stuck with a Disconnect button that did nothing -
		// every retry returned the same error, app state never
		// reset. Now we mark app state disconnected regardless and
		// surface a notification so the user knows the OS tunnel
		// MAY still be running and they should run the cleanup
		// PowerShell if their public IP still shows the VPN exit.
		// User-recoverable beats user-stuck.
		wailsRuntime.EventsEmit(appCtx, "vpn:error", downErr.Error())
		Notify("VPN disconnect (with errors)",
			fmt.Sprintf("%s reported: %s. If your public IP still shows the VPN, run the cleanup script.",
				strings.ToUpper(activeProtocol), downErr.Error()),
			NotifyError)
		a.connected = false
		return nil
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

	wailsRuntime.EventsEmit(appCtx, "vpn:disconnected", activeProtocol)
	log.Println("Disconnected")
	Notify("VPN disconnected", fmt.Sprintf("%s tunnel closed", strings.ToUpper(activeProtocol)), NotifyInfo)

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
			proto.Configure(a.applyDnsOverride(configContent, proto.Name()))
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
	log.Printf("ImportConfig: protocol=%q len=%d filename=%q connName=%q connID=%q (waiting for mu)", protocol, len(content), filename, connectionName, connectionID)
	a.mu.Lock()
	log.Printf("ImportConfig: acquired mu (validation phase)")

	// Auto-detect protocol if not specified
	if protocol == "" {
		protocol = detectProtocol(content, filename)
		if protocol == "" {
			a.mu.Unlock()
			log.Printf("ImportConfig: cannot detect protocol")
			return fmt.Errorf("cannot detect protocol from file content")
		}
	}

	proto, ok := a.protocols[protocol]
	if !ok {
		a.mu.Unlock()
		log.Printf("ImportConfig: unsupported protocol %q", protocol)
		return fmt.Errorf("unsupported protocol: %s", protocol)
	}

	// Set tunnel name from connection name so each connection
	// gets its own config file and Windows service name
	tunnelName := sanitizeTunnelName(connectionName)
	if tunnelName == "" {
		tunnelName = sanitizeTunnelName(filename)
	}
	setTunnelName(proto, tunnelName)
	a.mu.Unlock()

	// CONFIGURE phase — NO LOCK. applyDnsOverride takes a.mu.RLock()
	// internally via resolveDnsOverride; if we still held the write
	// lock here Go's non-reentrant sync.RWMutex would self-deadlock
	// (ImportConfig holds Lock, applyDnsOverride wants RLock, RLock
	// blocks until Lock is released, but the same goroutine holds
	// it — process hangs forever with no error). Same release-then-
	// reacquire pattern as ActivateConnection above.
	log.Printf("ImportConfig: starting Configure phase (no lock)")
	if err := proto.Configure(a.applyDnsOverride([]byte(content), proto.Name())); err != nil {
		log.Printf("ImportConfig: proto.Configure FAILED: %v", err)
		return fmt.Errorf("invalid config: %w", err)
	}
	log.Printf("ImportConfig: proto.Configure ok, calling Status() for server-address extraction")

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

	// STATE-UPDATE phase — re-acquire the write lock for the registry
	// mutation + settings save.
	a.mu.Lock()
	defer a.mu.Unlock()

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
	// SNAPSHOT phase: lookup conn + protocol handler, run any
	// disconnect-on-active under the lock. THEN release the lock for
	// the slow Configure call. Holding a.mu through Configure was
	// what made the user's "click on connection list does nothing"
	// symptom — every UI IPC waiting on a.mu blocked while Configure
	// did its file write + setTunnelName mutations.
	a.mu.Lock()

	conn := a.connections.Get(id)
	if conn == nil {
		a.mu.Unlock()
		return fmt.Errorf("connection not found: %s", id)
	}

	// Mutual exclusion with the Pool layer: activating a single
	// clears any active pool, just like ActivatePool clears the
	// singles' activeID. The picker UI assumes only one of (pool,
	// single) drives the connection at a time. Persist the cleared
	// pool selection so a restart does not revive it.
	if a.activePoolID != "" {
		log.Printf("ActivateConnection[%s]: clearing previously-active pool %q for mutual-exclusion", id, a.activePoolID)
		a.activePoolID = ""
		if a.poolRotator != nil {
			a.poolRotator.SetActivePool(nil)
		}
		if a.pools != nil {
			_ = a.pools.SetActiveID("")
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
		a.mu.Unlock()
		return fmt.Errorf("protocol %s not configured for connection %s", selectedProtocol, conn.Name)
	}

	protoHandler, ok := a.protocols[selectedProtocol]
	if !ok {
		a.mu.Unlock()
		return fmt.Errorf("protocol handler not available: %s", selectedProtocol)
	}

	connName := conn.Name
	cfgContent := cfg.ConfigContent

	// Disconnect current tunnel if connected. disconnectInternal
	// itself releases the lock around the slow proto.Down call
	// (PATCH 1 in v0.9.13.6) so this no longer freezes the UI.
	if a.connected {
		if err := a.disconnectInternal(); err != nil {
			log.Printf("Warning: disconnect failed during activate: %v", err)
		}
	}
	a.mu.Unlock()

	// CONFIGURE phase — NO LOCK. setTunnelName + applyDnsOverride +
	// Configure all run without holding a.mu so concurrent UI calls
	// (Status, ListConnections, GetSettings, …) stay responsive.
	setTunnelName(protoHandler, sanitizeTunnelName(connName))

	if err := protoHandler.Configure(a.applyDnsOverride([]byte(cfgContent), protoHandler.Name())); err != nil {
		return fmt.Errorf("failed to configure: %w", err)
	}

	// STATE-UPDATE phase.
	a.mu.Lock()
	a.connections.SetActive(id)
	conn.ActiveProtocol = selectedProtocol
	a.activeProtocol = selectedProtocol
	a.settings.ActiveProtocol = selectedProtocol
	a.connections.Save()
	SaveSettings(a.settings)
	a.mu.Unlock()

	log.Printf("Activated connection: %s (%s)", connName, selectedProtocol)
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

		// CRITICAL: force-tear-down any tunnel that may have come up
		// during ActivateConnection's disconnectInternal release-lock
		// window. Race scenario, observed by user on v0.9.14.32/.33:
		//
		//   1. User has Pool A connected, picks Single B in dropdown.
		//   2. ActivateConnection clears activePoolID, then calls
		//      disconnectInternal which RELEASES a.mu around proto.Down
		//      (so the slow Down call doesn't freeze the UI).
		//   3. The Down call triggers a network-interface change, which
		//      NetworkMonitor picks up and dispatches as onChange →
		//      autoConnect → connectActiveTarget.
		//   4. connectActiveTarget reads a.activePoolID="" (already
		//      cleared) AND a.activeProtocol=<old pool member protocol>.
		//      Falls through to a.Connect(proto). The protocol handler
		//      still has the OLD pool-member config because Configure
		//      runs only AFTER disconnectInternal returns.
		//   5. Tunnel comes up with the wrong (old pool member's) config.
		//   6. ActivateConnection finally finishes its Configure phase,
		//      but the tunnel is already running on the old config.
		//   7. Our reconnect goroutine here would then short-circuit on
		//      Status().Connected=true and leave the user stuck on the
		//      wrong tunnel.
		//
		// Force-disconnect first guarantees Status().Connected=false at
		// our Connect() call site, so the real Up() with the new
		// single's config runs.
		a.mu.Lock()
		wasConnected := a.connected
		a.mu.Unlock()
		if wasConnected {
			log.Printf("SwitchActiveConnection: tunnel was up at reconnect time (likely race-reconnect with stale config) — tearing down before new Up")
			a.mu.Lock()
			if err := a.disconnectInternal(); err != nil {
				log.Printf("SwitchActiveConnection: pre-reconnect disconnect: %v", err)
			}
			a.mu.Unlock()
			time.Sleep(500 * time.Millisecond)
		}

		a.mu.RLock()
		curPool := a.activePoolID
		curProto := a.activeProtocol
		curActive := ""
		if act := a.connections.Active(); act != nil {
			curActive = act.ID + "/" + act.Name
		}
		a.mu.RUnlock()
		log.Printf("SwitchActiveConnection reconnect: target single=%q proto=%q activePool=%q (should be empty if switch took effect)", curActive, curProto, curPool)
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
	a.connectActiveTarget()
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
	// IPC call returns fast. Routes through connectActiveTarget so
	// pool-active users come back to their pool, not to a stale
	// single connection.
	go a.connectActiveTarget()
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

	if err := protoHandler.Configure(a.applyDnsOverride([]byte(cfg.ConfigContent), protoHandler.Name())); err != nil {
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
	oldName := conn.Name
	conn.Name = newName
	a.connections.Save()
	log.Printf("Connection renamed to: %s", newName)

	// macOS IPSec: the System-Settings-installed VPN profile keeps
	// the OLD UserDefinedName until the user reinstalls. Renaming on
	// our side creates a name-mismatch between Privycs and macOS that
	// nothing programmatic can fix on a non-MDM Mac. Fire a UX hint
	// pointing at the Profiles pane so the user can clean up the
	// stale-named profile (and a reconnect generates a fresh one with
	// the new name).
	if conn.GetProtocol("ipsec") != nil {
		macOSDeleteIPSecProfileHint(oldName, "renamed")
	}
	return nil
}

// GetTunnelHealthState returns the current tunnel-health state
// (inactive / healthy / degraded / recovering) for Vue to render
// the indicator pill. Vue calls this on mount + listens for
// tunnelHealth:state events for live updates.
func (a *App) GetTunnelHealthState() string {
	if a.tunnelHealth == nil {
		return string(TunnelHealthInactive)
	}
	return string(a.tunnelHealth.State())
}

// SetConnectionDnsOverride sets the per-connection DNS override.
// Empty string clears it (= inherit Settings global). The connect
// pipeline reads this via resolveDnsOverride which walks
// pool > connection > global.
func (a *App) SetConnectionDnsOverride(id string, dns string) error {
	conn := a.connections.Get(id)
	if conn == nil {
		return fmt.Errorf("connection not found: %s", id)
	}
	conn.DnsOverride = strings.TrimSpace(dns)
	a.connections.Save()
	if conn.DnsOverride == "" {
		log.Printf("Connection %s DNS override cleared (inherits Settings)", id)
	} else {
		log.Printf("Connection %s DNS override set: %s", id, conn.DnsOverride)
	}
	return nil
}

// DeleteConnection removes a saved connection
func (a *App) DeleteConnection(id string) error {
	// Snapshot the IPSec/macOS hint inputs before delete: once
	// connections.Delete returns, the SavedConnection is gone and we
	// can no longer read its Name or check whether it carried IPSec.
	var ipsecConnName string
	if conn := a.connections.Get(id); conn != nil {
		if conn.GetProtocol("ipsec") != nil {
			ipsecConnName = conn.Name
		}
	}

	if err := a.connections.Delete(id); err != nil {
		return err
	}

	if ipsecConnName != "" {
		macOSDeleteIPSecProfileHint(ipsecConnName, "deleted")
	}
	return nil
}

// RemoveProtocolFromConnection removes a single protocol config from a connection
func (a *App) RemoveProtocolFromConnection(connectionID string, protocol string) error {
	// IPSec-on-macOS leaves a System-Settings VPN profile behind that
	// no longer corresponds to anything in Privycs. Surface the hint
	// before clearing the protocol so the user can act while the
	// profile name is still meaningful to them.
	var ipsecConnName string
	if protocol == "ipsec" {
		if conn := a.connections.Get(connectionID); conn != nil {
			ipsecConnName = conn.Name
		}
	}

	if err := a.connections.RemoveProtocol(connectionID, protocol); err != nil {
		return err
	}

	if ipsecConnName != "" {
		macOSDeleteIPSecProfileHint(ipsecConnName, "removed IPSec from")
	}
	return nil
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
	// SNAPSHOT phase: capture prevs, swap a.settings, persist.
	// Side-effects below run AFTER the lock is released — they do
	// not touch App state directly, only OS resources (firewall,
	// registry) and external goroutines (NetworkMonitor). Holding
	// a.mu during nm.Reevaluate() in particular risked re-entrant
	// deadlocks: Reevaluate can synchronously dispatch the
	// connectFn callback which calls connectActiveTarget →
	// PickAndConnectActivePool → connectToPoolMember which
	// re-acquires a.mu. PATCH 5 of v0.9.13.6.
	a.mu.Lock()

	prevAutostart := a.settings != nil && a.settings.AutostartEnabled
	prevKS := a.settings != nil && a.settings.KillSwitchEnabled
	prevCOD := a.settings != nil && a.settings.ConnectOnDemand.Enabled
	wasConnected := a.connected

	a.settings = settings
	SaveSettings(settings)

	appCtx := a.ctx
	a.mu.Unlock()

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
	//               poll.
	switch {
	case !prevCOD && settings.ConnectOnDemand.Enabled:
		a.startOnDemandMonitoring()
	case prevCOD && !settings.ConnectOnDemand.Enabled:
		a.autoConnect.Stop()
		if wasConnected {
			go func() {
				if err := a.Disconnect(); err != nil {
					log.Printf("UpdateSettings: COD-off triggered disconnect failed: %v", err)
				}
				// v0.9.14.12: belt-and-suspenders pool-service sweep.
				// proto.Down() inside Disconnect targets proto.ifaceName
				// only — if the actual RUNNING service is from an
				// earlier rotation under a different iface name, Down
				// early-returns ("service not RUNNING under this name")
				// and leaves the tunnel up at OS level. User reported
				// "wenn ich connect on demand abschalte bleibt tunnel
				// verbunden" — exactly this scenario. Force-sweep
				// guarantees the user's explicit "off" intent reaches
				// the kernel. Single-connection services are protected
				// by the pool-* prefix filter.
				a.forceUninstallAllPoolServices()
			}()
		}
	case prevCOD && settings.ConnectOnDemand.Enabled:
		nm := a.autoConnect.NetworkMonitor()
		nm.UpdateSettings(&settings.ConnectOnDemand)
		// Force an immediate re-eval so the change takes effect
		// within ~1s rather than up to 60s (the safety-poll
		// interval). Safe to call without a.mu now — Reevaluate's
		// connectFn callback will acquire the lock fresh.
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

	wailsRuntime.EventsEmit(appCtx, "vpn:settings_changed", settings)
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

// connectActiveTarget routes a "connect whatever the user has
// selected" intent to the right path. Pool-active wins: if the user
// has a Pool selected, the call goes through PickAndConnectActivePool
// (which runs the pool policy + connectToPoolMember). Otherwise it
// falls through to the single-connection Connect with the saved
// active protocol.
//
// Why this helper exists: every auto-reconnect path on desktop
// (Connect-on-Demand, pause expiry, Resume Now, autostart, system-
// tray reconnect) used to call a.Connect(activeProtocol) directly,
// which is pool-blind. When the user activated a Pool, ActivatePool
// clears connections.SetActive("") so the single-connection slot
// returns nil; without this routing the auto-reconnect would either
// no-op (no active connection) or revive a stale single tunnel
// instead of the pool the user actually selected.
//
// Mirrors Android's pool-vs-single branch in NetworkMonitor /
// VpnPauseTimer / BootReceiver / Tile / Widget after the pool-aware
// Coordinator refactor in v0.9.11.57.
func (a *App) connectActiveTarget() {
	// Serialize concurrent connect attempts. Network monitor fires
	// "triggering connect" on every WLAN notification + address-
	// change event — Windows produces 6-8 such events in the first
	// few seconds after app start as the network stack settles.
	// While Connect's Up() phase runs (multi-second window before
	// the WireGuard service enters RUNNING and a.connected flips to
	// true), each subsequent event sees connected=false and queues
	// another connect. Without this gate, all 6-8 ran in parallel,
	// each installing a different WireGuardTunnel$<id> service
	// against the same shared proto singleton — race on
	// proto.ifaceName / proto.confPath via setTunnelName, "Tunnel
	// already installed and running" errors, orphan services.
	//
	// TryLock + skip is the right primitive (vs. queue): a queued
	// connect would fire after the current one finished but with
	// stale member selection. Skipping silently lets the current
	// connect complete and the next legitimate event triggers a
	// fresh Pick.
	if !a.connectMu.TryLock() {
		log.Printf("connectActiveTarget: another connect in progress, skipping")
		return
	}
	defer a.connectMu.Unlock()

	a.mu.RLock()
	poolID := a.activePoolID
	proto := a.activeProtocol
	a.mu.RUnlock()
	if poolID != "" {
		if err := a.PickAndConnectActivePool(); err != nil {
			log.Printf("connectActiveTarget: pool connect failed: %v", err)
		}
		return
	}
	if _, err := a.Connect(proto); err != nil {
		log.Printf("connectActiveTarget: connect failed: %v", err)
	}
}

// poolKeepaliveLoop is a process-lifetime ticker that recovers a
// connection-dropped pool tunnel under one specific condition:
// COD is enabled AND the user did not just manually disconnect.
//
// Why those gates exist:
//
//   - COD off: the user has chosen manual mode. Disconnect means
//     "stay off until I tap Connect". The pre-fix loop fired
//     blind every 30s and undid the user's manual disconnect
//     after 10-30s - the user-reported "obwohl on demand aus,
//     connected vpn pool nach 30s wieder zu vpn pool" glitch.
//     Now: skip entirely when COD is off.
//
//   - 30s post-disconnect cooldown: even with COD on, a user who
//     just clicked Disconnect deserves a 30-second window where
//     their intent ("off right now") wins over auto-recovery.
//     Mirrors Android's AlwaysOnDetector cooldown which
//     PoolKeepaliveWatcher already honors.
//
// What's left for the loop to do: catch the rare case where the
// tunnel drops without a corresponding network-change event (so
// NetworkMonitor's COD path missed it) AND COD is enabled AND the
// drop was not user-initiated. Laptop suspend/resume on some
// configurations, helper-process crashes, etc.
//
// Why a ticker instead of a NetworkMonitor callback: Desktop's
// NetworkMonitor's onChange already fires for normal network
// transitions; the ticker is a defense-in-depth backup, not the
// primary path.
//
// Honored stops:
//   - PauseManager.IsPaused: user clicked "pause for N min".
//   - KillSwitchManager sinkhole: connectActiveTarget would refuse,
//     bailing here avoids log noise.
func (a *App) poolKeepaliveLoop() {
	const recentDisconnectCooldown = 30 * time.Second
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
			a.mu.RLock()
			poolID := a.activePoolID
			connected := a.connected
			lastUserDc := a.lastUserDisconnectAt
			codEnabled := a.settings.ConnectOnDemand.Enabled
			a.mu.RUnlock()

			if poolID == "" || connected {
				continue
			}
			// COD-off gate: user wants manual mode.
			if !codEnabled {
				continue
			}
			// Recent-disconnect cooldown: honour user intent.
			if !lastUserDc.IsZero() && time.Since(lastUserDc) < recentDisconnectCooldown {
				continue
			}
			if a.pauseManager != nil && a.pauseManager.IsPaused() {
				continue
			}
			if a.ksManager != nil && a.ksManager.IsSinkholeActive() {
				continue
			}
			log.Printf("PoolKeepalive: pool %s active but not connected - firing reconnect", poolID)
			a.connectActiveTarget()
		}
	}
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
		a.connectActiveTarget()
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

	// Phase 2: rules engine is GATED behind a settings flag in
	// v0.9.13.4 because the v0.9.13.0..3 versions caused
	// connect-cascade and 4-modal-flicker reports. The
	// transition-guard fix in v0.9.13.3 reduced the symptom but
	// did not eliminate it on every user's machine. Until we
	// have a clean reproduction + fix, the engine is opt-in via
	// AppSettings.NetworkRulesEnabled (default false). Existing
	// rules persist on disk; users who want them active enable
	// the toggle in Settings.
	if a.settings.NetworkRulesEnabled {
		if nm := a.autoConnect.NetworkMonitor(); nm != nil && a.networkRules != nil {
			nm.SetRuleEngine(a.networkRules.Resolve, a.applyRuleResolution)
			log.Printf("Network rules engine: ENABLED")
		}
	} else {
		log.Printf("Network rules engine: disabled (opt-in via Settings)")
	}
}

// startPoolRotator brings the rotator goroutine up. Independent of
// Connect-on-Demand: the rotator drives Round-Robin pool rotation
// regardless of COD state. Earlier this lived inside
// startOnDemandMonitoring which only fired when COD was enabled, so
// users with Round-Robin pools but no COD never saw the rotation
// timer fire and never saw the "Next server in" countdown on the
// Connect screen.
//
// Idempotent - PoolRotator.Start handles double-call by replacing
// callbacks but not spawning a second goroutine.
func (a *App) startPoolRotator() {
	if a.poolRotator == nil {
		return
	}
	a.poolRotator.Start(
		func(poolID string) {
			if err := a.PickAndConnectActivePool(); err != nil {
				log.Printf("PoolRotator: rotation for %s failed: %v", poolID, err)
			}
		},
		func(poolID string) {
			// Pre-warm: 60s before rotation, pick the next member
			// and persist as PendingMemberID. UI surfaces "Next:
			// <name>" once this lands. Cleared on actual rotate.
			a.preWarmActivePool()
		},
		a.poolTrafficSnapshot,
		func() bool {
			a.mu.RLock()
			defer a.mu.RUnlock()
			return a.connected
		},
	)
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
		if err := proto.Configure(a.applyDnsOverride([]byte(content), proto.Name())); err != nil {
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

// statusEmitterInterval is the cadence at which Status() is polled
// + emitted to the frontend. v0.9.14.69 dropped from 2 s → 1 s so
// the connection-time + traffic readouts feel live (the visible
// uptime tick was perceptibly laggy at 2 s, and the speed sparkline
// jumped in 2 s steps which read as "stuttering" on fast tunnels).
// 1 s keeps syscall load trivial — Status() is a few in-process
// reads on Linux/macOS, ~300 ms of PowerShell on Windows in the
// disconnected idle case which is now gated by the
// "skip Status() when not connected" branch in Status() itself.
const statusEmitterInterval = 1 * time.Second

// statusEmitter periodically sends status updates to the frontend via events.
// Includes panic recovery so a single Status() failure doesn't crash the emitter
// goroutine (which would leave the UI frozen without updates).
func (a *App) statusEmitter() {
	ticker := time.NewTicker(statusEmitterInterval)
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

// handleSystemWillSleep is invoked from the NSWorkspace will-sleep
// notification (macOS only; no-op shim on other platforms). We log
// the event so post-mortem analysis can correlate sleep timing with
// any subsequent connection issues. We DO NOT pre-emptively
// disconnect — leaving the tunnel up across sleep is the right
// default since the kernel SA is already torn down by the time we
// could clean it up, and an explicit Down() here would be racing the
// OS pause anyway.
func (a *App) handleSystemWillSleep() {
	a.mu.RLock()
	connected := a.connected
	a.mu.RUnlock()
	log.Printf("PowerEvents: willSleep notification (connected=%v) — no pre-sleep teardown, relying on wake handler", connected)
}

// handleSystemDidWake is invoked from the NSWorkspace did-wake
// notification (macOS only). When ReconnectOnSystemWake is enabled
// (default ON) AND a tunnel was up before sleep, we force a clean
// disconnect+reconnect via connectActiveTarget. This catches the
// stuck-route / dead-NAT case described in the v0.9.14.63 commit
// message — kernel SA still says ESTABLISHED but the upstream NAT
// mapping has long expired, packets black-hole. connectActiveTarget
// is the same recovery path tunnel_health_monitor.go uses, so the
// behaviour is consistent across "wake-detected" and "ICMP-detected"
// recovery triggers.
//
// Brief settle delay before reconnect: macOS's network stack takes
// 500-1500 ms after wake to re-establish Wi-Fi association + DHCP
// lease. Reconnecting before that completes would just fail-fast on a
// fresh "no route to host" and the user would see two failed attempts
// instead of one successful one. 2 s empirically catches the slow
// case.
func (a *App) handleSystemDidWake() {
	if !a.settings.ReconnectOnSystemWakeEnabled() {
		log.Printf("PowerEvents: didWake — ReconnectOnSystemWake disabled, no action")
		return
	}
	a.mu.RLock()
	connected := a.connected
	a.mu.RUnlock()
	if !connected {
		log.Printf("PowerEvents: didWake — no tunnel was up, nothing to recover")
		return
	}
	log.Printf("PowerEvents: didWake — forcing reconnect after 2s settle delay")
	// Capture the active protocol BEFORE disconnect so we can decide
	// whether to do the macOS-IPSec-only charon-restart step. We
	// only do the heavy charon-restart for IPSec on macOS because
	// the swanctl backend's daemon-cached IKE_SA state goes stale
	// across long sleep — symptom (per user): "after lid close+open
	// IPSec hangs; must `ipsec restart` manually before reconnect
	// works". WireGuard / OpenVPN don't have a long-lived daemon
	// with cached state, so the standard disconnect+reconnect path
	// is enough. v0.9.14.88 fix.
	a.mu.RLock()
	activeProto := ""
	if conn := a.connections.Active(); conn != nil {
		activeProto = strings.ToLower(string(conn.ActiveProtocol))
	}
	a.mu.RUnlock()
	needsCharonRestart := runtime.GOOS == "darwin" && activeProto == "ipsec"

	go func() {
		time.Sleep(2 * time.Second)
		a.mu.Lock()
		if err := a.disconnectInternal(); err != nil {
			log.Printf("PowerEvents: wake-recovery disconnect: %v", err)
		}
		a.mu.Unlock()
		// Brief pause matching tunnel-health recovery's 2 s gap so
		// kernel-side teardown completes before the new Up() races
		// the release.
		time.Sleep(2 * time.Second)

		if needsCharonRestart {
			log.Printf("PowerEvents: macOS IPSec — restarting charon daemon to clear stale IKE_SA state")
			helperClient := NewHelperClient()
			resp, err := helperClient.SendCommand("macos_restart_charon", nil)
			if err != nil || !resp.Success {
				log.Printf("PowerEvents: charon-restart failed (continuing anyway): err=%v resp=%+v", err, resp)
			} else {
				log.Printf("PowerEvents: charon-restart OK: %s", strings.TrimSpace(resp.Output))
			}
			// Tiny extra settle delay after charon-restart so the
			// fresh daemon has time to load configs before our
			// connect attempt issues swanctl --initiate.
			time.Sleep(1 * time.Second)
		}
		a.connectActiveTarget()
	}()
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
