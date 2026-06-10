package com.privycs.vpn.service

import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.content.ServiceConnection
import android.content.SharedPreferences
import android.os.Bundle
import android.os.IBinder
import android.security.KeyChain
import android.util.Base64
import android.util.Log
import com.privycs.vpn.data.models.VpnProtocol
import com.privycs.vpn.data.models.VpnStatus
import com.privycs.vpn.util.PrivycsLogger
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.TimeoutCancellationException
import kotlinx.coroutines.withContext
import kotlinx.coroutines.withTimeout
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json
import org.strongswan.android.data.VpnProfile
import org.strongswan.android.data.VpnProfileSource
import org.strongswan.android.data.VpnType
import org.strongswan.android.logic.TrustedCertificateManager
import org.strongswan.android.logic.VpnStateService
import org.strongswan.android.security.LocalCertificateStore
import java.io.ByteArrayInputStream
import java.security.cert.CertificateFactory
import java.security.cert.X509Certificate
import java.util.UUID

/**
 * IPSec/IKEv2 tunnel driven by strongSwan's bundled libcharon + CharonVpnService.
 *
 * Flow:
 *   1. parse .sswan JSON profile from the gateway
 *   2. map it to a strongSwan VpnProfile and persist via VpnProfileSource
 *   3. bind VpnStateService to drive CharonVpnService and receive state events
 *   4. disconnect closes the IKE SA and tears down the VpnService
 *
 * User-certificate handling: strongSwan expects the client cert + private key
 * to be reachable via Android KeyChain (alias-based). Our .sswan profiles
 * embed a PKCS#12 bundle (base64, with password), but KeyChain installs
 * require an Activity context. Callers must install the bundle via
 * `createKeyChainInstallIntent()` + `rememberInstalledAlias()` before the
 * first connect; the alias is persisted in SharedPreferences keyed by the
 * profile UUID so subsequent connects run without user interaction.
 */
class IpSecTunnel(private val context: Context) {

    companion object {
        private const val TAG = "IpSecTunnel"
        private const val PREF_FILE = "privycs_ipsec"
        private const val KEY_ALIAS_PREFIX = "keychain_alias_"
        // Bookkeeping: the local-store CA aliases imported for a profile (keyed
        // by .sswan UUID), so deleting the connection removes exactly them.
        private const val KEY_CHAIN_ALIASES_PREFIX = "chain_aliases_"

        private val json = Json { ignoreUnknownKeys = true; isLenient = true }
    }

    enum class State { DISCONNECTED, CONNECTING, CONNECTED, DISCONNECTING }

    // Data model matching cmd/gateway/ipsec_mobile_profiles.go:generateAndroidSSWANProfile.
    @Serializable
    data class SswanConfig(
        val uuid: String = "",
        val name: String = "",
        val type: String = "ikev2-cert",
        val remote: SswanRemote = SswanRemote(),
        val local: SswanLocal = SswanLocal(),
        val mtu: Int = 1400,
        @SerialName("split-tunneling")
        val splitTunneling: List<String> = emptyList(),
        @SerialName("dns-servers")
        val dnsServers: List<String> = emptyList()
    )

    @Serializable
    data class SswanRemote(
        val addr: String = "",
        val id: String = "",
        // Single server trust anchor (base64-DER X.509). Legacy field from the
        // first public-cert attempt; superseded by cert_chain below but still
        // imported (without pinning) for forward/backward compat.
        val cert: String = "",
        // Full PUBLIC server-cert chain as a PEM bundle: intermediate(s) + root
        // (e.g. Let's Encrypt YR2 → Root YR → ISRG Root X1). The server's
        // strongSwan sends only the leaf in IKE_AUTH and Android's KeyChain
        // strips the foreign CAs out of the client P12, so the intermediates
        // are imported from here into strongSwan's own trust store — otherwise
        // charon logs "no issuer certificate found". Empty on self-signed /
        // legacy profiles → nothing imported (ignoreUnknownKeys keeps them ok).
        @SerialName("cert_chain")
        val certChain: String = ""
    )

    @Serializable
    data class SswanLocal(
        val p12: String = "",
        val id: String = "",
        @SerialName("p12-password")
        val p12Password: String = "",
        // RFC 8784 PPK material from a pq_safe interface. Both fields are
        // empty on legacy / non-pq-safe profiles; in that case
        // buildVpnProfile() simply skips the PPK setters and the tunnel
        // negotiates over plain certificate auth. ppk_psk is the
        // 64-character hex encoding of the 256-bit secret.
        @SerialName("ppk_id")
        val ppkId: String = "",
        @SerialName("ppk_psk")
        val ppkPsk: String = ""
    )

    private val prefs: SharedPreferences =
        context.getSharedPreferences(PREF_FILE, Context.MODE_PRIVATE)

    // @Volatile on state + connectedSince + rx/tx baselines because
    // handleStateChanged runs on strongSwan's VpnStateListener thread
    // (Main) while getStatus runs on the polling coroutine
    // (Dispatchers.Default). Without the memory barrier the polling
    // thread can observe state=CONNECTED but a stale rxBaseline=0,
    // and the very first traffic sample after connect leaks the
    // per-UID lifetime counter (easily >1 GB if the app has been
    // around a while). Same pattern as OpenVpnTunnel's @Volatile
    // declarations.
    @Volatile
    private var state: State = State.DISCONNECTED
    // @Volatile: written in connect()/parseConfig() on Dispatchers.IO,
    // read in getStatus() on the polling coroutine. Same cross-thread
    // memory-visibility reasoning as state/connectedSince above.
    @Volatile
    private var sswanConfig: SswanConfig? = null
    @Volatile
    private var profileUuid: UUID? = null
    @Volatile
    private var connectedSince: Long = 0L

    // Snapshot of TrafficStats per-UID totals at CONNECT time, used as the
    // baseline for rx/tx deltas. The app-UID total is close to "VPN traffic"
    // because CharonVpnService runs in our process and tunneled traffic all
    // flows through our UID's socket path. It slightly over-counts by any
    // non-VPN HTTP the app makes (fetchProfile etc.) but those are rare
    // compared to tunnel data, so the number the user sees is dominated by
    // the tunnel.
    @Volatile
    private var rxBaseline: Long = 0L
    @Volatile
    private var txBaseline: Long = 0L

    // v0.9.15.75: separate baseline for the readVpnInterfaceBytes()
    // fallback used when per-UID TrafficStats is UNSUPPORTED. The
    // /sys + /proc counters are interface-lifetime, not session-
    // scoped, so they need their own baseline subtracted for the UI
    // counter to start at 0/0. -1 = not captured yet (0 is a valid
    // fresh-interface reading and must not be taken for "unset").
    @Volatile
    private var ifaceRxBaseline: Long = -1L
    @Volatile
    private var ifaceTxBaseline: Long = -1L

    // Long-lived bind to strongSwan's VpnStateService. Bound on connect(),
    // unbound on disconnect() so the VpnStateListener callbacks can drive
    // our state updates for the whole tunnel lifetime. Nullable because
    // the connection is transient.
    private var stateServiceConn: ServiceConnection? = null
    private var stateService: VpnStateService? = null
    private var stateListener: VpnStateService.VpnStateListener? = null

    // Called whenever strongSwan's state changes. Wired by the caller
    // (VpnServiceManager) so the UI can react to live transitions without
    // polling.
    var onStateChanged: ((State) -> Unit)? = null

    /**
     * Parse the gateway-emitted .sswan JSON and return the decoded model.
     * Also cached locally so later getStatus() calls can surface the server
     * endpoint without re-parsing.
     */
    fun parseConfig(configContent: String): SswanConfig {
        val cfg = json.decodeFromString<SswanConfig>(configContent)
        sswanConfig = cfg
        Log.d(TAG, "Parsed .sswan: uuid=${cfg.uuid} remote=${cfg.remote.addr} name=${cfg.name}")
        return cfg
    }

    /**
     * Extract the raw PKCS#12 bytes. Used by the caller to pass into
     * KeyChain.createInstallIntent(EXTRA_PKCS12, ...).
     */
    fun extractP12Bytes(): ByteArray? {
        val p12 = sswanConfig?.local?.p12 ?: return null
        if (p12.isEmpty()) return null
        return runCatching { Base64.decode(p12, Base64.DEFAULT) }
            .onFailure { Log.e(TAG, "PKCS#12 decode failed", it) }
            .getOrNull()
    }

    /**
     * Plaintext PKCS#12 password from the .sswan profile. The UI layer
     * uses this to prepopulate the clipboard because Android's KeyChain
     * install dialog has no extras to prefill the password field.
     */
    fun getP12Password(): String = sswanConfig?.local?.p12Password ?: ""

    /**
     * Build an Intent the caller launches from an Activity to prompt the
     * user to install the PKCS#12 bundle. The default alias seeded here is
     * `privycs-<connection-name>` so users can identify it in Android's
     * credential storage UI.
     */
    fun createKeyChainInstallIntent(connectionName: String): Intent? {
        val p12Bytes = extractP12Bytes() ?: return null
        val cfg = sswanConfig ?: return null
        return KeyChain.createInstallIntent().apply {
            putExtra(KeyChain.EXTRA_PKCS12, p12Bytes)
            putExtra(KeyChain.EXTRA_NAME, "privycs-${connectionName}")
            putExtra("PKCS12_PASSWORD", cfg.local.p12Password) // not standard but tolerated
        }
    }

    /**
     * Remember the alias the user chose during KeyChain install. Keyed by
     * .sswan profile UUID so different gateways/profiles can coexist.
     */
    fun rememberInstalledAlias(alias: String) {
        val cfgUuid = sswanConfig?.uuid ?: return
        prefs.edit().putString(KEY_ALIAS_PREFIX + cfgUuid, alias).apply()
    }

    /**
     * Look up the previously-stored alias for this profile. Returns null if
     * no install has been recorded yet.
     */
    fun getInstalledAlias(): String? {
        val cfgUuid = sswanConfig?.uuid ?: return null
        return prefs.getString(KEY_ALIAS_PREFIX + cfgUuid, null)
    }

    /**
     * True if `alias` still resolves to a usable certificate chain in the system
     * KeyChain. False if the cert was deleted (Clear credentials / user removal)
     * or access was revoked — letting connect() discard the "ghost" alias and
     * re-drive the install flow. MUST run off the main thread
     * (KeyChain.getCertificateChain blocks); connect() calls it on Dispatchers.IO.
     */
    private fun aliasStillInstalled(alias: String): Boolean =
        runCatching { KeyChain.getCertificateChain(context, alias)?.isNotEmpty() == true }
            .getOrDefault(false)

    /**
     * Drop the remembered alias for this profile (keyed by .sswan UUID) so the
     * next connect re-prompts the KeyChain install.
     */
    private fun forgetInstalledAlias() {
        val cfgUuid = sswanConfig?.uuid ?: return
        prefs.edit().remove(KEY_ALIAS_PREFIX + cfgUuid).apply()
    }

    /**
     * Connect the tunnel. Requires an alias already installed in KeyChain
     * (see rememberInstalledAlias). Throws IllegalStateException otherwise -
     * the UI layer catches this and drives the install flow via
     * createKeyChainInstallIntent().
     *
     * `vpnService` is kept in the signature for source-compat with the WG /
     * OpenVPN tunnel interfaces, but is unused: CharonVpnService manages its
     * own VpnService instance.
     */
    suspend fun connect(
        configContent: String,
        name: String = "privycs-ipsec",
        @Suppress("UNUSED_PARAMETER") vpnService: android.net.VpnService,
        dnsOverrideServers: List<String> = emptyList()
    ) = withContext(Dispatchers.IO) {
        if (state == State.CONNECTED || state == State.CONNECTING) {
            Log.w(TAG, "Already connected/connecting; ignoring")
            return@withContext
        }
        state = State.CONNECTING

        val parsedCfg = parseConfig(configContent)
        // DNS override from Settings: replace the .sswan profile's
        // dns-servers list. strongSwan's profile.setDnsServers takes
        // a space-separated string and propagates the value through
        // CharonVpnService into the VpnService.Builder.addDnsServer
        // call. Earlier versions ignored the user's Settings
        // dnsOverride entirely - the .sswan file's "dns-servers"
        // always won. v0.9.11.53 closes that gap.
        val cfg = if (dnsOverrideServers.isNotEmpty()) {
            Log.i(TAG, "DNS override (IPSec): applied ${dnsOverrideServers.joinToString(",")}")
            parsedCfg.copy(dnsServers = dnsOverrideServers)
        } else {
            parsedCfg
        }
        // Resolve the client-cert alias. A remembered alias whose cert was since
        // deleted from the system KeyChain (Settings ▸ Encryption & credentials ▸
        // Clear credentials, or the user removing it) would otherwise survive as
        // a ghost: getInstalledAlias() "succeeds" but charon can't fetch the
        // cert/key and the install flow never re-fires. Verify the chain still
        // resolves; if not, forget the stale alias and fail with the install
        // prompt so the UI re-drives the KeyChain install.
        val storedAlias = getInstalledAlias()
        val alias = storedAlias?.takeIf { aliasStillInstalled(it) }
            ?: run {
                if (storedAlias != null) {
                    PrivycsLogger.w(TAG, "Remembered KeyChain alias no longer resolves — forgetting it, re-prompt install")
                    forgetInstalledAlias()
                }
                throw IllegalStateException(
                    "PKCS#12 not yet installed into Android KeyChain. " +
                            "Launch createKeyChainInstallIntent() from an Activity, " +
                            "then call rememberInstalledAlias() before connecting."
                )
            }

        val profile = buildVpnProfile(cfg, name, alias)
        val uuid = persistProfile(profile)
        profileUuid = uuid

        startViaVpnStateService(uuid)

        // CharonVpnService runs asynchronously; our state flips to CONNECTED
        // once VpnStateService dispatches State.CONNECTED. For now we mark
        // CONNECTING and let status polling (getStatus) reflect the live state.
        connectedSince = System.currentTimeMillis()
        Log.d(TAG, "Dispatched CharonVpnService start for profile $uuid")
    }

    /**
     * Map our SswanConfig to strongSwan's VpnProfile.
     */
    private fun buildVpnProfile(
        cfg: SswanConfig,
        connectionName: String,
        keychainAlias: String
    ): VpnProfile {
        // All setters called explicitly because strongSwan's VpnProfile uses
        // all-caps getter/setter prefixes (setUUID, setMTU) that Kotlin's
        // property syntax maps inconsistently across versions.
        val profile = VpnProfile()
        val pUuid = runCatching { UUID.fromString(cfg.uuid) }.getOrNull() ?: UUID.randomUUID()
        profile.setUUID(pUuid)
        profile.setName(connectionName.ifEmpty { cfg.name.ifEmpty { "privycs-ipsec" } })
        profile.setGateway(cfg.remote.addr)
        profile.setVpnType(VpnType.IKEV2_CERT)
        profile.setUserCertificateAlias(keychainAlias)
        if (cfg.local.id.isNotEmpty()) profile.setLocalId(cfg.local.id)
        if (cfg.remote.id.isNotEmpty()) profile.setRemoteId(cfg.remote.id)
        // Server-cert trust: import the gateway-supplied public chain
        // (intermediates + root) into strongSwan's own trust store. We do NOT
        // pin via setCertificateAlias — pinning makes charon's
        // getTrustedCertificates() return ONLY that one anchor, so the
        // intermediate is missing and the leaf→intermediate→root chain can't be
        // built ("no issuer certificate found"). With the chain imported and CA
        // selection left automatic, charon validates against the system store +
        // the local store and assembles the chain itself. Self-signed / legacy
        // profiles carry no chain → nothing imported, validation falls back to
        // the P12-embedded CA exactly as before. remote.id/addr are never
        // overwritten.
        importServerTrustChain(cfg.remote)
        if (cfg.mtu > 0) profile.setMTU(cfg.mtu)
        if (cfg.dnsServers.isNotEmpty()) profile.setDnsServers(cfg.dnsServers.joinToString(" "))
        // RFC 8784 PPK — emitted by gateway when interface.pq_safe = true.
        // Setters land on the vendored strongSwan VpnProfile (see
        // android/vendor/strongswan patch); CharonVpnService persists them
        // and propagates via SettingsWriter -> JNI -> libcharon.
        if (cfg.local.ppkId.isNotEmpty() && cfg.local.ppkPsk.isNotEmpty()) {
            profile.setPPKId(cfg.local.ppkId)
            profile.setPPKPsk(cfg.local.ppkPsk)
        }
        // Gateway emits split-tunneling as a CIDR list of bypass subnets. strongSwan
        // uses the "excluded subnets" list for the same purpose (traffic that should
        // bypass the tunnel). Hand them over verbatim.
        if (cfg.splitTunneling.isNotEmpty()) {
            profile.setExcludedSubnets(cfg.splitTunneling.joinToString(" "))
        }
        applyPerAppVpnSettings(profile)
        return profile
    }

    /**
     * Import the gateway-supplied server-cert trust material into strongSwan's
     * local trusted-certificate store so charon can build the leaf → intermediate
     * → root chain itself. Handles both the PEM bundle in `cert_chain`
     * (intermediate(s) + root, possibly several concatenated certs) and the
     * legacy single base64-DER `cert`. Does NOT pin a profile alias — leaving CA
     * selection automatic is what lets charon use these imported intermediates
     * together with the system store. Idempotent: addCertificate() replaces a
     * same-key file, and TrustedCertificateManager.reset() reloads the cache for
     * charon's next getTrustedCertificates() call. No chain material → no-op, so
     * self-signed profiles are unaffected.
     */
    private fun importServerTrustChain(remote: SswanRemote) {
        val factory = CertificateFactory.getInstance("X.509")
        val certs = mutableListOf<X509Certificate>()
        if (remote.certChain.isNotEmpty()) {
            runCatching {
                factory.generateCertificates(
                    ByteArrayInputStream(remote.certChain.toByteArray(Charsets.US_ASCII))
                ).filterIsInstance<X509Certificate>()
            }.onSuccess { certs += it }
                .onFailure { Log.e(TAG, "cert_chain parse failed", it) }
        }
        if (remote.cert.isNotEmpty()) {
            runCatching {
                factory.generateCertificate(
                    ByteArrayInputStream(Base64.decode(remote.cert, Base64.DEFAULT))
                ) as X509Certificate
            }.onSuccess { certs += it }
                .onFailure { Log.e(TAG, "cert (single anchor) parse failed", it) }
        }
        if (certs.isEmpty()) return
        val store = LocalCertificateStore()
        val aliases = LinkedHashSet<String>()
        var imported = 0
        for (c in certs) {
            if (store.addCertificate(c)) imported++
            store.getCertificateAlias(c)?.let { aliases.add(it) }
        }
        TrustedCertificateManager.getInstance().reset()
        // Record the imported aliases against the profile UUID so cleanupOnDelete
        // can remove exactly these CA certs when the connection is deleted.
        sswanConfig?.uuid?.takeIf { it.isNotEmpty() }?.let { uuid ->
            prefs.edit().putStringSet(KEY_CHAIN_ALIASES_PREFIX + uuid, aliases).apply()
        }
        Log.i(TAG, "Imported $imported server-trust cert(s) into local store (chain=${remote.certChain.isNotEmpty()})")
    }

    /**
     * Remove the OS-level artifacts an IPSec profile leaves behind, called when
     * the connection (or its IPSec config) is deleted. Cleans: (1) the strongSwan
     * SQLite VpnProfile, (2) the server-trust CA certs imported into the local
     * store, (3) the remembered-alias + chain-alias bookkeeping in SharedPrefs.
     * NOTE: the user-installed client PKCS#12 in the system KeyChain itself
     * cannot be removed by an app (no Android API) — only its alias reference here.
     */
    suspend fun cleanupOnDelete(configContent: String) = withContext(Dispatchers.IO) {
        val cfg = runCatching { parseConfig(configContent) }.getOrNull() ?: return@withContext
        val uuid = cfg.uuid
        if (uuid.isEmpty()) return@withContext
        // 1. strongSwan SQLite VpnProfile
        runCatching {
            val source = VpnProfileSource(context)
            source.open()
            try {
                source.getVpnProfile(UUID.fromString(uuid))?.let { source.deleteVpnProfile(it) }
            } finally {
                source.close()
            }
        }.onFailure { Log.w(TAG, "cleanupOnDelete: VpnProfile delete failed: ${it.message}") }
        // 2. imported server-trust CA certs
        runCatching {
            val store = LocalCertificateStore()
            prefs.getStringSet(KEY_CHAIN_ALIASES_PREFIX + uuid, emptySet())
                ?.forEach { store.deleteCertificate(it) }
            TrustedCertificateManager.getInstance().reset()
        }.onFailure { Log.w(TAG, "cleanupOnDelete: CA cert delete failed: ${it.message}") }
        // 3. SharedPrefs bookkeeping (alias reference + chain alias set)
        prefs.edit()
            .remove(KEY_ALIAS_PREFIX + uuid)
            .remove(KEY_CHAIN_ALIASES_PREFIX + uuid)
            .apply()
        Log.i(TAG, "cleanupOnDelete: removed IPSec OS artifacts for profile $uuid")
    }

    /**
     * Propagate the app-level Per-App-VPN configuration into
     * strongSwan's VpnProfile. strongSwan's CharonVpnService already
     * honours these two fields (selectedApps + selectedAppsHandling)
     * by calling VpnService.Builder.addAllowedApplication /
     * addDisallowedApplication when it builds the tunnel.
     *
     * Source of truth is the shared "split_tunnel" SharedPreferences
     * bucket written by PerAppVpnScreen - same keys/values as the
     * WireGuard and OpenVPN paths consume, so one UI drives all
     * three protocols.
     */
    private fun applyPerAppVpnSettings(profile: VpnProfile) {
        try {
            val prefs = context.getSharedPreferences("split_tunnel", Context.MODE_PRIVATE)
            val mode = prefs.getString("mode", "disabled") ?: "disabled"
            val packages = prefs.getStringSet("packages", emptySet()) ?: emptySet()

            if (mode == "disabled" || packages.isEmpty()) {
                profile.setSelectedAppsHandling(VpnProfile.SelectedAppsHandling.SELECTED_APPS_DISABLE)
                return
            }

            // Include mode MUST include our own package so strongSwan's
            // charon daemon (running in-process) can reach the VPN
            // gateway to negotiate the SA. Without this, SELECTED_APPS_ONLY
            // with "only Elba" filtered out the IKE/ESP handshake
            // itself and the tunnel never established.
            val finalPackages = if (mode == "include") packages + context.packageName else packages
            profile.setSelectedApps(java.util.TreeSet(finalPackages))
            profile.setSelectedAppsHandling(
                when (mode) {
                    "include" -> VpnProfile.SelectedAppsHandling.SELECTED_APPS_ONLY
                    "exclude" -> VpnProfile.SelectedAppsHandling.SELECTED_APPS_EXCLUDE
                    else -> VpnProfile.SelectedAppsHandling.SELECTED_APPS_DISABLE
                },
            )
        } catch (e: Exception) {
            PrivycsLogger.w(TAG, "Failed to apply Per-App VPN settings to IPSec profile: ${e.message}")
        }
    }

    /**
     * Insert or update the profile in strongSwan's SQLite store and return
     * the canonical UUID.
     */
    private fun persistProfile(profile: VpnProfile): UUID {
        val source = VpnProfileSource(context)
        source.open()
        try {
            val newUuid: UUID? = profile.getUUID()
            val existing = newUuid?.let { source.getVpnProfile(it) }
            return if (existing != null) {
                existing.setName(profile.getName())
                existing.setGateway(profile.getGateway())
                existing.setVpnType(profile.getVpnType())
                existing.setUserCertificateAlias(profile.getUserCertificateAlias())
                // Server-CA alias: buildVpnProfile no longer pins one (we import
                // the whole chain + leave CA selection automatic instead), so the
                // freshly-built profile's alias is null. Copy it through to CLEAR
                // any stale pin a previous build wrote on this stored profile —
                // the .sswan UUID is deterministic so a re-download lands here.
                existing.setCertificateAlias(profile.getCertificateAlias())
                existing.setLocalId(profile.getLocalId())
                existing.setRemoteId(profile.getRemoteId())
                existing.setMTU(profile.getMTU())
                existing.setDnsServers(profile.getDnsServers())
                existing.setExcludedSubnets(profile.getExcludedSubnets())
                existing.setSelectedApps(profile.getSelectedApps())
                existing.setSelectedAppsHandling(profile.getSelectedAppsHandling())
                // PPK rotates whenever the gateway issues a new pq_safe
                // profile, so always copy from the freshly-built profile —
                // including clearing it when we transition from pq_safe back
                // to plain certificate auth.
                existing.setPPKId(profile.getPPKId())
                existing.setPPKPsk(profile.getPPKPsk())
                source.updateVpnProfile(existing)
                existing.getUUID()
            } else {
                source.insertProfile(profile).getUUID()
            }
        } finally {
            source.close()
        }
    }

    /**
     * Bind VpnStateService, register a listener, and call connect() with the
     * profile UUID. The bind is kept alive for the whole tunnel lifetime so
     * VpnStateListener callbacks continue to fire; disconnect() tears it down.
     */
    private suspend fun startViaVpnStateService(uuid: UUID) = withContext(Dispatchers.Main) {
        val bound = CompletableDeferred<VpnStateService>()
        val conn = object : ServiceConnection {
            override fun onServiceConnected(name: ComponentName?, binder: IBinder?) {
                val service = (binder as VpnStateService.LocalBinder).service
                stateService = service
                // Install the listener BEFORE dispatching connect() so we
                // never miss the first transition out of DISABLED.
                val listener = VpnStateService.VpnStateListener {
                    handleStateChanged(service.state)
                }
                stateListener = listener
                service.registerListener(listener)
                bound.complete(service)
            }
            override fun onServiceDisconnected(name: ComponentName?) {
                stateService = null
            }
        }
        stateServiceConn = conn

        val svcIntent = Intent(context, VpnStateService::class.java)
        context.bindService(svcIntent, conn, Context.BIND_AUTO_CREATE)
        val service = bound.await()
        val bundle = Bundle().apply {
            putString(/* KEY_UUID from VpnProfileDataSource = */ "_uuid", uuid.toString())
        }
        service.connect(bundle, true)
        // Do not flip state to CONNECTED here - the first listener callback
        // is responsible for that. We do mark CONNECTING so the UI can show
        // the right spinner.
        state = State.CONNECTING
        onStateChanged?.invoke(state)
    }

    /**
     * Translate strongSwan's VpnStateService.State into our local State enum
     * and propagate via onStateChanged. strongSwan uses DISABLED for
     * "not connected"; we normalize to DISCONNECTED.
     */
    private fun handleStateChanged(charonState: VpnStateService.State) {
        val newState = when (charonState) {
            VpnStateService.State.DISABLED -> State.DISCONNECTED
            VpnStateService.State.CONNECTING -> State.CONNECTING
            VpnStateService.State.CONNECTED -> {
                if (connectedSince == 0L) {
                    connectedSince = System.currentTimeMillis()
                    // Snapshot the traffic baseline the moment charon reports
                    // the tunnel up. Delta from here is what the UI shows
                    // for the "current session".
                    rxBaseline = android.net.TrafficStats.getUidRxBytes(android.os.Process.myUid())
                    txBaseline = android.net.TrafficStats.getUidTxBytes(android.os.Process.myUid())
                    // Re-arm the interface-counter fallback baseline so
                    // a new session re-captures it on its first poll.
                    ifaceRxBaseline = -1L
                    ifaceTxBaseline = -1L
                }
                State.CONNECTED
            }
            VpnStateService.State.DISCONNECTING -> State.DISCONNECTING
        }
        if (newState == state) return
        state = newState
        if (newState == State.DISCONNECTED) connectedSince = 0L
        PrivycsLogger.i(TAG, "IPSec state: $charonState -> $newState")
        onStateChanged?.invoke(newState)
    }

    /**
     * Unregister the listener and unbind. Idempotent.
     */
    private fun cleanupStateService() {
        val conn = stateServiceConn ?: return
        try {
            stateListener?.let { stateService?.unregisterListener(it) }
        } catch (_: Exception) { /* already gone, tolerate */ }
        try {
            context.unbindService(conn)
        } catch (_: IllegalArgumentException) { /* not bound, tolerate */ }
        stateServiceConn = null
        stateService = null
        stateListener = null
    }

    /**
     * Tell CharonVpnService to tear down. We use the already-bound
     * VpnStateService from connect() if available; otherwise we bind
     * briefly just to dispatch disconnect. After disconnect is dispatched
     * the listener fires us back to DISCONNECTED and we clean up the bind.
     */
    suspend fun disconnect() = withContext(Dispatchers.Main) {
        if (state == State.DISCONNECTED) return@withContext
        state = State.DISCONNECTING
        onStateChanged?.invoke(state)

        val svc = stateService
        if (svc != null) {
            svc.disconnect()
        } else {
            // Fallback: no prior bind. This can happen if the process was
            // recycled after connect but before disconnect. Briefly bind,
            // dispatch, and unbind.
            val bound = CompletableDeferred<VpnStateService>()
            val fallbackConn = object : ServiceConnection {
                override fun onServiceConnected(name: ComponentName?, binder: IBinder?) {
                    bound.complete((binder as VpnStateService.LocalBinder).service)
                }
                override fun onServiceDisconnected(name: ComponentName?) {}
            }
            val svcIntent = Intent(context, VpnStateService::class.java)
            context.bindService(svcIntent, fallbackConn, Context.BIND_AUTO_CREATE)
            try {
                // Bound await with a timeout: if bindService never delivers
                // onServiceConnected (service refuses to start, process under
                // memory pressure), bound.await() would otherwise hang this
                // coroutine forever — blocking teardown/disconnect for good.
                // 5s is generous for a local same-process service bind.
                withTimeout(5_000) {
                    bound.await().disconnect()
                }
            } catch (e: TimeoutCancellationException) {
                PrivycsLogger.w(
                    TAG,
                    "disconnect fallback bind timed out after 5s; cleaning up"
                )
            } finally {
                try {
                    context.unbindService(fallbackConn)
                } catch (_: IllegalArgumentException) { /* never bound, tolerate */ }
            }
        }
        // The listener will flip state to DISCONNECTED once charon reports
        // it. Tear down our long-lived bind here regardless - we do not
        // need further events after an explicit disconnect.
        cleanupStateService()
        state = State.DISCONNECTED
        connectedSince = 0L
        onStateChanged?.invoke(state)
    }

    fun getState(): State = state

    /**
     * Snapshot for UI status display. Byte counters use TrafficStats with a
     * baseline captured at CONNECTED time (see handleStateChanged) rather
     * than per-interface /proc|/sys reads, because on Android 10+ those
     * paths are UID-scoped and return zeros for the VPN tun even though
     * our UID owns CharonVpnService.
     */
    fun getStatus(connectionName: String, connectionId: String): VpnStatus {
        val up = state == State.CONNECTED
        val localAddrStr = if (up) readTunInterfaceLocalAddress() else ""
        val (rx, tx) = if (up) {
            val uid = android.os.Process.myUid()
            val curRx = android.net.TrafficStats.getUidRxBytes(uid)
            val curTx = android.net.TrafficStats.getUidTxBytes(uid)
            if (curRx < 0L || curTx < 0L) {
                // v0.9.15.75: per-UID TrafficStats returns UNSUPPORTED
                // (-1) on many Android 10+ ROMs — the baseline would
                // then be -1 and every delta clamps to 0, freezing the
                // counter at 0/0. Fall back to reading the VPN tun
                // interface's own statistics directly (not UID-scoped).
                // Those counters are interface-lifetime, so baseline
                // them on the first poll of the session — otherwise
                // the counter jumps to a large absolute number at
                // connect instead of starting at 0/0.
                val (ifRx, ifTx) = readVpnInterfaceBytes()
                if (ifaceRxBaseline < 0L) ifaceRxBaseline = ifRx
                if (ifaceTxBaseline < 0L) ifaceTxBaseline = ifTx
                (ifRx - ifaceRxBaseline).coerceAtLeast(0L) to
                    (ifTx - ifaceTxBaseline).coerceAtLeast(0L)
            } else {
            // Self-heal lazy baseline. The primary baseline write is
            // in handleStateChanged on the CONNECTED transition, BEFORE
            // state is published. With @Volatile the visibility window
            // is closed in normal operation, but there's still a tiny
            // window where state=CONNECTED but the polling thread may
            // not yet observe the baseline write (compiler/CPU
            // reorder, or — more importantly — when this poll happens
            // BEFORE handleStateChanged ran at all because charon went
            // CONNECTED faster than the listener registered).
            // Latching here on the first observation prevents the
            // first sample from showing the per-UID lifetime counter.
            if (rxBaseline == 0L) rxBaseline = curRx
            if (txBaseline == 0L) txBaseline = curTx
            val r = (curRx - rxBaseline).coerceAtLeast(0L)
            val t = (curTx - txBaseline).coerceAtLeast(0L)
            r to t
            }
        } else 0L to 0L
        return VpnStatus(
            connected = up,
            connectionName = connectionName,
            connectionId = connectionId,
            activeProtocol = VpnProtocol.IPSEC,
            uptime = if (up && connectedSince > 0) System.currentTimeMillis() - connectedSince else 0L,
            rxBytes = rx,
            txBytes = tx,
            serverEndpoint = sswanConfig?.remote?.addr ?: "",
            localAddress = localAddrStr
        )
    }

    /**
     * Sum of unicast CIDR addresses on every UP `tun*` interface in
     * the system, comma-separated. strongSwan's CharonVpnService owns
     * the VpnService.Builder.establish() call which creates the tun;
     * the inner-IP charon got assigned via IKE_AUTH-CFG_REPLY ends up
     * here. Same pattern + caveats as OpenVpnTunnel's equivalent —
     * skip link-local + loopback, return "" if no iface yet.
     */
    private fun readTunInterfaceLocalAddress(): String {
        val parts = mutableListOf<String>()
        try {
            val ifaces = java.net.NetworkInterface.getNetworkInterfaces() ?: return ""
            for (iface in ifaces) {
                if (!iface.isUp) continue
                val n = iface.name.lowercase()
                if (!(n.startsWith("tun") || n.startsWith("vpn"))) continue
                for (ifAddr in iface.interfaceAddresses) {
                    val ip = ifAddr.address
                    if (ip.isLinkLocalAddress || ip.isLoopbackAddress) continue
                    parts.add("${ip.hostAddress}/${ifAddr.networkPrefixLength}")
                }
            }
        } catch (_: Throwable) {
            return ""
        }
        return parts.joinToString(", ")
    }

    /**
     * Read rx/tx bytes for the active VPN network's interface. Tries
     * several sources in order because Android 10+ restricts app access
     * to some of them:
     *   1. /sys/class/net/<iface>/statistics/{rx_bytes,tx_bytes} - per
     *      interface counters published by the kernel, typically readable
     *      even on scoped-storage Android.
     *   2. /proc/net/dev - fallback. Publicly world-readable but on
     *      Android 10+ sometimes returns only the reader's own app's
     *      rows (confusingly zero for the VPN tun).
     *
     * Logs the resolved interface name the first time it's read and on
     * every failure so the Logs screen reveals which iface we queried;
     * this turns "rx/tx stays at 0" into an actionable bug report.
     */
    @Volatile private var loggedIface = ""

    @Suppress("DEPRECATION")  // cm.allNetworks: fine for a one-shot
    // lookup; the modern NetworkCallback path needs long-lived state
    // that would be overkill for a byte-counter read.
    private fun readVpnInterfaceBytes(): Pair<Long, Long> {
        return try {
            val cm = context.getSystemService(Context.CONNECTIVITY_SERVICE)
                    as? android.net.ConnectivityManager ?: return 0L to 0L
            val vpnNet = cm.allNetworks.firstOrNull { net ->
                val caps = cm.getNetworkCapabilities(net) ?: return@firstOrNull false
                caps.hasTransport(android.net.NetworkCapabilities.TRANSPORT_VPN)
            }
            if (vpnNet == null) {
                if (loggedIface != "<none>") {
                    PrivycsLogger.w(TAG, "No VPN-transport network - rx/tx cannot be read")
                    loggedIface = "<none>"
                }
                return 0L to 0L
            }
            val iface = cm.getLinkProperties(vpnNet)?.interfaceName
            if (iface.isNullOrEmpty()) {
                if (loggedIface != "<empty>") {
                    PrivycsLogger.w(TAG, "VPN network has no interface name - rx/tx cannot be read")
                    loggedIface = "<empty>"
                }
                return 0L to 0L
            }
            if (iface != loggedIface) {
                PrivycsLogger.i(TAG, "Reading rx/tx for VPN interface '$iface'")
                loggedIface = iface
            }

            // Try /sys/class/net first - most reliable on modern Android.
            val sysRx = java.io.File("/sys/class/net/$iface/statistics/rx_bytes")
            val sysTx = java.io.File("/sys/class/net/$iface/statistics/tx_bytes")
            if (sysRx.exists() && sysTx.exists()) {
                val rx = runCatching { sysRx.readText().trim().toLong() }.getOrNull() ?: 0L
                val tx = runCatching { sysTx.readText().trim().toLong() }.getOrNull() ?: 0L
                if (rx > 0 || tx > 0) return rx to tx
            }

            // Fallback: /proc/net/dev row.
            val line = java.io.File("/proc/net/dev").useLines { lines ->
                lines.firstOrNull { it.trim().startsWith("$iface:") }
            }
            if (line != null) {
                val parts = line.substringAfter(":").trim().split(Regex("\\s+"))
                if (parts.size >= 10) {
                    val rx = parts[0].toLongOrNull() ?: 0L
                    val tx = parts[8].toLongOrNull() ?: 0L
                    return rx to tx
                }
            }

            // Both sources silent.
            0L to 0L
        } catch (e: Exception) {
            PrivycsLogger.w(TAG, "readVpnInterfaceBytes failed: ${e.message}")
            0L to 0L
        }
    }
}
