package com.privycs.vpn.backup

import android.content.Context
import android.net.Uri
import android.util.Base64
import android.util.Log
import com.privycs.vpn.PrivycsApp
import com.privycs.vpn.data.models.AppSettings
import com.privycs.vpn.data.models.ConnectionRegistry
import com.privycs.vpn.data.models.NetworkRule
import com.privycs.vpn.data.models.VpnConnection
import kotlinx.coroutines.runBlocking
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.jsonObject
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
        // v1: plaintext = ConnectionRegistry only (no settings).
        // v2: plaintext = BackupPayload (connections + AppSettings, so
        //     gateway URL + API key + kill-switch + theme + on-demand
        //     rules + DNS override round-trip through a backup).
        // v3: plaintext = BackupPayload (connections + AppSettings +
        //     pools). Pool DEFINITIONS only (members, policy,
        //     rotation params, restrict-regions). Runtime state
        //     (active-member, pending-member, region-cursors,
        //     unreachable-flags) is intentionally NOT backed up - on
        //     a fresh device the user activates the pool which
        //     regenerates state cleanly.
        // v4: plaintext = BackupPayload (... + networkRules). The
        //     per-network auto-tunnel rule list (SSID/BSSID/network-
        //     type matching from v0.9.13.0) round-trips through the
        //     backup so a user restoring on a new device gets their
        //     auto-tunnel routing rules back. v3 backups still load
        //     cleanly on v4-aware clients with the field defaulting
        //     to empty list.
        // Import accepts all four for backward compatibility.
        private const val BACKUP_VERSION = 4
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
     * Full backup payload emitted since version 2. Wraps the connection
     * registry plus the app-global settings (gateway URL, API key,
     * kill-switch, theme, on-demand rules, DNS override). Earlier
     * backups stored only the registry, so imports must gracefully fall
     * back when `settings` is absent.
     */
    @Serializable
    data class BackupPayload(
        val connections: ConnectionRegistry = ConnectionRegistry(),
        val settings: AppSettings = AppSettings(),
        // v3+: pool definitions. Nullable so v2 backups (which don't
        // carry the field) deserialize cleanly with the default
        // value. PoolFile shape is the same on-disk format used by
        // PoolRepository so import is a single repository write.
        val pools: com.privycs.vpn.data.PoolFile? = null,
        // v4+: per-network auto-tunnel rules. Default empty list so
        // v3 backups deserialize without the field producing a null.
        val networkRules: List<NetworkRule> = emptyList()
    )

    /**
     * Export all VPN connections as an AES-256-GCM encrypted backup to the given URI.
     * The user selects the output location via SAF (Storage Access Framework).
     */
    fun exportBackup(password: String, outputUri: Uri) {
        require(password.isNotEmpty()) { "Password must not be empty" }

        // Read the current registry directly (in-memory state beats the
        // on-disk file in case the user just edited a connection and
        // connections.json has not been fsynced yet).
        val connRepo = PrivycsApp.instance.connectionRepository
        val registry = ConnectionRegistry(
            connections = connRepo.connections.toMutableList(),
            activeId = connRepo.activeId
        )

        // Settings live in DataStore (async); settingsFlow is cold so we
        // block here because this export operation is itself blocking.
        val settings = PrivycsApp.instance.settingsRepository.getSettingsBlocking()

        // Pool definitions snapshot from the in-memory registry.
        // Same rationale as for connections above: prefer the live
        // value over the on-disk pools.json which may not be
        // fsynced if the user just edited a pool.
        val poolFile = PrivycsApp.instance.poolRepository.registry.value

        // v4+ network rules snapshot. The repository exposes a
        // StateFlow whose .value is the current in-memory list; same
        // live-state-over-disk rationale as connections + pools.
        val networkRules = PrivycsApp.instance.networkRulesRepository.rules.value

        val payload = BackupPayload(
            connections = registry,
            settings = settings,
            pools = poolFile,
            networkRules = networkRules
        )
        val plaintext = json.encodeToString(BackupPayload.serializer(), payload)

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
    /**
     * Public shim kept for callers that only need the connections list
     * (e.g. UI previews). Internally delegates to importBackupPayload
     * so v1 and v2 formats are handled through the same code path.
     */
    fun importBackup(password: String, inputUri: Uri): List<VpnConnection> {
        return importBackupPayload(password, inputUri).connections.connections
    }

    /**
     * Decrypt + decode a backup into a normalised `DecodedBackup`.
     *
     * v2 plaintext: `{ connections: {...}, settings: {...} }` -
     *               deserialises as BackupPayload; hasSettings=true.
     * v1 plaintext: bare ConnectionRegistry `{ connections: [...],
     *               active_id: "..." }` -  hasSettings=false so the
     *               caller knows not to overwrite the user's current
     *               app settings with defaults.
     *
     * The top-level shape is probed via jsonObject first so version
     * metadata lies on the EncryptedBackup wrapper do not mislead us.
     */
    private data class DecodedBackup(
        val connections: ConnectionRegistry,
        val settings: AppSettings,
        val hasSettings: Boolean,
        val pools: com.privycs.vpn.data.PoolFile?,
        // v4+: rules carried by the backup. Empty list when the
        // backup is v1..v3 (field absent) — the import path leaves
        // the user's existing rules untouched in that case.
        val networkRules: List<NetworkRule>
    )

    private fun importBackupPayload(password: String, inputUri: Uri): DecodedBackup {
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

        // Peek at the top-level JSON to decide whether this is a v2
        // BackupPayload or a v1 bare ConnectionRegistry. Looking at the
        // EncryptedBackup.version field alone is not enough because a
        // user could import a v1 file that somehow had version=2 set;
        // actually sniffing the shape is robust.
        val hasSettings = try {
            val probe = json.parseToJsonElement(plaintext).jsonObject
            probe.containsKey("settings") && probe.containsKey("connections")
        } catch (_: Exception) {
            false
        }

        val decoded = if (hasSettings) {
            val p = json.decodeFromString(BackupPayload.serializer(), plaintext)
            DecodedBackup(
                connections = p.connections,
                settings = p.settings,
                hasSettings = true,
                pools = p.pools,
                networkRules = p.networkRules,
            )
        } else {
            val registry = json.decodeFromString(ConnectionRegistry.serializer(), plaintext)
            DecodedBackup(
                connections = registry,
                settings = AppSettings(),
                hasSettings = false,
                pools = null,
                networkRules = emptyList(),
            )
        }

        Log.d(
            TAG,
            "Backup imported: ${decoded.connections.connections.size} connections, " +
                "${decoded.pools?.pools?.size ?: 0} pools, " +
                "${decoded.networkRules.size} network rules, " +
                "settingsRestored=${decoded.hasSettings}"
        )
        return decoded
    }

    /**
     * Import backup and merge connections into the current repository.
     * Connections with duplicate IDs are skipped; new ones are added.
     * App-global settings (gateway URL, API key, kill-switch, theme,
     * on-demand rules, DNS override) are restored too when the backup is
     * v2 or newer - v1 backups contained only the connection registry.
     */
    fun importAndMerge(password: String, inputUri: Uri): Int {
        val decoded = importBackupPayload(password, inputUri)
        val connRepo = PrivycsApp.instance.connectionRepository
        var addedCount = 0

        for (conn in decoded.connections.connections) {
            if (connRepo.getById(conn.id) == null) {
                for (config in conn.protocols) {
                    connRepo.addOrUpdate(conn.id, conn.name, config)
                }
                addedCount++
            }
        }

        // Only apply settings from v2+ backups. `hasSettings` flags whether
        // the imported payload actually carried a settings block, so v1
        // legacy backups - which deserialise with default AppSettings -
        // do not silently overwrite the user's current gateway URL /
        // API key with empty strings.
        if (decoded.hasSettings) {
            runBlocking {
                PrivycsApp.instance.settingsRepository.updateSettings(decoded.settings)
            }
            Log.d(
                TAG,
                "Restored app settings from backup (gatewayUrl=${decoded.settings.gatewayUrl.isNotEmpty()}, apiKey=${decoded.settings.apiKey.isNotEmpty()})"
            )
        } else {
            Log.d(TAG, "Backup has no settings block (v1 legacy); settings left unchanged")
        }

        // Pool definitions (v3+ backups). Skip-on-existing-ID merge
        // semantics matching the connections branch: pools that
        // already exist on this device are left alone; pools whose
        // IDs are new get added with the original ID preserved so
        // the backup's activeId pointer still resolves.
        var addedPools = 0
        decoded.pools?.let { poolFile ->
            val poolRepo = PrivycsApp.instance.poolRepository
            runBlocking {
                for (p in poolFile.pools) {
                    if (poolRepo.restorePool(p)) addedPools++
                }
                // Restore activeId only if (a) it points to a pool
                // we restored OR an existing pool with that id, AND
                // (b) the user has no current single-connection
                // active selection - we do not silently switch the
                // user's current connection to a pool on import.
                val targetPoolId = poolFile.activeId
                val activeSinglePresent = PrivycsApp.instance
                    .connectionRepository.activeId.isNotEmpty()
                if (targetPoolId.isNotEmpty() &&
                    !activeSinglePresent &&
                    poolRepo.get(targetPoolId) != null
                ) {
                    poolRepo.setActiveId(targetPoolId)
                }
            }
            Log.d(TAG, "Merged $addedPools new pools from backup")
        }

        // Network rules (v4+ backups). REPLACE semantics rather than
        // merge: rules are a small priority-ordered list, and merging
        // two ordered lists by ID would destroy the user's intentional
        // priority ordering on the source machine. Symmetric with how
        // settings import works (replace, not merge). Skipped when the
        // backup is v1..v3 (decoded.networkRules is empty list) so we
        // do NOT silently wipe the user's existing rules just because
        // they imported an old backup.
        if (decoded.networkRules.isNotEmpty()) {
            runBlocking {
                PrivycsApp.instance.networkRulesRepository.save(decoded.networkRules)
            }
            Log.d(TAG, "Restored ${decoded.networkRules.size} network rules from backup")
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
