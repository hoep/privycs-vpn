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

        return response.body()
    }

    /**
     * Download a specific VPN config with full secrets.
     * Matches desktop FetchMyConfig().
     */
    suspend fun fetchConfig(protocol: String, configId: Int): String {
        val path = "/api/v1/connect/my-configs/$protocol-$configId"
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

        return when (protocol) {
            "wireguard" -> buildWireGuardConf(bodyText)
            "openvpn" -> extractOpenVpnConfig(bodyText)
            else -> bodyText
        }
    }

    /**
     * Build a WireGuard .conf from the JSON response.
     * Matches desktop buildWireGuardConf().
     */
    private fun buildWireGuardConf(jsonBody: String): String {
        val root = jsonParser.parseToJsonElement(jsonBody).jsonObject
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

        return buildString {
            appendLine("[Interface]")
            appendLine("PrivateKey = $privateKey")
            appendLine("Address = $address")
            if (dns.isNotBlank()) appendLine("DNS = $dns")
            if (mtu > 0) appendLine("MTU = $mtu")
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
        val root = jsonParser.parseToJsonElement(jsonBody).jsonObject
        return root["config"]?.jsonPrimitive?.content
            ?: throw ApiException("Config not available")
    }

    fun close() {
        client.close()
    }

    class ApiException(message: String) : Exception(message)
}
