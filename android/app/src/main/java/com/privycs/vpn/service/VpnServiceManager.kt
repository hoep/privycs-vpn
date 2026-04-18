package com.privycs.vpn.service

import android.content.Context
import android.content.Intent
import android.net.VpnService
import android.util.Log
import com.privycs.vpn.PrivycsApp
import com.privycs.vpn.data.models.VpnConnection
import com.privycs.vpn.data.models.VpnProtocol
import com.privycs.vpn.data.models.VpnStatus
import com.privycs.vpn.util.PrivycsLogger
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
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
        scope.launch {
            _isConnecting.value = true
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
     */
    fun switchProtocol(protocol: VpnProtocol) {
        val activeConn = connectionRepo.getActive() ?: return
        if (!activeConn.hasProtocol(protocol)) return

        val wasConnected = isConnected
        if (wasConnected) {
            disconnect()
        }

        connectionRepo.setActiveProtocol(activeConn.id, protocol)

        if (wasConnected) {
            connect(activeConn.id)
        } else {
            _status.value = _status.value.copy(activeProtocol = protocol)
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
        if (status.connected || status.error != null) {
            _isConnecting.value = false
        }
    }

    /**
     * Refresh status by querying the service.
     */
    fun refreshStatus() {
        val activeConn = connectionRepo.getActive()
        if (activeConn != null && !isConnected) {
            _status.value = VpnStatus(
                connectionName = activeConn.name,
                connectionId = activeConn.id,
                activeProtocol = activeConn.activeProtocol,
                serverEndpoint = activeConn.getActiveConfig()?.serverAddress ?: ""
            )
        }
    }
}
