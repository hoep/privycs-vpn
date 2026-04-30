package com.privycs.vpn.data.models

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

@Serializable
enum class VpnProtocol(val label: String, val shortLabel: String, val extensions: List<String>) {
    @SerialName("wireguard")
    WIREGUARD("WireGuard", "WG", listOf(".conf")),

    @SerialName("openvpn")
    OPENVPN("OpenVPN", "OVPN", listOf(".ovpn")),

    @SerialName("ipsec")
    IPSEC("IPSec", "IPSec", listOf(".sswan", ".mobileconfig", ".p12"));

    companion object {
        fun fromString(value: String): VpnProtocol? = when (value.lowercase()) {
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
    @SerialName("server_address")
    val serverAddress: String = "",
    @SerialName("local_address")
    val localAddress: String = "",
    @SerialName("added_at")
    val addedAt: String = ""
)

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
    var isFavorite: Boolean = false
) {
    fun getProtocol(protocol: VpnProtocol): ProtocolConfig? =
        protocols.find { it.protocol == protocol }

    fun getActiveConfig(): ProtocolConfig? =
        getProtocol(activeProtocol)

    fun availableProtocols(): List<VpnProtocol> =
        protocols.map { it.protocol }

    fun hasProtocol(protocol: VpnProtocol): Boolean =
        protocols.any { it.protocol == protocol }
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
    val nextRotationAt: Long = 0L
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
    val firstLaunchCompleted: Boolean = false
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
    val lastHandshake: String = ""
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
