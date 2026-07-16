package com.privycs.vpn.service

import org.bouncycastle.jce.provider.BouncyCastleProvider
import java.io.ByteArrayInputStream
import java.io.IOException
import java.security.GeneralSecurityException
import java.security.KeyFactory
import java.security.KeyStore
import java.security.PrivateKey
import java.security.UnrecoverableKeyException
import java.security.cert.X509Certificate
import java.security.spec.PKCS8EncodedKeySpec
import java.util.Base64

/**
 * Parses the PKCS#12 embedded in a .sswan profile into an in-memory client identity.
 *
 * Deliberately free of Android imports so the JVM unit-test source set can exercise the
 * real JCA path (see P12IdentityTest) rather than an instrumented approximation.
 *
 * Replaces the KeyChain-backed identity store. An app cannot delete a credential it
 * installed into KeyChain — there is no Android API for it (IpSecTunnel.cleanupOnDelete) — so
 * the store leaked entries no user could reasonably clean up. The private key must never
 * be persisted: it lives only for the lifetime of the tunnel and is re-derived from
 * ProtocolConfig.configContent (itself AES-GCM sealed) on every connect.
 */
object P12Identity {

    /**
     * charon holds the key as an opaque signing oracle: it NewGlobalRef's the jobject and
     * only ever feeds it to Signature.initSign (android_private_key.c:291), while type,
     * size and fingerprint are read from [leaf]'s public key (android_private_key_create
     * takes key + pubkey as a pair). get_encoding is hard-wired FALSE, so the two must be
     * handed over together or charon cannot describe the key at all.
     */
    data class Identity(
        val privateKey: PrivateKey,
        val leafDer: ByteArray,
        val leaf: X509Certificate,
    ) {
        // ByteArray in a data class gives identity-based equals/hashCode; the certificate
        // is the real identity of this pair.
        override fun equals(other: Any?): Boolean =
            this === other || (other is Identity && leaf == other.leaf)

        override fun hashCode(): Int = leaf.hashCode()
    }

    enum class Reason {
        INVALID_BASE64,
        WRONG_PASSWORD,
        NO_KEY_ENTRY,
        NO_LEAF_CERTIFICATE,
        UNUSABLE_KEY,
        MALFORMED,
    }

    /** Carries no key material and no password — the message is safe to surface and log. */
    class P12Exception(
        val reason: Reason,
        message: String,
        cause: Throwable? = null,
    ) : Exception(message, cause)

    /**
     * The provider is passed as an INSTANCE, never registered. The Android platform already
     * owns the name "BC" with its own crippled provider, and Security.addProvider silently
     * returns -1 rather than replacing it — a name lookup would resolve to the platform's.
     */
    private fun bouncyCastle() = BouncyCastleProvider()

    /**
     * @param p12Base64 base64 of the raw PKCS#12, as carried verbatim in the .sswan's
     *   local.p12. May contain whitespace: the gateway pretty-prints its JSON.
     * @param password the .sswan's local.p12-password (per-download random, same document).
     */
    fun parse(p12Base64: String, password: String): Identity {
        val der = decodeBase64(p12Base64)
        val store = load(der, password)
        val alias = firstKeyAlias(store)

        val bcKey = try {
            store.getKey(alias, password.toCharArray()) as? PrivateKey
                ?: throw P12Exception(Reason.NO_KEY_ENTRY, "PKCS#12 entry is not a private key")
        } catch (e: UnrecoverableKeyException) {
            throw P12Exception(Reason.WRONG_PASSWORD, "Incorrect PKCS#12 password", e)
        }

        // Keep the LEAF only. charon's android_creds adds every element of whatever chain it
        // is handed with add_cert(..., TRUE, ...) (android_creds.c:290-300) — i.e. as TRUSTED.
        // Returning the p12's CA bundle would silently widen server trust beyond the .sswan's
        // remote.cert_chain, which is the only intended anchor.
        val chain = store.getCertificateChain(alias)
        val leaf = chain?.firstOrNull() as? X509Certificate
            ?: throw P12Exception(Reason.NO_LEAF_CERTIFICATE, "PKCS#12 has no client certificate")

        return Identity(
            privateKey = neutralize(bcKey),
            leafDer = leaf.encoded,
            leaf = leaf,
        )
    }

    /**
     * Re-imports the key through the platform's default KeyFactory.
     *
     * charon calls Signature.getInstance(alg) with NO provider argument
     * (android_private_key.c:264-265), so the signature comes from Conscrypt while the key
     * would arrive from BouncyCastle. A cross-provider key is only usable via its encoding,
     * and Conscrypt's fallback for a foreign key is not guaranteed. Round-tripping the PKCS#8
     * encoding yields a provider-neutral key that initSign accepts — possible here precisely
     * because the key is in-memory; a KeyChain key returns null from getEncoded().
     */
    private fun neutralize(key: PrivateKey): PrivateKey {
        val encoded = key.encoded
            ?: throw P12Exception(Reason.UNUSABLE_KEY, "PKCS#12 private key exposes no encoding")
        // BC reports "ECDSA" for some EC keys; the JCA KeyFactory name is "EC".
        val algorithm = if (key.algorithm.equals("ECDSA", ignoreCase = true)) "EC" else key.algorithm
        return try {
            KeyFactory.getInstance(algorithm).generatePrivate(PKCS8EncodedKeySpec(encoded))
        } catch (e: GeneralSecurityException) {
            throw P12Exception(Reason.UNUSABLE_KEY, "Unsupported private key type: $algorithm", e)
        }
    }

    private fun decodeBase64(value: String): ByteArray {
        // iOS strips whitespace for the same reason (SswanProfile.swift:74). Strip explicitly
        // rather than using the MIME decoder, which also swallows genuinely corrupt input.
        val compact = value.filterNot { it.isWhitespace() }
        if (compact.isEmpty()) {
            throw P12Exception(Reason.INVALID_BASE64, "PKCS#12 payload is empty")
        }
        return try {
            Base64.getDecoder().decode(compact)
        } catch (e: IllegalArgumentException) {
            throw P12Exception(Reason.INVALID_BASE64, "PKCS#12 payload is not valid base64", e)
        }
    }

    private fun load(der: ByteArray, password: String): KeyStore {
        val store = KeyStore.getInstance("PKCS12", bouncyCastle())
        try {
            ByteArrayInputStream(der).use { store.load(it, password.toCharArray()) }
        } catch (e: IOException) {
            // BC signals a failed MAC check — the wrong-password symptom — as an IOException
            // indistinguishable by type from a truncated file. Only the cause/message separate
            // "you typed it wrong" from "this isn't a PKCS#12".
            throw if (isPasswordFailure(e)) {
                P12Exception(Reason.WRONG_PASSWORD, "Incorrect PKCS#12 password", e)
            } else {
                P12Exception(Reason.MALFORMED, "PKCS#12 payload could not be read", e)
            }
        }
        return store
    }

    private fun isPasswordFailure(e: IOException): Boolean {
        var t: Throwable? = e
        while (t != null) {
            if (t is UnrecoverableKeyException) return true
            val m = t.message?.lowercase().orEmpty()
            if (m.contains("wrong password") || m.contains("mac invalid") ||
                m.contains("password") && m.contains("invalid")
            ) {
                return true
            }
            t = t.cause
        }
        return false
    }

    /**
     * Gateway p12s carry exactly one key entry; the alias it lands under is an encoder
     * detail (go-pkcs12 leaves it unset), so it is discovered rather than assumed.
     */
    private fun firstKeyAlias(store: KeyStore): String =
        store.aliases().toList().firstOrNull { store.isKeyEntry(it) }
            ?: throw P12Exception(Reason.NO_KEY_ENTRY, "PKCS#12 contains no private key entry")
}
