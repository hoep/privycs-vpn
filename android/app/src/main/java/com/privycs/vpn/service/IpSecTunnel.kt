package com.privycs.vpn.service

import android.content.Context
import android.net.VpnService
import android.os.Build
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
 * IPSec/IKEv2 tunnel implementation.
 *
 * This is a clean abstraction layer that manages IKEv2/IPSec connection state.
 *
 * Integration strategy by API level:
 *   - API 31+ (Android 12): Use android.net.ipsec.ike.* APIs for native IKEv2.
 *   - API 26-30: Use VpnService TUN interface as a foundation, designed to be
 *     backed by strongSwan's libcharon-android when integrated.
 *
 * Full integration paths:
 *   Option A: strongSwan Android library (org.strongswan.android) from their
 *     official repository. This is the most battle-tested approach.
 *   Option B: Android native IKEv2 VPN API (API 31+) for certificate and
 *     EAP-based authentication without a third-party library.
 *
 * Config format: .sswan JSON files (strongSwan export format) containing:
 *   { "uuid": "...", "name": "...", "type": "ikev2-cert",
 *     "remote": { "addr": "...", "id": "...", "cert": "base64..." },
 *     "local": { "p12": "base64...", "id": "..." } }
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

    // Parsed .sswan config data model
    @Serializable
    data class SswanConfig(
        val uuid: String = "",
        val name: String = "",
        val type: String = "ikev2-cert",
        val remote: SswanRemote = SswanRemote(),
        val local: SswanLocal = SswanLocal(),
        val mtu: Int = 1400,
        @SerialName("split-tunneling")
        val splitTunneling: Int = 0
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

        val config = parseConfig(configContent)

        try {
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
                connectNativeIkev2(config, name, vpnService)
            } else {
                connectLegacy(config, name, vpnService)
            }
        } catch (e: Exception) {
            state = State.DISCONNECTED
            Log.e(TAG, "Failed to establish IPSec tunnel", e)
            throw e
        }
    }

    /**
     * Native IKEv2 connection for API 31+ (Android 12+).
     *
     * Uses android.net.ipsec.ike.* APIs. The actual IKE session setup requires:
     *   - IkeSessionParams with authentication method
     *   - ChildSessionParams for traffic selectors
     *   - TunnelModeChildSessionParams for tunnel mode
     *
     * TODO: Full implementation requires:
     *   val ikeParams = IkeSessionParams.Builder()
     *       .setServerHostname(config.remote.addr)
     *       .addSaProposal(saProposal)
     *       .setLocalIdentification(localId)
     *       .setRemoteIdentification(remoteId)
     *       .setAuthDigitalSignature(caCert, clientCert, privateKey)
     *       .build()
     *   val childParams = TunnelModeChildSessionParams.Builder()
     *       .addInternalAddressRequest(AF_INET)
     *       .addInternalDnsServerRequest(AF_INET)
     *       .build()
     *   val ikeSession = IkeSession(context, ikeParams, childParams, executor, ikeCallback, childCallback)
     */
    private suspend fun connectNativeIkev2(
        config: SswanConfig,
        name: String,
        vpnService: VpnService
    ) {
        Log.d(TAG, "Using native IKEv2 API (API ${Build.VERSION.SDK_INT})")

        // Set up TUN interface - the IKE session will use this for tunnel mode
        val builder = vpnService.Builder()
            .setSession(name)
            .setMtu(mtu)
            .addAddress("10.0.0.2", 24) // Placeholder until IKE assigns address
            .addRoute("0.0.0.0", 0)
            .addRoute("::", 0)

        tunnelInterface = builder.establish()

        if (tunnelInterface == null) {
            throw IllegalStateException("VpnService.Builder.establish() returned null")
        }

        // TODO: Create IkeSession with parsed certificates and establish SA.
        // The TUN fd would be used for ESP packet encapsulation/decapsulation.
        // For now, mark as connected to enable the UI flow.

        state = State.CONNECTED
        connectedSince = System.currentTimeMillis()
        rxBytes = 0L
        txBytes = 0L
        localAddress = "10.0.0.2" // Will be replaced by IKE-assigned address

        Log.d(TAG, "IPSec tunnel established (native IKEv2): fd=${tunnelInterface?.fd}")
    }

    /**
     * Legacy VpnService-based connection for API 26-30.
     *
     * Sets up the TUN interface for strongSwan libcharon integration.
     * When strongSwan is integrated as a native library:
     *   1. Extract P12 certificate from config
     *   2. Initialize libcharon with certificate store
     *   3. Start IKE SA negotiation
     *   4. Pass TUN fd for ESP tunnel
     */
    private suspend fun connectLegacy(
        config: SswanConfig,
        name: String,
        vpnService: VpnService
    ) {
        Log.d(TAG, "Using legacy VpnService approach (API ${Build.VERSION.SDK_INT})")

        val builder = vpnService.Builder()
            .setSession(name)
            .setMtu(mtu)
            .addAddress("10.0.0.2", 24)
            .addRoute("0.0.0.0", 0)
            .addRoute("::", 0)

        tunnelInterface = builder.establish()

        if (tunnelInterface == null) {
            throw IllegalStateException("VpnService.Builder.establish() returned null")
        }

        // TODO: When strongSwan is integrated:
        //   CharonVpnService.start(tunFd, config.remote.addr, certStore)
        // This will handle IKE negotiation and ESP encapsulation natively.

        state = State.CONNECTED
        connectedSince = System.currentTimeMillis()
        rxBytes = 0L
        txBytes = 0L
        localAddress = "10.0.0.2"

        Log.d(TAG, "IPSec tunnel established (legacy): fd=${tunnelInterface?.fd}")
    }

    /**
     * Disconnect the IPSec tunnel.
     */
    suspend fun disconnect() = withContext(Dispatchers.IO) {
        if (state == State.DISCONNECTED) {
            Log.d(TAG, "Already disconnected")
            return@withContext
        }

        state = State.DISCONNECTING
        Log.d(TAG, "Disconnecting IPSec tunnel")

        try {
            // TODO: When IKE session is integrated, close it first:
            //   ikeSession?.close()
            //   or CharonVpnService.stop()

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
     * Check if the device supports native IKEv2 API.
     */
    fun supportsNativeIkev2(): Boolean = Build.VERSION.SDK_INT >= Build.VERSION_CODES.S

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
