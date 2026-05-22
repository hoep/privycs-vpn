package com.privycs.vpn.service

import android.content.Context
import androidx.work.CoroutineWorker
import androidx.work.WorkerParameters
import com.privycs.vpn.PrivycsApp
import com.privycs.vpn.util.AlwaysOnDetector
import com.privycs.vpn.util.ConnectCoordinator
import com.privycs.vpn.util.PrivycsLogger

/**
 * WorkManager-backed backstop for the auto-tunnel engine.
 *
 * Architecture: NetworkMonitor's NetworkCallback remains the
 * primary fast-reaction path inside the app process. This worker
 * is a slow safety net that survives Doze, battery-saver, force-
 * stop, and process death cycles where the in-process scope would
 * otherwise miss events.
 *
 * Schedule: 15-minute periodic, the OS hard floor for periodic
 * work. Backoff exponential from 30s on retry. Constraint:
 * NetworkType.CONNECTED so we don't burn cycles when there is no
 * network to evaluate against.
 *
 * What it does on each invocation:
 *
 *   1. Make sure NetworkMonitor is started (idempotent). After
 *      a process restart NetworkMonitor.start() needs to be
 *      called again to re-register the platform NetworkCallback
 *      and the settings/status flow listeners.
 *
 *   2. Ask NetworkMonitor to re-evaluate now. If COD rules match
 *      and the VPN is down, the evaluator fires a connect intent
 *      through the Coordinator. Pool / connection / no-VPN
 *      branching is handled inside the evaluator already.
 *
 *   3. Pool keepalive fallback: if a pool is the active selection
 *      and the VPN is not currently connected and we are not in
 *      the manual-disconnect cooldown, fire a pool reconnect.
 *      This is the same logic as PoolKeepaliveWatcher.
 *      onAvailable but driven by the WorkManager tick instead of
 *      a NetworkCallback - useful when the tunnel dropped during
 *      Doze and onAvailable never fired.
 *
 * The Coordinator gates duplicate intents (AlreadyConnecting /
 * AlreadyConnected), so racing the worker against the live
 * NetworkCallback is safe.
 */
class AutoTunnelWorker(
    ctx: Context,
    params: WorkerParameters,
) : CoroutineWorker(ctx, params) {

    override suspend fun doWork(): Result {
        return try {
            // Defensive: if PrivycsApp.instance is not yet initialised
            // (extremely rare race; Application.onCreate runs before
            // any worker), bail without retry. Next periodic tick
            // (15 min) will find the instance ready.
            val app = try {
                PrivycsApp.instance
            } catch (_: UninitializedPropertyAccessException) {
                PrivycsLogger.w(TAG, "PrivycsApp.instance not ready, skipping tick")
                return Result.success()
            }

            // 1. Re-arm the NetworkMonitor + force a re-evaluation.
            //    start() is idempotent - returns immediately if
            //    already running. reevaluate() is the public
            //    re-trigger surface used by Settings, Boot, and
            //    now also by us as the periodic backstop.
            // Convert any legacy "simple COD" into rules (idempotent),
            // then gate on whether ANY rule exists — the engine is
            // rule-driven post-conversion.
            app.networkRulesRepository.migrateLegacyCod(app.settingsRepository)
            app.networkRulesRepository.awaitLoaded()
            val codEnabled = app.networkRulesRepository.rules.value.isNotEmpty()
            if (codEnabled) {
                val nm = NetworkMonitor.getInstance(applicationContext)
                nm.start()
                nm.reevaluate()
            }

            // 2. Pool keepalive fallback. Mirrors
            //    PoolKeepaliveWatcher.tryReconnectPool but driven
            //    by the WorkManager tick. Honours the same
            //    manual-disconnect cooldown via AlwaysOnDetector
            //    so a recent tap-Disconnect is not undone by the
            //    next 15-min tick.
            val poolReg = app.poolRepository.registry.value
            val activePoolId = poolReg.activeId
            val activePool = poolReg.pools.firstOrNull { it.id == activePoolId }
            if (activePool != null) {
                val vpnManager = VpnServiceManager.getInstance(applicationContext)
                if (!vpnManager.isConnected && !vpnManager.isConnecting.value &&
                    !AlwaysOnDetector.wasRecentlyManuallyDisconnected(applicationContext, 30_000L)
                ) {
                    PrivycsLogger.i(
                        TAG,
                        "backstop tick: pool '${activePool.name}' active but disconnected, firing reconnect",
                    )
                    ConnectCoordinator.requestPoolConnect(
                        applicationContext,
                        ConnectCoordinator.IntentSource.ON_DEMAND,
                        activePoolId,
                        activePool.name,
                    )
                }
            }

            Result.success()
        } catch (e: Exception) {
            PrivycsLogger.w(TAG, "AutoTunnelWorker failed: ${e.message}")
            // Retry with exponential backoff (30s, 60s, 120s, ...).
            // The next periodic tick will fire regardless after 15
            // min, so retry is just for cases where we want a
            // sooner second attempt (e.g. transient I/O error).
            Result.retry()
        }
    }

    companion object {
        private const val TAG = "AutoTunnelWorker"
    }
}
