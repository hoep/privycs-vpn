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
        // VPN permission revoked by user or system
        Log.w(TAG, "VPN permission revoked")
        handleDisconnect()
        super.onRevoke()
    }

    private fun handleConnect(
        connectionId: String,
        protocolStr: String,
        configContent: String,
        connectionName: String
    ) {
        currentConnectionId = connectionId
        currentConnectionName = connectionName
        currentProtocol = VpnProtocol.fromString(protocolStr)

        scope.launch {
            try {
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

        tunnel.connect(configContent, currentConnectionName, this@PrivycsVpnService)

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
            val connRepo = PrivycsApp.instance.connectionRepository
            val settings = PrivycsApp.instance.settingsRepository.getSettingsBlocking()

            val activeConn = connRepo.getActive()
            if (activeConn == null || !settings.alwaysOn) {
                Log.d(TAG, "No active connection or always-on disabled, stopping")
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
            while (isActive) {
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
                        // Break only on hard DISCONNECTED. IPSec spends the
                        // first ~5-10s of every connect in CONNECTING, during
                        // which getStatus().connected is false. Breaking the
                        // poll loop there meant the loop exited BEFORE the SA
                        // came up, so uptime froze at 0/1s forever. For
                        // WireGuard/OpenVPN tunnel.connect() returns only
                        // after the tunnel is live, so their State is always
                        // CONNECTED by the time the poll starts - checking
                        // the tunnel State instead of status.connected keeps
                        // that fast path intact.
                        val stillTransient = when (currentProtocol) {
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
