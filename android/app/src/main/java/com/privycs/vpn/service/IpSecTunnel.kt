package com.privycs.vpn.service

import android.net.VpnService
import android.os.ParcelFileDescriptor
import android.util.Base64
import android.util.Log
import com.privycs.vpn.data.models.VpnProtocol
import com.privycs.vpn.data.models.VpnStatus
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json

/**
 * IPSec/IKEv2 tunnel - parses .sswan profiles and (once LibC-5 lands) drives
 * strongSwan's CharonVpnService. The actual IKE + ESP data plane runs in the
 * bundled libcharon/libipsec native libraries shipped by :strongswan-lib.
 *
 * Config format: .sswan JSON as emitted by the gateway at
 * cmd/gateway/ipsec_mobile_profiles.go:generateAndroidSSWANProfile.
 */
class IpSecTunnel {

    companion object {
        private const val TAG = "IpSecTunnel"
        private val json = Json { ignoreUnknownKeys = true; isLenient = true }
    }

    enum class State {
        DISCONNECTED,
        CONNECTING,
        CONNECTED,
        DISCONNECTING
    }

    // Parsed .sswan config data model. Field names match the gateway emitter at
    // cmd/gateway/ipsec_mobile_profiles.go:generateAndroidSSWANProfile.
    @Serializable
    data class SswanConfig(
        val uuid: String = "",
        val name: String = "",
        val type: String = "ikev2-cert",
        val remote: SswanRemote = SswanRemote(),
        val local: SswanLocal = SswanLocal(),
        val mtu: Int = 1400,
        // Gateway emits a CIDR list (bypass/split-tunnel subnets), NOT the
        // strongSwan-official Int-flag form. Deserializing as Int would fail.
        @SerialName("split-tunneling")
        val splitTunneling: List<String> = emptyList(),
        @SerialName("dns-servers")
        val dnsServers: List<String> = emptyList()
    )

    @Serializable
    data class SswanRemote(
        val addr: String = "",
        val id: String = "",
        val cert: String = "",
        @SerialName("certCA")
        val certCA: String = ""
    )

    @Serializable
    data class SswanLocal(
        val p12: String = "",
        val id: String = "",
        // Gateway JSON key is "p12-password" (hyphen). Without the explicit
        // SerialName this deserializes to empty and PKCS#12 decoding fails.
        @SerialName("p12-password")
        val p12Password: String = "",
        @SerialName("eap_id")
        val eapId: String = ""
    )

    private var state: State = State.DISCONNECTED
    private var sswanConfig: SswanConfig? = null
    private var tunnelInterface: ParcelFileDescriptor? = null
    private var connectedSince: Long = 0L
    private var rxBytes: Long = 0L
    private var txBytes: Long = 0L

    // Connection parameters extracted from config
    private var remoteAddress: String = ""
    private var remoteId: String = ""
    private var localId: String = ""
    private var localAddress: String = ""
    private var authType: String = "ikev2-cert"
    private var mtu: Int = 1400

    /**
     * Parse a .sswan JSON config file.
     */
    fun parseConfig(configContent: String): SswanConfig {
        val config = json.decodeFromString<SswanConfig>(configContent)
        sswanConfig = config

        remoteAddress = config.remote.addr
        remoteId = config.remote.id.ifEmpty { config.remote.addr }
        localId = config.local.id
        authType = config.type
        mtu = config.mtu

        Log.d(TAG, "Parsed .sswan config: remote=$remoteAddress type=$authType name=${config.name}")
        return config
    }

    /**
     * Connect to the IPSec/IKEv2 VPN.
     *
     * For API 31+: Prepares for native IKEv2 VPN API usage.
     * For API 26-30: Sets up VPN TUN interface for strongSwan integration.
     *
     * @param configContent The .sswan JSON config content
     * @param name Display name for the connection
     * @param vpnService The active VpnService instance
     */
    suspend fun connect(
        configContent: String,
        name: String = "privycs-ipsec",
        vpnService: VpnService
    ) = withContext(Dispatchers.IO) {
        if (state == State.CONNECTED || state == State.CONNECTING) {
            Log.w(TAG, "Already connected or connecting, ignoring connect request")
            return@withContext
        }

        state = State.CONNECTING
        Log.d(TAG, "Connecting IPSec tunnel: $name")

        // Parse to validate the profile and capture the fields the libcharon
        // bridge will need (server hostname, IDs, PKCS#12 bytes+password).
        parseConfig(configContent)

        // The libcharon integration groundwork is in place (strongSwan submodule
        // wired as :strongswan-lib, native libs build into the APK, PrivycsApp
        // extends StrongSwanApplication so JNI_OnLoad runs), but the connect
        // path to CharonVpnService is not hooked up yet:
        //   1. Map our SswanConfig to org.strongswan.android.data.VpnProfile
        //   2. Install the PKCS#12 bundle into the Android KeyChain
        //      (requires Activity context for KeyChain.createInstallIntent)
        //   3. Persist the profile into strongSwan's VpnProfileSource (DB)
        //   4. Start CharonVpnService with KEY_UUID extra
        //   5. Bind VpnStateService to drive our VpnStatus updates
        // Tracked as task LibC-5.
        state = State.DISCONNECTED
        throw UnsupportedOperationException(
            "IPSec connect is not yet wired to CharonVpnService (LibC-5 pending)."
        )
    }

    /**
     * Disconnect the IPSec tunnel. Wired to CharonVpnService.DISCONNECT_ACTION
     * as part of LibC-5; currently a no-op because connect() is still a stub.
     */
    suspend fun disconnect() = withContext(Dispatchers.IO) {
        if (state == State.DISCONNECTED) {
            Log.d(TAG, "Already disconnected")
            return@withContext
        }

        state = State.DISCONNECTING
        Log.d(TAG, "Disconnecting IPSec tunnel")

        try {
            tunnelInterface?.close()
            tunnelInterface = null
        } catch (e: Exception) {
            Log.w(TAG, "Error closing IPSec tunnel: ${e.message}")
        }

        state = State.DISCONNECTED
        connectedSince = 0L
        Log.d(TAG, "IPSec tunnel disconnected")
    }

    /**
     * Get the current tunnel state.
     */
    fun getState(): State = state

    /**
     * Get the TUN file descriptor.
     */
    fun getTunFd(): Int? = tunnelInterface?.fd

    /**
     * Update transfer statistics.
     * Called by the IKE/ESP layer when fully integrated.
     */
    fun updateStats(rx: Long, tx: Long) {
        rxBytes = rx
        txBytes = tx
    }

    /**
     * Build a VpnStatus from current tunnel state.
     */
    fun getStatus(connectionName: String, connectionId: String): VpnStatus {
        val isUp = state == State.CONNECTED

        return VpnStatus(
            connected = isUp,
            connectionName = connectionName,
            connectionId = connectionId,
            activeProtocol = VpnProtocol.IPSEC,
            uptime = if (isUp && connectedSince > 0) System.currentTimeMillis() - connectedSince else 0L,
            rxBytes = rxBytes,
            txBytes = txBytes,
            serverEndpoint = remoteAddress,
            localAddress = localAddress
        )
    }

    /**
     * Extract certificate data from the .sswan config for key store import.
     */
    fun extractP12Bytes(): ByteArray? {
        val p12Base64 = sswanConfig?.local?.p12 ?: return null
        if (p12Base64.isEmpty()) return null
        return try {
            Base64.decode(p12Base64, Base64.DEFAULT)
        } catch (e: Exception) {
            Log.e(TAG, "Failed to decode P12 data", e)
            null
        }
    }

    /**
     * Extract CA certificate data from the .sswan config.
     */
    fun extractCaCertBytes(): ByteArray? {
        val certBase64 = sswanConfig?.remote?.certCA ?: return null
        if (certBase64.isEmpty()) return null
        return try {
            Base64.decode(certBase64, Base64.DEFAULT)
        } catch (e: Exception) {
            Log.e(TAG, "Failed to decode CA cert data", e)
            null
        }
    }
}
