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

    private val _status = MutableStateFlow(VpnStatus())
    val status: StateFlow<VpnStatus> = _status.asStateFlow()

    private val _isConnecting = MutableStateFlow(false)
    val isConnecting: StateFlow<Boolean> = _isConnecting.asStateFlow()

    val isConnected: Boolean
        get() = _status.value.connected

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

            connect(activeConn.id)
        }
    }

    /**
     * Update status from VpnService (called via service binding or broadcast).
     * Once the tunnel reports either connected=true or an error, clear the
     * connecting spinner - otherwise ConnectScreen keeps showing
     * "Connecting..." forever for IPSec (WG/OpenVPN clear this via the
     * polled connected=true the same way).
     */
    fun updateStatus(status: VpnStatus) {
        val prev = _status.value
        _status.value = status
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
            scope.launch {
                com.privycs.vpn.util.ConnectCoordinator.markConnected(status.connectionId)
            }
        } else if (!status.connected && prev.connected) {
            scope.launch {
                com.privycs.vpn.util.ConnectCoordinator.markDisconnected()
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

        if (systemVpnActive && !isConnected) {
            // Trust the system: a VPN is live but our state disagrees.
            // Flip connected=true so the UI stops asking the user to
            // re-connect something that's already connected, and clear
            // any lingering spinner from a prior aborted connect().
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
