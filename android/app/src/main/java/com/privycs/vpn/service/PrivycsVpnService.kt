package com.privycs.vpn.service

import android.app.Notification
import android.app.PendingIntent
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
    private var currentConnectionName: String = ""
    private var currentConnectionId: String = ""
    private var currentProtocol: VpnProtocol? = null

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
                    VpnProtocol.OPENVPN -> {
                        Log.w(TAG, "OpenVPN not yet implemented on Android")
                        updateNotification("OpenVPN support coming soon")
                    }
                    VpnProtocol.IPSEC -> {
                        Log.w(TAG, "IPSec not yet implemented on Android")
                        updateNotification("IPSec support coming soon")
                    }
                    null -> {
                        Log.e(TAG, "Unknown protocol: $protocolStr")
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

        updateNotification("Connected to $currentConnectionName")
        startStatusPolling()
    }

    private fun handleDisconnect() {
        scope.launch {
            try {
                wireGuardTunnel?.disconnect()
                wireGuardTunnel = null
            } catch (e: Exception) {
                Log.e(TAG, "Error during disconnect", e)
            }

            val manager = VpnServiceManager.getInstance(this@PrivycsVpnService)
            manager.updateStatus(VpnStatus())

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
     */
    private fun startStatusPolling() {
        scope.launch {
            val manager = VpnServiceManager.getInstance(this@PrivycsVpnService)
            while (isActive) {
                val tunnel = wireGuardTunnel
                if (tunnel != null) {
                    val status = tunnel.getStatus(currentConnectionName, currentConnectionId)
                    manager.updateStatus(status)

                    if (!status.connected) {
                        Log.w(TAG, "Tunnel went down unexpectedly")
                        updateNotification("Disconnected")
                        break
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
}
