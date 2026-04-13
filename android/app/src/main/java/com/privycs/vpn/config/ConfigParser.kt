package com.privycs.vpn.config

import com.privycs.vpn.data.models.ProtocolConfig
import com.privycs.vpn.data.models.VpnProtocol
import java.time.Instant

/**
 * Parses VPN configuration files and auto-detects protocol.
 * Mirrors the desktop app's detectProtocol() and config parsing logic.
 */
object ConfigParser {

    data class ParseResult(
        val protocol: VpnProtocol,
        val serverAddress: String,
        val localAddress: String,
        val connectionName: String
    )

    /**
     * Auto-detect protocol from filename extension and content.
     * Matches desktop protocol.go detectProtocol().
     */
    fun detectProtocol(content: String, filename: String): VpnProtocol? {
        val lower = filename.lowercase()

        // By extension
        if (lower.endsWith(".conf")) return VpnProtocol.WIREGUARD
        if (lower.endsWith(".ovpn")) return VpnProtocol.OPENVPN
        if (lower.endsWith(".sswan") || lower.endsWith(".mobileconfig") || lower.endsWith(".p12")) {
            return VpnProtocol.IPSEC
        }

        // By content
        if (content.contains("[Interface]") && content.contains("PrivateKey")) {
            return VpnProtocol.WIREGUARD
        }
        if (content.contains("remote ") || content.contains("<ca>") || content.contains("client")) {
            return VpnProtocol.OPENVPN
        }

        return null
    }

    /**
     * Parse a VPN config file and extract metadata.
     */
    fun parse(content: String, filename: String): ParseResult? {
        val protocol = detectProtocol(content, filename) ?: return null

        return when (protocol) {
            VpnProtocol.WIREGUARD -> parseWireGuard(content, filename)
            VpnProtocol.OPENVPN -> parseOpenVpn(content, filename)
            VpnProtocol.IPSEC -> parseIpSec(content, filename)
        }
    }

    /**
     * Build a ProtocolConfig from raw content and filename.
     */
    fun buildProtocolConfig(content: String, filename: String): ProtocolConfig? {
        val result = parse(content, filename) ?: return null

        return ProtocolConfig(
            protocol = result.protocol,
            configContent = content,
            filename = filename,
            serverAddress = result.serverAddress,
            localAddress = result.localAddress,
            addedAt = Instant.now().toString()
        )
    }

    /**
     * Derive a connection name from the filename.
     */
    fun deriveConnectionName(filename: String): String {
        return filename
            .substringBeforeLast(".")
            .replace(Regex("[_-]+"), " ")
            .trim()
            .ifBlank { "VPN Connection" }
    }

    // -- WireGuard parsing --

    private fun parseWireGuard(content: String, filename: String): ParseResult {
        var endpoint = ""
        var address = ""

        for (line in content.lines()) {
            val trimmed = line.trim()
            when {
                trimmed.startsWith("Endpoint", ignoreCase = true) -> {
                    endpoint = trimmed.substringAfter("=").trim()
                }
                trimmed.startsWith("Address", ignoreCase = true) -> {
                    address = trimmed.substringAfter("=").trim()
                }
            }
        }

        return ParseResult(
            protocol = VpnProtocol.WIREGUARD,
            serverAddress = endpoint,
            localAddress = address,
            connectionName = deriveConnectionName(filename)
        )
    }

    // -- OpenVPN parsing --

    private fun parseOpenVpn(content: String, filename: String): ParseResult {
        var remote = ""

        for (line in content.lines()) {
            val trimmed = line.trim()
            if (trimmed.startsWith("remote ", ignoreCase = true)) {
                // "remote server.example.com 1194 udp"
                val parts = trimmed.split("\\s+".toRegex())
                if (parts.size >= 2) {
                    remote = parts[1]
                    if (parts.size >= 3) {
                        remote += ":${parts[2]}"
                    }
                }
                break
            }
        }

        return ParseResult(
            protocol = VpnProtocol.OPENVPN,
            serverAddress = remote,
            localAddress = "",
            connectionName = deriveConnectionName(filename)
        )
    }

    // -- IPSec parsing --

    private fun parseIpSec(content: String, filename: String): ParseResult {
        var server = ""

        // Try to parse .sswan JSON-like format
        for (line in content.lines()) {
            val trimmed = line.trim()
            if (trimmed.contains("\"remote\"") || trimmed.contains("\"server\"")) {
                val value = trimmed.substringAfter(":").trim().trim('"', ',', ' ')
                if (value.isNotBlank()) {
                    server = value
                    break
                }
            }
        }

        return ParseResult(
            protocol = VpnProtocol.IPSEC,
            serverAddress = server,
            localAddress = "",
            connectionName = deriveConnectionName(filename)
        )
    }
}
