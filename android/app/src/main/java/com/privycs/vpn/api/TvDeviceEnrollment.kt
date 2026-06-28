package com.privycs.vpn.api

import io.ktor.client.HttpClient
import io.ktor.client.call.body
import io.ktor.client.engine.okhttp.OkHttp
import io.ktor.client.plugins.contentnegotiation.ContentNegotiation
import io.ktor.client.plugins.defaultRequest
import io.ktor.client.request.post
import io.ktor.client.request.setBody
import io.ktor.client.statement.bodyAsText
import io.ktor.http.ContentType
import io.ktor.http.contentType
import io.ktor.serialization.kotlinx.json.json
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json
import java.util.concurrent.TimeUnit

/**
 * TV device-code ("link a TV") enrollment client.
 *
 * TVs have no camera (no QR scan) and a painful on-screen keyboard, so
 * instead of typing a gateway URL + API key the user enters a short
 * code shown on the TV at privycs.com/link on their already-logged-in
 * phone. This is the OAuth 2.0 Device Authorization Grant (RFC 8628)
 * trimmed to our needs. The full design + endpoint contract lives in
 * the private docs (TV_PORT_PLAN.md §2 + GATEWAY_TASK_tv-device-
 * enrollment.md).
 *
 * NOTE (2026-06-04): the gateway endpoints under /api/v1/tv/device/
 * do NOT exist yet — they are P0 in the TV port plan and tracked in
 * GATEWAY_TASK_tv-device-enrollment.md. Until they land, [start] / [poll]
 * will fail with a network/HTTP error and the TV enrollment UI falls
 * back to the manual gateway-URL + token entry path (TvEnrollScreen).
 *
 * Flow:
 *   1. [start]  → server returns { user_code, device_code, verification_uri,
 *                 interval, expires_in }. TV shows user_code + verification_uri.
 *   2. user opens verification_uri on their phone, enters user_code, taps Link.
 *   3. [poll]   → repeatedly (respecting `interval`) until it returns
 *                 [PollResult.Approved] carrying the scoped (token, gatewayUrl).
 *
 * The resulting (gatewayUrl, token) pair is exactly what the existing
 * QR / manual gateway enrollment produces, so the TV reuses
 * [GatewayApiClient] + SettingsRepository.updateGatewayConfig unchanged.
 *
 * Uses the same Ktor-OkHttp + kotlinx-serialization stack as
 * [GatewayApiClient]; no auth header (start/poll are unauthenticated —
 * the phone-side /approve is the authenticated half, handled in the web UI).
 */
class TvDeviceEnrollment(
    private val gatewayUrl: String,
    /** "androidtv" per the endpoint spec (Apple TV sends "appletv"). */
    private val client: String = CLIENT_ANDROID_TV,
    private val appVersion: String = "",
) {

    companion object {
        const val CLIENT_ANDROID_TV = "androidtv"

        // Spec defaults (overridden by the server's StartResponse when present).
        private const val DEFAULT_POLL_INTERVAL_SEC = 5
        private const val DEFAULT_EXPIRES_IN_SEC = 600
    }

    private val jsonParser = Json {
        ignoreUnknownKeys = true
        isLenient = true
        coerceInputValues = true
    }

    private val http = HttpClient(OkHttp) {
        install(ContentNegotiation) {
            json(jsonParser)
        }
        engine {
            config {
                connectTimeout(15, TimeUnit.SECONDS)
                readTimeout(15, TimeUnit.SECONDS)
                // Don't follow redirects to a host the user didn't configure
                // (SSRF hardening, consistent with GatewayApiClient).
                followRedirects(false)
                followSslRedirects(false)
            }
        }
        defaultRequest {
            contentType(ContentType.Application.Json)
        }
    }

    private fun baseUrl(): String = gatewayUrl.trimEnd('/')

    // ---- wire models -------------------------------------------------------

    @Serializable
    private data class StartRequest(
        val client: String,
        @SerialName("app_version") val appVersion: String,
    )

    @Serializable
    data class StartResponse(
        /** Opaque server-stored secret; passed back verbatim to [poll]. Never shown. */
        @SerialName("device_code") val deviceCode: String = "",
        /** Short human-enterable code, e.g. "WDJB-MJHT". Shown to the user. */
        @SerialName("user_code") val userCode: String = "",
        /** Where the user goes on their phone, e.g. https://www.privycs.com/link */
        @SerialName("verification_uri") val verificationUri: String = "",
        /** Same URL with ?code= embedded (for a QR the phone can scan). */
        @SerialName("verification_uri_complete") val verificationUriComplete: String = "",
        /** Minimum seconds between [poll] calls. */
        val interval: Int = DEFAULT_POLL_INTERVAL_SEC,
        /** Seconds the user_code + device_code stay valid. */
        @SerialName("expires_in") val expiresIn: Int = DEFAULT_EXPIRES_IN_SEC,
    )

    @Serializable
    private data class PollRequest(
        @SerialName("device_code") val deviceCode: String,
    )

    @Serializable
    private data class PollApprovedBody(
        val token: String = "",
        @SerialName("gateway_url") val gatewayUrl: String = "",
        val label: String = "",
    )

    @Serializable
    private data class PollErrorBody(
        val error: String = "",
    )

    /**
     * Outcome of a single [poll] call. Maps the spec's HTTP-status-coded
     * responses onto a sealed type the UI polling loop branches on.
     */
    sealed class PollResult {
        /** 428 authorization_pending — keep polling. */
        object Pending : PollResult()

        /** 429 slow_down — back off (add a few seconds) then keep polling. */
        object SlowDown : PollResult()

        /** 400 expired_token — device_code expired or already consumed; restart from [start]. */
        object Expired : PollResult()

        /** 200 — approved. Carries the scoped credential to persist. */
        data class Approved(
            val token: String,
            val gatewayUrl: String,
            val label: String,
        ) : PollResult()

        /** Transport / unexpected error (incl. endpoint-not-yet-live). Caller decides retry vs. surface. */
        data class Error(val message: String) : PollResult()
    }

    /**
     * Begin a device-code session. Returns the user-facing code +
     * polling parameters. Throws on transport / non-2xx failure (the
     * UI catches this and offers the manual fallback).
     */
    suspend fun start(): StartResponse {
        val response = http.post("${baseUrl()}/api/v1/tv/device/start") {
            setBody(StartRequest(client = client, appVersion = appVersion))
        }
        if (response.status.value !in 200..299) {
            throw TvEnrollmentException(
                "device/start failed (HTTP ${response.status.value}): ${response.bodyAsText()}"
            )
        }
        return response.body()
    }

    /**
     * Poll once for approval. Branch on the returned [PollResult]:
     * Pending/SlowDown → wait `interval` (+grace on SlowDown) and call again;
     * Approved → persist (token, gatewayUrl) and stop;
     * Expired → restart from [start];
     * Error → transient — caller may retry or surface.
     */
    suspend fun poll(deviceCode: String): PollResult {
        return try {
            val response = http.post("${baseUrl()}/api/v1/tv/device/poll") {
                setBody(PollRequest(deviceCode = deviceCode))
            }
            when (response.status.value) {
                200 -> {
                    val body = jsonParser.decodeFromString(
                        PollApprovedBody.serializer(), response.bodyAsText()
                    )
                    if (body.token.isBlank()) {
                        PollResult.Error("approved response missing token")
                    } else {
                        PollResult.Approved(
                            token = body.token,
                            // Fall back to the gateway we polled if the
                            // server omits gateway_url (it may, when the
                            // TV already knows the base URL it talked to).
                            gatewayUrl = body.gatewayUrl.ifBlank { baseUrl() },
                            label = body.label,
                        )
                    }
                }
                428 -> PollResult.Pending
                429 -> PollResult.SlowDown
                400, 410 -> PollResult.Expired
                else -> {
                    // Try to read the spec's { "error": ... } body for a
                    // friendlier message; fall back to the raw text.
                    val raw = response.bodyAsText()
                    val parsed = runCatching {
                        jsonParser.decodeFromString(PollErrorBody.serializer(), raw).error
                    }.getOrNull()
                    when (parsed) {
                        "authorization_pending" -> PollResult.Pending
                        "slow_down" -> PollResult.SlowDown
                        "expired_token" -> PollResult.Expired
                        else -> PollResult.Error("HTTP ${response.status.value}: $raw")
                    }
                }
            }
        } catch (e: Exception) {
            PollResult.Error(e.message ?: e.javaClass.simpleName)
        }
    }

    fun close() {
        http.close()
    }

    class TvEnrollmentException(message: String) : Exception(message)
}
