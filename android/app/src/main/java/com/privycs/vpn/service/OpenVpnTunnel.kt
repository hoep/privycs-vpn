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
    // Device-wide RX/TX counters snapshotted at connect time so we can
    // compute session-scoped deltas via android.net.TrafficStats when
    // both /proc and the ByteCountListener are blocked or silent. Also
    // @Volatile because read/write happen on different threads (connect
    // coroutine vs. polling goroutine).
    @Volatile
    private var sessionStartTotalRx: Long = 0L
    @Volatile
    private var sessionStartTotalTx: Long = 0L
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
        // Snapshot the device-wide total byte counters at connect time.
        // TrafficStats.getTotal{Rx,Tx}Bytes is available on every
        // Android version since API 4 (no permission required), always
        // returns live kernel counters, and never hits the SELinux
        // restrictions that block /proc/net access on Android 11+. At
        // status-poll time we subtract this baseline to get a
        // session-scoped delta, which is what the UI actually wants to
        // display. Values reset on reboot — irrelevant since our
        // baseline is taken per-connect.
        try {
            sessionStartTotalRx = android.net.TrafficStats.getTotalRxBytes()
            sessionStartTotalTx = android.net.TrafficStats.getTotalTxBytes()
            if (sessionStartTotalRx == android.net.TrafficStats.UNSUPPORTED.toLong()) {
                sessionStartTotalRx = 0L
            }
            if (sessionStartTotalTx == android.net.TrafficStats.UNSUPPORTED.toLong()) {
                sessionStartTotalTx = 0L
            }
        } catch (_: Throwable) {
            sessionStartTotalRx = 0L
            sessionStartTotalTx = 0L
        }
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

        // Traffic counters — four-layer fallback chain. Each layer
        // produces a value; the highest non-zero wins. All four have
        // independent failure modes so it is extremely unlikely for
        // every layer to silently return zero simultaneously.
        //
        //   Layer 1: ByteCountListener-fed rxBytes / txBytes. Fires
        //     correctly on some devices, silently does nothing on
        //     others because ics-openvpn's VpnStatus is per-process
        //     and the :openvpn subprocess boundary is crossed via
        //     RemoteCallbackList that sometimes desyncs after a
        //     reconnect.
        //
        //   Layer 2: /proc/self/net/dev — sum of RX/TX across every
        //     tun* interface currently visible. Reads kernel counters
        //     directly; no IPC. Always accurate when readable, which
        //     is the default on Android 10 and older AND on newer
        //     Android through /proc/self/<pid>/net/dev (restrictions
        //     only hit /proc/net/*, not /proc/self/*).
        //
        //   Layer 3: TrafficStats.getTotal{Rx,Tx}Bytes delta against
        //     the baseline captured at connect() time. This counts
        //     DEVICE-WIDE traffic, not just tunnel traffic, so the
        //     numbers are upper-bound estimates rather than exact —
        //     but they are monotone, non-zero whenever any traffic
        //     flows, and never fail. TrafficStats has been public and
        //     permission-free since API 4; SELinux doesn't touch it.
        //
        //   Layer 4: fallback zero — never reached if at least one of
        //     the above produces a value.
        var rx = 0L
        var tx = 0L
        if (isUp) {
            // Layer 1
            val lsRx = rxBytes
            val lsTx = txBytes

            // Layer 2
            val (procRx, procTx) = readTunInterfaceStats()

            // Layer 3 — device-wide delta since connect
            var deltaRx = 0L
            var deltaTx = 0L
            try {
                val totalRx = android.net.TrafficStats.getTotalRxBytes()
                val totalTx = android.net.TrafficStats.getTotalTxBytes()
                if (totalRx != android.net.TrafficStats.UNSUPPORTED.toLong() &&
                    sessionStartTotalRx > 0L && totalRx > sessionStartTotalRx
                ) {
                    deltaRx = totalRx - sessionStartTotalRx
                }
                if (totalTx != android.net.TrafficStats.UNSUPPORTED.toLong() &&
                    sessionStartTotalTx > 0L && totalTx > sessionStartTotalTx
                ) {
                    deltaTx = totalTx - sessionStartTotalTx
                }
            } catch (_: Throwable) {
                // ignore
            }

            // Take the MAX of all three — since all sources increase
            // monotonically per second, the max represents the most
            // trusted value at this instant. /proc and listener are
            // per-tunnel (accurate), TrafficStats is device-wide
            // (upper bound); when the first two report zero due to
            // plumbing issues, the device-wide number is the ground
            // truth the user should see.
            rx = maxOf(lsRx, procRx, deltaRx)
            tx = maxOf(lsTx, procTx, deltaTx)

            // Log every call. Earlier version throttled to >2000ms but the
            // poll interval itself is 2000ms so the strict-greater check
            // suppressed almost every log on an idle scheduler. Log volume
            // at 2 lines/second is still trivially cheap.
            android.util.Log.i(
                TAG,
                "traffic layers listener=$lsRx/$lsTx " +
                    "proc=$procRx/$procTx " +
                    "delta=$deltaRx/$deltaTx " +
                    "winner=$rx/$tx"
            )
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
     * /proc/self/net/dev. Returns (0, 0) if no tun interface is visible
     * or the file cannot be parsed — a safe default that the caller
     * treats as "no fallback available, keep listener value".
     *
     * Why /proc/self and not /proc/net: Android 11+ tightened SELinux
     * policy on `untrusted_app` so /proc/net access is blocked for most
     * non-system apps, but /proc/self/net/ is always readable because
     * it routes through the process-owned /proc/<pid>/net/ tree and
     * inherits the per-process permissions. Interface list is the
     * same — Android does not namespace network interfaces per process
     * so tun0 is visible regardless of which /proc subtree we read.
     *
     * /proc/pid/net/dev format (header + one row per interface):
     *   Interface  |        Receive                                           |  Transmit
     *   face       | bytes    packets errs drop fifo frame compressed multi  | bytes    packets errs drop fifo colls carrier compressed
     *   tun0:        123456    100     0    0    0    0     0          0       78901     80      0    0    0    0     0       0
     *
     * Columns after the colon: rxBytes, rxPackets, ..., txBytes, ...
     * We only need rxBytes (index 0) and txBytes (index 8) of the
     * per-interface row.
     */
    private fun readTunInterfaceStats(): Pair<Long, Long> {
        // Try /proc/self/net/dev first (always readable). Fall back to
        // /proc/net/dev for the pre-Android-11 case where it's still
        // readable — some devices keep the old permissive policy.
        val candidates = listOf("/proc/self/net/dev", "/proc/net/dev")
        for (path in candidates) {
            val result = parseProcNetDev(path)
            if (result != null) return result
        }
        return 0L to 0L
    }

    private fun parseProcNetDev(path: String): Pair<Long, Long>? {
        return try {
            val lines = java.io.File(path).readLines()
            // Sum RX/TX across ALL tun* interfaces visible in /proc.
            // Rationale: on Android a VpnService reconnect creates a
            // fresh tun device whose index may differ from the previous
            // one (tun0 -> tun1 -> tun0 again). If we returned only the
            // first matching row the user would see frozen counters from
            // a stale interface after reconnect. Summing is monotonically
            // correct for the current session because the previous tun
            // is closed by the kernel and disappears from /proc before
            // the next one appears — at any moment there is typically
            // exactly one live tun, and summing over zero stale rows
            // still gives the right answer.
            var totalRx = 0L
            var totalTx = 0L
            var foundAny = false
            for (line in lines) {
                val trimmed = line.trim()
                if (!trimmed.startsWith("tun")) continue
                val colonIdx = trimmed.indexOf(':')
                if (colonIdx < 0) continue
                val fields = trimmed.substring(colonIdx + 1).trim().split(Regex("\\s+"))
                if (fields.size < 9) continue
                val rx = fields[0].toLongOrNull() ?: continue
                val tx = fields[8].toLongOrNull() ?: continue
                totalRx += rx
                totalTx += tx
                foundAny = true
            }
            if (foundAny) totalRx to totalTx else null
        } catch (_: Exception) {
            null
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

        // Include mode MUST contain our own package or the VPN
        // daemon's handshake traffic gets filtered out too and the
        // tunnel never establishes ("connected NICHT" with Include
        // + only-Elba was exactly this bug). Exclude mode does not
        // need this - a disallowed list cannot accidentally lock
        // the service out.
        val finalPackages = if (mode == "include") packages + context.packageName else packages
        profile.mAllowedAppsVpn = HashSet(finalPackages)
        profile.mAllowedAppsVpnAreDisallowed = (mode == "exclude")
        PrivycsLogger.i(
            TAG,
            "Split tunnel: mode=$mode apps=${finalPackages.size} " +
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
