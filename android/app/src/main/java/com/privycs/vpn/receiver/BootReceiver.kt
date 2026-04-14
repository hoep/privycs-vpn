package com.privycs.vpn.receiver

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.util.Log
import com.privycs.vpn.PrivycsApp
import com.privycs.vpn.service.NetworkMonitor
import com.privycs.vpn.service.PrivycsVpnService

/**
 * Handles BOOT_COMPLETED broadcast.
 * If connect-on-demand is enabled, starts the NetworkMonitor to evaluate rules.
 * Otherwise, starts VPN connection directly if auto_connect_on_start is enabled.
 */
class BootReceiver : BroadcastReceiver() {

    companion object {
        private const val TAG = "BootReceiver"
    }

    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action != Intent.ACTION_BOOT_COMPLETED &&
            intent.action != "android.intent.action.QUICKBOOT_POWERON"
        ) {
            return
        }

        Log.d(TAG, "Boot completed, checking auto-connect settings")

        try {
            val app = context.applicationContext as PrivycsApp
            val settings = app.settingsRepository.getSettingsBlocking()

            // If connect-on-demand is enabled, start the network monitor
            // which will handle connecting based on network rules
            if (settings.connectOnDemand.enabled) {
                Log.d(TAG, "Connect on demand enabled, starting NetworkMonitor")
                val monitor = NetworkMonitor.getInstance(context)
                monitor.start()
                return
            }

            if (!settings.autoConnectOnStart) {
                Log.d(TAG, "Auto-connect disabled, skipping")
                return
            }

            val activeConn = app.connectionRepository.getActive()
            if (activeConn == null) {
                Log.d(TAG, "No active connection configured, skipping")
                return
            }

            val config = activeConn.getActiveConfig()
            if (config == null) {
                Log.d(TAG, "No config for active protocol, skipping")
                return
            }

            Log.d(TAG, "Auto-connecting to ${activeConn.name} via ${activeConn.activeProtocol.label}")

            val vpnIntent = Intent(context, PrivycsVpnService::class.java).apply {
                action = PrivycsVpnService.ACTION_CONNECT
                putExtra(PrivycsVpnService.EXTRA_CONNECTION_ID, activeConn.id)
                putExtra(PrivycsVpnService.EXTRA_PROTOCOL, activeConn.activeProtocol.name)
                putExtra(PrivycsVpnService.EXTRA_CONFIG_CONTENT, config.configContent)
                putExtra(PrivycsVpnService.EXTRA_CONNECTION_NAME, activeConn.name)
            }
            context.startForegroundService(vpnIntent)
        } catch (e: Exception) {
            Log.e(TAG, "Auto-connect failed", e)
        }
    }
}
