package com.privycs.vpn.util

import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Base64
import java.security.KeyStore
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

/**
 * At-rest encryption for the app's secrets — v0.9.15.74, audit item B.
 *
 * Protects the gateway API key and the connections store
 * (connections.json holds WireGuard private keys and OpenVPN
 * credentials) against a forensic image / rooted-device read of
 * app-private storage. app-private storage + `allowBackup=false`
 * already block every non-root attacker; this closes the root /
 * forensic gap.
 *
 * AES-256-GCM with a key held in the Android Keystore (hardware-backed
 * on devices with a TEE / StrongBox — the key material never leaves
 * secure hardware). The key is deliberately **non-auth-bound** (no
 * `setUserAuthenticationRequired`): it survives reboots and app
 * updates and is destroyed only on app uninstall / factory reset,
 * which already destroy the data anyway. Auth-bound keys can be
 * invalidated by a biometric / lock-screen change — that would brick
 * the user's whole connection store, an unacceptable failure mode for
 * a VPN client, so it is avoided here.
 *
 * Blob format: Base64(NO_WRAP) of  [12-byte GCM IV][ciphertext+tag].
 */
object SecretCrypto {
    private const val KEY_ALIAS = "privycs_at_rest_v1"
    private const val KEYSTORE = "AndroidKeyStore"
    private const val TRANSFORM = "AES/GCM/NoPadding"
    private const val IV_LENGTH = 12
    private const val TAG_BITS = 128

    private fun secretKey(): SecretKey {
        val ks = KeyStore.getInstance(KEYSTORE).apply { load(null) }
        (ks.getEntry(KEY_ALIAS, null) as? KeyStore.SecretKeyEntry)?.let { return it.secretKey }
        val generator = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, KEYSTORE)
        generator.init(
            KeyGenParameterSpec.Builder(
                KEY_ALIAS,
                KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT,
            )
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
                .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                .setKeySize(256)
                .build(),
        )
        return generator.generateKey()
    }

    /** Encrypt a UTF-8 string into a self-contained Base64 blob. */
    fun encrypt(plaintext: String): String {
        val cipher = Cipher.getInstance(TRANSFORM)
        cipher.init(Cipher.ENCRYPT_MODE, secretKey())
        val iv = cipher.iv
        val ciphertext = cipher.doFinal(plaintext.toByteArray(Charsets.UTF_8))
        return Base64.encodeToString(iv + ciphertext, Base64.NO_WRAP)
    }

    /**
     * Decrypt a Base64 blob produced by [encrypt]. Throws on a
     * malformed blob or when the Keystore key is unavailable — callers
     * MUST catch and degrade gracefully (the data is unrecoverable in
     * that rare case; e.g. start with an empty store rather than
     * crash).
     */
    fun decrypt(blob: String): String {
        val raw = Base64.decode(blob, Base64.NO_WRAP)
        require(raw.size > IV_LENGTH) { "ciphertext too short" }
        val iv = raw.copyOfRange(0, IV_LENGTH)
        val ciphertext = raw.copyOfRange(IV_LENGTH, raw.size)
        val cipher = Cipher.getInstance(TRANSFORM)
        cipher.init(Cipher.DECRYPT_MODE, secretKey(), GCMParameterSpec(TAG_BITS, iv))
        return String(cipher.doFinal(ciphertext), Charsets.UTF_8)
    }
}
