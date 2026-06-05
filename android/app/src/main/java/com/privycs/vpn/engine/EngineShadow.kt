package com.privycs.vpn.engine

import com.privycs.engine.ffi.Ffi
import com.privycs.engine.ffi.Session
import com.privycs.vpn.PrivycsApp
import com.privycs.vpn.data.models.VpnProtocol
import com.privycs.vpn.service.TunnelHealthMonitor
import com.privycs.vpn.util.PrivycsLogger
import kotlinx.serialization.Serializable
import kotlinx.serialization.builtins.ListSerializer
import kotlinx.serialization.json.Json

/**
 * Shadow-mode bridge to the cross-platform Smart Decision Engine — the same
 * Go core (engine/ffi) the desktop runs, packaged here as a gomobile AAR
 * (com.privycs.engine.ffi.{Ffi,Session}).
 *
 * Mirrors desktop/engine_bridge.go field-for-field: it OBSERVES the real
 * connection lifecycle (connect/disconnect via [ConnectCoordinator]) and the
 * tunnel-health transitions ([TunnelHealthMonitor]), runs the engine's pure
 * FSM, and exposes the explainable decision log for the Settings panel — while
 * driving NOTHING (the engine's tunnel/prober spokes are no-ops on the Go
 * side). Zero behaviour change; flipping the toggle to active selection is a
 * later slice, exactly as on desktop.
 *
 * Every gomobile call is isolated in this one file so any generated-binding
 * name mismatch is a single-file fix. The candidate set is the user's
 * protocolFailoverOrder, matching desktop's shadowStore.
 */
object EngineShadow {
    private const val TAG = "EngineShadow"
    private val json = Json { ignoreUnknownKeys = true }

    @Volatile private var session: Session? = null
    @Volatile private var orderJson: String = ""

    /**
     * Build or refresh the engine session from the current protocol-failover
     * order. No-op when the order is unchanged. Synchronised so the lazy
     * init from the UI poller and the connect/health observers can't race.
     */
    @Synchronized
    fun ensure() {
        val order = try {
            PrivycsApp.instance.settingsRepository.getSettingsBlocking().protocolFailoverOrder
        } catch (t: Throwable) {
            emptyList()
        }
        val js = orderToJson(order)
        if (session != null && js == orderJson) return
        try {
            session?.close()
        } catch (_: Throwable) {
        }
        session = try {
            Ffi.newSession(js).also { orderJson = js }
        } catch (t: Throwable) {
            PrivycsLogger.w(TAG, "engine session init failed: ${t.message}")
            null
        }
    }

    /**
     * Idle→…→Connected in shadow. proto is the ACTUAL connected protocol so the
     * decision log reflects reality ("Connected via <proto>") instead of a
     * hypothetical failover-order pick. Called from VpnServiceManager on the
     * confirmed-connect transition.
     */
    fun observeConnect(proto: VpnProtocol?, country: String, awgAvailable: Boolean) {
        ensure()
        val token = when (proto) {
            VpnProtocol.AMNEZIAWG -> "amneziawg"
            VpnProtocol.WIREGUARD -> "wireguard"
            VpnProtocol.OPENVPN -> "openvpn"
            VpnProtocol.IPSEC -> "ipsec"
            null -> ""
        }
        try {
            session?.observeConnect(token, country, awgAvailable)
        } catch (t: Throwable) {
            PrivycsLogger.w(TAG, "observeConnect: ${t.message}")
        }
    }

    /** Called from ConnectCoordinator.markDisconnected. */
    fun observeDisconnect() {
        try {
            session?.observeDisconnect()
        } catch (t: Throwable) {
            PrivycsLogger.w(TAG, "observeDisconnect: ${t.message}")
        }
    }

    /** Forwards a TunnelHealthMonitor transition; INACTIVE is ignored. */
    fun observeHealth(state: TunnelHealthMonitor.State) {
        val token = when (state) {
            TunnelHealthMonitor.State.HEALTHY -> "healthy"
            TunnelHealthMonitor.State.DEGRADED -> "degraded"
            TunnelHealthMonitor.State.RECOVERING -> "recovering"
            TunnelHealthMonitor.State.INACTIVE -> return
        }
        try {
            session?.observeHealth(token)
        } catch (t: Throwable) {
            PrivycsLogger.w(TAG, "observeHealth: ${t.message}")
        }
    }

    /**
     * Active-mode engine protocol order (country-aware), most-preferred first;
     * empty on any error. Static engine call — no session needed.
     */
    fun protocolOrder(country: String): List<VpnProtocol> = try {
        Ffi.protocolOrder(country).split(",").mapNotNull {
            when (it.trim()) {
                "wireguard" -> VpnProtocol.WIREGUARD
                "amneziawg" -> VpnProtocol.AMNEZIAWG
                "openvpn" -> VpnProtocol.OPENVPN
                "ipsec" -> VpnProtocol.IPSEC
                else -> null
            }
        }
    } catch (t: Throwable) {
        PrivycsLogger.w(TAG, "protocolOrder: ${t.message}")
        emptyList()
    }

    /**
     * The order to drive connect + failover: the engine's country-aware order
     * when Automatic protocol selection is on, else the manual failover order.
     * This is the single gate — OFF leaves the existing path untouched.
     */
    fun effectiveOrder(settings: com.privycs.vpn.data.models.AppSettings): List<VpnProtocol> {
        if (!settings.autoProtocolSelection) return settings.protocolFailoverOrder
        val cc = PrivycsApp.instance.selfIpDetector.cachedResult()?.country.orEmpty()
        return protocolOrder(cc).ifEmpty { settings.protocolFailoverOrder }
    }

    /** Recent decisions (newest last); empty list on any error. */
    fun decisions(): List<EngineDecision> {
        ensure()
        val raw = try {
            session?.pollDecisions() ?: "[]"
        } catch (t: Throwable) {
            "[]"
        }
        return try {
            json.decodeFromString(ListSerializer(EngineDecision.serializer()), raw)
        } catch (t: Throwable) {
            emptyList()
        }
    }

    private fun orderToJson(order: List<VpnProtocol>): String {
        val src = if (order.isEmpty()) {
            listOf(VpnProtocol.AMNEZIAWG, VpnProtocol.WIREGUARD, VpnProtocol.OPENVPN, VpnProtocol.IPSEC)
        } else {
            order
        }
        return src.joinToString(prefix = "[", postfix = "]") { p ->
            val tok = when (p) {
                VpnProtocol.AMNEZIAWG -> "amneziawg"
                VpnProtocol.WIREGUARD -> "wireguard"
                VpnProtocol.OPENVPN -> "openvpn"
                VpnProtocol.IPSEC -> "ipsec"
            }
            "\"$tok\""
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
