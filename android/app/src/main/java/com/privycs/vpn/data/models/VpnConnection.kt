package com.privycs.vpn.data.models

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

@Serializable
enum class VpnProtocol(val label: String, val shortLabel: String, val extensions: List<String>) {
    // AMNEZIAWG sits ahead of WIREGUARD in the enum because the
    // pool-failover order in PoolStrategy iterates the enum's natural
    // ordering (AWG > WG > OVPN > IPSec). AWG-first matches the
    // "try the DPI-evasion variant before the unobfuscated one"
    // intent: on a restrictive network the vanilla-WG handshake
    // would burn time blackholed by the censor; AWG either gets
    // through or fails fast on its own DPI signature.
    @SerialName("amneziawg")
    AMNEZIAWG("AmneziaWG", "AWG", listOf(".conf")),

    @SerialName("wireguard")
    WIREGUARD("WireGuard", "WG", listOf(".conf")),

    @SerialName("openvpn")
    OPENVPN("OpenVPN", "OVPN", listOf(".ovpn")),

    @SerialName("ipsec")
    IPSEC("IPSec", "IPSec", listOf(".sswan", ".mobileconfig", ".p12"));

    companion object {
        fun fromString(value: String): VpnProtocol? = when (value.lowercase()) {
            "amneziawg", "awg" -> AMNEZIAWG
            "wireguard", "wg" -> WIREGUARD
            "openvpn", "ovpn" -> OPENVPN
            "ipsec" -> IPSEC
            else -> null
        }
    }
}

@Serializable
data class ProtocolConfig(
    val protocol: VpnProtocol,
    @SerialName("config_content")
    val configContent: String,
    val filename: String,
    // Stable per-config UUID. Drives the "multi-config-per-protocol-
    // per-connection" model: a VpnConnection can hold any number of
    // ProtocolConfigs (including multiples of the same protocol
    // type — e.g. two WG endpoints UDP+TCP), and VpnConnection.
    // activeConfigId names the one the connect path uses.
    // Defaults to "" so older persisted JSON deserialises; the
    // load-time heal in ConnectionRepository.load assigns a fresh
    // UUID when empty.
    val id: String = "",
    // User-editable label rendered in the pill row when a connection
    // has more than one config of the same protocol type ("Home WG
    // UDP" vs "Home WG TCP"). Empty → fall back to filename / the
    // protocol's label. var so the rename action can mutate without
    // copy-and-replace gymnastics across the list.
    var nickname: String = "",
    // var (not val) so the load-time sanitization in
    // ConnectionRepository.load() can rewrite obviously-broken values
    // — historically the IPSec parser at ConfigParser.parseIpSec()
    // mis-extracted "{" from the .sswan JSON-object-opening line and
    // persisted that as the server address. Existing affected
    // installations get auto-healed on next app launch instead of
    // having to manually re-import.
    @SerialName("server_address")
    var serverAddress: String = "",
    // var (not val) so the post-connect status persistence in
    // ConnectionRepository.updateLocalAddress can mutate it without
    // copying the whole ProtocolConfig. WireGuard's address is parsed
    // from the .conf at import (always present); OpenVPN/IPSec only
    // learn their inner IP after the server pushes one in IKE_AUTH /
    // TLS, so we need to update the persisted entry whenever the
    // tunnel reports a non-empty localAddress in VpnStatus.
    @SerialName("local_address")
    var localAddress: String = "",
    @SerialName("added_at")
    val addedAt: String = ""
) {
    /** Display label — nickname if set, else filename minus extension, else protocol label. */
    fun displayLabel(): String {
        if (nickname.isNotBlank()) return nickname
        val name = filename.substringBeforeLast('.', filename).trim()
        if (name.isNotBlank()) return name
        return protocol.label
    }
}

@Serializable
data class VpnConnection(
    val id: String,
    var name: String,
    @SerialName("active_protocol")
    var activeProtocol: VpnProtocol,
    val protocols: MutableList<ProtocolConfig> = mutableListOf(),
    @SerialName("created_at")
    val createdAt: String = "",
    @SerialName("last_connected")
    var lastConnected: String = "",
    @SerialName("is_favorite")
    var isFavorite: Boolean = false,
    /**
     * Per-connection DNS override. Comma- or whitespace-separated
     * IPv4/IPv6 list. When non-empty, takes priority over both
     * Pool.dnsOverride (irrelevant for single connections) and
     * the global Settings.dnsOverride. Empty falls back to
     * global. Use case: "Home connection uses 192.168.1.1 (local
     * Pi-hole), Work uses corporate DNS, Public uses Cloudflare"
     * without flipping the global Settings field on every switch.
     */
    @SerialName("dns_override")
    var dnsOverride: String = "",
    /**
     * The ID of the currently-active ProtocolConfig on this
     * connection. Replaces the old "activeProtocol enum" semantics
     * which assumed at most one config per protocol type — with
     * multi-config-per-protocol the enum alone is no longer
     * sufficient. Empty until the load-time heal in
     * ConnectionRepository.load() reconciles it against the older
     * activeProtocol field. Both fields are kept on disk for now,
     * with activeConfigId being authoritative; activeProtocol is
     * derived via getActiveConfig().protocol when needed.
     */
    @SerialName("active_config_id")
    var activeConfigId: String = ""
) {
    /**
     * The configs configured on this connection, sorted in
     * failover-preference order: protocol enum first (AWG → WG →
     * OVPN → IPSec), then insertion order within a protocol. Used
     * by TunnelHealthMonitor.recovery and ProtocolBadges in the UI.
     * Reflects multi-config-per-protocol — the same list may
     * contain e.g. two WG entries if the user added both a UDP and
     * a TCP endpoint to the same logical "Home Server" connection.
     */
    fun orderedConfigs(): List<ProtocolConfig> =
        protocols.sortedWith(compareBy({ it.protocol.ordinal }, { it.addedAt }))

    /**
     * Returns the active ProtocolConfig — i.e. the one identified
     * by activeConfigId. Falls back to the first config of
     * activeProtocol (legacy path) when activeConfigId is unset,
     * and finally to the first config period.
     */
    fun getActiveConfig(): ProtocolConfig? {
        if (activeConfigId.isNotEmpty()) {
            val byId = protocols.find { it.id == activeConfigId }
            if (byId != null) return byId
        }
        // Legacy fallback for connections that pre-date the
        // multi-config refactor: pick the first config matching
        // the historical activeProtocol enum field.
        return protocols.find { it.protocol == activeProtocol } ?: protocols.firstOrNull()
    }

    /** Lookup helper used by callers that want a specific config by id. */
    fun getConfigById(id: String): ProtocolConfig? =
        protocols.find { it.id == id }

    /** First config matching the given protocol — back-compat for code that doesn't care about multi-config. */
    fun getProtocol(protocol: VpnProtocol): ProtocolConfig? =
        protocols.find { it.protocol == protocol }

    /**
     * Distinct list of protocol types present in this connection,
     * enum-sorted. Used for UI groupings that don't care which
     * specific config — e.g. "this connection supports AWG, WG,
     * OVPN" summaries.
     */
    fun availableProtocols(): List<VpnProtocol> =
        protocols.map { it.protocol }.distinct().sortedBy { it.ordinal }

    fun hasProtocol(protocol: VpnProtocol): Boolean =
        protocols.any { it.protocol == protocol }

    /**
     * True iff the currently-active config's protocol is AmneziaWG.
     * Used by widget code that thinks in protocol terms; new call
     * sites should just check `getActiveConfig()?.protocol`.
     */
    fun isActiveAmneziaWg(): Boolean = getActiveConfig()?.protocol == VpnProtocol.AMNEZIAWG
}

@Serializable
data class ConnectionRegistry(
    val connections: MutableList<VpnConnection> = mutableListOf(),
    @SerialName("active_id")
    var activeId: String = ""
)

data class VpnStatus(
    val connected: Boolean = false,
    val connectionName: String = "",
    val connectionId: String = "",
    val activeProtocol: VpnProtocol? = null,
    val uptime: Long = 0L,
    val rxBytes: Long = 0L,
    val txBytes: Long = 0L,
    val serverEndpoint: String = "",
    val localAddress: String = "",
    val lastHandshake: String = "",
    val error: String? = null,
    // Pool context. Set when the active "connection" is actually a
    // pool member (instead of a single saved connection). The UI uses
    // these to render the pool indicator card with current member,
    // upcoming pre-warmed member, and round-robin countdown.
    //
    // Mirrors the flat pool fields desktop carries in its VpnStatus
    // struct - kept flat (not a nested object) so existing copy()
    // call sites that only update a few fields don't accidentally
    // wipe pool context.
    val poolId: String = "",
    val poolName: String = "",
    val poolPolicy: String = "",
    val activeMemberName: String = "",
    val activeMemberCountry: String = "",
    val pendingMemberName: String = "",
    val pendingMemberCountry: String = "",
    // Epoch-ms timestamp of the next scheduled rotation. UI reads
    // this and computes "now -> nextRotationAt" live so the
    // countdown ticks down without a fresh status push every second.
    // Zero means no rotation scheduled (non-RR pool, or no pool
    // active).
    val nextRotationAt: Long = 0L,
    // v0.9.15.x AmneziaWG Stage 1.4 — variant marker for
    // WireGuard-class tunnels. Empty / "wireguard" = vanilla WG,
    // "amneziawg" = AWG-DPI-evasion variant. ConnectScreen reads
    // this to show a read-only "Obfuscation" badge when active.
    // Other protocols (OpenVPN, IPSec) leave this empty.
    val variant: String = "",
)

@Serializable
enum class AppTheme {
    @SerialName("dark")
    DARK,
    @SerialName("light")
    LIGHT,
    @SerialName("system")
    SYSTEM
}

@Serializable
data class ConnectOnDemandSettings(
    val enabled: Boolean = false,
    val trigger: String = "wifi_mobile",  // "wifi", "mobile", "wifi_mobile"
    @SerialName("ssid_mode")
    val ssidMode: String = "all",  // "all", "only", "except"
    @SerialName("ssid_list")
    val ssidList: List<String> = emptyList()
)

@Serializable
data class AppSettings(
    @SerialName("active_protocol")
    val activeProtocol: VpnProtocol = VpnProtocol.WIREGUARD,
    @SerialName("kill_switch_enabled")
    val killSwitchEnabled: Boolean = false,
    @SerialName("auto_connect_on_start")
    val autoConnectOnStart: Boolean = false,
    val theme: AppTheme = AppTheme.SYSTEM,
    @SerialName("dns_override")
    val dnsOverride: String = "",
    @SerialName("gateway_url")
    val gatewayUrl: String = "",
    @SerialName("api_key")
    val apiKey: String = "",
    @SerialName("connect_on_demand")
    val connectOnDemand: ConnectOnDemandSettings = ConnectOnDemandSettings(),
    // First-launch tracking. Set to true by MainActivity after the
    // post-install location-permission rationale has been shown
    // exactly once. Defaults to false so an upgrade-from-old-version
    // counts as "still need to ask" - users who were never prompted
    // get the rationale dialog on the next app open.
    @SerialName("first_launch_completed")
    val firstLaunchCompleted: Boolean = false,
    // Tunnel-health monitoring (Phase 1 visible UX). Mode controls
    // whether the periodic ICMP-ping liveness check runs:
    //   - AUTO:   on for pools, off for single connections.
    //   - ALWAYS: on for both.
    //   - OFF:    never run.
    // Pools default-on because pool member rotation is the natural
    // recovery action when a member dies silently. Single
    // connections default-off because the recovery action is
    // disconnect-then-reconnect which can be disruptive on a
    // flaky network.
    @SerialName("tunnel_health_mode")
    val tunnelHealthMode: String = "auto",
    // Custom ping target for tunnel-health. Empty = use built-in
    // default 1.1.1.1. Useful for users behind networks that block
    // ICMP to 1.1.1.1 specifically, or who prefer pinging an
    // internal target (Pi-hole, gateway).
    @SerialName("tunnel_health_target")
    val tunnelHealthTarget: String = "",
    // v0.9.14.75 — opt-in foreground-keepalive for on-demand
    // reaction in standby. When true AND connectOnDemand.enabled,
    // PrivycsApp starts PrivycsVpnService with ACTION_START_MONITOR
    // at app boot so the service runs as a foreground service even
    // without an active tunnel. Trade-off: persistent low-priority
    // notification, but NetworkMonitor's 30 s tick + system
    // NetworkCallback survive Doze and on-demand reaction stays
    // <1 s instead of the ≤15 min WorkManager fallback. Default
    // off so users opt in only if they value reaction speed over
    // a persistent notification entry.
    @SerialName("keep_monitor_alive")
    val keepMonitorAlive: Boolean = false
)

// Gateway API models matching desktop api_client.go
@Serializable
data class RemoteConfigEntry(
    val id: Int,
    @SerialName("peer_name")
    val peerName: String,
    val protocol: String,
    @SerialName("interface_name")
    val interfaceName: String = "",
    @SerialName("agent_id")
    val agentId: String = "",
    @SerialName("vpn_ip")
    val vpnIp: String = "",
    val status: String = "",
    @SerialName("last_handshake")
    val lastHandshake: String = "",
    // True when this WireGuard entry carries AmneziaWG obfuscation
    // (jc/jmin/jmax/s*/h*/i* keys in the rendered config). The
    // gateway labels AWG enrollments as `protocol: "wireguard"`
    // for backwards-compat with the API; this flag lets the
    // client surface the AmneziaWG icon + label without having
    // to download the .conf first to content-detect.
    //
    // Server-side field name is `obfuscation_enabled` per
    // privycs/cmd/gateway/connect_my_configs_api.go:45 — earlier
    // we had this as `obfuscated` which silently never matched
    // (kotlinx-serialization ignoreUnknownKeys made the mismatch
    // invisible). Defaults to false so older gateway builds keep
    // working.
    @SerialName("obfuscation_enabled")
    val obfuscationEnabled: Boolean = false
)

@Serializable
data class RemoteProfile(
    val success: Boolean = true,
    val user: String = "",
    val count: Int = 0,
    val configs: List<RemoteConfigEntry> = emptyList()
)

@Serializable
data class RemoteConfigResponse(
    val success: Boolean = true,
    val config: kotlinx.serialization.json.JsonElement? = null
)
