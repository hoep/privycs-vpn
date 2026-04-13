package com.privycs.vpn.service

import android.net.VpnService
import android.os.ParcelFileDescriptor
import android.util.Log
import com.privycs.vpn.data.models.VpnProtocol
import com.privycs.vpn.data.models.VpnStatus
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import java.net.InetAddress

/**
 * OpenVPN tunnel implementation.
 *
 * This is a clean abstraction layer that manages OpenVPN connection state
 * and VpnService TUN interface setup. It is designed to be swapped for a
 * full ics-openvpn (de.blinkt.openvpn) integration later.
 *
 * Integration path for full OpenVPN support:
 *   Option A: Add "de.blinkt.openvpn:openvpn-core" from JitPack if published.
 *   Option B: Clone ics-openvpn as a Git submodule and include as a local module.
 *   Option C: Use the OpenVPN management interface with a bundled openvpn binary.
 *
 * Current implementation: manages connection lifecycle, config parsing, state
 * tracking, and statistics reporting via Android VpnService TUN interface.
 */
class OpenVpnTunnel {

    companion object {
        private const val TAG = "OpenVpnTunnel"
    }

    enum class State {
        DISCONNECTED,
        CONNECTING,
        CONNECTED,
        DISCONNECTING
    }

    private var state: State = State.DISCONNECTED
    private var configContent: String = ""
    private var tunnelInterface: ParcelFileDescriptor? = null
    private var connectedSince: Long = 0L
    private var rxBytes: Long = 0L
    private var txBytes: Long = 0L

    // Parsed config fields
    private var remoteHost: String = ""
    private var remotePort: Int = 1194
    private var protocol: String = "udp"
    private var localAddress: String = ""
    private var localSubnet: String = ""
    private var dnsServers: List<String> = emptyList()
    private var mtu: Int = 1500

    /**
     * Parse an .ovpn config file content and extract relevant parameters.
     */
    fun parseConfig(ovpnContent: String) {
        configContent = ovpnContent

        for (line in ovpnContent.lines()) {
            val trimmed = line.trim()
            when {
                trimmed.startsWith("remote ") -> {
                    val parts = trimmed.split("\\s+".toRegex())
                    if (parts.size >= 2) remoteHost = parts[1]
                    if (parts.size >= 3) remotePort = parts[2].toIntOrNull() ?: 1194
                    if (parts.size >= 4) protocol = parts[3]
                }
                trimmed.startsWith("proto ") -> {
                    protocol = trimmed.removePrefix("proto ").trim()
                }
                trimmed.startsWith("ifconfig ") -> {
                    val parts = trimmed.split("\\s+".toRegex())
                    if (parts.size >= 2) localAddress = parts[1]
                    if (parts.size >= 3) localSubnet = parts[2]
                }
                trimmed.startsWith("dhcp-option DNS ") -> {
                    val dns = trimmed.removePrefix("dhcp-option DNS ").trim()
                    dnsServers = dnsServers + dns
                }
                trimmed.startsWith("tun-mtu ") -> {
                    mtu = trimmed.removePrefix("tun-mtu ").trim().toIntOrNull() ?: 1500
                }
            }
        }

        Log.d(TAG, "Parsed config: remote=$remoteHost:$remotePort proto=$protocol local=$localAddress")
    }

    /**
     * Connect to the VPN using the provided .ovpn config content.
     *
     * NOTE: This implementation sets up the TUN interface via VpnService.Builder.
     * A full OpenVPN data-channel implementation requires either:
     *   - The ics-openvpn native library (recommended)
     *   - A bundled openvpn binary with management interface
     *
     * The VpnService builder call is included to establish the TUN fd, which a
     * real OpenVPN process would use for packet I/O.
     *
     * @param ovpnContent The full .ovpn configuration file content
     * @param name Display name for the connection
     * @param vpnService The active VpnService instance for TUN interface creation
     */
    suspend fun connect(
        ovpnContent: String,
        name: String = "privycs-ovpn",
        vpnService: VpnService
    ) = withContext(Dispatchers.IO) {
        if (state == State.CONNECTED || state == State.CONNECTING) {
            Log.w(TAG, "Already connected or connecting, ignoring connect request")
            return@withContext
        }

        state = State.CONNECTING
        Log.d(TAG, "Connecting OpenVPN tunnel: $name")

        parseConfig(ovpnContent)

        try {
            val builder = vpnService.Builder()
                .setSession(name)
                .setMtu(mtu)

            // Configure local address
            if (localAddress.isNotEmpty()) {
                val prefixLen = subnetToPrefixLength(localSubnet)
                builder.addAddress(localAddress, prefixLen)
            } else {
                // Fallback address if none parsed from config
                builder.addAddress("10.8.0.2", 24)
            }

            // Configure DNS servers
            if (dnsServers.isNotEmpty()) {
                for (dns in dnsServers) {
                    try {
                        builder.addDnsServer(dns)
                    } catch (e: Exception) {
                        Log.w(TAG, "Invalid DNS server: $dns")
                    }
                }
            }

            // Route all traffic through VPN by default
            builder.addRoute("0.0.0.0", 0)
            builder.addRoute("::", 0)

            tunnelInterface = builder.establish()

            if (tunnelInterface == null) {
                state = State.DISCONNECTED
                throw IllegalStateException("VpnService.Builder.establish() returned null - VPN permission may not be granted")
            }

            state = State.CONNECTED
            connectedSince = System.currentTimeMillis()
            rxBytes = 0L
            txBytes = 0L

            Log.d(TAG, "OpenVPN tunnel established: fd=${tunnelInterface?.fd}")

            // TODO: When integrating ics-openvpn or native binary, pass the TUN fd
            // to the OpenVPN process for actual packet handling.
            // Example with management interface:
            //   openvpnProcess = ProcessBuilder("openvpn", "--config", configPath,
            //       "--management", "127.0.0.1", mgmtPort.toString(),
            //       "--dev-type", "tun", "--dev-node", "/dev/tun")
            //       .start()

        } catch (e: Exception) {
            state = State.DISCONNECTED
            Log.e(TAG, "Failed to establish OpenVPN tunnel", e)
            throw e
        }
    }

    /**
     * Disconnect the OpenVPN tunnel.
     */
    suspend fun disconnect() = withContext(Dispatchers.IO) {
        if (state == State.DISCONNECTED) {
            Log.d(TAG, "Already disconnected")
            return@withContext
        }

        state = State.DISCONNECTING
        Log.d(TAG, "Disconnecting OpenVPN tunnel")

        try {
            tunnelInterface?.close()
            tunnelInterface = null
        } catch (e: Exception) {
            Log.w(TAG, "Error closing TUN interface: ${e.message}")
        }

        state = State.DISCONNECTED
        connectedSince = 0L
        Log.d(TAG, "OpenVPN tunnel disconnected")
    }

    /**
     * Get the current tunnel state.
     */
    fun getState(): State = state

    /**
     * Get the TUN file descriptor for packet I/O.
     * Used by the actual OpenVPN process when integrated.
     */
    fun getTunFd(): Int? = tunnelInterface?.fd

    /**
     * Update transfer statistics.
     * Called by the OpenVPN process stats callback when fully integrated.
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
            activeProtocol = VpnProtocol.OPENVPN,
            uptime = if (isUp && connectedSince > 0) System.currentTimeMillis() - connectedSince else 0L,
            rxBytes = rxBytes,
            txBytes = txBytes,
            serverEndpoint = if (remoteHost.isNotEmpty()) "$remoteHost:$remotePort" else "",
            localAddress = localAddress
        )
    }

    /**
     * Convert a dotted subnet mask to a CIDR prefix length.
     */
    private fun subnetToPrefixLength(subnet: String): Int {
        if (subnet.isEmpty()) return 24
        return try {
            val bytes = InetAddress.getByName(subnet).address
            var prefix = 0
            for (b in bytes) {
                var bits = b.toInt() and 0xFF
                while (bits != 0) {
                    prefix += bits and 1
                    bits = bits shr 1
                }
            }
            if (prefix == 0) 24 else prefix
        } catch (e: Exception) {
            24
        }
    }
}
