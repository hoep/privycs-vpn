package com.privycs.vpn.service

import android.app.Notification
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.net.VpnService
import android.os.Build
import android.util.Log
import androidx.core.app.NotificationCompat
import com.privycs.vpn.MainActivity
import com.privycs.vpn.PrivycsApp
import com.privycs.vpn.R
import com.privycs.vpn.data.models.VpnProtocol
import com.privycs.vpn.data.models.VpnStatus
import com.privycs.vpn.widget.VpnWidget
import com.wireguard.android.backend.GoBackend
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch

class PrivycsVpnService : VpnService() {

    companion object {
        private const val TAG = "PrivycsVpnService"

        const val ACTION_CONNECT = "com.privycs.vpn.CONNECT"
        const val ACTION_DISCONNECT = "com.privycs.vpn.DISCONNECT"

        const val EXTRA_CONNECTION_ID = "connection_id"
        const val EXTRA_PROTOCOL = "protocol"
        const val EXTRA_CONFIG_CONTENT = "config_content"
        const val EXTRA_CONNECTION_NAME = "connection_name"

        private const val STATUS_POLL_INTERVAL_MS = 2000L
    }

    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private var goBackend: GoBackend? = null
    private var wireGuardTunnel: WireGuardTunnel? = null
    private var openVpnTunnel: OpenVpnTunnel? = null
    private var ipSecTunnel: IpSecTunnel? = null
    private var currentConnectionName: String = ""
    private var currentConnectionId: String = ""
    private var currentProtocol: VpnProtocol? = null
    private var connectStartTime: Long = 0L

    override fun onCreate() {
        super.onCreate()
        goBackend = GoBackend(this)
        Log.d(TAG, "VPN service created")
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        when (intent?.action) {
            ACTION_CONNECT -> {
                val connectionId = intent.getStringExtra(EXTRA_CONNECTION_ID) ?: ""
                val protocolStr = intent.getStringExtra(EXTRA_PROTOCOL) ?: ""
                val configContent = intent.getStringExtra(EXTRA_CONFIG_CONTENT) ?: ""
                val connectionName = intent.getStringExtra(EXTRA_CONNECTION_NAME) ?: ""

                startForeground(
                    PrivycsApp.NOTIFICATION_ID_VPN,
                    buildNotification("Connecting to $connectionName...")
                )

                handleConnect(connectionId, protocolStr, configContent, connectionName)
            }

            ACTION_DISCONNECT -> {
                handleDisconnect()
            }

            else -> {
                // Always-on VPN restart: try to reconnect with last active connection
                startForeground(
                    PrivycsApp.NOTIFICATION_ID_VPN,
                    buildNotification("Reconnecting...")
                )
                handleAlwaysOnReconnect()
            }
        }

        return START_STICKY
    }

    override fun onDestroy() {
        scope.cancel()
        super.onDestroy()
        Log.d(TAG, "VPN service destroyed")
    }

    override fun onRevoke() {
        // VPN permission revoked by user or system. Typical triggers:
        // the user disables our Always-On toggle, another VPN app
        // takes over the VPN slot, or the user taps Disconnect on the
        // system VPN settings page. Stamp the timestamp so
        // NetworkMonitor skips on-demand auto-reconnect for the next
        // few seconds - otherwise it collides with the in-flight
        // service teardown and spawns a second GoBackend on the same
        // /dev/tun (observed symptom: "Failed to write packet to TUN
        // device: input/output error" + keepalive storm).
        Log.w(TAG, "VPN permission revoked")
        com.privycs.vpn.util.AlwaysOnDetector.stampSystemRevoke(this)
        handleDisconnect()
        super.onRevoke()
    }

    private fun handleConnect(
        connectionId: String,
        protocolStr: String,
        configContent: String,
        connectionName: String
    ) {
        val newProtocol = VpnProtocol.fromString(protocolStr)

        scope.launch {
            try {
                // CRITICAL: tear down ANY previous protocol tunnel before
                // starting a new one. Android VpnService allows only one
                // active TUN per user; if a previous tunnel's native-side
                // state (WireGuard GoBackend goroutines, strongSwan charon,
                // OpenVPN subprocess + management thread) is still alive
                // when the new protocol calls VpnService.Builder.establish(),
                // the new tunnel fd collides with the old one's writes. The
                // symptom is a connected-looking UI where no app traffic
                // reaches the remote server (server shows only keepalives)
                // and the old protocol's goroutines spam
                // "Failed to write packet to TUN device: input/output error"
                // until ping-restart kills the new tunnel ~60s later.
                //
                // Kill EVERY possible leftover, not just the one matching
                // currentProtocol: a zombie from a crashed previous connect
                // may have currentProtocol=null but still hold a tunnel
                // object referenced from the singleton field.
                teardownAllProtocols()

                currentConnectionId = connectionId
                currentConnectionName = connectionName
                currentProtocol = newProtocol

                when (currentProtocol) {
                    VpnProtocol.WIREGUARD -> connectWireGuard(configContent)
                    VpnProtocol.OPENVPN -> connectOpenVpn(configContent)
                    VpnProtocol.IPSEC -> connectIpSec(configContent)
                    null -> {
                        Log.e(TAG, "Unknown protocol: $protocolStr")
                        val manager = VpnServiceManager.getInstance(this@PrivycsVpnService)
                        manager.updateStatus(VpnStatus(error = "Unknown protocol: $protocolStr"))
                        stopSelf()
                    }
                }
            } catch (e: Exception) {
                Log.e(TAG, "Connect failed", e)
                val manager = VpnServiceManager.getInstance(this@PrivycsVpnService)
                manager.updateStatus(VpnStatus(error = "Connection failed: ${e.message}"))
                stopSelf()
            }
        }
    }

    /**
     * Aggressively dispose all protocol tunnels and give native-side
     * cleanup time to complete before the next tunnel grabs the VpnService
     * slot. Safe to call even when no tunnel is active (all disconnect()
     * calls swallow exceptions on already-down tunnels).
     *
     * Delay rationale:
     * - WireGuard GoBackend select-loop goroutines: ~300-500ms to exit
     * - strongSwan charon IKE_SA_DELETE handshake: ~1-2s round-trip
     * - OpenVPN subprocess SIGTERM + management-socket close: ~200-500ms
     *
     * We wait 1500ms total so even the slowest (charon) has a chance to
     * finish. This shows up to the user as a brief pause on protocol
     * switch, which is acceptable vs the broken-tunnel symptom.
     */
    private suspend fun teardownAllProtocols() {
        val hadSomething = wireGuardTunnel != null || openVpnTunnel != null || ipSecTunnel != null
        try { wireGuardTunnel?.disconnect() } catch (e: Exception) { Log.w(TAG, "WG teardown: ${e.message}") }
        wireGuardTunnel = null
        try { openVpnTunnel?.disconnect() } catch (e: Exception) { Log.w(TAG, "OpenVPN teardown: ${e.message}") }
        openVpnTunnel = null
        try { ipSecTunnel?.disconnect() } catch (e: Exception) { Log.w(TAG, "IPSec teardown: ${e.message}") }
        ipSecTunnel = null

        if (hadSomething) {
            Log.i(TAG, "Previous tunnel torn down, waiting 1500ms for native-side cleanup")
            delay(1500)
        }
    }

    private suspend fun connectWireGuard(configContent: String) {
        val backend = goBackend ?: throw IllegalStateException("GoBackend not initialized")
        val tunnel = WireGuardTunnel(backend)
        wireGuardTunnel = tunnel

        tunnel.connect(configContent, "privycs0")

        connectStartTime = System.currentTimeMillis()
        updateNotification("Connected to $currentConnectionName")
        sendWidgetUpdate(connected = true)
        startStatusPolling()
    }

    private suspend fun connectOpenVpn(configContent: String) {
        // OpenVPN is owned by ics-openvpn's OpenVPNService which runs in the
        // :openvpn process and calls VpnService.Builder() internally. Our
        // own VpnService instance does NOT establish a tun fd for OpenVPN -
        // only one VpnService at a time can hold the slot, and we hand it
        // to OpenVPNService. PrivycsVpnService stays alive purely as a
        // controller so handleDisconnect() has an instance to dispatch
        // through.
        val tunnel = OpenVpnTunnel(applicationContext)
        openVpnTunnel = tunnel

        // Mirror IPSec: forward live state transitions into VpnServiceManager
        // so the UI reflects CONNECTING / CONNECTED / DISCONNECTED without
        // needing a poll loop. Upstream VpnStatus fires its StateListener on
        // every relevant native event (AUTH, GET_CONFIG, CONNECTED, ...)
        // and we translate them to our 3-state enum in OpenVpnTunnel.mapLevel.
        val manager = VpnServiceManager.getInstance(this@PrivycsVpnService)
        tunnel.onStateChanged = { s ->
            val connected = s == OpenVpnTunnel.State.CONNECTED
            manager.updateStatus(tunnel.getStatus(currentConnectionName, currentConnectionId))
            when (s) {
                OpenVpnTunnel.State.CONNECTING -> updateNotification("Connecting $currentConnectionName (OpenVPN)...")
                OpenVpnTunnel.State.CONNECTED  -> updateNotification("Connected to $currentConnectionName (OpenVPN)")
                OpenVpnTunnel.State.DISCONNECTING -> updateNotification("Disconnecting...")
                OpenVpnTunnel.State.DISCONNECTED -> updateNotification("Disconnected")
                OpenVpnTunnel.State.FAILED -> updateNotification("OpenVPN failed")
            }
            sendWidgetUpdate(connected = connected)
        }

        // Pass currentConnectionId (the stable VpnConnection.id we hand
        // through from VpnServiceManager) so OpenVpnTunnel forces the
        // same deterministic UUID PrivycsApp.preloadOpenVpnProfiles()
        // used at app boot - this closes the pre-load <-> connect UUID
        // race.
        tunnel.connect(configContent, currentConnectionName, this@PrivycsVpnService, currentConnectionId)

        connectStartTime = System.currentTimeMillis()
        sendWidgetUpdate(connected = false)
        // Shared poll loop: state-listener callbacks drive connected/uptime,
        // byte counters tick live via ByteCountListener. We still run the
        // periodic poll for parity with WireGuard/IPSec so the UI refresh
        // cadence is uniform across protocols.
        startStatusPolling()
    }

    private suspend fun connectIpSec(configContent: String) {
        // For IPSec the actual VPN tunnel is owned by strongSwan's
        // CharonVpnService (started indirectly by IpSecTunnel.connect via
        // VpnStateService). We keep this service alive as a thin controller
        // so handleDisconnect() still has an instance to route through.
        // Only one VpnService can hold the tunnel slot at a time, so we do
        // NOT call VpnService.Builder.establish() here - CharonVpnService
        // holds the TUN.
        val tunnel = IpSecTunnel(applicationContext)
        ipSecTunnel = tunnel

        // Forward strongSwan's live state transitions into our VpnStatus so
        // the UI reflects CONNECTING/CONNECTED/DISCONNECTED without polling.
        val manager = VpnServiceManager.getInstance(this@PrivycsVpnService)
        tunnel.onStateChanged = { s ->
            val connected = s == IpSecTunnel.State.CONNECTED
            manager.updateStatus(tunnel.getStatus(currentConnectionName, currentConnectionId))
            when (s) {
                IpSecTunnel.State.CONNECTING -> updateNotification("Connecting $currentConnectionName (IPSec)...")
                IpSecTunnel.State.CONNECTED  -> updateNotification("Connected to $currentConnectionName (IPSec)")
                IpSecTunnel.State.DISCONNECTING -> updateNotification("Disconnecting...")
                IpSecTunnel.State.DISCONNECTED -> updateNotification("Disconnected")
            }
            sendWidgetUpdate(connected = connected)
        }

        tunnel.connect(configContent, currentConnectionName, this@PrivycsVpnService)

        connectStartTime = System.currentTimeMillis()
        sendWidgetUpdate(connected = false)
        // Start the shared poll loop - VpnStateListener only fires on STATE
        // changes, not every second, so uptime stays frozen at the moment
        // of CONNECTED without a periodic push. Byte counters remain 0
        // (charon's public API does not expose per-SA byte counters through
        // VpnStateService; that requires a separate native bridge).
        startStatusPolling()
    }

    private fun handleDisconnect() {
        scope.launch {
            try {
                when (currentProtocol) {
                    VpnProtocol.WIREGUARD -> {
                        wireGuardTunnel?.disconnect()
                        wireGuardTunnel = null
                    }
                    VpnProtocol.OPENVPN -> {
                        openVpnTunnel?.disconnect()
                        openVpnTunnel = null
                    }
                    VpnProtocol.IPSEC -> {
                        ipSecTunnel?.disconnect()
                        ipSecTunnel = null
                    }
                    null -> {
                        // Disconnect all in case protocol is unknown
                        wireGuardTunnel?.disconnect()
                        wireGuardTunnel = null
                        openVpnTunnel?.disconnect()
                        openVpnTunnel = null
                        ipSecTunnel?.disconnect()
                        ipSecTunnel = null
                    }
                }
            } catch (e: Exception) {
                Log.e(TAG, "Error during disconnect", e)
            }

            val manager = VpnServiceManager.getInstance(this@PrivycsVpnService)
            manager.updateStatus(VpnStatus())

            connectStartTime = 0L
            sendWidgetUpdate(connected = false)

            stopForeground(STOP_FOREGROUND_REMOVE)
            stopSelf()
        }
    }

    private fun handleAlwaysOnReconnect() {
        scope.launch {
            // Detection: every null-intent wake-up is a signal. If it
            // happened within the detection window after a user-
            // initiated disconnect, we are definitely under system
            // Always-On control. AlwaysOnDetector persists the flag so
            // the UI can subsequently show the pause-or-settings sheet
            // instead of a disconnect that Always-On would just undo.
            com.privycs.vpn.util.AlwaysOnDetector.onAlwaysOnReconnectTriggered(
                this@PrivycsVpnService
            )

            // Pause honor: if the UI asked for a temporary pause (user
            // tapped "Pause 5 minutes" in the Always-On disconnect
            // sheet), we must NOT start the tunnel. stopSelf so Android
            // does not keep the foreground notification alive for a
            // service that is intentionally doing nothing.
            if (com.privycs.vpn.util.AlwaysOnDetector.isPausedNow(this@PrivycsVpnService)) {
                Log.i(TAG, "handleAlwaysOnReconnect: pause flag active - skipping reconnect")
                stopForeground(STOP_FOREGROUND_REMOVE)
                stopSelf()
                return@launch
            }

            val connRepo = PrivycsApp.instance.connectionRepository
            // NOTE: drop the settings.alwaysOn check. Android already
            // wakes this VpnService with a null intent only when its
            // system-level always-on VPN toggle is ON (Settings ->
            // Network & Internet -> VPN -> Privycs -> Always-on VPN) or
            // when the foreground service gets restarted by the system
            // after a crash. In either case, "we have an active
            // connection" is the only precondition we actually care about
            // - the old `&& settings.alwaysOn` branch required the user
            // to ALSO flip a redundant app-level toggle which confused
            // everyone who configured always-on via the system sheet.
            val activeConn = connRepo.getActive()
            if (activeConn == null) {
                Log.d(TAG, "No active connection to restore, stopping")
                stopForeground(STOP_FOREGROUND_REMOVE)
                stopSelf()
                return@launch
            }

            val config = activeConn.getActiveConfig()
            if (config != null) {
                handleConnect(
                    activeConn.id,
                    activeConn.activeProtocol.name,
                    config.configContent,
                    activeConn.name
                )
            }
        }
    }

    /**
     * Poll tunnel statistics and update the global VPN status.
     * Delegates to the active protocol's tunnel implementation.
     */
    private fun startStatusPolling() {
        scope.launch {
            val manager = VpnServiceManager.getInstance(this@PrivycsVpnService)
            var iter = 0
            // Delay BEFORE the first status read so the tunnel has a
            // chance to transition out of DISCONNECTED. Otherwise the
            // very first iteration races the state listener and sees
            // state=DISCONNECTED, the stillTransient check fails
            // (neither CONNECTING nor CONNECTED yet), and the loop
            // breaks immediately with "Tunnel went down unexpectedly".
            // That single-iteration death left the UI stuck at uptime
            // 0 and rxBytes/txBytes frozen at the onStateChanged
            // callback's one-shot values for the rest of the session.
            delay(STATUS_POLL_INTERVAL_MS)
            val loopStart = System.currentTimeMillis()
            Log.i(TAG, "startStatusPolling: loop starting, scope.isActive=$isActive, currentProtocol=$currentProtocol")
            while (isActive) {
                iter++
                Log.i(TAG, "startStatusPolling: iteration=$iter protocol=$currentProtocol")
                val status = when (currentProtocol) {
                    VpnProtocol.WIREGUARD -> {
                        wireGuardTunnel?.getStatus(currentConnectionName, currentConnectionId)
                    }
                    VpnProtocol.OPENVPN -> {
                        openVpnTunnel?.getStatus(currentConnectionName, currentConnectionId)
                    }
                    VpnProtocol.IPSEC -> {
                        ipSecTunnel?.getStatus(currentConnectionName, currentConnectionId)
                    }
                    null -> null
                }

                if (status != null) {
                    manager.updateStatus(status)
                    sendWidgetUpdate(status.connected)

                    if (!status.connected) {
                        // Break only on hard DISCONNECTED AND outside the
                        // initial warm-up window. The first 15 seconds are
                        // always treated as "still coming up" regardless
                        // of current state, because OpenVPN state goes
                        // DISCONNECTED -> CONNECTING -> ... -> CONNECTED
                        // and we race the state listener on every fresh
                        // poll-loop start. Without the warm-up guard we
                        // would break on iteration 1 before the state
                        // listener even fired once.
                        val warmingUp = System.currentTimeMillis() - loopStart < 15_000L
                        val stillTransient = warmingUp || when (currentProtocol) {
                            VpnProtocol.IPSEC -> {
                                val s = ipSecTunnel?.getState()
                                s == IpSecTunnel.State.CONNECTING ||
                                        s == IpSecTunnel.State.CONNECTED
                            }
                            VpnProtocol.OPENVPN -> {
                                // Symmetric to IPSec: OpenVPN spends the first
                                // ~2-5s of a fresh connect in CONNECTING while
                                // TLS handshake + PUSH_REPLY roundtrip complete.
                                // getStatus().connected is false throughout;
                                // breaking the poll loop here would freeze
                                // uptime at 0 even though the tunnel is about
                                // to go live.
                                val s = openVpnTunnel?.getState()
                                s == OpenVpnTunnel.State.CONNECTING ||
                                        s == OpenVpnTunnel.State.CONNECTED
                            }
                            else -> false
                        }
                        if (!stillTransient) {
                            Log.w(TAG, "Tunnel went down unexpectedly")
                            updateNotification("Disconnected")
                            break
                        }
                    }
                }
                delay(STATUS_POLL_INTERVAL_MS)
            }
        }
    }

    private fun buildNotification(text: String): Notification {
        val openIntent = Intent(this, MainActivity::class.java)
        val pendingOpen = PendingIntent.getActivity(
            this, 0, openIntent,
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT
        )

        val disconnectIntent = Intent(this, PrivycsVpnService::class.java).apply {
            action = ACTION_DISCONNECT
        }
        val pendingDisconnect = PendingIntent.getService(
            this, 1, disconnectIntent,
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT
        )

        return NotificationCompat.Builder(this, PrivycsApp.NOTIFICATION_CHANNEL_VPN)
            .setContentTitle(getString(R.string.vpn_notification_title))
            .setContentText(text)
            .setSmallIcon(android.R.drawable.ic_lock_lock)
            .setContentIntent(pendingOpen)
            .addAction(
                android.R.drawable.ic_menu_close_clear_cancel,
                getString(R.string.action_disconnect),
                pendingDisconnect
            )
            .setOngoing(true)
            .setSilent(true)
            .build()
    }

    private fun updateNotification(text: String) {
        val notification = buildNotification(text)
        val manager = getSystemService(NOTIFICATION_SERVICE) as android.app.NotificationManager
        manager.notify(PrivycsApp.NOTIFICATION_ID_VPN, notification)
    }

    /**
     * Read split tunnel preferences and apply to VpnService.Builder.
     * Call this before establishing the tunnel to configure per-app VPN.
     */
    fun applySplitTunnelSettings(builder: android.net.VpnService.Builder) {
        try {
            val prefs = getSharedPreferences("split_tunnel", Context.MODE_PRIVATE)
            val mode = prefs.getString("mode", "exclude") ?: "exclude"
            val packages = prefs.getStringSet("packages", emptySet()) ?: emptySet()

            if (packages.isEmpty()) {
                Log.d(TAG, "Split tunnel: no apps configured, using default (all apps through VPN)")
                return
            }

            when (mode) {
                "exclude" -> {
                    for (pkg in packages) {
                        try {
                            builder.addDisallowedApplication(pkg)
                            Log.d(TAG, "Split tunnel: excluding $pkg")
                        } catch (e: Exception) {
                            Log.w(TAG, "Split tunnel: cannot exclude $pkg: ${e.message}")
                        }
                    }
                }
                "include" -> {
                    for (pkg in packages) {
                        try {
                            builder.addAllowedApplication(pkg)
                            Log.d(TAG, "Split tunnel: including $pkg")
                        } catch (e: Exception) {
                            Log.w(TAG, "Split tunnel: cannot include $pkg: ${e.message}")
                        }
                    }
                    // Always include ourselves
                    try {
                        builder.addAllowedApplication(packageName)
                    } catch (e: Exception) {
                        Log.w(TAG, "Split tunnel: cannot include self: ${e.message}")
                    }
                }
            }

            Log.d(TAG, "Split tunnel: mode=$mode, apps=${packages.size}")
        } catch (e: Exception) {
            Log.e(TAG, "Failed to apply split tunnel settings", e)
        }
    }

    /**
     * Send a broadcast to update the home screen widget with current VPN status.
     */
    private fun sendWidgetUpdate(connected: Boolean) {
        val uptimeSeconds = if (connected && connectStartTime > 0) {
            (System.currentTimeMillis() - connectStartTime) / 1000
        } else {
            0L
        }

        VpnWidget.sendStatusUpdate(
            context = this,
            connected = connected,
            connectionName = currentConnectionName,
            protocol = currentProtocol?.shortLabel ?: "",
            uptime = uptimeSeconds
        )
    }
}
