package com.privycs.vpn.data

import android.content.Context
import com.privycs.vpn.BuildConfig
import com.privycs.vpn.license.License
import com.privycs.vpn.util.PrivycsLogger
import com.privycs.vpn.util.SecretCrypto
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import java.io.File

/**
 * Single source of truth for the "Privycs Pro" entitlement (Free vs Pro).
 *
 * Pro can be unlocked two ways:
 *  - PLAY   — a one-time Google Play purchase of `privycs_pro_lifetime`,
 *             fed by BillingManager. The only path wired today.
 *  - BUNDLE — a cross-platform licence key from the privycs.com bundle.
 *             The enum value and [activateLicenseKey] entry point exist
 *             so the bundle path can be switched on later without a
 *             storage-schema change; activation currently no-ops.
 *
 * The entitlement is cached on disk, encrypted at rest via SecretCrypto,
 * so Pro survives offline launches. This is NOT a security boundary —
 * the app is GPL / open source, so a recompiled build can bypass any
 * client-side check. Play Billing's queryPurchasesAsync is the
 * authoritative refresh on each launch.
 */
class EntitlementRepository(context: Context) {

    enum class Source { NONE, PLAY, BUNDLE }

    private val file = File(context.filesDir, "entitlement.dat")

    private val _isPro = MutableStateFlow(false)
    val isPro: StateFlow<Boolean> = _isPro.asStateFlow()

    @Volatile
    var source: Source = Source.NONE
        private set

    init { load() }

    private fun load() {
        try {
            if (!file.exists()) return
            val raw = SecretCrypto.decrypt(file.readText()).trim()
            source = runCatching { Source.valueOf(raw) }.getOrDefault(Source.NONE)
            _isPro.value = source != Source.NONE
        } catch (e: Exception) {
            // Unreadable cache → treat as Free; a Play refresh re-grants.
            source = Source.NONE
            _isPro.value = false
            PrivycsLogger.w(TAG, "entitlement cache unreadable: ${e.message}")
        }
    }

    private fun persist() {
        try {
            file.parentFile?.mkdirs()
            // Atomic write (temp + rename) so an interrupted write never
            // leaves a half-file — a corrupt cache would only force a
            // re-grant from Play, but tidiness costs nothing here.
            val tmp = File(file.parentFile, "${file.name}.tmp")
            tmp.writeText(SecretCrypto.encrypt(source.name))
            if (!tmp.renameTo(file)) {
                file.writeText(tmp.readText())
                tmp.delete()
            }
        } catch (e: Exception) {
            PrivycsLogger.w(TAG, "entitlement persist failed: ${e.message}")
        }
    }

    /** Called by BillingManager when an owned Play purchase is seen. */
    fun grantFromPlay() {
        if (source == Source.PLAY && _isPro.value) return
        source = Source.PLAY
        _isPro.value = true
        persist()
        PrivycsLogger.i(TAG, "Pro granted (Play)")
    }

    /**
     * Called by BillingManager when queryPurchasesAsync SUCCEEDS and
     * finds no owned Play purchase. Only clears a PLAY entitlement — a
     * BUNDLE licence is independent of Play and must not be revoked
     * here. A failed/offline query never reaches this path, so Pro
     * survives offline indefinitely.
     */
    fun revokeIfPlayOnly() {
        if (source == Source.PLAY) {
            source = Source.NONE
            _isPro.value = false
            persist()
            PrivycsLogger.i(TAG, "Pro revoked — Play purchase no longer owned")
        }
    }

    /**
     * Cross-platform bundle licence key (privycs.com). The same
     * .privycs-license file activates on Android, Desktop and (later)
     * iOS — verifier is byte-compatible across all three platforms.
     *
     * Verification is OFFLINE — the embedded public key
     * ([BuildConfig.LICENSE_PUBLIC_KEY_HEX]) is the trust anchor. A
     * passing key sets [Source.BUNDLE] and unlocks Pro; the cached
     * state survives offline launches like the Play path.
     *
     * @return a [LicenseActivationResult] the UI can render directly —
     *         distinguished error kinds so wrong-platform / bad-sig
     *         get tailored messages rather than a generic "failed".
     */
    fun activateLicenseKey(rawKey: String): LicenseActivationResult {
        val result = License.verify(
            rawKey,
            BuildConfig.LICENSE_PUBLIC_KEY_HEX,
            License.PLATFORM_ANDROID,
        )
        return when (result) {
            is License.Result.Ok -> {
                source = Source.BUNDLE
                _isPro.value = true
                persist()
                PrivycsLogger.i(TAG, "Pro granted (bundle): sku=${result.payload.sku}")
                LicenseActivationResult.Ok(result.payload.sku)
            }

            is License.Result.Err -> {
                PrivycsLogger.w(TAG, "licence activation failed: ${result.kind} ${result.detail}")
                LicenseActivationResult.Err(result.kind)
            }
        }
    }

    /**
     * Result of [activateLicenseKey] — a sealed pair so the UI can
     * pattern-match instead of inspecting exception text.
     */
    sealed class LicenseActivationResult {
        data class Ok(val sku: String) : LicenseActivationResult()
        data class Err(val kind: License.ErrorKind) : LicenseActivationResult()
    }

    /**
     * Sign out of a bundle licence. Symmetric to [revokeIfPlayOnly] for
     * the Play path. Only clears a BUNDLE entitlement.
     */
    fun deactivateBundleLicense() {
        if (source == Source.BUNDLE) {
            source = Source.NONE
            _isPro.value = false
            persist()
            PrivycsLogger.i(TAG, "Pro deactivated (bundle)")
        }
    }

    /**
     * True when Pro features are available — either the user has Pro,
     * or gating is globally disabled (GATING_ENABLED = false, the
     * test-track default). The single entitlement check the Pro gates
     * (see util/ProGate.kt) consult.
     */
    fun isUnlocked(): Boolean = !GATING_ENABLED || _isPro.value

    companion object {
        private const val TAG = "EntitlementRepo"

        /**
         * Master switch for the Pro feature gates. While false, every
         * feature stays free regardless of entitlement — used for the
         * test tracks so testers are not paywalled. Flip to true for
         * the production release.
         */
        const val GATING_ENABLED = false
    }
}
