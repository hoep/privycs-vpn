package com.privycs.vpn.service

import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.content.ServiceConnection
import android.net.VpnService
import android.os.IBinder
import com.privycs.vpn.data.models.VpnProtocol
import com.privycs.vpn.data.models.VpnStatus
import com.privycs.vpn.util.PrivycsLogger
import de.blinkt.openvpn.VpnProfile
import de.blinkt.openvpn.core.ConfigParser
import de.blinkt.openvpn.core.ConnectionStatus
import de.blinkt.openvpn.core.IOpenVPNServiceInternal
import de.blinkt.openvpn.core.LogItem
import de.blinkt.openvpn.core.OpenVPNService
import de.blinkt.openvpn.core.ProfileManager
import de.blinkt.openvpn.core.VPNLaunchHelper
import de.blinkt.openvpn.core.VpnStatus as OvpnVpnStatus
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import java.io.StringReader

/**
 * OpenVPN tunnel backed by ics-openvpn's OpenVPNService + minivpn PIE.
 *
 * Lifecycle:
 *   connect(config)  -> parse .ovpn into a VpnProfile
 *                    -> register profile with ProfileManager (OpenVPNService
 *                       looks it up by UUID when onStartCommand fires)
 *                    -> attach StateListener + ByteCountListener so we can
 *                       surface status without polling
 *                    -> VPNLaunchHelper.startOpenVpn() fires a foreground
 *                       service intent; OpenVPNService.onStartCommand spawns
 *                       the pie_openvpn subprocess and establishes the tun fd
 *                       via its own VpnService.Builder() call
 *   disconnect()     -> bind to OpenVPNService, call IOpenVPNServiceInternal
 *                       .stopVPN(false) over the AIDL binder, unbind
 *
 * OpenVPNService runs in the MAIN app process (the historic `:openvpn`
 * subprocess was removed because MODE_MULTI_PROCESS SharedPreferences
 * cross-sync is unreliable on Samsung Android 16 — the subprocess never
 * saw profiles we had just persisted from main, producing 10-second
 * "Used x 101 tries to get current version (-1/1)" stalls followed by
 * a NullPointerException in vendor notifyProfileVersionChanged). Keeping
 * OpenVPNService and our OpenVpnTunnel in the same process means the
 * ProfileManager singleton is shared - addProfile puts the profile in a
 * HashMap that OpenVPNService.onStartCommand reads from directly, no
 * disk round-trip required.
 *
 * Tradeoff: a native libopenvpn crash takes down the whole app rather
 * than just a subprocess. Matches how we already run strongSwan
 * libcharon.so in the main process for IPSec.
 *
 * Byte counters come from OpenVPNService's ByteCountListener callback -
 * values reported here are cumulative since connect, in bytes.
 */
class OpenVpnTunnel(private val context: Context) {

    enum class State {
        DISCONNECTED,
        CONNECTING,
        CONNECTED,
        DISCONNECTING,
        FAILED
    }

    private var profile: VpnProfile? = null
    @Volatile
    private var state: State = State.DISCONNECTED
    private var connectedSince: Long = 0L
    @Volatile
    private var rxBytes: Long = 0L
    @Volatile
    private var txBytes: Long = 0L
    private var remoteEndpoint: String = ""
    private var localAddress: String = ""
    private var lastErrorMessage: String = ""

    /** Invoked whenever [state] transitions. Used by PrivycsVpnService to
     *  forward status into VpnServiceManager + update the notification. */
    var onStateChanged: ((State) -> Unit)? = null

    // VpnStatus listeners. We keep references so we can deregister on
    // disconnect - otherwise they leak across reconnects and we'd see stale
    // state from the previous session bleed into the next.
    private val stateListener = object : OvpnVpnStatus.StateListener {
        override fun updateState(
            stateName: String?,
            logmessage: String?,
            localizedResId: Int,
            level: ConnectionStatus?,
            intent: Intent?
        ) {
            val newState = mapLevel(level)
            if (newState != null && newState != state) {
                PrivycsLogger.i(
                    TAG,
                    "state=$stateName level=$level -> $newState (msg=${logmessage ?: "-"})"
                )
                if (newState == State.CONNECTED) {
                    connectedSince = System.currentTimeMillis()
                } else if (newState == State.DISCONNECTED || newState == State.FAILED) {
                    connectedSince = 0L
                    if (level == ConnectionStatus.LEVEL_AUTH_FAILED) {
                        lastErrorMessage = logmessage ?: "Authentication failed"
                    }
                }
                state = newState
                onStateChanged?.invoke(newState)
            }
        }

        override fun setConnectedVPN(uuid: String?) {
            // UUID of the profile that just connected. We only manage one at
            // a time so nothing to route here - the existing state
            // transition in updateState already told the UI.
        }
    }

    private val byteCountListener = OvpnVpnStatus.ByteCountListener { inBytes, outBytes, _, _ ->
        rxBytes = inBytes
        txBytes = outBytes
    }

    /**
     * Bridge ics-openvpn's internal VpnStatus log stream into PrivycsLogger
     * so it lands in the in-app Logs screen alongside WG / IPSec events.
     * Without this, native OpenVPN events (TLS handshake, PUSH_REPLY, ping
     * timeouts, reconnects) only hit logcat - invisible to users
     * troubleshooting a connect failure in the app UI.
     *
     * Filter: only INFO/WARNING/ERROR. The VERBOSE/DEBUG stream is high-
     * volume (multiple lines per packet in debug builds) and would swamp
     * the 500-line rolling log file we keep on disk.
     */
    private val logListener = OvpnVpnStatus.LogListener { item: LogItem ->
        val msg = try {
            item.getString(context)
        } catch (_: Exception) {
            item.toString()
        }
        when (item.logLevel) {
            OvpnVpnStatus.LogLevel.ERROR -> PrivycsLogger.e(NATIVE_TAG, msg)
            OvpnVpnStatus.LogLevel.WARNING -> PrivycsLogger.w(NATIVE_TAG, msg)
            OvpnVpnStatus.LogLevel.INFO -> PrivycsLogger.i(NATIVE_TAG, msg)
            else -> {
                // DEBUG / VERBOSE dropped intentionally - too chatty for the
                // in-app Logs screen; full stream is still available via
                // `adb logcat -s OpenVPN:V`.
            }
        }
    }

    /**
     * Parse an .ovpn config and start the tunnel.
     *
     * @param ovpnContent  raw .ovpn file body (may contain inline <ca>, <cert>,
     *                     <key>, <tls-auth> blocks - ConfigParser handles them)
     * @param name         display name for the connection
     * @param vpnService   kept in the signature for parity with the other
     *                     tunnel classes; unused because OpenVPNService owns
     *                     its own VpnService instance in a separate process
     */
    @Suppress("UNUSED_PARAMETER")
    suspend fun connect(
        ovpnContent: String,
        name: String,
        vpnService: VpnService,
        stableConnectionId: String = ""
    ) = withContext(Dispatchers.IO) {
        if (state == State.CONNECTED || state == State.CONNECTING) {
            PrivycsLogger.w(TAG, "connect() called while state=$state, ignoring")
            return@withContext
        }

        state = State.CONNECTING
        lastErrorMessage = ""
        rxBytes = 0L
        txBytes = 0L
        onStateChanged?.invoke(state)

        val parsedProfile = try {
            parseOvpn(ovpnContent, name)
        } catch (e: Exception) {
            PrivycsLogger.e(TAG, "Failed to parse .ovpn config", e)
            state = State.FAILED
            lastErrorMessage = "Config parse failed: ${e.message}"
            onStateChanged?.invoke(state)
            throw e
        }

        // Force a deterministic UUID derived from our stable
        // VpnConnection.id so that a profile pre-loaded at app boot time
        // (PrivycsApp.preloadOpenVpnProfiles) matches the profile built
        // here at connect time. Without it ConfigParser.convertProfile()
        // hands us a fresh random UUID every call, the pre-load path
        // becomes useless, and the race returns (:openvpn process loops
        // "Used 101 tries" because SharedPreferences sync has not
        // completed yet).
        if (stableConnectionId.isNotBlank()) {
            de.blinkt.openvpn.core.PrivycsStatusListenerBridge
                .forceProfileUuid(parsedProfile, stableConnectionId)
        }

        profile = parsedProfile
        remoteEndpoint = parsedProfile.mConnections?.firstOrNull()?.let {
            "${it.mServerName}:${it.mServerPort}"
        } ?: ""

        // Translate our SharedPreferences-based split-tunnel config into the
        // VpnProfile fields OpenVPNService reads when building its own
        // VpnService.Builder (OpenVPNService.java:~1145). For WireGuard and
        // IPSec this happens via PrivycsVpnService.applySplitTunnelSettings
        // because WE own the Builder; for OpenVPN the Builder lives in the
        // :openvpn subprocess, so we have to bake the decision into the
        // profile itself before VPNLaunchHelper.startOpenVpn fires.
        applySplitTunnelToProfile(parsedProfile)

        // Persist the profile so OpenVPNService in the :openvpn subprocess
        // can find it on its first ProfileManager.get() call. Three disk
        // writes are needed and the third one MUST be synchronous,
        // otherwise the subprocess loops "Used x 101 tries to get current
        // version (-1/1)" and eventually crashes in
        // ProfileManager.notifyProfileVersionChanged with a null
        // VpnProfile.mVersion read. An earlier build called vendor's
        // ProfileManager.saveProfileList(ctx) which internally uses
        // SharedPreferences.apply() (async); MODE_MULTI_PROCESS is
        // deprecated since API 11 and on modern Android no longer
        // reliably cross-process-syncs before the file has been flushed
        // to disk. PrivycsStatusListenerBridge.persistProfileSync does
        // the same three writes but forces .commit() on the final
        // StringSet update, so the subprocess reads the new UUID on its
        // very next SharedPreferences cache miss.
        de.blinkt.openvpn.core.PrivycsStatusListenerBridge.persistProfileSync(context, parsedProfile)

        // Attach listeners BEFORE startOpenVpn so we don't miss the first
        // LEVEL_START callback. addStateListener fires the current state
        // synchronously on attach, so stale state from a previous run would
        // flood in here - clearing via clearVpnStatus() below resets the
        // singletons to LEVEL_NOTCONNECTED.
        OvpnVpnStatus.initLogCache(context.cacheDir)
        OvpnVpnStatus.addStateListener(stateListener)
        OvpnVpnStatus.addByteCountListener(byteCountListener)
        OvpnVpnStatus.addLogListener(logListener)

        PrivycsLogger.i(
            TAG,
            "Starting OpenVPN: name=$name remote=$remoteEndpoint uuid=${parsedProfile.uuid}"
        )

        // Pre-flight diagnostic: OpenVPNThread.ProcessBuilder swallows the
        // real failure reason into VpnStatus.logException (ics-openvpn's
        // internal log buffer, NOT Android logcat), and the followup NPE in
        // stopProcess() obscures everything. Log the binary state BEFORE
        // handing control to VPNLaunchHelper so we can tell in logcat
        // whether libovpnexec.so is missing, wrong size, non-executable,
        // or fine. On API 28+ the executable lives in nativeLibraryDir.
        try {
            val nativeDir = context.applicationInfo.nativeLibraryDir
            val bin = java.io.File(nativeDir, "libovpnexec.so")
            PrivycsLogger.i(
                TAG,
                "OpenVPN binary diag: path=${bin.absolutePath} " +
                    "exists=${bin.exists()} " +
                    "canExecute=${bin.exists() && bin.canExecute()} " +
                    "length=${if (bin.exists()) bin.length() else -1}"
            )
            if (bin.exists() && !bin.canExecute()) {
                // Some devices extract libs read-only; try to promote to
                // executable so ProcessBuilder.start() can launch it.
                val marked = bin.setExecutable(true, false)
                PrivycsLogger.w(TAG, "OpenVPN binary was not executable, setExecutable(true)=$marked")
            }
        } catch (t: Throwable) {
            PrivycsLogger.w(TAG, "OpenVPN binary diag failed: ${t.message}")
        }

        VPNLaunchHelper.startOpenVpn(parsedProfile, context, "Privycs connect", true)
    }

    /**
     * Ask OpenVPNService to stop the tunnel. Binds over AIDL, calls
     * stopVPN(false), unbinds. Safe to call from any state - no-op if
     * already disconnected.
     */
    suspend fun disconnect() = withContext(Dispatchers.IO) {
        if (state == State.DISCONNECTED) {
            PrivycsLogger.d(TAG, "disconnect() no-op, already DISCONNECTED")
            return@withContext
        }

        PrivycsLogger.i(TAG, "Disconnecting OpenVPN tunnel")
        state = State.DISCONNECTING
        onStateChanged?.invoke(state)

        // Bind to OpenVPNService across the :openvpn process boundary,
        // issue stopVPN via IOpenVPNServiceInternal, then unbind. We can't
        // fire-and-forget a START_SERVICE intent with a stop flag -
        // OpenVPNService doesn't have an intent action for that; the only
        // public stop path is the AIDL binder.
        val stopLatch = kotlinx.coroutines.CompletableDeferred<Unit>()
        val connection = object : ServiceConnection {
            override fun onServiceConnected(name: ComponentName?, binder: IBinder?) {
                try {
                    val svc = IOpenVPNServiceInternal.Stub.asInterface(binder)
                    svc?.stopVPN(false)
                } catch (e: Exception) {
                    PrivycsLogger.w(TAG, "stopVPN() call failed: ${e.message}")
                } finally {
                    try { context.unbindService(this) } catch (_: Exception) {}
                    stopLatch.complete(Unit)
                }
            }

            override fun onServiceDisconnected(name: ComponentName?) {
                stopLatch.complete(Unit)
            }
        }

        val bindIntent = Intent(context, OpenVPNService::class.java).apply {
            action = OpenVPNService.START_SERVICE
        }
        val bound = context.bindService(bindIntent, connection, Context.BIND_AUTO_CREATE)
        if (!bound) {
            PrivycsLogger.w(TAG, "bindService(OpenVPNService) returned false; forcing DISCONNECTED")
            try { context.unbindService(connection) } catch (_: Exception) {}
            state = State.DISCONNECTED
            onStateChanged?.invoke(state)
            cleanupListeners()
            return@withContext
        }

        // Give the service up to 3s to acknowledge the stop. OpenVPN's
        // SIGTERM handling is fast (<200ms) in practice - if we're past 3s
        // something is wrong and we drop the bind regardless.
        try {
            kotlinx.coroutines.withTimeout(3000) { stopLatch.await() }
        } catch (_: Exception) {
            PrivycsLogger.w(TAG, "stopVPN() timed out after 3s")
            try { context.unbindService(connection) } catch (_: Exception) {}
        }
        cleanupListeners()

        // State is finalized via the VpnStatus StateListener callback
        // when the native process exits (it emits LEVEL_NOTCONNECTED). If
        // that callback never arrives (rare, but happens when the service
        // is killed externally), enforce DISCONNECTED here.
        if (state == State.DISCONNECTING) {
            state = State.DISCONNECTED
            onStateChanged?.invoke(state)
        }
    }

    private fun cleanupListeners() {
        OvpnVpnStatus.removeStateListener(stateListener)
        OvpnVpnStatus.removeByteCountListener(byteCountListener)
        OvpnVpnStatus.removeLogListener(logListener)
    }

    fun getState(): State = state

    fun getStatus(connectionName: String, connectionId: String): VpnStatus {
        val isUp = state == State.CONNECTED

        // Prefer the byteCountListener-fed counters; fall back to the
        // live tun interface read if those are still zero. In practice
        // ics-openvpn's ByteCountListener is sometimes silent when the
        // management protocol omits the `bytecount` pacing command, or
        // when the VpnStatus singleton state gets reset across a
        // reconnect inside the same process. /proc/net/dev is always
        // authoritative — the Linux kernel keeps these counters and
        // every packet crossing the tun device increments them. Reading
        // here instead of at listener time means the status reflects
        // traffic even on connections where the listener never fires.
        var rx = rxBytes
        var tx = txBytes
        if (isUp && (rx == 0L || tx == 0L)) {
            val (fbRx, fbTx) = readTunInterfaceStats()
            if (fbRx > rx) rx = fbRx
            if (fbTx > tx) tx = fbTx
        }

        return VpnStatus(
            connected = isUp,
            connectionName = connectionName,
            connectionId = connectionId,
            activeProtocol = VpnProtocol.OPENVPN,
            uptime = if (isUp && connectedSince > 0) System.currentTimeMillis() - connectedSince else 0L,
            rxBytes = rx,
            txBytes = tx,
            serverEndpoint = remoteEndpoint,
            localAddress = localAddress,
            error = if (state == State.FAILED) lastErrorMessage else ""
        )
    }

    /**
     * Read RX/TX byte counters for the first tun* interface from
     * /proc/net/dev. Returns (0, 0) if no tun interface is visible or
     * the file cannot be parsed — a safe default that the caller treats
     * as "no fallback available, keep listener value".
     *
     * /proc/net/dev format (header + one row per interface):
     *   Interface  |        Receive                                           |  Transmit
     *   face       | bytes    packets errs drop fifo frame compressed multi  | bytes    packets errs drop fifo colls carrier compressed
     *   tun0:        123456    100     0    0    0    0     0          0       78901     80      0    0    0    0     0       0
     *
     * Columns after the colon: [rxBytes, rxPackets, ..., txBytes, ...]
     * We only need rxBytes (index 0) and txBytes (index 8) of the
     * per-interface row.
     */
    private fun readTunInterfaceStats(): Pair<Long, Long> {
        return try {
            val lines = java.io.File("/proc/net/dev").readLines()
            for (line in lines) {
                val trimmed = line.trim()
                // Match tun0, tun1, ... — the ics-openvpn VpnService is
                // always established under a tun-family name on Android.
                if (!trimmed.startsWith("tun")) continue
                val colonIdx = trimmed.indexOf(':')
                if (colonIdx < 0) continue
                val fields = trimmed.substring(colonIdx + 1).trim().split(Regex("\\s+"))
                if (fields.size < 9) continue
                val rx = fields[0].toLongOrNull() ?: 0L
                val tx = fields[8].toLongOrNull() ?: 0L
                return rx to tx
            }
            0L to 0L
        } catch (_: Exception) {
            0L to 0L
        }
    }

    /**
     * Parse .ovpn content into an ics-openvpn VpnProfile. Handles all the
     * inline blocks (<ca>, <cert>, <key>, <tls-auth>, <tls-crypt>) and
     * `remote` / `proto` / `cipher` / `auth` directives natively - the
     * gateway's .ovpn output feeds in unchanged.
     *
     * Cert-only auth (no user/pass) is the only mode we support today;
     * setting mAuthenticationType to TYPE_CERTIFICATES short-circuits the
     * password prompt the upstream app would otherwise show.
     */
    private fun parseOvpn(content: String, name: String): VpnProfile {
        val parser = ConfigParser()
        parser.parseConfig(StringReader(content))
        val profile = parser.convertProfile()
        profile.mName = name
        profile.mUsername = null
        profile.mPassword = null
        profile.mAuthenticationType = VpnProfile.TYPE_CERTIFICATES
        return profile
    }

    /**
     * Read the user's split-tunnel preferences (mode + package list) from
     * the shared SharedPreferences bucket "split_tunnel" and translate them
     * into VpnProfile.mAllowedAppsVpn + mAllowedAppsVpnAreDisallowed.
     *
     * Semantics match PrivycsVpnService.applySplitTunnelSettings so the
     * user sees identical behaviour across WireGuard / IPSec / OpenVPN:
     *   mode="exclude" -> disallowed list (VPN routes everyone except these)
     *   mode="include" -> allowed list    (VPN routes only these)
     *   empty list     -> no filtering (all apps go through VPN)
     */
    private fun applySplitTunnelToProfile(profile: VpnProfile) {
        val prefs = context.getSharedPreferences("split_tunnel", Context.MODE_PRIVATE)
        val mode = prefs.getString("mode", "exclude") ?: "exclude"
        val packages = prefs.getStringSet("packages", emptySet()) ?: emptySet()

        if (packages.isEmpty()) {
            // Empty set = no filter. Clear any stale state in case a previous
            // connect had populated these fields on the same profile instance.
            profile.mAllowedAppsVpn = HashSet()
            profile.mAllowedAppsVpnAreDisallowed = true
            PrivycsLogger.d(TAG, "Split tunnel: no apps configured, all apps routed through VPN")
            return
        }

        profile.mAllowedAppsVpn = HashSet(packages)
        profile.mAllowedAppsVpnAreDisallowed = (mode == "exclude")
        PrivycsLogger.i(
            TAG,
            "Split tunnel: mode=$mode apps=${packages.size} " +
                "(disallowed=${profile.mAllowedAppsVpnAreDisallowed})"
        )
    }

    private fun mapLevel(level: ConnectionStatus?): State? = when (level) {
        ConnectionStatus.LEVEL_CONNECTED -> State.CONNECTED
        ConnectionStatus.LEVEL_NOTCONNECTED -> State.DISCONNECTED
        ConnectionStatus.LEVEL_AUTH_FAILED -> State.FAILED
        ConnectionStatus.LEVEL_CONNECTING_NO_SERVER_REPLY_YET,
        ConnectionStatus.LEVEL_CONNECTING_SERVER_REPLIED,
        ConnectionStatus.LEVEL_START,
        ConnectionStatus.LEVEL_WAITING_FOR_USER_INPUT,
        ConnectionStatus.LEVEL_VPNPAUSED,
        ConnectionStatus.LEVEL_NONETWORK -> State.CONNECTING
        null, ConnectionStatus.UNKNOWN_LEVEL -> null
    }

    companion object {
        private const val TAG = "OpenVpnTunnel"
        // Distinct tag for native ics-openvpn log stream so users filtering
        // by tag in the Logs screen can separate our tunnel-state events
        // from the underlying OpenVPN process events (TLS / PUSH_REPLY /
        // ping-restart / etc).
        private const val NATIVE_TAG = "OpenVPN"
    }
}
