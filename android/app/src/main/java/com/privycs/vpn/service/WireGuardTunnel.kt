package com.privycs.vpn.service

import android.util.Log
import com.wireguard.config.Config
import com.wireguard.config.InetEndpoint
import com.wireguard.crypto.Key
import com.wireguard.android.backend.Backend
import com.wireguard.android.backend.GoBackend
import com.wireguard.android.backend.Statistics
import com.wireguard.android.backend.Tunnel
import com.privycs.vpn.data.models.VpnStatus
import com.privycs.vpn.data.models.VpnProtocol
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import java.io.BufferedReader
import java.io.StringReader

/**
 * WireGuard tunnel implementation using the wireguard-android tunnel library.
 * Uses GoBackend for userspace WireGuard (no root required).
 */
class WireGuardTunnel(
    private val backend: GoBackend
) : Tunnel {

    companion object {
        private const val TAG = "WireGuardTunnel"
    }

    private var tunnelName: String = "privycs0"
    private var config: Config? = null
    private var currentState: Tunnel.State = Tunnel.State.DOWN
    private var connectedSince: Long = 0L

    override fun getName(): String = tunnelName

    override fun onStateChange(newState: Tunnel.State) {
        Log.d(TAG, "Tunnel state changed: $currentState -> $newState")
        currentState = newState
        if (newState == Tunnel.State.UP) {
            connectedSince = System.currentTimeMillis()
        } else {
            connectedSince = 0L
        }
    }

    /**
     * Parse a WireGuard .conf file content into a Config object.
     */
    fun parseConfig(configContent: String): Config {
        return Config.parse(BufferedReader(StringReader(configContent)))
    }

    /**
     * Connect to the VPN using the provided config content.
     */
    suspend fun connect(configContent: String, name: String = "privycs0") = withContext(Dispatchers.IO) {
        tunnelName = name
        config = parseConfig(configContent)
        Log.d(TAG, "Connecting tunnel: $tunnelName")
        // Verify Per-App VPN made it through the parser. If PrivycsVpnService
        // injected IncludedApplications / ExcludedApplications lines into the
        // config text, toWgQuickString() on the parsed Interface will
        // round-trip them back out. Using the library's own serializer
        // avoids assumptions about internal field names across versions.
        config?.let { c ->
            try {
                val rendered = c.toWgQuickString()
                val perAppLines = rendered.lines()
                    .filter { it.contains("Applications", ignoreCase = true) }
                if (perAppLines.isEmpty()) {
                    Log.i(TAG, "Parsed WG config has NO IncludedApplications / ExcludedApplications (Per-App VPN not applied at parser level)")
                } else {
                    Log.i(TAG, "Parsed WG config Per-App lines: $perAppLines")
                }
            } catch (e: Exception) {
                Log.w(TAG, "WG config round-trip log failed: ${e.message}")
            }
        }
        backend.setState(this@WireGuardTunnel, Tunnel.State.UP, config)
        Log.d(TAG, "Tunnel $tunnelName is UP")
    }

    /**
     * Disconnect the VPN tunnel.
     */
    suspend fun disconnect() = withContext(Dispatchers.IO) {
        Log.d(TAG, "Disconnecting tunnel: $tunnelName")
        try {
            backend.setState(this@WireGuardTunnel, Tunnel.State.DOWN, null)
        } catch (e: Exception) {
            Log.w(TAG, "Error disconnecting tunnel: ${e.message}")
        }
        Log.d(TAG, "Tunnel $tunnelName is DOWN")
    }

    /**
     * Get current tunnel state.
     */
    fun getState(): Tunnel.State {
        return try {
            backend.getState(this)
        } catch (e: Exception) {
            Tunnel.State.DOWN
        }
    }

    /**
     * Collect tunnel statistics (transfer bytes).
     */
    fun getStatistics(): Statistics? {
        return try {
            backend.getStatistics(this)
        } catch (e: Exception) {
            null
        }
    }

    /**
     * Build a VpnStatus from current tunnel state.
     */
    fun getStatus(connectionName: String, connectionId: String): VpnStatus {
        val state = getState()
        val isUp = state == Tunnel.State.UP
        val stats = if (isUp) getStatistics() else null

        var rxBytes = 0L
        var txBytes = 0L
        if (stats != null) {
            for (peer in config?.peers ?: emptyList()) {
                val peerStats = stats.peer(peer.publicKey)
                if (peerStats != null) {
                    rxBytes += peerStats.rxBytes
                    txBytes += peerStats.txBytes
                }
            }
        }

        val endpoint = config?.peers?.firstOrNull()?.endpoint?.map { it.toString() }?.orElse("") ?: ""
        val address = config?.`interface`?.addresses?.joinToString(", ") { it.toString() } ?: ""

        return VpnStatus(
            connected = isUp,
            connectionName = connectionName,
            connectionId = connectionId,
            activeProtocol = VpnProtocol.WIREGUARD,
            uptime = if (isUp && connectedSince > 0) System.currentTimeMillis() - connectedSince else 0L,
            rxBytes = rxBytes,
            txBytes = txBytes,
            serverEndpoint = endpoint,
            localAddress = address
        )
    }
}
