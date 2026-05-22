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

                // Convert any legacy "simple COD" into network rules
                // (idempotent), then gate on whether ANY rule exists —
                // the engine is rule-driven post-conversion.
                app.networkRulesRepository.migrateLegacyCod(app.settingsRepository)
                app.networkRulesRepository.awaitLoaded()
                val codConfigured = app.networkRulesRepository.rules.value.isNotEmpty()

                // Always start the NetworkMonitor when rules exist so
                // ongoing rule-based connects/disconnects work after
                // boot. Independent of autoConnectOnStart - the
                // monitor's job is the long-lived lifecycle, the
                // boot intent below is a one-shot kick.
                if (codConfigured) {
                    Log.d(TAG, "Network rules configured, starting NetworkMonitor")
                    NetworkMonitor.getInstance(context).start()
                    // v0.9.14.97: also register the process-death-surviving
                    // PendingIntent NetworkCallback so OEM battery-killer
                    // terminations don't strand us with a dead runtime
                    // callback until the user opens the app.
                    try {
                        com.privycs.vpn.util.CodWakeRegistrar.register(context)
                    } catch (t: Throwable) {
                        Log.e(TAG, "CodWakeRegistrar registration failed", t)
                    }
                }

                if (!settings.autoConnectOnStart) {
                    Log.d(TAG, "Auto-connect at boot disabled, no boot intent fired")
                    return@launch
                }

                // COD-rule gate. When the user has BOTH
                // autoConnectOnStart AND connectOnDemand enabled,
                // they expect boot connect ONLY when COD rules
                // currently match. Without this check the BOOT
                // intent would fire on every reboot regardless of
                // SSID / network-type filter, defeating the whole
                // point of "connect only on trusted/untrusted
                // networks". Pre-fix the BootReceiver had a hard
                // early-return on COD-enabled, which leaked the
                // opposite bug (no boot connect even when rules
                // matched). The right answer is: rules-aware gate.
                //
                // evaluateRulesNow() is synchronous, no intent
                // firing, pure read of detectNetworkType() +
                // detectCurrentSsid(). Returns true if a connect
                // SHOULD fire right now.
                if (codConfigured) {
                    val shouldConnect = NetworkMonitor.getInstance(context)
                        .evaluateRulesNow()
                    if (!shouldConnect) {
                        Log.d(TAG, "Boot auto-connect suppressed: network rules do not match current network")
                        return@launch
                    }
                    Log.d(TAG, "Boot auto-connect: network rules match, proceeding")
                }

                // Pool-active wins. Without this branch boot-time
                // auto-connect would silently no-op for pool users
                // (getActive() returns null when a pool owns the
                // user-selected slot). Coordinator's BOOT-source
                // gate set is identical for pool and single, so
                // boot-time double-tunnel races against System
                // Always-On stay prevented.
                val poolReg = app.poolRepository.registry.value
                val activePoolId = poolReg.activeId
                val activePool = poolReg.pools.firstOrNull { it.id == activePoolId }
                if (activePoolId.isNotEmpty() && activePool != null) {
                    Log.d(TAG, "Auto-connecting to pool ${activePool.name}")
                    com.privycs.vpn.util.ConnectCoordinator.requestPoolConnect(
                        context,
                        com.privycs.vpn.util.ConnectCoordinator.IntentSource.BOOT,
                        activePoolId,
                        activePool.name,
                    )
                    return@launch
                }

                val activeConn = app.connectionRepository.getActive()
                if (activeConn == null) {
                    Log.d(TAG, "No active connection or pool configured, skipping")
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
