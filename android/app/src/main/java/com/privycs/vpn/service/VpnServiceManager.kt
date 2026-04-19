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
    private var connectingWatchdog: Job? = null

    init {
        // Register a permanent NetworkCallback filtered on TRANSPORT_VPN
        // so we get a live signal whenever ANY VPN comes up or goes down
        // on the device - including the always-on auto-start path where
        // VpnServiceManager.connect() is never called and the UI would
        // otherwise be left with _isConnecting stuck at true forever.
        // Also recovers the process-death case where singleton state
        // resets to defaults but the tunnel is already live from a
        // previous process.
        try {
            val cm = context.getSystemService(Context.CONNECTIVITY_SERVICE) as ConnectivityManager
            val request = NetworkRequest.Builder()
                .addTransportType(NetworkCapabilities.TRANSPORT_VPN)
                // removeCapability NOT_VPN explicit so the request matches
                // VPN networks specifically (default NetworkRequest builders
                // implicitly add NOT_VPN which would filter them out).
                .removeCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN)
                .build()
            cm.registerNetworkCallback(request, object : ConnectivityManager.NetworkCallback() {
                override fun onAvailable(network: Network) {
                    // A VPN is live on the device. We don't try to verify
                    // it's OURS (would require comparing interface names
                    // which aren't stable across protocols); we just
                    // unblock the UI. If another VPN app has the tunnel,
                    // our own connect() will have already failed with an
                    // error status which is handled by updateStatus().
                    if (_isConnecting.value) {
                        PrivycsLogger.i(TAG, "TRANSPORT_VPN available -> clearing stuck isConnecting")
                        _isConnecting.value = false
                    }
                }

                override fun onLost(network: Network) {
                    // VPN went away. Clear spinner if a disconnect was
                    // pending, and mark status as disconnected so the
                    // UI reflects reality regardless of who shut the
                    // tunnel down (user action, always-on toggle off,
                    // other VPN app, tunnel crash).
                    if (_status.value.connected) {
                        _status.value = _status.value.copy(connected = false)
                    }
                    if (_isConnecting.value) {
                        _isConnecting.value = false
                    }
                }
            })
        } catch (e: Exception) {
            PrivycsLogger.w(TAG, "Failed to register TRANSPORT_VPN callback: ${e.message}")
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
            _isConnecting.value = true
            startConnectingWatchdog()
            try {
                connectionRepo.setActive(connId)

                val intent = Intent(context, PrivycsVpnService::class.java).apply {
                    action = PrivycsVpnService.ACTION_CONNECT
                    putExtra(PrivycsVpnService.EXTRA_CONNECTION_ID, connId)
                    putExtra(PrivycsVpnService.EXTRA_PROTOCOL, connection.activeProtocol.name)
                    putExtra(PrivycsVpnService.EXTRA_CONFIG_CONTENT, config.configContent)
                    putExtra(PrivycsVpnService.EXTRA_CONNECTION_NAME, connection.name)
                }
                context.startForegroundService(intent)

                connectionRepo.updateLastConnected(connId)

                // Tentative status - actually-connected flag flips only when
                // the service pushes a VpnStatus with connected=true (polled
                // for WG/OpenVPN, VpnStateListener-driven for IPSec). An
                // optimistic connected=true here caused "briefly Connected,
                // then Disconnected until IKE finishes, then Connected again"
                // flicker for IPSec where the actual tunnel takes ~5-10s.
                _status.value = VpnStatus(
                    connected = false,
                    connectionName = connection.name,
                    connectionId = connId,
                    activeProtocol = connection.activeProtocol,
                    serverEndpoint = config.serverAddress,
                    localAddress = config.localAddress
                )
                // Intentionally do NOT reset _isConnecting here; it stays
                // true until updateStatus() sees connected=true or an error.
            } catch (e: Exception) {
                PrivycsLogger.e(TAG, "Connect failed", e)
                _status.value = VpnStatus(error = "Connection failed: ${e.message}")
                _isConnecting.value = false
            }
        }
    }

    /**
     * Disconnect the active VPN connection.
     */
    fun disconnect() {
        PrivycsLogger.i(TAG, "disconnect requested")
        // Stamp the time stamp BEFORE sending the intent so if the
        // system's Always-On START_STICKY respawn fires while our
        // service teardown is still in flight, handleAlwaysOnReconnect
        // sees a fresh timestamp and can flag Always-On as detected.
        com.privycs.vpn.util.AlwaysOnDetector.stampUserDisconnect(context)
        scope.launch {
            _isConnecting.value = true
            try {
                val intent = Intent(context, PrivycsVpnService::class.java).apply {
                    action = PrivycsVpnService.ACTION_DISCONNECT
                }
                context.startService(intent)
                _status.value = VpnStatus()
            } catch (e: Exception) {
                PrivycsLogger.e(TAG, "Disconnect failed", e)
                _status.value = _status.value.copy(error = "Disconnect failed: ${e.message}")
            } finally {
                _isConnecting.value = false
            }
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
        if (status.connected || status.error != null) {
            _isConnecting.value = false
            connectingWatchdog?.cancel()
            connectingWatchdog = null
        }
    }

    /**
     * Safety net against a stuck "Connecting..." spinner. Android's
     * always-on auto-start path, process-death-without-service-death
     * races, and collisions between user connect() and a pre-existing
     * tunnel established externally can all leave _isConnecting true
     * with no resolving updateStatus() ever arriving. This coroutine
     * force-clears the flag after a ceiling timeout so the UI button
     * stays usable.
     *
     * 90 s covers every realistic tunnel-establish time on this app
     * (IPSec charon worst-case ~30 s, OpenVPN TCP over slow-3G
     * ~45 s) with plenty of margin. Anything taking longer is a
     * genuine failure the user should be able to cancel.
     */
    private fun startConnectingWatchdog() {
        connectingWatchdog?.cancel()
        connectingWatchdog = scope.launch {
            delay(90_000L)
            if (_isConnecting.value) {
                PrivycsLogger.w(TAG, "Connecting watchdog fired after 90s - force-clearing stuck spinner")
                _isConnecting.value = false
            }
        }
    }

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
            _isConnecting.value = false
            connectingWatchdog?.cancel()
            connectingWatchdog = null
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
