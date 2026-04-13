package com.privycs.vpn.backup

import android.content.Context
import android.net.Uri
import android.util.Base64
import android.util.Log
import com.privycs.vpn.PrivycsApp
import com.privycs.vpn.data.models.ConnectionRegistry
import com.privycs.vpn.data.models.VpnConnection
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json
import java.io.File
import java.security.SecureRandom
import javax.crypto.Cipher
import javax.crypto.SecretKeyFactory
import javax.crypto.spec.GCMParameterSpec
import javax.crypto.spec.PBEKeySpec
import javax.crypto.spec.SecretKeySpec

/**
 * Manages encrypted cloud backup of VPN connections.
 * Uses AES-256-GCM encryption with PBKDF2 key derivation.
 */
class CloudBackupManager(private val context: Context) {

    companion object {
        private const val TAG = "CloudBackupManager"
        private const val PBKDF2_ITERATIONS = 100_000
        private const val SALT_LENGTH = 16
        private const val IV_LENGTH = 12
        private const val KEY_LENGTH = 256
        private const val GCM_TAG_LENGTH = 128
        private const val BACKUP_VERSION = 1
    }

    private val json = Json {
        prettyPrint = true
        ignoreUnknownKeys = true
        encodeDefaults = true
    }

    @Serializable
    data class EncryptedBackup(
        val salt: String,
        val iv: String,
        val data: String,
        val version: Int
    )

    /**
     * Export all VPN connections as an AES-256-GCM encrypted backup to the given URI.
     * The user selects the output location via SAF (Storage Access Framework).
     */
    fun exportBackup(password: String, outputUri: Uri) {
        require(password.isNotEmpty()) { "Password must not be empty" }

        val connectionsFile = File(context.filesDir, "connections.json")
        val plaintext = if (connectionsFile.exists()) {
            connectionsFile.readText()
        } else {
            json.encodeToString(ConnectionRegistry.serializer(), ConnectionRegistry())
        }

        val salt = ByteArray(SALT_LENGTH).also { SecureRandom().nextBytes(it) }
        val iv = ByteArray(IV_LENGTH).also { SecureRandom().nextBytes(it) }

        val secretKey = deriveKey(password, salt)

        val cipher = Cipher.getInstance("AES/GCM/NoPadding")
        cipher.init(Cipher.ENCRYPT_MODE, secretKey, GCMParameterSpec(GCM_TAG_LENGTH, iv))
        val encryptedBytes = cipher.doFinal(plaintext.toByteArray(Charsets.UTF_8))

        val backup = EncryptedBackup(
            salt = Base64.encodeToString(salt, Base64.NO_WRAP),
            iv = Base64.encodeToString(iv, Base64.NO_WRAP),
            data = Base64.encodeToString(encryptedBytes, Base64.NO_WRAP),
            version = BACKUP_VERSION
        )

        val backupJson = json.encodeToString(EncryptedBackup.serializer(), backup)

        context.contentResolver.openOutputStream(outputUri)?.use { stream ->
            stream.write(backupJson.toByteArray(Charsets.UTF_8))
            stream.flush()
        } ?: throw IllegalStateException("Cannot open output stream for URI: $outputUri")

        Log.d(TAG, "Backup exported successfully")
    }

    /**
     * Import VPN connections from an encrypted backup file.
     * The user selects the input file via SAF and provides the decryption password.
     *
     * @return List of VpnConnection objects restored from the backup
     */
    fun importBackup(password: String, inputUri: Uri): List<VpnConnection> {
        require(password.isNotEmpty()) { "Password must not be empty" }

        val backupJson = context.contentResolver.openInputStream(inputUri)?.use { stream ->
            stream.bufferedReader(Charsets.UTF_8).readText()
        } ?: throw IllegalStateException("Cannot open input stream for URI: $inputUri")

        val backup = json.decodeFromString(EncryptedBackup.serializer(), backupJson)

        if (backup.version > BACKUP_VERSION) {
            throw IllegalArgumentException(
                "Backup version ${backup.version} is not supported. " +
                "Please update the app to restore this backup."
            )
        }

        val salt = Base64.decode(backup.salt, Base64.NO_WRAP)
        val iv = Base64.decode(backup.iv, Base64.NO_WRAP)
        val encryptedData = Base64.decode(backup.data, Base64.NO_WRAP)

        val secretKey = deriveKey(password, salt)

        val cipher = Cipher.getInstance("AES/GCM/NoPadding")
        cipher.init(Cipher.DECRYPT_MODE, secretKey, GCMParameterSpec(GCM_TAG_LENGTH, iv))

        val decryptedBytes = try {
            cipher.doFinal(encryptedData)
        } catch (e: Exception) {
            throw IllegalArgumentException("Decryption failed. Wrong password or corrupted backup.", e)
        }

        val plaintext = String(decryptedBytes, Charsets.UTF_8)
        val registry = json.decodeFromString(ConnectionRegistry.serializer(), plaintext)

        Log.d(TAG, "Backup imported successfully: ${registry.connections.size} connections")
        return registry.connections
    }

    /**
     * Import backup and merge connections into the current repository.
     * Connections with duplicate IDs are skipped; new ones are added.
     */
    fun importAndMerge(password: String, inputUri: Uri): Int {
        val importedConnections = importBackup(password, inputUri)
        val connRepo = PrivycsApp.instance.connectionRepository
        var addedCount = 0

        for (conn in importedConnections) {
            if (connRepo.getById(conn.id) == null) {
                for (config in conn.protocols) {
                    connRepo.addOrUpdate(conn.id, conn.name, config)
                }
                addedCount++
            }
        }

        Log.d(TAG, "Merged $addedCount new connections from backup")
        return addedCount
    }

    /**
     * Derive an AES-256 key from the password using PBKDF2WithHmacSHA256.
     */
    private fun deriveKey(password: String, salt: ByteArray): SecretKeySpec {
        val keySpec = PBEKeySpec(password.toCharArray(), salt, PBKDF2_ITERATIONS, KEY_LENGTH)
        val factory = SecretKeyFactory.getInstance("PBKDF2WithHmacSHA256")
        val keyBytes = factory.generateSecret(keySpec).encoded
        return SecretKeySpec(keyBytes, "AES")
    }
}
