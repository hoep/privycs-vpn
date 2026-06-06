package com.privycs.vpn.engine

import com.privycs.vpn.PrivycsApp
import com.privycs.vpn.data.models.VpnProtocol
import com.privycs.vpn.service.TunnelHealthMonitor
import com.privycs.vpn.util.PrivycsLogger
import kotlinx.serialization.Serializable

/**
 * Smart Decision Engine bridge for Android — now backed by the PURE-KOTLIN
 * [LocalEngine] port, NOT the gomobile binding.
 *
 * Why: the gomobile engine ran its own Go runtime (libgojni.so) in the SAME
 * process as the WireGuard/AmneziaWG Go runtime (libwg-go-awg.so). Two Go
 * runtimes in one process fight over the process-wide signal handlers and
 * turned an internally-recovered SIGSEGV fatal → a deterministic native crash
 * on connect (Android-only; iOS runs the tunnel in a separate NE process so the
 * two runtimes never share a process). Reimplementing the engine's deterministic
 * selection + reasoning in Kotlin removes the second Go runtime entirely, so the
 * engine is fully active again with no crash risk.
 *
 * It still OBSERVES the real connection lifecycle (connect/disconnect, health
 * transitions) to build the explainable decision log, and drives connect-time
 * protocol selection via [effectiveOrder] when Automatic protocol selection is
 * on. Identical ranking/reason logic to iOS/desktop.
 */
object EngineShadow {
    private const val TAG = "EngineShadow"

    /** The engine may drive connect-time protocol selection again — the Kotlin
     *  port has no native runtime to conflict with the tunnel. */
    fun connectOrderingEnabled(): Boolean = true

    // ── Decision log (in-memory; what the Settings "what the engine decided &
    // why" panel renders). Same EngineDecision shape as before. ──
    private val logMu = Any()
    private val decisionLog = ArrayDeque<EngineDecision>()
    private const val LOG_CAP = 50

    private fun addDecision(
        key: String,
        active: String = "",
        chosen: String = "",
        args: List<String> = emptyList(),
        reason: String = "",
        reasonArgs: List<String> = emptyList(),
    ) {
        val rec = EngineDecision(
            at = java.time.Instant.now().toString(),
            active = active,
            chosen = chosen,
            key = key,
            args = args,
            reason = reason,
            reasonArgs = reasonArgs,
        )
        synchronized(logMu) {
            decisionLog.addLast(rec)
            while (decisionLog.size > LOG_CAP) decisionLog.removeFirst()
        }
    }

    /**
     * Connect transition observed: log "connecting → connected via <proto>" with
     * the network-aware reason. proto is the ACTUAL connected protocol. Called
     * from VpnServiceManager on the confirmed-connect transition.
     */
    fun observeConnect(proto: VpnProtocol?, country: String, awgAvailable: Boolean) {
        try {
            val (rk, ra) = LocalEngine.countryReason(country, proto, awgAvailable)
            val tok = proto?.let { tokenOf(it) } ?: ""
            // args carry the protocol token → the UI substitutes it into the
            // "Connecting/Connected via %1$s" strings (and renders the brand label).
            val a = if (tok.isEmpty()) emptyList() else listOf(tok)
            addDecision("decision.connecting", chosen = tok, args = a, reason = rk, reasonArgs = ra)
            addDecision("decision.connected", active = tok, args = a, reason = rk, reasonArgs = ra)
        } catch (t: Throwable) {
            PrivycsLogger.w(TAG, "observeConnect: ${t.message}")
        }
    }

    /** Called from ConnectCoordinator.markDisconnected. */
    fun observeDisconnect() {
        addDecision("decision.disconnected")
    }

    /** Forwards a TunnelHealthMonitor transition; INACTIVE/HEALTHY add no entry. */
    fun observeHealth(state: TunnelHealthMonitor.State) {
        val key = when (state) {
            TunnelHealthMonitor.State.DEGRADED -> "decision.degraded"
            TunnelHealthMonitor.State.RECOVERING -> "decision.recover_restart"
            TunnelHealthMonitor.State.HEALTHY, TunnelHealthMonitor.State.INACTIVE -> return
        }
        addDecision(key)
    }

    /** Recent decisions (newest last). */
    fun decisions(): List<EngineDecision> = synchronized(logMu) { decisionLog.toList() }

    // ── Adaptive (P4) stats: in-memory per-protocol success/fail on the
    // CURRENT network (iface-scoped key), reset on network change. ──
    private data class Stat(var ewma: Int = 500, var lastFailSec: Long = 0)
    private val statsMu = Any()
    private var stats = mutableMapOf<String, Stat>()
    private var statsKey = ""

    private fun ensureStats(netKey: String) {
        if (statsKey != netKey) {
            statsKey = netKey
            stats = mutableMapOf()
        }
    }

    /** Fold a connect outcome into the protocol's stat (integer EWMA). */
    fun recordOutcome(proto: VpnProtocol?, success: Boolean) {
        val tok = proto?.let { tokenOf(it) } ?: return
        synchronized(statsMu) {
            ensureStats(currentIface())
            val st = stats.getOrPut(tok) { Stat() }
            val v = if (success) 1000 else 0
            st.ewma = (st.ewma * 7 + v * 3) / 10
            if (!success) st.lastFailSec = System.currentTimeMillis() / 1000
        }
    }

    /** Per-protocol stats for the current network, shaped for [LocalEngine]. */
    private fun statsForSelect(netKey: String): Map<VpnProtocol, LocalEngine.Stat> = synchronized(statsMu) {
        ensureStats(netKey)
        stats.mapNotNull { (tok, st) ->
            protoOf(tok)?.let { it to LocalEngine.Stat(st.ewma, st.lastFailSec) }
        }.toMap()
    }

    /** Current interface ("wifi"/"cellular"/"ethernet"/"") via ConnectivityManager. */
    private fun currentIface(): String = try {
        val cm = PrivycsApp.instance.getSystemService(android.content.Context.CONNECTIVITY_SERVICE)
            as android.net.ConnectivityManager
        val caps = cm.getNetworkCapabilities(cm.activeNetwork)
        when {
            caps == null -> ""
            caps.hasTransport(android.net.NetworkCapabilities.TRANSPORT_WIFI) -> "wifi"
            caps.hasTransport(android.net.NetworkCapabilities.TRANSPORT_CELLULAR) -> "cellular"
            caps.hasTransport(android.net.NetworkCapabilities.TRANSPORT_ETHERNET) -> "ethernet"
            else -> ""
        }
    } catch (t: Throwable) {
        ""
    }

    private fun tokenOf(p: VpnProtocol): String = when (p) {
        VpnProtocol.AMNEZIAWG -> "amneziawg"
        VpnProtocol.WIREGUARD -> "wireguard"
        VpnProtocol.OPENVPN -> "openvpn"
        VpnProtocol.IPSEC -> "ipsec"
    }

    private fun protoOf(token: String): VpnProtocol? = when (token) {
        "amneziawg" -> VpnProtocol.AMNEZIAWG
        "wireguard" -> VpnProtocol.WIREGUARD
        "openvpn" -> VpnProtocol.OPENVPN
        "ipsec" -> VpnProtocol.IPSEC
        else -> null
    }

    /**
     * The order to drive connect + failover when Automatic protocol selection is
     * on: the engine's ranked order (context + roaming-interface + adaptive
     * stats) over the connection's available protocols. OFF / on error → the
     * manual failover order (the single gate; existing path untouched).
     */
    fun effectiveOrder(
        settings: com.privycs.vpn.data.models.AppSettings,
        connection: com.privycs.vpn.data.models.VpnConnection?,
    ): List<VpnProtocol> {
        if (!settings.autoProtocolSelection) return settings.protocolFailoverOrder
        return try {
            val avail = connection?.protocols?.map { it.protocol }?.distinct().orEmpty()
            if (avail.isEmpty()) return settings.protocolFailoverOrder
            val cc = PrivycsApp.instance.selfIpDetector.cachedResult()?.country.orEmpty()
            val iface = currentIface()
            val now = System.currentTimeMillis() / 1000
            val order = LocalEngine.selectOrder(
                available = avail,
                country = cc,
                iface = iface,
                nowSec = now,
                stats = statsForSelect(iface),
            )
            order.ifEmpty { settings.protocolFailoverOrder }
        } catch (t: Throwable) {
            PrivycsLogger.w(TAG, "effectiveOrder: ${t.message}")
            settings.protocolFailoverOrder
        }
    }
}

/**
 * Wire shape of one engine decision, identical JSON to the desktop
 * EngineDecisionDTO so all platforms share one render contract. [key] is the
 * stable i18n key (e.g. "decision.connecting"); never pre-translated text.
 */
@Serializable
data class EngineDecision(
    val at: String = "",
    val from: String = "",
    val to: String = "",
    val rule: String = "",
    val active: String = "",
    val chosen: String = "",
    val key: String = "",
    val args: List<String> = emptyList(),
    val reason: String = "",
    val reasonArgs: List<String> = emptyList(),
)
