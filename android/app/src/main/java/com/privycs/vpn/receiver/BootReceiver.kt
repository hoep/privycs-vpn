package com.privycs.vpn.receiver

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.util.Log
import com.privycs.vpn.PrivycsApp
import com.privycs.vpn.service.NetworkMonitor
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.launch

/**
 * Handles BOOT_COMPLETED broadcast.
 * If connect-on-demand is enabled, starts the NetworkMonitor to evaluate rules.
 * Otherwise, starts VPN connection directly if auto_connect_on_start is enabled.
 *
 * Uses goAsync() so the receiver returns immediately while the
 * settings-read + coordinator dispatch run on a coroutine. The
 * earlier runBlocking risked ANR if DataStore was slow on first
 * boot after factory reset (cold disk cache + competing I/O from
 * the OS boot wave).
 */
class BootReceiver : BroadcastReceiver() {

    companion object {
        private const val TAG = "BootReceiver"
        // Receiver-scoped scope (process-lifetime). Boot work is
        // tied to the process being alive long enough for the work
        // to complete; goAsync's PendingResult holds the receiver
        // open up to ~30s.
        private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    }

    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action != Intent.ACTION_BOOT_COMPLETED &&
            intent.action != "android.intent.action.QUICKBOOT_POWERON"
        ) {
            return
        }

        Log.d(TAG, "Boot completed, checking auto-connect settings")

        // goAsync extends the receiver's lifetime so we can do
        // async work without blocking the broadcast dispatch thread.
        // PendingResult MUST be finish()ed in every code path or
        // the system holds the receiver alive forever.
        val pending = goAsync()
        scope.launch {
            try {
                val app = context.applicationContext as PrivycsApp
                val settings = app.settingsRepository.getSettingsBlocking()

                if (settings.connectOnDemand.enabled) {
                    Log.d(TAG, "Connect on demand enabled, starting NetworkMonitor")
                    val monitor = NetworkMonitor.getInstance(context)
                    monitor.start()
                    return@launch
                }

                if (!settings.autoConnectOnStart) {
                    Log.d(TAG, "Auto-connect disabled, skipping")
                    return@launch
                }

                val activeConn = app.connectionRepository.getActive()
                if (activeConn == null) {
                    Log.d(TAG, "No active connection configured, skipping")
                    return@launch
                }

                val config = activeConn.getActiveConfig()
                if (config == null) {
                    Log.d(TAG, "No config for active protocol, skipping")
                    return@launch
                }

                Log.d(TAG, "Auto-connecting to ${activeConn.name} via ${activeConn.activeProtocol.label}")

                // Route through coordinator so if System Always-On also
                // wakes our service at boot with its null-intent path,
                // whichever arrives first wins and the other yields -
                // preventing the boot-time double-tunnel race we saw in
                // v0.9.3.10..12.
                com.privycs.vpn.util.ConnectCoordinator.requestConnect(
                    context,
                    com.privycs.vpn.util.ConnectCoordinator.IntentSource.BOOT,
                    activeConn,
                )
            } catch (e: Exception) {
                Log.e(TAG, "Auto-connect failed", e)
            } finally {
                pending.finish()
            }
        }
    }
}
