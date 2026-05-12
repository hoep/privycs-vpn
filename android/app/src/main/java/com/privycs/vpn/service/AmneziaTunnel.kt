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
        // Defensive logging — if a native panic kills the process
        // mid-call, the in-app log file's last lines pinpoint the
        // step. Each entry is flushed by PrivycsLogger's PrintWriter
        // .use{} block before returning, so crash-after-this-step
        // means the next step blew up.
        com.privycs.vpn.util.PrivycsLogger.i(TAG,
            "AWG connect[$name] step 1/3: parsing config (len=${configContent.length} bytes)")
        try {
            config = parseConfig(configContent)
        } catch (t: Throwable) {
            com.privycs.vpn.util.PrivycsLogger.e(TAG,
                "AWG connect[$name] step 1/3 FAILED: parseConfig threw ${t.javaClass.simpleName}: ${t.message}", t)
            throw t
        }
        com.privycs.vpn.util.PrivycsLogger.i(TAG,
            "AWG connect[$name] step 2/3: config parsed " +
                "(peers=${config?.peers?.size ?: 0}, " +
                "addresses=${config?.`interface`?.addresses?.size ?: 0}, " +
                "jc=${config?.`interface`?.junkPacketCount?.orElse(null)}, " +
                "calling backend.setState UP")
        try {
            backend.setState(this@AmneziaTunnel, Tunnel.State.UP, config)
        } catch (t: Throwable) {
            com.privycs.vpn.util.PrivycsLogger.e(TAG,
                "AWG connect[$name] step 2/3 FAILED: backend.setState threw ${t.javaClass.simpleName}: ${t.message}", t)
            throw t
        }
        com.privycs.vpn.util.PrivycsLogger.i(TAG,
            "AWG connect[$name] step 3/3: tunnel up")
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
