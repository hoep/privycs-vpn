package com.privycs.vpn.service

import android.content.Context
import android.content.Intent
import android.net.ConnectivityManager
import android.net.Network
import android.net.NetworkCapabilities
import android.net.NetworkRequest
import android.net.VpnService
import android.util.Log
import com.privycs.vpn.PrivycsApp
import com.privycs.vpn.data.models.VpnConnection
import com.privycs.vpn.data.models.VpnProtocol
import com.privycs.vpn.data.models.VpnStatus
import com.privycs.vpn.util.PrivycsLogger
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.filter
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.launch

/**
 * Central VPN manager that coordinates VPN connections across protocols.
 * Delegates to protocol-specific implementations (WireGuard, OpenVPN, IPSec).
 */
class VpnServiceManager private constructor(private val context: Context) {

    companion object {
        private const val TAG = "VpnServiceManager"

        @Volatile
        private var instance: VpnServiceManager? = null

        fun getInstance(context: Context): VpnServiceManager {
            return instance ?: synchronized(this) {
                instance ?: VpnServiceManager(context.applicationContext).also { instance = it }
            }
        }
    }

    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main)
    private val connectionRepo = PrivycsApp.instance.connectionRepository
    private val poolRepo = PrivycsApp.instance.poolRepository

    private val _status = MutableStateFlow(VpnStatus())
    val status: StateFlow<VpnStatus> = _status.asStateFlow()

    private val _isConnecting = MutableStateFlow(false)
    val isConnecting: StateFlow<Boolean> = _isConnecting.asStateFlow()

    val isConnected: Boolean
        get() = _status.value.connected

    // Zombie-VpnService defense (v0.9.15.23). Tracks whether THIS app
    // instance has ever brought a tunnel up to Connected. Set on every
    // disconnected→connected transition, cleared on the reverse and at
    // process start (= var default).
    //
    // The problem this guards against: a previous app/service crash
    // leaves Android's VpnService framework holding an open TUN fd
    // that the new app instance can't drive. isSystemVpnActive()
    // returns true (TRANSPORT_VPN capability present on the network),
    // refreshStatus() then mis-reconciles to connected=true via
    // markConnected, TunnelHealth pings 8.8.8.8 → fails 3/3 → triggers
    // recovery → disconnect, disconnect hangs (nothing in our
    // singletons to tear down), Watchdog resets, user taps connect,
    // teardownAllProtocols hits the same null-singletons → loop.
    //
    // With this flag: a fresh process that sees systemVpnActive=true
    // but weEstablishedTunnel=false treats it as a zombie. Force
    // cleanup (stopService + DISCONNECT intent) + report disconnected
    // to the UI. User can then tap Connect for a fresh tunnel.
    @Volatile
    private var weEstablishedTunnel: Boolean = false

    // Watchdog that force-clears _isConnecting after a ceiling timeout
    // so the spinner can NEVER get stuck indefinitely. Stored as a Job
    // so a new connect() or a status push with connected=true can
    // cancel and restart the timer cleanly.
    init {
        // Bridge ConnectCoordinator.state -> _isConnecting so all the
        // UI code that already observes vpnManager.isConnecting keeps
        // working without knowing the coordinator exists. Connecting
        // and Disconnecting both display as "spinner"; Connected and
        // Idle as "no spinner".
        scope.launch {
            com.privycs.vpn.util.ConnectCoordinator.state.collect { s ->
                val connecting = s is com.privycs.vpn.util.ConnectCoordinator.State.Connecting ||
                    s is com.privycs.vpn.util.ConnectCoordinator.State.Disconnecting
                if (_isConnecting.value != connecting) {
                    _isConnecting.value = connecting
                }
            }
        }
    }

    /**
     * Check if VPN permission has been granted.
     * Returns null if permission is granted, or an Intent to request it.
     */
    fun prepareVpn(): Intent? {
        return VpnService.prepare(context)
    }

    /**
     * v0.9.14.96 — non-error warning channel. Surfaces a soft
     * warning into the status flow so the UI can show a banner
     * without flipping connected=false. Used by post-connect
     * IPv6-leak-detection in PrivycsVpnService.connectIpSec when
     * the server narrowed our v6 traffic-selector during IKE_AUTH.
     * Updates only the `error` field; connected stays as-is.
     */
    fun emitWarning(text: String) {
        val cur = _status.value
        // Don't override a real connect-time error.
        if (cur.error != null && cur.error.isNotBlank()) {
            return
        }
        _status.value = cur.copy(error = "WARNING: $text")
    }

    /**
     * Connect using the active connection's active protocol.
     *
     * All the heavy lifting (Intent firing, state serialisation,
     * preemption of automated intents, cooldown + pause gating) now
     * lives in ConnectCoordinator. This method is a thin adapter
     * that validates the connection, sets up UI-facing status
     * optimistically, and hands control to the coordinator. The
     * coordinator's state flow is bridged into _isConnecting via
     * the observer started in init{}, so existing UI code that
     * watches isConnecting keeps working without changes.
     */
    fun connect(connectionId: String? = null) {
        // Pool mode wins. When a pool is the active selection the
        // user expects the connect button to bring up *the pool* —
        // i.e. pick a member via the pool's policy and connect to
        // that member, not to whatever single connection happens
        // to be in connectionRepo.activeId. The previous version
        // ignored poolRepository entirely and either errored with
        // "No connection selected" (when only a pool was set up)
        // or silently connected to the wrong target (when both a
        // pool and a connection were configured).
        //
        // We honor an explicit connectionId override (e.g. the
        // dropdown picker's switchActiveConnection path) by
        // skipping pool routing in that case — explicit beats
        // implicit.
        if (connectionId == null) {
            val activePoolId = poolRepo.registry.value.activeId
            val activePool = poolRepo.registry.value.pools.firstOrNull { it.id == activePoolId }
            if (activePoolId.isNotEmpty() && activePool != null) {
                PrivycsLogger.i(TAG, "connect() routing to pool path (poolId=$activePoolId)")
                com.privycs.vpn.util.AlwaysOnDetector.clearPause(context)

                // Tentative pool status so the UI has pool-name +
                // policy to render while the picker runs and the
                // tunnel establishes. The full status (active member
                // name, country, server endpoint, scheduled rotation
                // timestamp) is filled in by the service-side
                // broadcastPoolStatus call once pickAndConnect
                // succeeds. Mirrors the single-connection branch
                // below which also pre-fills a tentative status
                // before kicking off the connect intent.
                _status.value = VpnStatus(
                    connected = false,
                    connectionName = activePool.name,
                    connectionId = "pool:${activePool.id}",
                    poolId = activePool.id,
                    poolName = activePool.name,
                    poolPolicy = activePool.policy.name
                )

                // Pool now goes through ConnectCoordinator like
                // single-connection. The Coordinator fires
                // ACTION_POOL_CONNECT for us and applies the same
                // gates (Kill Switch sinkhole, system-revoke
                // cooldown, Always-On pause, manual pause) +
                // serialisation as the single path. Without this
                // unification, COD never fired for pool users
                // (NetworkMonitor's Coordinator handoff routed only
                // single connections) and the always-on / manual
                // pause flags were silently bypassed when the user
                // had a Pool selected.
                scope.launch {
                    val result = com.privycs.vpn.util.ConnectCoordinator.requestPoolConnect(
                        context,
                        com.privycs.vpn.util.ConnectCoordinator.IntentSource.USER,
                        activePoolId,
                        activePool.name,
                    )
                    if (result is com.privycs.vpn.util.ConnectCoordinator.Result.Error ||
                        result is com.privycs.vpn.util.ConnectCoordinator.Result.Gated
                    ) {
                        PrivycsLogger.w(TAG, "connect() pool rejected by coordinator: $result")
                        _status.value = _status.value.copy(error = "Pool connect rejected: $result")
                    }
                }
                return
            }
        }

        val connId = connectionId ?: connectionRepo.activeId
        val connection = connectionRepo.getById(connId)
        if (connection == null) {
            PrivycsLogger.w(TAG, "connect() called with no matching connection (id=$connId)")
            _status.value = VpnStatus(error = "No connection selected")
            return
        }

        val config = connection.getActiveConfig()
        if (config == null) {
            PrivycsLogger.w(TAG, "connect() connection '${connection.name}' has no config for ${connection.activeProtocol}")
            _status.value = VpnStatus(error = "No config for ${connection.activeProtocol.label}")
            return
        }

        PrivycsLogger.i(TAG, "connect '${connection.name}' via ${connection.activeProtocol}")
        // Explicit user connect cancels any active Always-On pause so
        // the OS resumes normal auto-reconnect behavior from this point.
        com.privycs.vpn.util.AlwaysOnDetector.clearPause(context)
        scope.launch {
            connectionRepo.setActive(connId)
            connectionRepo.updateLastConnected(connId)

            // Tentative status so the UI has connection-name + endpoint
            // to show while the tunnel establishes.
            _status.value = VpnStatus(
                connected = false,
                connectionName = connection.name,
                connectionId = connId,
                activeProtocol = connection.activeProtocol,
                serverEndpoint = config.serverAddress,
                localAddress = config.localAddress,
            )

            val result = com.privycs.vpn.util.ConnectCoordinator.requestConnect(
                context,
                com.privycs.vpn.util.ConnectCoordinator.IntentSource.USER,
                connection,
            )
            if (result is com.privycs.vpn.util.ConnectCoordinator.Result.Error ||
                result is com.privycs.vpn.util.ConnectCoordinator.Result.Gated
            ) {
                PrivycsLogger.w(TAG, "connect() rejected by coordinator: $result")
                _status.value = _status.value.copy(error = "Connection rejected: $result")
            }
        }
    }

    /**
     * Disconnect the active VPN connection.
     */
    fun disconnect() {
        PrivycsLogger.i(TAG, "disconnect requested")
        // Stamp the time stamp BEFORE the coordinator fires the intent
        // so if the system's Always-On START_STICKY respawn races our
        // service teardown, handleAlwaysOnReconnect sees a fresh
        // timestamp and flags Always-On as detected.
        com.privycs.vpn.util.AlwaysOnDetector.stampUserDisconnect(context)
        scope.launch {
            com.privycs.vpn.util.ConnectCoordinator.requestDisconnect(
                context,
                com.privycs.vpn.util.ConnectCoordinator.IntentSource.USER,
            )
            // Tentative UI wipe. The real transition to idle happens
            // when the service pushes updateStatus with connected=false
            // after teardown, which bridges into the coordinator's
            // markDisconnected() and back into our _status.
            _status.value = VpnStatus()
        }
    }

    /**
     * Switch the active protocol for the current connection.
     *
     * When already connected we must tear the current tunnel down AND wait
     * for PrivycsVpnService to fully destroy itself before firing the new
     * connect intent. Otherwise Android queues the new ACTION_CONNECT
     * against a service that has just called stopSelf(); its coroutine
     * scope gets cancelled in onDestroy and the new connect coroutine
     * aborts with "Connection failed: Job was cancelled". Surface bug that
     * hits routinely on IPSec -> WireGuard transitions because IPSec
     * teardown (charon deleteIKE_SA + UDP DELETE message) is slower than
     * the typical ~300 ms we had before the connect arrived.
     */
    fun switchProtocol(protocol: VpnProtocol) {
        val activeConn = connectionRepo.getActive() ?: return
        if (!activeConn.hasProtocol(protocol)) return

        val wasConnected = isConnected
        connectionRepo.setActiveProtocol(activeConn.id, protocol)

        if (!wasConnected) {
            _status.value = _status.value.copy(activeProtocol = protocol)
            return
        }
        performSwitch(activeConn.id)
    }

    /**
     * Pin a specific ProtocolConfig (by id) as active on the
     * currently-active connection and reconnect through it. Used
     * by the per-config protocol pill picker in the UI: when a
     * connection holds multiple configs (e.g. two WG endpoints
     * UDP+TCP), the user taps any pill to switch to that exact
     * config — the pill identity is the config-id, not the
     * protocol enum.
     */
    fun switchConfig(configId: String) {
        val activeConn = connectionRepo.getActive() ?: return
        val target = activeConn.getConfigById(configId) ?: return
        val wasConnected = isConnected
        connectionRepo.setActiveConfig(activeConn.id, configId)
        if (!wasConnected) {
            _status.value = _status.value.copy(activeProtocol = target.protocol)
            return
        }
        performSwitch(activeConn.id)
    }

    private fun performSwitch(activeConnId: String) {

        // Serialize disconnect -> wait -> connect on the manager's long-lived
        // scope (not the service's scope, which dies with the service).
        scope.launch {
            val intent = Intent(context, PrivycsVpnService::class.java).apply {
                action = PrivycsVpnService.ACTION_DISCONNECT
            }
            try {
                context.startService(intent)
            } catch (e: Exception) {
                PrivycsLogger.w(TAG, "switchProtocol: disconnect intent failed: ${e.message}")
            }

            // Wait for status.connected to flip false OR a 4s ceiling.
            // 4s covers IPSec + WG teardown with ample margin; WireGuard
            // typically <500ms, IPSec <2s. If the race runs short we
            // return sooner via first().
            try {
                kotlinx.coroutines.withTimeout(4000) {
                    _status.filter { !it.connected }.first()
                }
            } catch (_: Exception) {
                PrivycsLogger.w(TAG, "switchProtocol: disconnect wait timed out, connecting anyway")
            }

            // Pad to let Service.onDestroy() complete its scope.cancel()
            // AND let the previous protocol's native-side state finish its
            // teardown before the next VpnService.Builder.establish() grabs
            // the TUN slot. 150ms was not enough — the WireGuard GoBackend
            // goroutines, strongSwan charon IKE_SA_DELETE, and OpenVPN
            // subprocess exit all run asynchronously and routinely take
            // 500ms-1500ms. A too-short pad causes the next tunnel to
            // collide with the old one's lingering writes to /dev/tun,
            // manifesting as "connected" UI with zero app traffic reaching
            // the server (only keepalives visible in server status.log).
            // 1500ms covers the slowest case (charon) with margin.
            kotlinx.coroutines.delay(1500)

            connect(activeConnId)
        }
    }

    /**
     * Switch the ACTIVE CONNECTION (different VpnConnection entry,
     * potentially different server/credentials/protocol set).
     *
     * Reconnect policy:
     *  - Tunnel currently UP: tear it down, reconnect to the new
     *    connection. Mirrors switchProtocol's disconnect/wait/
     *    reconnect serialisation so the same races (charon
     *    IKE_SA_DELETE, WG GoBackend goroutines, OVPN management
     *    socket close) cannot collide a fresh tun fd into a still-
     *    teardown-running native plugin.
     *  - Tunnel DOWN but Connect-on-Demand wants a tunnel up on the
     *    current network (settings.connectOnDemand.enabled AND
     *    networkMonitor.networkState.shouldConnect): auto-connect
     *    with the new connection. Without this branch the COD
     *    auto-management would re-evaluate at next network event,
     *    leaving an inconsistent gap where the user thought they
     *    switched but the auto-state stayed unbound.
     *  - Tunnel DOWN and COD inactive: only persist setActive. User
     *    taps Connect when they want it.
     *
     * Returns true when a reconnect will be attempted (caller can
     * surface a Kill Switch warning toast in that case), false when
     * the call is purely a setActive. KS interaction unchanged: the
     * sinkhole gate in ConnectCoordinator refuses every reconnect
     * attempt while sinkhole is engaged; the new active id is still
     * persisted so toggling KS off later resumes with the right
     * connection.
     */
    fun switchActiveConnection(connectionId: String): Boolean {
        val current = connectionRepo.activeId
        if (current == connectionId) return false

        val wasConnected = isConnected
        // Pool ↔ single mutual exclusion (v0.9.15.24 fix). switchActivePool
        // already clears connectionRepo.setActive(""), but the inverse
        // was missing — switching FROM a pool TO a single connection
        // left poolRepo.activeId set, violating the
        // "never both set simultaneously" invariant from CLAUDE.md.
        // User-visible: Connect screen rendered the pool indicator
        // banner at the top alongside the single connection card,
        // and pill-row stayed on the pool's protocol set instead of
        // updating to the picked connection's. Clear pool here so
        // both StateFlows emit consistent values to the UI.
        val poolRepo = com.privycs.vpn.PrivycsApp.instance.poolRepository
        if (poolRepo.registry.value.activeId.isNotEmpty()) {
            scope.launch { poolRepo.setActiveId("") }
        }
        connectionRepo.setActive(connectionId)
        refreshStatus()

        if (wasConnected) {
            scope.launch {
                val intent = Intent(context, PrivycsVpnService::class.java).apply {
                    action = PrivycsVpnService.ACTION_DISCONNECT
                }
                try {
                    context.startService(intent)
                } catch (e: Exception) {
                    PrivycsLogger.w(TAG, "switchActiveConnection: disconnect intent failed: ${e.message}")
                }

                try {
                    kotlinx.coroutines.withTimeout(4000) {
                        _status.filter { !it.connected }.first()
                    }
                } catch (_: Exception) {
                    PrivycsLogger.w(TAG, "switchActiveConnection: disconnect wait timed out, connecting anyway")
                }

                // Native-side teardown grace - same rationale as switchProtocol.
                kotlinx.coroutines.delay(1500)

                connect(connectionId)
            }
            return true
        }

        // Not currently connected. Honour Connect-on-Demand: if the
        // user has COD enabled and the current network's rule says
        // the tunnel should be up, switching connection should bring
        // it up with the new connection rather than wait for the
        // next network event.
        val settings = com.privycs.vpn.PrivycsApp.instance
            .settingsRepository.getSettingsBlocking()
        if (!settings.connectOnDemand.enabled) return false

        val nm = com.privycs.vpn.service.NetworkMonitor.getInstance(context)
        nm.reevaluate()
        val ns = nm.networkState.value
        if (!ns.shouldConnect) return false

        PrivycsLogger.i(
            TAG,
            "switchActiveConnection: COD rule matches (${ns.ruleMatch}) - reconnecting with new connection",
        )
        scope.launch {
            // Brief delay so setActive's StateFlow update has propagated
            // before connect() reads connectionRepo.activeId.
            kotlinx.coroutines.delay(200)
            connect(connectionId)
        }
        return true
    }

    /**
     * Switch the active selection to a pool. Mirrors
     * switchActiveConnection but for pools. Called from every UI
     * path that picks a pool: PoolDetailHost.onActivate,
     * ConnectionsScreen.onTap, ConnectScreen picker.
     *
     * Behaviour:
     *   - Always clears single-connection active id (UI does not
     *     show stale single name + protocol pills under the pool
     *     card).
     *   - Always sets pool active id (poolRepository).
     *   - Always updates _status to a tentative pool status so the
     *     Connect screen reflects the new selection immediately,
     *     even if a connect intent does not fire here. Without this
     *     a pool pick from the dropdown left the UI showing the
     *     previously-active single connection name until the
     *     tunnel actually came up - the v0.9.11.59 user-reported
     *     "Frontend bleibt auf privycs shielded obelix" bug.
     *   - If currently connected, fires a USER-source disconnect
     *     via the Coordinator. NetworkMonitor's status-flow
     *     listener (see NetworkMonitor.start) then re-evaluates
     *     on the disconnected transition - if COD is enabled and
     *     rules match, it fires a fresh pool connect via the pool
     *     branch. If COD is disabled, the tunnel stays down and
     *     the user must tap the big Connect button to bring the
     *     pool up. This matches the user's mental model: "COD off
     *     = manual mode, nothing connects automatically; COD on =
     *     rules drive the lifecycle".
     *   - If NOT currently connected, calls
     *     NetworkMonitor.reevaluate() so COD can fire a pool
     *     connect right away if rules already match (instead of
     *     waiting for the next network event tick).
     *
     * Returns true if the active selection changed, false if it
     * was already this pool (idempotent re-tap).
     */
    fun switchActivePool(poolId: String): Boolean {
        val poolRepo = com.privycs.vpn.PrivycsApp.instance.poolRepository
        val activePool = poolRepo.registry.value.pools
            .firstOrNull { it.id == poolId } ?: return false
        val prevPool = poolRepo.registry.value.activeId
        val prevSingle = connectionRepo.activeId
        val isChange = prevPool != poolId || prevSingle.isNotEmpty()
        if (!isChange) return false

        val wasConnected = isConnected

        scope.launch {
            // Order: clear single FIRST so any state observer that
            // recomputes activeConnection sees null before pool
            // becomes active. Then set pool active.
            connectionRepo.setActive("")
            poolRepo.setActiveId(poolId)

            // Tentative pool status so the Connect screen and any
            // observers see pool-name + policy immediately, even
            // before a tunnel comes up. Always set, regardless of
            // wasConnected - the dropdown switch from single to
            // pool needs the UI to flip to the pool's name right
            // away so the user is not staring at the previous
            // single connection name during the disconnect.
            _status.value = VpnStatus(
                connected = false,
                connectionName = activePool.name,
                connectionId = "pool:${activePool.id}",
                poolId = activePool.id,
                poolName = activePool.name,
                poolPolicy = activePool.policy.name,
            )

            if (wasConnected) {
                // Disconnect-wait-reconnect pattern, mirroring
                // switchActiveConnection. v0.9.11.60 just fired
                // requestDisconnect via the Coordinator and let
                // NetworkMonitor's status-flow listener race the
                // new connect against the old tunnel's native-
                // side teardown. Result: ACTION_POOL_CONNECT
                // landed before WireGuard / OpenVPN / strongSwan
                // had fully released, the new tunnel never came
                // up cleanly, and the Coordinator sat in
                // Connecting until the 90s watchdog reset - the
                // user-visible "stuck spinner on pool dropdown
                // switch" bug. Now: explicit disconnect intent,
                // wait for status to reach disconnected, 1.5s
                // grace for native teardown, then explicit USER-
                // source pool connect through the Coordinator.
                val intent = Intent(context, PrivycsVpnService::class.java).apply {
                    action = PrivycsVpnService.ACTION_DISCONNECT
                }
                try {
                    context.startService(intent)
                } catch (e: Exception) {
                    PrivycsLogger.w(TAG, "switchActivePool: disconnect intent failed: ${e.message}")
                }

                try {
                    kotlinx.coroutines.withTimeout(4000) {
                        _status.filter { !it.connected }.first()
                    }
                } catch (_: Exception) {
                    PrivycsLogger.w(TAG, "switchActivePool: disconnect wait timed out, connecting anyway")
                }

                // Native-side teardown grace - same delay used by
                // switchActiveConnection and switchProtocol.
                kotlinx.coroutines.delay(1500)

                val result = com.privycs.vpn.util.ConnectCoordinator.requestPoolConnect(
                    context,
                    com.privycs.vpn.util.ConnectCoordinator.IntentSource.USER,
                    poolId,
                    activePool.name,
                )
                PrivycsLogger.i(TAG, "switchActivePool: post-disconnect pool connect -> $result")
            } else {
                // Not connected. If COD is enabled and current
                // network already matches rules, fire the
                // re-evaluation so the new pool gets connected
                // right away instead of waiting for the next
                // network event. With COD off this is a no-op
                // and the user will tap Connect manually.
                val codEnabled = com.privycs.vpn.PrivycsApp.instance
                    .settingsRepository.getSettingsBlocking()
                    .connectOnDemand.enabled
                if (codEnabled) {
                    val nm = com.privycs.vpn.service.NetworkMonitor.getInstance(context)
                    nm.reevaluate()
                }
            }
        }
        return true
    }

    /**
     * Update status from VpnService (called via service binding or broadcast).
     * Once the tunnel reports either connected=true or an error, clear the
     * connecting spinner - otherwise ConnectScreen keeps showing
     * "Connecting..." forever for IPSec (WG/OpenVPN clear this via the
     * polled connected=true the same way).
     */
    fun updateStatus(status: VpnStatus) {
        // While the kill switch sinkhole is engaged, the real tunnel
        // plugin (WireGuard, OpenVPN, strongSwan) will keep polling
        // and reporting connected=true from its own state object -
        // Android replaced its tun fd with the sinkhole's, but the
        // plugin doesn't know that until it tries to write and
        // gets EBADF. Rather than let those stale status pushes
        // leak into the UI (widget shows "Connected", app label
        // shows "Connected 0:42"), mask every push during
        // sinkhole as a clean disconnected status. The sinkhole
        // itself holds the block-all fd; the user sees the "Kill
        // Switch active" notification and a disconnected UI.
        if (com.privycs.vpn.util.KillSwitchManager.isSinkholeActive()) {
            val masked = VpnStatus(
                connected = false,
                connectionName = status.connectionName,
                connectionId = status.connectionId,
                activeProtocol = status.activeProtocol,
                uptime = 0L,
                rxBytes = 0L,
                txBytes = 0L,
                error = "Kill switch active",
            )
            _status.value = masked
            com.privycs.vpn.util.SpeedTracker.record(0L, 0L, connected = false)
            return
        }

        // Pool overlay. Underlying tunnel pollers (WG GoBackend, OVPN
        // management thread, strongSwan listener) build a fresh
        // VpnStatus from their own state and pass it here without any
        // pool-context awareness. If we just stored that fresh status
        // it would blow away poolName/activeMember/nextRotationAt on
        // every poll tick.
        //
        // Two-rule overlay:
        //   1. Caller supplied pool fields (status.poolId.isNotEmpty()):
        //      authoritative push from PrivycsVpnService's
        //      broadcastPoolStatus - store as-is. Don't read prevPool
        //      because it's STALE (we're about to replace it).
        //   2. Caller did NOT supply pool fields (typical tunnel-
        //      poller path): if a pool is active, overlay the pool
        //      fields from the previous status so live tunnel data
        //      (connected, uptime, rx/tx, endpoint) flows through
        //      while pool framing is preserved.
        val activePoolId = poolRepo.registry.value.activeId
        val finalStatus = when {
            // Authoritative pool push from service-side broadcast.
            status.poolId.isNotEmpty() -> status

            // Tunnel-poller push while a pool is active: overlay
            // pool fields from previous status.
            activePoolId.isNotEmpty() -> {
                val prevPool = _status.value
                status.copy(
                    connectionName = if (prevPool.poolName.isNotEmpty())
                        prevPool.poolName
                    else status.connectionName,
                    connectionId = if (prevPool.poolId.isNotEmpty())
                        "pool:${prevPool.poolId}"
                    else status.connectionId,
                    poolId = prevPool.poolId,
                    poolName = prevPool.poolName,
                    poolPolicy = prevPool.poolPolicy,
                    activeMemberName = prevPool.activeMemberName,
                    activeMemberCountry = prevPool.activeMemberCountry,
                    pendingMemberName = prevPool.pendingMemberName,
                    pendingMemberCountry = prevPool.pendingMemberCountry,
                    nextRotationAt = prevPool.nextRotationAt
                )
            }

            // No pool active: store as-is (single-connection path).
            else -> status
        }
        val prev = _status.value
        _status.value = finalStatus
        // Feed the sparkline tracker so the upload/download cards have
        // a speed history to render. Non-connected samples reset the
        // tracker so the sparkline flatlines immediately on disconnect
        // instead of holding a stale spike into the next session.
        com.privycs.vpn.util.SpeedTracker.record(
            status.rxBytes,
            status.txBytes,
            status.connected,
        )

        // Bridge actual-tunnel-state -> coordinator intent-state. The
        // coordinator needs to know when the tunnel actually reaches
        // Connected / returns to Disconnected so its state flow stays
        // in sync with reality. Only fire on transitions to avoid
        // redundant coordinator writes on every poll tick.
        if (status.connected && !prev.connected) {
            // First successful connect of this process: claim
            // ownership so a future refreshStatus() distinguishes our
            // live tunnel from a zombie from a previous instance.
            weEstablishedTunnel = true
            scope.launch {
                com.privycs.vpn.util.ConnectCoordinator.markConnected(status.connectionId)
            }
            // Tunnel is up: decide whether to start the periodic
            // ICMP-based liveness monitor based on user settings.
            //   - mode = "off": never run
            //   - mode = "auto" (default): run + recovery for pool
            //     AND single. Recovery for single is a disconnect+
            //     reconnect handled inside TunnelHealthMonitor.
            //   - mode = "always": same as auto today; kept for
            //     forward-compat / parity with desktop.
            val healthSettings = try {
                com.privycs.vpn.PrivycsApp.instance.settingsRepository
                    .getSettingsBlocking()
            } catch (_: Exception) { null }
            val healthMode = healthSettings?.tunnelHealthMode ?: "auto"
            val isPool = status.poolId.isNotEmpty()
            val shouldRun = healthMode != "off"
            if (shouldRun) {
                val target = healthSettings?.tunnelHealthTarget?.takeIf { it.isNotBlank() }
                    ?: ""
                val intervalSec = healthSettings?.tunnelHealthPingIntervalSec ?: 0
                val deadThreshold = healthSettings?.tunnelHealthDeadThreshold ?: 0
                if (target.isNotBlank()) {
                    TunnelHealthMonitor.start(target, intervalSec, deadThreshold)
                } else {
                    TunnelHealthMonitor.start(
                        intervalSec = intervalSec,
                        deadThreshold = deadThreshold,
                    )
                }
            } else {
                TunnelHealthMonitor.stop()
            }
        }
        // Kill Switch: defensively arm on every connected status
        // push, not only on the disconnected->connected transition.
        // The transition-only check missed several real cases:
        //  - User toggles Kill Switch ON while already connected
        //  - App process restarts while Always-On VPN holds the
        //    tunnel up (no transition event in the new process)
        //  - User toggles KS off-and-on during a connected session
        //    (disarm() drops to IDLE, but no re-arm fired because
        //    status.connected didn't change)
        // arm() is idempotent (no-op when already ARMED), so the
        // overhead of re-checking on every poll tick is negligible.
        if (status.connected) {
            scope.launch {
                val settings = com.privycs.vpn.PrivycsApp.instance
                    .settingsRepository.getSettingsBlocking()
                // Gate: arm() iff state is NOT already ARMED.
                // The previous gate was `!isArmed()` which returns false
                // for both ARMED and SINKHOLE states - which meant after
                // a successful reconnect out of the sinkhole the arm()
                // call was skipped, leaving state stuck in SINKHOLE
                // forever. UI continued to show the red shield "Kill
                // Switch active" even though the real tunnel was up,
                // because ConnectScreen reads
                // `state == SINKHOLE` to render the danger palette.
                // arm() is internally idempotent on ARMED (no-op) and
                // explicitly handles SINKHOLE -> ARMED, so any state
                // != ARMED is a safe trigger.
                if (settings.killSwitchEnabled &&
                    com.privycs.vpn.util.KillSwitchManager.state.value !=
                    com.privycs.vpn.util.KillSwitchManager.State.ARMED
                ) {
                    com.privycs.vpn.util.KillSwitchManager.arm()
                }
            }
        }
        if (!status.connected && prev.connected) {
            // Release ownership claim. A subsequent refreshStatus
            // that finds systemVpnActive=true now means it's either
            // a zombie we couldn't tear down or a third-party VPN,
            // not us.
            weEstablishedTunnel = false
            scope.launch {
                com.privycs.vpn.util.ConnectCoordinator.markDisconnected()
            }
            // Tunnel is down: stop the liveness monitor so its
            // background ping cycle does not fire false positives
            // against a torn-down tunnel.
            TunnelHealthMonitor.stop()
            // Kill Switch: decide whether this is an "expected" or
            // "unexpected" disconnect. User-initiated disconnects
            // (ConnectCoordinator.requestDisconnect with USER/WIDGET/
            // TILE source) already called KillSwitchManager.disarm()
            // before we see the transition, so the state here is
            // IDLE - nothing to do. If it's still ARMED when we
            // reach this point, the tunnel dropped unexpectedly
            // and we engage the sinkhole.
            val alwaysOn = com.privycs.vpn.util.AlwaysOnDetector.detected.value
            if (com.privycs.vpn.util.KillSwitchManager.isArmed() && !alwaysOn) {
                com.privycs.vpn.util.KillSwitchManager.engageSinkhole(
                    "tunnel drop while armed",
                )
            }
        } else if (status.error != null) {
            // A connect attempt failed before reaching connected=true.
            // Reset coordinator to Idle so subsequent intents aren't
            // blocked on a zombie Connecting state.
            scope.launch {
                com.privycs.vpn.util.ConnectCoordinator.markDisconnected()
            }
        }
    }

    // Note: the previous in-class connecting watchdog was removed in
    // v0.9.4. ConnectCoordinator.startWatchdog now owns the "force-
    // reset after 90s stuck Connecting" responsibility for ALL intent
    // sources, and _isConnecting is derived from the coordinator state
    // via the init{} bridge - so when the coordinator's watchdog
    // fires, the spinner clears automatically on the next state tick.

    /**
     * Refresh status by querying the service.
     *
     * Also does a ConnectivityManager reality check: if the system
     * reports an active VPN transport but our own status believes
     * we're disconnected (typical after process death + always-on
     * restart, where the UI singleton resets to defaults but the
     * tunnel is already live), reconcile our state so the UI shows
     * connected. Without this reconciliation the user would see a
     * "Connect" button that, when tapped, collides with the existing
     * tunnel and manifests as the stuck-spinner bug.
     */
    fun refreshStatus() {
        val activeConn = connectionRepo.getActive()
        val systemVpnActive = isSystemVpnActive()

        // Zombie detection (v0.9.15.23). If the OS reports a VPN
        // capability but THIS app instance has not established a
        // tunnel, the active TUN fd belongs to either:
        //   (a) a previous instance of us that crashed without
        //       releasing the fd — Android-framework still holds it,
        //       our singletons are gone, we cannot drive it
        //   (b) a third-party VPN app (rare, deserves to be left
        //       alone — user picked that themselves)
        //
        // Pre-v0.9.15.23 we trusted the system flag and called
        // markConnected on any systemVpnActive, which caused the
        // user-reported loop: TunnelHealth pings fail through the
        // zombie/foreign VPN → recovery → disconnect hangs (nothing
        // in our singletons) → watchdog reset → connect → repeat.
        //
        // Verified by the user with reboot test: after a clean
        // phone reboot (Android-framework TUN cache wiped), the
        // same connect path that was looping starts working
        // immediately. The TUN handle was the difference; the
        // surrounding code is correct.
        val zombieLikely = systemVpnActive && !weEstablishedTunnel
        if (zombieLikely) {
            PrivycsLogger.w(
                TAG,
                "refreshStatus: zombie / foreign VPN detected " +
                    "(systemVpnActive=true, weEstablishedTunnel=false) — " +
                    "NOT marking connected; attempting cleanup"
            )
            forceCleanupZombie()
            // Report disconnected to the UI so the user can tap
            // Connect for a fresh tunnel. Keep the active connection
            // name visible so the UI doesn't blank out the connection
            // card.
            _status.value = VpnStatus(
                connectionName = activeConn?.name ?: "",
                connectionId = activeConn?.id ?: "",
                activeProtocol = activeConn?.activeProtocol,
                serverEndpoint = activeConn?.getActiveConfig()?.serverAddress ?: "",
            )
            scope.launch {
                com.privycs.vpn.util.ConnectCoordinator.markDisconnected()
            }
            return
        }

        if (systemVpnActive && !isConnected) {
            // System VPN is up AND we established it — but our flow
            // somehow has connected=false (race / aborted spinner).
            // Reconcile to connected.
            PrivycsLogger.i(TAG, "refreshStatus: system says VPN is active - reconciling UI state")
            _status.value = _status.value.copy(
                connected = true,
                connectionName = activeConn?.name ?: _status.value.connectionName,
                connectionId = activeConn?.id ?: _status.value.connectionId,
                activeProtocol = activeConn?.activeProtocol ?: _status.value.activeProtocol,
                serverEndpoint = activeConn?.getActiveConfig()?.serverAddress ?: _status.value.serverEndpoint,
            )
            // Let coordinator know actual state is Connected; its flow
            // will ripple back into _isConnecting via the init{} bridge.
            scope.launch {
                com.privycs.vpn.util.ConnectCoordinator.markConnected(
                    activeConn?.id ?: _status.value.connectionId,
                )
            }
            return
        }

        if (activeConn != null && !isConnected) {
            _status.value = VpnStatus(
                connectionName = activeConn.name,
                connectionId = activeConn.id,
                activeProtocol = activeConn.activeProtocol,
                serverEndpoint = activeConn.getActiveConfig()?.serverAddress ?: ""
            )
        }
    }

    /**
     * Force-cleanup a detected zombie VpnService. First sends an
     * explicit DISCONNECT intent to our service in case it's still
     * alive but unresponsive (chance the in-process tunnel-singletons
     * are valid and a clean disconnect path works). Then unconditionally
     * stopService to kill any lingering instance. Android-framework
     * will release the TUN fd when the owning Service is destroyed.
     *
     * Caller in refreshStatus has already established we believe a
     * zombie is present (systemVpnActive=true, weEstablishedTunnel=
     * false). This function is best-effort; failures are swallowed
     * because there's nothing constructive to do if cleanup itself
     * throws.
     */
    private fun forceCleanupZombie() {
        try {
            val disconnectIntent = Intent(context, PrivycsVpnService::class.java).apply {
                action = PrivycsVpnService.ACTION_DISCONNECT
            }
            context.startService(disconnectIntent)
        } catch (e: Exception) {
            PrivycsLogger.w(TAG, "forceCleanupZombie: DISCONNECT intent failed: ${e.message}")
        }
        try {
            context.stopService(Intent(context, PrivycsVpnService::class.java))
        } catch (e: Exception) {
            PrivycsLogger.w(TAG, "forceCleanupZombie: stopService failed: ${e.message}")
        }
    }

    /**
     * True if any Network on the device currently advertises
     * TRANSPORT_VPN. Does not distinguish "our VPN" from a third-party
     * VPN app, but for the refreshStatus() use case (resolving stuck
     * spinner / post-process-death sync) that distinction is academic
     * - the user sees a VPN shield in the status bar either way, and
     * our connect() would collide with it either way.
     */
    @Suppress("DEPRECATION")  // cm.allNetworks: fine for a one-shot
    // probe; a NetworkCallback would need long-lived state for no
    // benefit here (we just need a yes/no snapshot).
    private fun isSystemVpnActive(): Boolean {
        return try {
            val cm = context.getSystemService(Context.CONNECTIVITY_SERVICE) as ConnectivityManager
            cm.allNetworks.any { net ->
                cm.getNetworkCapabilities(net)?.hasTransport(NetworkCapabilities.TRANSPORT_VPN) == true
            }
        } catch (e: Exception) {
            false
        }
    }
}
