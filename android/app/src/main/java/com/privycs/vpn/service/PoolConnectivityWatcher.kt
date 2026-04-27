package com.privycs.vpn.service

import android.content.Context
import android.net.ConnectivityManager
import android.net.Network
import android.net.NetworkCapabilities
import android.net.NetworkRequest
import android.util.Log

/**
 * ConnectivityManager.NetworkCallback wrapper that fires
 * "opportunistic rotation" hints when the underlying network
 * changes (WiFi switch, cell tower roam, mobile↔WiFi handover).
 *
 * Rationale: a network change already invalidates DNS caches +
 * forces TCP re-handshakes. Rotating the pool member at this
 * moment is essentially "free" from a user-disruption POV —
 * the disruption was going to happen anyway. Letting it coincide
 * with a planned rotation means the user sees ONE perceived
 * outage instead of TWO (network change, then 5min later
 * scheduled rotation).
 *
 * Battery effect: positive — fewer scheduled-rotation alarms
 * fire because each network-change rotation effectively resets
 * the rotation timer for free.
 *
 * Constraint: only fires for VPN-DEFAULT-NETWORK changes (the
 * underlying transport that the VPN uses). The VPN tunnel
 * itself coming up/down is NOT an opportunistic-rotation event;
 * it's the consequence of one.
 */
class PoolConnectivityWatcher(
    private val context: Context,
    private val onOpportunisticRotation: () -> Unit
) {

    private val cm = context.getSystemService(Context.CONNECTIVITY_SERVICE) as ConnectivityManager
    private var callback: ConnectivityManager.NetworkCallback? = null
    private var lastNetworkHandle: Long = -1L

    /**
     * Starts watching. onOpportunisticRotation fires when the
     * VPN's underlying transport changes (different WiFi SSID,
     * mobile↔WiFi swap, etc.).
     */
    fun start() {
        if (callback != null) return

        val request = NetworkRequest.Builder()
            .addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
            // NOT_VPN: we want underlying transports (WiFi, mobile),
            // not the tunnel we just made on top.
            .addCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN)
            .build()

        callback = object : ConnectivityManager.NetworkCallback() {
            override fun onAvailable(network: Network) {
                val handle = network.networkHandle
                if (lastNetworkHandle != -1L && lastNetworkHandle != handle) {
                    // Debounce: a real WiFi-roam fires multiple
                    // onAvailable events in quick succession (mobile
                    // up, then wifi up, then wifi-roam). Only fire
                    // ONE rotation hint per debounce window.
                    val now = System.currentTimeMillis()
                    if (now - lastRotationFiredAt > DEBOUNCE_MS) {
                        Log.i(TAG, "underlying network changed - firing opportunistic rotation hint")
                        lastRotationFiredAt = now
                        onOpportunisticRotation()
                    } else {
                        Log.d(TAG, "network change suppressed by debounce window")
                    }
                }
                lastNetworkHandle = handle
            }

            override fun onLost(network: Network) {
                Log.d(TAG, "network lost: ${network.networkHandle}")
            }
        }

        try {
            cm.registerNetworkCallback(request, callback!!)
        } catch (e: SecurityException) {
            Log.w(TAG, "network callback register failed: ${e.message}")
            callback = null
        }
    }

    private var lastRotationFiredAt: Long = 0L

    fun stop() {
        callback?.let {
            try {
                cm.unregisterNetworkCallback(it)
            } catch (e: Exception) {
                Log.d(TAG, "unregister: ${e.message}")
            }
        }
        callback = null
        lastNetworkHandle = -1L
    }

    companion object {
        private const val TAG = "PoolConnWatcher"
        // Debounce window: a typical WiFi-roam settles within
        // 2-3 seconds. 5s leaves a safety margin without making
        // genuinely-new transitions feel laggy to the rotator.
        private const val DEBOUNCE_MS = 5_000L
    }
}
