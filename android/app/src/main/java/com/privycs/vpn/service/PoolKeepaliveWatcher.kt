package com.privycs.vpn.service

import android.content.Context
import android.net.ConnectivityManager
import android.net.Network
import android.net.NetworkCapabilities
import android.net.NetworkRequest
import android.util.Log
import com.privycs.vpn.PrivycsApp
import com.privycs.vpn.util.AlwaysOnDetector
import com.privycs.vpn.util.ConnectCoordinator
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch

/**
 * Process-lifetime watcher that auto-reconnects the active pool
 * after the underlying physical network returns. Closes the
 * "pool stopped overnight" hole:
 *
 *   - Phone goes into Doze, deep idle, or moves out of network
 *     range. The pool tunnel drops, all members eventually get
 *     marked unreachable as the rotation scheduler keeps trying
 *     against a dead network.
 *   - Phone wakes / network returns. NetworkMonitor's
 *     ConnectivityManager callback fires ON the underlying default
 *     network's onAvailable (NET_CAPABILITY_NOT_VPN filter so we
 *     do NOT self-trigger from our own VPN tunnel coming up).
 *   - Watcher fires a fresh ON_DEMAND-source pool connect through
 *     the Coordinator. The connect path runs eligibleMembers
 *     which, with connectivity now plausible, clears the all-
 *     unreachable flags and retries.
 *
 * Independent of NetworkMonitor (COD-driven) because the user's
 * intent of "pool selected = stay-on-pool" should hold even when
 * COD is disabled. Coordinator gates handle the dedupe with COD's
 * own connect intent.
 *
 * Cheap to run: a single registered NetworkCallback, no polling,
 * fires only on actual network transitions. The 30s manual-
 * disconnect cooldown still applies via AlwaysOnDetector so a
 * user who explicitly tapped Disconnect is not overridden.
 */
object PoolKeepaliveWatcher {

    private const val TAG = "PoolKeepaliveWatcher"

    // Brief settle delay before firing the connect intent. Lets
    // the new network finish DHCP / DNS-config / route install so
    // the WireGuard handshake / IPSec IKE_SA does not race the
    // network's own bring-up. 1.5s mirrors the desktop side's
    // post-Activate settle delay.
    private const val NETWORK_SETTLE_MS = 1500L

    @Volatile
    private var started = false

    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main)

    private var callback: ConnectivityManager.NetworkCallback? = null

    /**
     * Idempotent: subsequent calls are no-ops if the watcher is
     * already registered. Called from PrivycsApp.onCreate so the
     * watcher is up the moment the process starts, including after
     * a Doze-induced process restart.
     */
    fun start(context: Context) {
        synchronized(this) {
            if (started) return
            started = true
        }

        val cm = context.getSystemService(Context.CONNECTIVITY_SERVICE)
            as ConnectivityManager

        val cb = object : ConnectivityManager.NetworkCallback() {
            override fun onAvailable(network: Network) {
                Log.d(TAG, "non-VPN network available: $network - checking pool state")
                scope.launch {
                    delay(NETWORK_SETTLE_MS)
                    tryReconnectPool(context)
                }
            }
        }

        try {
            val req = NetworkRequest.Builder()
                .addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
                .addCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN)
                .build()
            cm.registerNetworkCallback(req, cb)
            callback = cb
            Log.i(TAG, "pool keepalive watcher registered")
        } catch (e: Exception) {
            // Failure here means we lose the pool-recovery feature
            // but the rest of the app still works. Reset started
            // so a future retry can attempt re-registration.
            Log.e(TAG, "Failed to register network callback", e)
            synchronized(this) { started = false }
        }
    }

    private suspend fun tryReconnectPool(context: Context) {
        val app = PrivycsApp.instance
        val poolReg = app.poolRepository.registry.value
        val activePoolId = poolReg.activeId
        if (activePoolId.isEmpty()) {
            // No pool selected - nothing to keepalive. Return
            // silently; the watcher fires on every non-VPN
            // onAvailable so logging would be noisy.
            return
        }
        val activePool = poolReg.pools.firstOrNull { it.id == activePoolId }
        if (activePool == null) {
            Log.w(TAG, "active pool id=$activePoolId not found in registry, skipping")
            return
        }

        val vpnManager = VpnServiceManager.getInstance(context)
        if (vpnManager.isConnected || vpnManager.isConnecting.value) {
            // Already up or in flight. The Coordinator would gate
            // this anyway as AlreadyConnected / AlreadyConnecting
            // but bailing here saves the round-trip + log noise.
            return
        }

        // Honour user intent: if they tapped Disconnect within the
        // last 30s, do NOT auto-reconnect. Lets a "I just want VPN
        // off for a minute" gesture stick instead of being undone
        // by the next onAvailable tick.
        if (AlwaysOnDetector.wasRecentlyManuallyDisconnected(context, 30_000L)) {
            Log.d(TAG, "skip: manual disconnect within 30s cooldown")
            return
        }

        Log.i(TAG, "pool '${activePool.name}' not connected after network restore - firing reconnect")
        try {
            ConnectCoordinator.requestPoolConnect(
                context,
                ConnectCoordinator.IntentSource.ON_DEMAND,
                activePoolId,
                activePool.name,
            )
        } catch (e: Exception) {
            Log.e(TAG, "requestPoolConnect failed", e)
        }
    }
}
