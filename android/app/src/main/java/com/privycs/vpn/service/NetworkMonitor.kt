package com.privycs.vpn.service

import android.content.Context
import android.net.ConnectivityManager
import android.net.Network
import android.net.NetworkCapabilities
import android.net.NetworkRequest
import android.net.wifi.WifiManager
import android.os.Build
import android.util.Log
import com.privycs.vpn.PrivycsApp
import com.privycs.vpn.data.models.ConnectOnDemandSettings
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.launch

data class NetworkState(
    val networkType: String = "none",  // "wifi", "mobile", "ethernet", "none"
    val ssid: String = "",
    val shouldConnect: Boolean = false,
    val ruleMatch: String = ""  // description of why/why not
)

/**
 * Monitors network state changes and evaluates connect-on-demand rules.
 * When rules match, automatically connects or disconnects the VPN.
 *
 * Uses ConnectivityManager.registerDefaultNetworkCallback() for network changes
 * and WifiManager for SSID detection (requires ACCESS_FINE_LOCATION on Android 8+).
 */
class NetworkMonitor private constructor(private val context: Context) {

    companion object {
        private const val TAG = "NetworkMonitor"

        @Volatile
        private var instance: NetworkMonitor? = null

        fun getInstance(context: Context): NetworkMonitor {
            return instance ?: synchronized(this) {
                instance ?: NetworkMonitor(context.applicationContext).also { instance = it }
            }
        }
    }

    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main)
    private val connectivityManager =
        context.getSystemService(Context.CONNECTIVITY_SERVICE) as ConnectivityManager
    private val wifiManager =
        context.applicationContext.getSystemService(Context.WIFI_SERVICE) as WifiManager

    private val _networkState = MutableStateFlow(NetworkState())
    val networkState: StateFlow<NetworkState> = _networkState.asStateFlow()

    private var started = false
    private var networkCallback: ConnectivityManager.NetworkCallback? = null

    /**
     * Start monitoring network changes. Safe to call multiple times.
     */
    fun start() {
        if (started) return
        started = true
        Log.d(TAG, "Starting network monitor")

        val callback = object : ConnectivityManager.NetworkCallback() {
            override fun onAvailable(network: Network) {
                Log.d(TAG, "Network available")
                evaluateCurrentNetwork()
            }

            override fun onLost(network: Network) {
                Log.d(TAG, "Network lost")
                scope.launch {
                    _networkState.value = NetworkState(
                        networkType = "none",
                        ssid = "",
                        shouldConnect = false,
                        ruleMatch = "No network available"
                    )
                }
            }

            override fun onCapabilitiesChanged(
                network: Network,
                networkCapabilities: NetworkCapabilities
            ) {
                Log.d(TAG, "Network capabilities changed")
                evaluateCurrentNetwork()
            }
        }

        networkCallback = callback

        try {
            connectivityManager.registerDefaultNetworkCallback(callback)
        } catch (e: Exception) {
            Log.e(TAG, "Failed to register network callback", e)
        }

        // Evaluate current state immediately
        evaluateCurrentNetwork()
    }

    /**
     * Stop monitoring network changes.
     */
    fun stop() {
        if (!started) return
        started = false
        Log.d(TAG, "Stopping network monitor")

        networkCallback?.let {
            try {
                connectivityManager.unregisterNetworkCallback(it)
            } catch (e: Exception) {
                Log.e(TAG, "Failed to unregister network callback", e)
            }
        }
        networkCallback = null
    }

    /**
     * Evaluate the current network state against connect-on-demand rules
     * and trigger VPN connect/disconnect as needed.
     */
    private fun evaluateCurrentNetwork() {
        scope.launch {
            val settings = PrivycsApp.instance.settingsRepository.settingsFlow.first()
            val codSettings = settings.connectOnDemand

            val networkType = detectNetworkType()
            val ssid = detectCurrentSsid()

            val (shouldConnect, ruleMatch) = evaluateRules(codSettings, networkType, ssid)

            val newState = NetworkState(
                networkType = networkType,
                ssid = ssid,
                shouldConnect = shouldConnect,
                ruleMatch = ruleMatch
            )
            _networkState.value = newState

            if (!codSettings.enabled) {
                Log.d(TAG, "Connect on demand disabled, skipping action")
                return@launch
            }

            val vpnManager = VpnServiceManager.getInstance(context)

            if (shouldConnect && !vpnManager.isConnected) {
                Log.d(TAG, "Rules matched, connecting VPN: $ruleMatch")
                vpnManager.connect()
            } else if (!shouldConnect && vpnManager.isConnected) {
                Log.d(TAG, "Rules no longer match, disconnecting VPN: $ruleMatch")
                vpnManager.disconnect()
            }
        }
    }

    /**
     * Detect the current network type from ConnectivityManager capabilities.
     */
    private fun detectNetworkType(): String {
        val network = connectivityManager.activeNetwork ?: return "none"
        val caps = connectivityManager.getNetworkCapabilities(network) ?: return "none"

        return when {
            caps.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) -> "wifi"
            caps.hasTransport(NetworkCapabilities.TRANSPORT_CELLULAR) -> "mobile"
            caps.hasTransport(NetworkCapabilities.TRANSPORT_ETHERNET) -> "ethernet"
            else -> "none"
        }
    }

    /**
     * Detect the current WiFi SSID.
     * Requires ACCESS_FINE_LOCATION permission on Android 8+.
     * Returns empty string if not on WiFi or SSID cannot be determined.
     */
    @Suppress("DEPRECATION")
    private fun detectCurrentSsid(): String {
        val networkType = detectNetworkType()
        if (networkType != "wifi") return ""

        return try {
            val wifiInfo = wifiManager.connectionInfo
            val ssid = wifiInfo?.ssid ?: return ""
            // Android wraps SSID in quotes
            ssid.removeSurrounding("\"").let { cleaned ->
                if (cleaned == "<unknown ssid>") "" else cleaned
            }
        } catch (e: SecurityException) {
            Log.w(TAG, "No location permission for SSID detection", e)
            ""
        } catch (e: Exception) {
            Log.e(TAG, "Failed to get SSID", e)
            ""
        }
    }

    /**
     * Evaluate connect-on-demand rules against current network state.
     * Returns a pair of (shouldConnect, ruleDescription).
     */
    private fun evaluateRules(
        settings: ConnectOnDemandSettings,
        networkType: String,
        ssid: String
    ): Pair<Boolean, String> {
        if (!settings.enabled) {
            return Pair(false, "Connect on demand is disabled")
        }

        if (networkType == "none") {
            return Pair(false, "No network available")
        }

        // Check if the network type matches the trigger
        val typeMatches = when (settings.trigger) {
            "wifi" -> networkType == "wifi"
            "mobile" -> networkType == "mobile"
            "wifi_mobile" -> networkType == "wifi" || networkType == "mobile"
            else -> false
        }

        if (!typeMatches) {
            val triggerLabel = when (settings.trigger) {
                "wifi" -> "WiFi"
                "mobile" -> "Mobile"
                "wifi_mobile" -> "WiFi or Mobile"
                else -> settings.trigger
            }
            return Pair(false, "Network type \"$networkType\" does not match trigger \"$triggerLabel\"")
        }

        // For non-WiFi networks, type match is sufficient
        if (networkType != "wifi") {
            return Pair(true, "Connected via $networkType (matches trigger)")
        }

        // For WiFi, evaluate SSID rules
        return when (settings.ssidMode) {
            "all" -> {
                Pair(true, "Connected to WiFi" + if (ssid.isNotEmpty()) " \"$ssid\"" else "" + " (all SSIDs)")
            }
            "only" -> {
                if (ssid.isEmpty()) {
                    Pair(false, "Cannot determine SSID (location permission required)")
                } else if (settings.ssidList.any { it.equals(ssid, ignoreCase = true) }) {
                    Pair(true, "WiFi \"$ssid\" is in the allowed list")
                } else {
                    Pair(false, "WiFi \"$ssid\" is not in the allowed list")
                }
            }
            "except" -> {
                if (ssid.isEmpty()) {
                    // Cannot determine SSID, connect to be safe
                    Pair(true, "Cannot determine SSID, connecting (exception list not checked)")
                } else if (settings.ssidList.any { it.equals(ssid, ignoreCase = true) }) {
                    Pair(false, "WiFi \"$ssid\" is in the exception list")
                } else {
                    Pair(true, "WiFi \"$ssid\" is not in the exception list")
                }
            }
            else -> Pair(false, "Unknown SSID mode: ${settings.ssidMode}")
        }
    }
}
