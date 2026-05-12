package com.privycs.vpn.service

import android.util.Log
import com.privycs.vpn.data.models.VpnProtocol
import com.privycs.vpn.data.models.VpnStatus
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import org.amnezia.awg.backend.GoBackend
import org.amnezia.awg.backend.Statistics
import org.amnezia.awg.backend.Tunnel
import org.amnezia.awg.config.Config
import java.io.BufferedReader
import java.io.StringReader

/**
 * AmneziaWG tunnel implementation using the amneziawg-android tunnel
 * library (vendored git submodule, pinned to v1.1.7). Parallel
 * structure to WireGuardTunnel for the DPI-evasion fork. Same
 * userspace GoBackend approach (no root required).
 *
 * Stage 1 of AMNEZIAWG_CLIENT_PLAN.md. Selected by
 * PrivycsVpnService.connectWireGuard when TunnelVariant.detect on
 * the conf payload returns AMNEZIAWG (any of the AWG-specific
 * [Interface] keys present: Jc, Jmin, Jmax, S1-4, H1-4, I1-5).
 *
 * Note: this wrapper looks line-for-line like WireGuardTunnel.kt
 * because the upstream amneziawg-android library mirrors
 * wireguard-android's API surface verbatim — only the package
 * paths differ (`org.amnezia.awg.*` vs `com.wireguard.*`). Both
 * are forks of Jason Donenfeld's wireguard-android. We can't
 * unify the two wrappers behind one Kotlin interface without
 * adapters: the upstream `Tunnel` types are distinct JVM types
 * from different package roots that don't implement a common
 * interface from a third source.
 */
class AmneziaTunnel(
    private val backend: GoBackend,
) : Tunnel {

    companion object {
        private const val TAG = "AmneziaTunnel"
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

    fun parseConfig(configContent: String): Config {
        return Config.parse(BufferedReader(StringReader(configContent)))
    }

    suspend fun connect(configContent: String, name: String = "privycs0") = withContext(Dispatchers.IO) {
        tunnelName = name
        config = parseConfig(configContent)
        Log.d(TAG, "Connecting AWG tunnel: $tunnelName")
        backend.setState(this@AmneziaTunnel, Tunnel.State.UP, config)
        Log.d(TAG, "AWG tunnel $tunnelName is UP")
    }

    suspend fun disconnect() = withContext(Dispatchers.IO) {
        Log.d(TAG, "Disconnecting AWG tunnel: $tunnelName")
        try {
            backend.setState(this@AmneziaTunnel, Tunnel.State.DOWN, null)
        } catch (e: Exception) {
            Log.w(TAG, "Error disconnecting AWG tunnel: ${e.message}")
        }
        Log.d(TAG, "AWG tunnel $tunnelName is DOWN")
    }

    fun getState(): Tunnel.State {
        return try {
            backend.getState(this)
        } catch (e: Exception) {
            Tunnel.State.DOWN
        }
    }

    fun getStatistics(): Statistics? {
        return try {
            backend.getStatistics(this)
        } catch (e: Exception) {
            null
        }
    }

    fun isConnected(): Boolean = getState() == Tunnel.State.UP

    fun bytesReceived(): Long {
        val stats = getStatistics() ?: return 0L
        var total = 0L
        for (peer in config?.peers ?: emptyList()) {
            val peerStats = stats.peer(peer.publicKey)
            if (peerStats != null) total += peerStats.rxBytes
        }
        return total
    }

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
            activeProtocol = VpnProtocol.AMNEZIAWG,
            variant = "amneziawg",
            uptime = if (isUp && connectedSince > 0) System.currentTimeMillis() - connectedSince else 0L,
            rxBytes = rxBytes,
            txBytes = txBytes,
            serverEndpoint = endpoint,
            localAddress = address,
        )
    }
}
