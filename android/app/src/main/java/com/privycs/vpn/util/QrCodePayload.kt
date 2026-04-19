package com.privycs.vpn.util

import android.net.Uri

/**
 * Parsed representation of a scanned QR payload.
 *
 * QR codes in the wild come in two relevant shapes:
 *
 *  - **Raw WireGuard .conf**: the standard wg-quick QR, as produced by
 *    `qrencode -t ansiutf8 < wg0.conf` and as shown in WireGuard's
 *    own apps. The payload is the entire [Interface]/[Peer] config
 *    text, small enough (~400-800 bytes) to fit comfortably in a QR.
 *    No wrapper, no base64 - just the raw text.
 *
 *  - **Privycs enrollment URL**: our own `privycs://enroll?...` scheme
 *    used for OpenVPN and IPSec where embedding the full config in a
 *    QR is impractical (.ovpn with inline certs is 8-20KB, too big).
 *    The QR contains a pointer that tells the client where to download
 *    the config from (gateway URL + API key + optional connection id
 *    and protocol hint).
 *
 * OpenVPN/IPSec do not have a native "config-in-QR" standard, which is
 * why we need the URL redirect for those protocols. The enrollment URL
 * also works for WireGuard as an alternative when the config is served
 * by a Privycs gateway that already holds the canonical .conf.
 */
sealed class QrCodePayload {
    /** Standard wg-quick QR: the raw config body. */
    data class WireGuardConfig(val content: String) : QrCodePayload()

    /**
     * Privycs-native enrollment URL. All fields optional; the client
     * decides which gateway-API call to make based on what is present.
     */
    data class PrivycsEnrollment(
        val gatewayUrl: String?,
        val apiKey: String?,
        val connectionId: String?,
        val protocol: String?, // "wireguard" | "openvpn" | "ipsec"
        val peerId: String?,
    ) : QrCodePayload()

    /** Unrecognised format - caller decides whether to surface an error. */
    data class Unknown(val raw: String) : QrCodePayload()
}

/**
 * Detect and parse a QR-scanner payload into a [QrCodePayload].
 *
 * Order of checks matters: we test for the Privycs URL first because
 * a malformed Privycs URL would be a real user error worth surfacing,
 * whereas a WireGuard config is a valid fallback that we should accept
 * even if it looks odd. `[Interface]` is a distinctive enough marker
 * that WG detection has close to zero false-positive risk.
 */
fun parseQrPayload(raw: String): QrCodePayload {
    val trimmed = raw.trim()

    // 1. Privycs custom URL scheme
    if (trimmed.startsWith("privycs://", ignoreCase = true)) {
        return parsePrivycsUri(trimmed)
    }

    // 2. Raw WireGuard config. The [Interface] section header is
    //    mandatory in every wg-quick config - if it is present the
    //    payload is a WireGuard config with very high confidence.
    //    We match case-insensitive because some tooling capitalises
    //    differently, and we allow leading whitespace / comments.
    val firstNonCommentLine = trimmed.lines()
        .map { it.trim() }
        .firstOrNull { it.isNotEmpty() && !it.startsWith("#") }
        ?: ""
    if (firstNonCommentLine.equals("[Interface]", ignoreCase = true)) {
        return QrCodePayload.WireGuardConfig(trimmed)
    }

    return QrCodePayload.Unknown(trimmed)
}

private fun parsePrivycsUri(uri: String): QrCodePayload {
    return try {
        val parsed = Uri.parse(uri)
        if (!parsed.scheme.equals("privycs", ignoreCase = true)) {
            return QrCodePayload.Unknown(uri)
        }
        // Only `enroll` is defined today. Other hosts (`connect`,
        // `invite`, ...) can be added later without breaking the
        // existing check.
        if (!parsed.host.equals("enroll", ignoreCase = true)) {
            return QrCodePayload.Unknown(uri)
        }
        QrCodePayload.PrivycsEnrollment(
            gatewayUrl = parsed.getQueryParameter("url")
                ?: parsed.getQueryParameter("gateway"),
            apiKey = parsed.getQueryParameter("apikey")
                ?: parsed.getQueryParameter("token"),
            connectionId = parsed.getQueryParameter("connection_id")
                ?: parsed.getQueryParameter("connection"),
            protocol = parsed.getQueryParameter("protocol")?.lowercase(),
            peerId = parsed.getQueryParameter("peer_id")
                ?: parsed.getQueryParameter("peer"),
        )
    } catch (_: Exception) {
        QrCodePayload.Unknown(uri)
    }
}
