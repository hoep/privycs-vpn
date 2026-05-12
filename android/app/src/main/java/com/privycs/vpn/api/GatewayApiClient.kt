package com.privycs.vpn.api

import com.privycs.vpn.data.models.RemoteConfigEntry
import com.privycs.vpn.data.models.RemoteProfile
import io.ktor.client.HttpClient
import io.ktor.client.call.body
import io.ktor.client.engine.okhttp.OkHttp
import io.ktor.client.plugins.contentnegotiation.ContentNegotiation
import io.ktor.client.plugins.defaultRequest
import io.ktor.client.request.get
import io.ktor.client.request.header
import io.ktor.client.statement.bodyAsText
import io.ktor.http.ContentType
import io.ktor.http.contentType
import io.ktor.http.isSuccess
import io.ktor.serialization.kotlinx.json.json
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.boolean
import kotlinx.serialization.json.int
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import java.util.concurrent.TimeUnit

/**
 * Gateway API client matching the desktop app's api_client.go.
 * Uses Ktor HttpClient with OkHttp engine for API calls.
 */
class GatewayApiClient(
    private val gatewayUrl: String,
    private val apiKey: String
) {

    private val jsonParser = Json {
        ignoreUnknownKeys = true
        isLenient = true
        // Coerce explicit nulls in server responses to model defaults so a
        // gateway-side `"field": null` for a String field we declared as
        // non-nullable doesn't blow up the whole fetchProfile() call with
        // "Unexpected Json token at offset <N>".
        coerceInputValues = true
    }

    private val client = HttpClient(OkHttp) {
        install(ContentNegotiation) {
            json(jsonParser)
        }
        engine {
            config {
                connectTimeout(15, TimeUnit.SECONDS)
                readTimeout(15, TimeUnit.SECONDS)
            }
        }
        defaultRequest {
            header("Authorization", "Bearer $apiKey")
            header("Accept", "application/json")
            contentType(ContentType.Application.Json)
        }
    }

    private fun baseUrl(): String = gatewayUrl.trimEnd('/')

    /**
     * Fetch the user's profile and config list.
     * Matches desktop FetchMyProfile().
     */
    suspend fun fetchProfile(): RemoteProfile {
        val response = client.get("${baseUrl()}/api/v1/connect/my-configs")

        if (!response.status.isSuccess()) {
            val code = response.status.value
            when (code) {
                401 -> throw ApiException("Authentication failed - check your API key")
                403 -> throw ApiException("Access denied")
                else -> throw ApiException("API error $code: ${response.bodyAsText()}")
            }
        }

        // Decode the body via our lenient Json parser rather than Ktor's
        // auto-body<T>() which uses its own strict defaults. Then trim to
        // the first balanced {...} so any trailing bytes the gateway or an
        // HTTP middleware may have accidentally appended after the JSON
        // object (kotlinx reports those as "Expected EOF after parsing at
        // offset N") do not blow up the whole fetch.
        val bodyText = response.bodyAsText()
        val jsonOnly = firstJsonObject(bodyText)
        return try {
            jsonParser.decodeFromString(RemoteProfile.serializer(), jsonOnly)
        } catch (e: Exception) {
            throw ApiException(buildImportErrorMessage("/my-configs", bodyText, e))
        }
    }

    /**
     * Return the substring from the first '{' up to its matching '}',
     * treating string literals and escape sequences correctly. Used to
     * defensively strip trailing bytes that some responses carry after a
     * complete JSON object. Returns the original body unchanged if a
     * balanced object cannot be found.
     */
    private fun firstJsonObject(body: String): String {
        val start = body.indexOf('{')
        if (start < 0) return body
        var depth = 0
        var inString = false
        var escape = false
        for (i in start until body.length) {
            val c = body[i]
            if (escape) { escape = false; continue }
            if (inString) {
                if (c == '\\') escape = true
                else if (c == '"') inString = false
                continue
            }
            when (c) {
                '"' -> inString = true
                '{' -> depth++
                '}' -> {
                    depth--
                    if (depth == 0) return body.substring(start, i + 1)
                }
            }
        }
        return body
    }

    private fun buildImportErrorMessage(endpoint: String, body: String, e: Exception): String {
        val msg = e.message ?: e.javaClass.simpleName
        // SerializationException typically contains "at offset N"; extract
        // the number and show the ~80 bytes around it.
        val offset = Regex("offset (\\d+)").find(msg)?.groupValues?.get(1)?.toIntOrNull()
        val summary = if (offset != null && offset in body.indices) {
            val start = (offset - 40).coerceAtLeast(0)
            val end = (offset + 40).coerceAtMost(body.length)
            val window = body.substring(start, end).replace("\n", "\\n")
            "Failed to parse $endpoint response ($msg). Body near offset $offset: '$window'"
        } else {
            "Failed to parse $endpoint response ($msg). Body length=${body.length}."
        }
        // Tee to logcat AND the in-app log file (visible in the Logs screen)
        // so the user can diagnose import failures without a USB adb cable.
        com.privycs.vpn.util.PrivycsLogger.w("GatewayApiClient",
            "$summary\nFull body head: ${body.take(200)}")
        if (offset != null && offset in body.indices) {
            val start = (offset - 200).coerceAtLeast(0)
            val end = (offset + 200).coerceAtMost(body.length)
            com.privycs.vpn.util.PrivycsLogger.w("GatewayApiClient",
                "Body near offset $offset:\n${body.substring(start, end)}")
        }
        return summary
    }

    /**
     * Download a specific VPN config with full secrets.
     * Matches desktop FetchMyConfig().
     */
    suspend fun fetchConfig(protocol: String, configId: Int): String {
        // IPSec: request the .sswan JSON format. Without ?format=sswan the
        // gateway defaults to iOS .mobileconfig (signed plist) which the
        // Android client cannot parse -- surfaces as a misleading base64
        // decode error like "Input-length = 1" when ktor tries to read the
        // binary body as text. Matches desktop api_client.go:FetchMyConfig.
        val path = if (protocol == "ipsec") {
            "/api/v1/connect/my-configs/$protocol-$configId?format=sswan"
        } else {
            "/api/v1/connect/my-configs/$protocol-$configId"
        }
        val response = client.get("${baseUrl()}$path")

        if (!response.status.isSuccess()) {
            val code = response.status.value
            when (code) {
                401 -> throw ApiException("Authentication failed - check your API key")
                403 -> throw ApiException("Access denied")
                else -> throw ApiException("API error $code: ${response.bodyAsText()}")
            }
        }

        val bodyText = response.bodyAsText()

        return try {
            when (protocol) {
                // Server emits AWG configs under the "wireguard"
                // endpoint slug with `obfuscation_config_lines`
                // appended to the [Interface] block. We accept
                // either label for forward-compat with a future
                // server that splits them.
                "wireguard", "amneziawg" -> buildWireGuardConf(bodyText)
                "openvpn" -> extractOpenVpnConfig(bodyText)
                else -> bodyText
            }
        } catch (e: ApiException) {
            // Already descriptive (config-not-available style) - bubble up.
            throw e
        } catch (e: Exception) {
            throw ApiException(buildImportErrorMessage(
                "my-configs/$protocol-$configId", bodyText, e
            ))
        }
    }

    /**
     * Build a WireGuard .conf from the JSON response.
     * Matches desktop buildWireGuardConf().
     */
    private fun buildWireGuardConf(jsonBody: String): String {
        val root = jsonParser.parseToJsonElement(firstJsonObject(jsonBody)).jsonObject
        val config = root["config"]?.jsonObject
            ?: throw ApiException("Config not available")

        val privateKey = config["peer_private_key"]?.jsonPrimitive?.content
            ?: throw ApiException("Config not available (private key missing)")
        val address = config["peer_address"]?.jsonPrimitive?.content ?: ""
        val serverPublicKey = config["server_public_key"]?.jsonPrimitive?.content ?: ""
        val presharedKey = config["preshared_key"]?.jsonPrimitive?.content
        val serverEndpoint = config["server_endpoint"]?.jsonPrimitive?.content ?: ""
        val serverPort = config["server_port"]?.jsonPrimitive?.int ?: 51820
        val allowedIPs = config["allowed_ips"]?.jsonPrimitive?.content ?: "0.0.0.0/0"
        val dns = config["dns"]?.jsonPrimitive?.content ?: ""
        val mtu = config["mtu"]?.jsonPrimitive?.int ?: 0
        val keepalive = config["persistent_keepalive"]?.jsonPrimitive?.int ?: 25
        // AmneziaWG obfuscation block — server (privycs v0.8.4.18+)
        // pre-renders the AWG keys (Jc/Jmin/Jmax/S1-4/H1-4/I1-5) as
        // ready-to-append config lines. Without this, AWG configs
        // downloaded from the gateway would lose their obfuscation
        // params and the client would fall back to vanilla-WG —
        // server with AWG handshake-magic-headers then drops every
        // packet we send. User-visible: tunnel "connects" but no
        // traffic flows.
        val obfuscationLines = try {
            config["obfuscation_config_lines"]?.jsonPrimitive?.content
        } catch (_: Throwable) { null }

        return buildString {
            appendLine("[Interface]")
            appendLine("PrivateKey = $privateKey")
            appendLine("Address = $address")
            if (dns.isNotBlank()) appendLine("DNS = $dns")
            if (mtu > 0) appendLine("MTU = $mtu")
            if (!obfuscationLines.isNullOrBlank()) {
                appendLine(obfuscationLines.trim())
            }
            appendLine()
            appendLine("[Peer]")
            appendLine("PublicKey = $serverPublicKey")
            if (!presharedKey.isNullOrBlank()) appendLine("PresharedKey = $presharedKey")
            appendLine("Endpoint = $serverEndpoint:$serverPort")
            appendLine("AllowedIPs = $allowedIPs")
            appendLine("PersistentKeepalive = $keepalive")
        }
    }

    private fun extractOpenVpnConfig(jsonBody: String): String {
        val root = jsonParser.parseToJsonElement(firstJsonObject(jsonBody)).jsonObject
        return root["config"]?.jsonPrimitive?.content
            ?: throw ApiException("Config not available")
    }

    fun close() {
        client.close()
    }

    class ApiException(message: String) : Exception(message)
}
