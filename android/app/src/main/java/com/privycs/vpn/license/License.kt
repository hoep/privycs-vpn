package com.privycs.vpn.license

import org.bouncycastle.crypto.params.Ed25519PublicKeyParameters
import org.bouncycastle.crypto.signers.Ed25519Signer
import org.json.JSONObject
import java.security.MessageDigest

/**
 * Privycs cross-platform Pro license-key verifier.
 *
 * Byte-identical wire format to the Desktop verifier
 * (desktop/internal/license/license.go) and the gateway-side LemonSqueezy
 * webhook signer (when wired). The same `.privycs-license` file activates
 * on Android, Desktop and (later) iOS — the bundle SKU's promise.
 *
 * Format:
 *
 *     PRVC-{base32(canonicalJSON(payload))}-{base32(ed25519-sig)}
 *
 * base32 = RFC 4648 standard alphabet, no padding, uppercase.
 *
 * Verification is OFFLINE — the embedded public key (BuildConfig
 * .LICENSE_PUBLIC_KEY_HEX) is the trust anchor. No phone-home, no online
 * validation; an offline-only customer can activate.
 *
 * Stdlib + BouncyCastle only. No coroutines, no Android dependencies in
 * this file — pure Kotlin/JVM so the verifier is unit-testable on the
 * JVM without an emulator.
 */
object License {
    const val PREFIX = "PRVC"
    private const val SEP = "-"

    const val SKU_DESKTOP = "privycs_pro_desktop"
    const val SKU_BUNDLE = "privycs_pro_bundle_all"

    const val PLATFORM_ANDROID = "android"
    const val PLATFORM_DESKTOP = "desktop"
    const val PLATFORM_IOS = "ios"

    /** Schema version this app accepts. Bumping requires a coordinated
     *  signer + all-platform verifier upgrade. */
    const val CURRENT_SCHEMA_VERSION = 1

    /**
     * Payload structure inside the canonical-JSON segment. Field names
     * (JSON keys) MUST match the signer exactly — the verifier reads via
     * org.json with explicit key names below.
     */
    data class Payload(
        val v: Int,
        val tier: String,
        val sku: String,
        val platforms: List<String>,
        val issued: String,
        val buyerEmailHash: String?,
    )

    /** Distinct error kinds so the UI can render a tailored message. */
    enum class ErrorKind {
        MALFORMED,
        BAD_SIGNATURE,
        UNSUPPORTED_VERSION,
        WRONG_PLATFORM,
        UNKNOWN_TIER,
        NO_PUBLIC_KEY,
    }

    sealed class Result {
        data class Ok(val payload: Payload) : Result()
        data class Err(val kind: ErrorKind, val detail: String) : Result()
    }

    /**
     * Verify a raw PRVC-...-... key against [pubKeyHex] (a 64-char hex
     * string of the 32-byte ed25519 public key). Returns Ok with the
     * parsed payload on success.
     *
     * The caller passes [expectedPlatform] (typically [PLATFORM_ANDROID])
     * — keys whose payload.platforms array doesn't include it are
     * rejected with WRONG_PLATFORM. Cross-platform bundle keys list all
     * three platforms; a single-Desktop key would fail here as expected.
     */
    fun verify(
        rawKey: String,
        pubKeyHex: String,
        expectedPlatform: String = PLATFORM_ANDROID,
    ): Result {
        if (pubKeyHex.isEmpty()) {
            return Result.Err(ErrorKind.NO_PUBLIC_KEY, "no pubkey configured")
        }
        val pubKey = hexToBytes(pubKeyHex)
            ?: return Result.Err(ErrorKind.NO_PUBLIC_KEY, "invalid hex")
        if (pubKey.size != 32) {
            return Result.Err(ErrorKind.NO_PUBLIC_KEY, "wrong length ${pubKey.size}")
        }

        val parts = rawKey.trim().split(SEP)
        if (parts.size != 3 || parts[0] != PREFIX) {
            return Result.Err(ErrorKind.MALFORMED, "expected PRVC-<payload>-<sig>")
        }

        val payloadBytes = decodeBase32(parts[1])
            ?: return Result.Err(ErrorKind.MALFORMED, "payload not base32")
        val sigBytes = decodeBase32(parts[2])
            ?: return Result.Err(ErrorKind.MALFORMED, "sig not base32")
        if (sigBytes.size != 64) {
            return Result.Err(ErrorKind.MALFORMED, "sig length ${sigBytes.size}, want 64")
        }

        if (!ed25519Verify(pubKey, payloadBytes, sigBytes)) {
            return Result.Err(ErrorKind.BAD_SIGNATURE, "signature does not match")
        }

        val payload = parsePayload(payloadBytes)
            ?: return Result.Err(ErrorKind.MALFORMED, "payload not JSON")
        if (payload.v != CURRENT_SCHEMA_VERSION) {
            return Result.Err(ErrorKind.UNSUPPORTED_VERSION, "v=${payload.v}")
        }
        if (payload.tier != "pro") {
            return Result.Err(ErrorKind.UNKNOWN_TIER, payload.tier)
        }
        if (expectedPlatform.isNotEmpty() && expectedPlatform !in payload.platforms) {
            return Result.Err(
                ErrorKind.WRONG_PLATFORM,
                "platforms=${payload.platforms} does not include $expectedPlatform",
            )
        }
        return Result.Ok(payload)
    }

    /** SHA-256 hex of lowercased trimmed email — matches the Go signer. */
    fun hashBuyerEmail(email: String): String {
        val md = MessageDigest.getInstance("SHA-256")
        val digest = md.digest(email.trim().lowercase().toByteArray(Charsets.UTF_8))
        return digest.joinToString("") { "%02x".format(it) }
    }

    // ---- private helpers ----

    private fun ed25519Verify(pubKey: ByteArray, message: ByteArray, signature: ByteArray): Boolean {
        return try {
            val signer = Ed25519Signer()
            signer.init(false, Ed25519PublicKeyParameters(pubKey, 0))
            signer.update(message, 0, message.size)
            signer.verifySignature(signature)
        } catch (_: Throwable) {
            false
        }
    }

    /**
     * Parse the canonical-JSON payload bytes into a [Payload]. Uses
     * org.json (built into Android) — we don't need the canonicalisation
     * here because the signer already produced canonical bytes and the
     * verifier matches the SIGNATURE against those exact bytes. JSON
     * decoding is for the field-level access only.
     */
    private fun parsePayload(bytes: ByteArray): Payload? {
        return try {
            val obj = JSONObject(String(bytes, Charsets.UTF_8))
            val platforms = mutableListOf<String>()
            val arr = obj.optJSONArray("platforms")
            if (arr != null) {
                for (i in 0 until arr.length()) {
                    platforms.add(arr.getString(i))
                }
            }
            Payload(
                v = obj.optInt("v", 0),
                tier = obj.optString("tier", ""),
                sku = obj.optString("sku", ""),
                platforms = platforms.toList(),
                issued = obj.optString("issued", ""),
                buyerEmailHash = obj.optString("buyer_email_hash").takeIf { it.isNotEmpty() },
            )
        } catch (_: Throwable) {
            null
        }
    }

    /**
     * RFC 4648 base32 decoder — standard alphabet, no padding, uppercase.
     * Matches Go's `base32.StdEncoding.WithPadding(base32.NoPadding)` so
     * round-trip with the signer is byte-identical. Returns null on
     * invalid character.
     */
    private fun decodeBase32(s: String): ByteArray? {
        val alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"
        val src = s.uppercase()
        if (src.isEmpty()) return ByteArray(0)
        val out = ByteArray(src.length * 5 / 8)
        var buffer = 0L
        var bitsLeft = 0
        var outIdx = 0
        for (c in src) {
            val v = alphabet.indexOf(c)
            if (v < 0) return null
            buffer = (buffer shl 5) or v.toLong()
            bitsLeft += 5
            if (bitsLeft >= 8) {
                bitsLeft -= 8
                if (outIdx >= out.size) return null
                out[outIdx++] = ((buffer shr bitsLeft) and 0xFF).toByte()
            }
        }
        return out.copyOf(outIdx)
    }

    private fun hexToBytes(s: String): ByteArray? {
        if (s.length % 2 != 0) return null
        return try {
            ByteArray(s.length / 2) { i ->
                Integer.parseInt(s.substring(i * 2, i * 2 + 2), 16).toByte()
            }
        } catch (_: NumberFormatException) {
            null
        }
    }
}
