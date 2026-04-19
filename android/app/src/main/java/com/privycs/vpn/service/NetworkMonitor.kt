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
import com.privycs.vpn.util.AlwaysOnDetector
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.flow.map
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

    // Edge-trigger state. Only used on the DISCONNECT side so that a
    // flipping rule does not keep issuing redundant disconnect calls while
    // the VPN is already torn down. The connect side intentionally does
    // NOT require a transition: if on-demand rules match and the VPN is
    // off, we connect immediately, even 2 seconds after the user manually
    // disconnected. That is the whole point of on-demand - the "VPN will
    // connect" banner has to be true, not aspirational.
    private var lastShouldConnect: Boolean? = null

    /**
     * Start monitoring network changes. Safe to call multiple times.
     */
    fun start() {
        if (started) return
        started = true
        Log.d(TAG, "Starting network monitor")

        // Re-evaluate when on-demand settings change (SSID list, mode,
        // trigger). Without this the user's rule edits only take effect on
        // the next spontaneous network event.
        scope.launch {
            PrivycsApp.instance.settingsRepository.settingsFlow
                .map { it.connectOnDemand }
                .distinctUntilChanged()
                .collect {
                    Log.d(TAG, "Connect-on-demand settings changed, re-evaluating")
                    // Force edge transition on settings change so a rule flip
                    // is applied immediately even if the network state is
                    // unchanged.
                    lastShouldConnect = null
                    evaluateCurrentNetwork()
                }
        }

        // Re-evaluate whenever the VPN goes DOWN (connected transitions
        // from true to false). This handles the manual-disconnect case
        // directly: without it we relied solely on Android's
        // NetworkCallback which does not always fire on a VPN-only
        // teardown (the underlying WiFi/mobile network did not change).
        // Result of the old behaviour: user taps Disconnect while
        // on-demand rules match, VPN stays down because no network event
        // triggered a re-evaluation. Now: VpnStatus drops connected=false
        // -> we run evaluateCurrentNetwork() -> shouldConnect=true,
        // !vpnManager.isConnected -> connect kicks in within ~100ms.
        scope.launch {
            var wasConnected = false
            VpnServiceManager.getInstance(context).status.collect { status ->
                if (wasConnected && !status.connected) {
                    Log.d(TAG, "VPN transitioned to disconnected, re-evaluating on-demand rules")
                    lastShouldConnect = null // force edge so connect branch fires
                    evaluateCurrentNetwork()
                }
                wasConnected = status.connected
            }
        }

        val callback = object : ConnectivityManager.NetworkCallback() {
            override fun onAvailable(network: Network) {
                Log.d(TAG, "Network available: $network")
                evaluateCurrentNetwork()
            }

            override fun onLost(network: Network) {
                // Do NOT hard-code "no network" here. Android frequently calls
                // onLost for the outgoing default network a few milliseconds
                // BEFORE onAvailable fires for the new default (typical on
                // WiFi→Mobile handover). Hard-coding "none" created a window
                // where the UI showed "No Network" and the auto-connect
                // evaluator saw state=none, tore down the VPN, and only then
                // noticed the new network had arrived.
                //
                // Re-evaluate based on whatever the ConnectivityManager says
                // is the current active network. If it is truly gone
                // detectNetworkType() will return "none" naturally.
                Log.d(TAG, "Network lost: $network — re-evaluating current state")
                evaluateCurrentNetwork()
            }

            override fun onCapabilitiesChanged(
                network: Network,
                networkCapabilities: NetworkCapabilities
            ) {
                Log.d(TAG, "Network capabilities changed: $network")
                evaluateCurrentNetwork()
            }

            override fun onLinkPropertiesChanged(
                network: Network,
                linkProperties: android.net.LinkProperties
            ) {
                // IP address / DNS / route change on the current default
                // network. Still the same transport, but can influence SSID
                // detection on some devices (e.g. WiFi reassociation).
                Log.d(TAG, "Network link properties changed: $network")
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
                lastShouldConnect = null
                return@launch
            }

            val vpnManager = VpnServiceManager.getInstance(context)
            val prev = lastShouldConnect
            lastShouldConnect = shouldConnect

            // CONNECT side: on-demand is authoritative. If the rules match
            // and the VPN is off, bring it up - no matter whether
            // shouldConnect just flipped or was already `true` on the last
            // evaluation. This is intentional: a user who manually taps
            // Disconnect while on-demand is enabled and the current
            // network satisfies the rules sees the tunnel come back up
            // within one NetworkCallback tick. On-demand only does
            // anything USEFUL if it can override stale user intent - the
            // "VPN will connect" banner has to actually connect.
            //
            // DISCONNECT side keeps the edge-trigger (only act on
            // transition from match -> no-match) so we do not fight an
            // already-down tunnel with spurious disconnect calls while
            // rules consistently fail to match. prevState comparison above
            // handled the network-transition case; here we just suppress
            // noise.
            val transitioned = prev == null || prev != shouldConnect

            if (shouldConnect && !vpnManager.isConnected && !vpnManager.isConnecting.value) {
                // Cooldown gate: do NOT fire on-demand reconnect while
                // an in-flight OS-initiated service teardown is still
                // running. Without this, connecting here spawns a new
                // GoBackend on /dev/tun while the old one is still
                // writing to the same fd; observed symptoms were
                // "Failed to write packet to TUN device: input/output
                // error", keepalive storm (~15/s vs ~0.04/s normal)
                // and the UI flipping connect/disconnect indefinitely
                // when the user turned off Always-On in system
                // settings. AlwaysOnDetector.stampSystemRevoke() is
                // called from PrivycsVpnService.onRevoke() so this
                // window opens the moment the OS tells us to tear
                // down.
                if (AlwaysOnDetector.isInSystemRevokeCooldown(context)) {
                    Log.d(TAG, "Skipping auto-connect: in system-revoke cooldown window")
                    return@launch
                }
                // Guard against issuing overlapping connect calls while a
                // previous attempt is still in-flight. Android's
                // ConnectivityManager.NetworkCallback fires several
                // events per second during a WiFi association (onAvailable
                // + onCapabilitiesChanged + onLinkPropertiesChanged + ...);
                // each would call evaluateCurrentNetwork and, without this
                // check, each would start another connect before the first
                // one even finishes. PrivycsVpnService is not reentrant -
                // a second handleConnect while the first is mid-flight
                // double-starts the native tunnel.
                Log.d(TAG, "Rules match and VPN is off, connecting: $ruleMatch")
                vpnManager.connect()
            } else if (!shouldConnect && vpnManager.isConnected && transitioned) {
                Log.d(TAG, "Rules transitioned to no-match, disconnecting VPN: $ruleMatch")
                vpnManager.disconnect()
            } else if (transitioned) {
                Log.d(TAG, "Rules transitioned but already in desired state: $ruleMatch")
            }
        }
    }

    /**
     * Kept as no-ops: older ConnectScreen builds call these when the user
     * taps the Connect / Disconnect button. They previously latched an
     * `userIntentDisconnected` flag that suppressed on-demand reconnect.
     * The flag was removed in favour of "on-demand is authoritative when
     * enabled" - the UI calls are still here so this class stays
     * binary-compatible with the existing ConnectScreen.
     */
    @Suppress("unused") fun onUserDisconnect() { /* no-op */ }
    @Suppress("unused") fun onUserConnect() { /* no-op */ }

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
                // Explicit parentheses: the previous version had a Kotlin operator-precedence
                // bug where " (all SSIDs)" was only appended when ssid was empty.
                val ssidPart = if (ssid.isNotEmpty()) " \"$ssid\"" else ""
                Pair(true, "Connected to WiFi$ssidPart (all SSIDs)")
            }
            "only" -> {
                val list = settings.ssidList.filter { it.isNotBlank() }
                when {
                    list.isEmpty() ->
                        Pair(false, "Only-SSIDs list is empty — add at least one SSID or switch to All SSIDs")
                    ssid.isEmpty() ->
                        Pair(false, "Cannot determine SSID (grant Location permission to detect WiFi name)")
                    list.any { it.equals(ssid, ignoreCase = true) } ->
                        Pair(true, "WiFi \"$ssid\" matches the allowed list")
                    else ->
                        Pair(false, "WiFi \"$ssid\" is not in the allowed list")
                }
            }
            "except" -> {
                val list = settings.ssidList.filter { it.isNotBlank() }
                when {
                    list.isEmpty() ->
                        // No exceptions configured = behave like "all"
                        Pair(true, "Connected to WiFi" + if (ssid.isNotEmpty()) " \"$ssid\"" else "" + " (no exceptions)")
                    ssid.isEmpty() ->
                        // Cannot determine SSID — default to connect so the user
                        // is not stranded without VPN on a new WiFi.
                        Pair(true, "Cannot determine SSID, connecting (except-list not checked)")
                    list.any { it.equals(ssid, ignoreCase = true) } ->
                        Pair(false, "WiFi \"$ssid\" is in the exception list")
                    else ->
                        Pair(true, "WiFi \"$ssid\" is not in the exception list")
                }
            }
            else -> Pair(false, "Unknown SSID mode: ${settings.ssidMode}")
        }
    }

    /**
     * Force a re-evaluation from outside. Call this after the user changes
     * connect-on-demand settings so the state/rule-match text and the auto
     * connect decision refresh immediately instead of waiting for the next
     * network event.
     */
    fun reevaluate() {
        Log.d(TAG, "Manual re-evaluate requested")
        evaluateCurrentNetwork()
    }
}
