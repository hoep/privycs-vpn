package com.privycs.vpn.service

import android.content.Context
import android.net.ConnectivityManager
import android.net.Network
import android.net.NetworkCapabilities
import android.net.NetworkRequest
import android.net.wifi.WifiManager
import android.os.Build
import com.privycs.vpn.PrivycsApp
import com.privycs.vpn.data.models.RuleMatchType
import com.privycs.vpn.data.models.RuleResolution
import com.privycs.vpn.util.AlwaysOnDetector
import com.privycs.vpn.util.PrivycsLogger
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.flow.map
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
 * Callback strategy (v0.9.14.71):
 *   - Android 12+ (API 31+): registerSystemDefaultNetworkCallback()
 *     fires for the SYSTEM physical default, which stays the
 *     underlying WiFi/Cellular network even while a VPN tunnel is
 *     active. This is what tells us about WiFi↔Mobile handovers
 *     under an active VPN.
 *   - Android <12: registerNetworkCallback(NetworkRequest) with
 *     NET_CAPABILITY_NOT_VPN. Same idea, just the older API. We
 *     deliberately use registerNetworkCallback (observation-only),
 *     NOT requestNetwork (would actively keep the network up).
 *
 * Pre-v0.9.14.71 we used registerDefaultNetworkCallback which
 * silently switches to "the VPN itself" once the tunnel comes up,
 * making all underlying transport changes invisible to us. User-
 * reported as "WiFi → Mobile handover doesn't trigger on-demand."
 *
 * SSID detection (v0.9.14.71): WifiManager.connectionInfo is
 * unreliable once VPN is active on Android 10+ (returns
 * "<unknown ssid>" or empty). We prefer NetworkCapabilities
 * .transportInfo (which carries the real WifiInfo for the underlying
 * network) on API 29+, fall back to wifiManager.connectionInfo on
 * older releases. Sticky-cache lastResolvedSsid is still in place
 * as a third layer.
 */
class NetworkMonitor private constructor(private val context: Context) {

    companion object {
        private const val TAG = "NetworkMonitor"

        /**
         * How long after a manual user-disconnect the on-demand
         * monitor suppresses auto-reconnect attempts. 30 seconds is
         * enough that the user's intent ("I want to be off VPN
         * right now") is honoured for a meaningful window without
         * permanently disabling on-demand: a network-change event
         * after the cooldown re-enables auto-connect on the next
         * matching rule transition.
         */
        private const val MANUAL_DISCONNECT_COOLDOWN_MS = 30_000L

        /**
         * Short cooldown applied AFTER an on-demand-triggered
         * disconnect to suppress an immediate same-source reconnect.
         * Bridges the SSID-cache-clear-during-teardown race: while
         * the VPN tunnel is being torn down, Android fires a series
         * of NetworkCapabilities events (VPN transport going away,
         * underlying physical transport reappearing). One of those
         * events can flip networkType transiently, which clears
         * lastResolvedSsid (see line 421). The next evaluate() then
         * has ssid="" + ssidMode="except", which the legacy COD
         * logic defaults to shouldConnect=true ("Cannot determine
         * SSID, connecting"). That fires a reconnect we just
         * disconnected from — loop until the SSID stabilises.
         *
         * 5 s is enough for: teardown (~2 s) + Network-Capabilities
         * settle (~1-2 s) + buffer. If the rule still says
         * "disconnect" after the cooldown, no reconnect happens.
         * If the user genuinely moved networks during those 5 s,
         * the next callback after the cooldown picks up the new
         * SSID correctly.
         */
        private const val ON_DEMAND_DISCONNECT_COOLDOWN_MS = 5_000L

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

    // v0.9.14.88: extra-wakeup BroadcastReceiver registered at
    // runtime. Fires on Intent.ACTION_SCREEN_ON (instant reaction
    // when user wakes the display) and CONNECTIVITY_ACTION (legacy
    // connectivity-change broadcast that often beats the modern
    // NetworkCallback by a few seconds, especially during the OS's
    // captive-portal-validation phase). Unregistered in stop().
    private var wakeReceiver: android.content.BroadcastReceiver? = null

    // v0.9.15.70 — Callback-driven SSID state machine. On Android 12+
    // the OS REDACTS WifiInfo.ssid in every poll-style API
    // (WifiManager.connectionInfo, ConnectivityManager.getNetwork-
    // Capabilities) when the app is in the background, EVEN with
    // ACCESS_BACKGROUND_LOCATION granted. The unredacted SSID is
    // delivered exclusively through a NetworkCallback registered with
    // FLAG_INCLUDE_LOCATION_INFO. We therefore make the callback the
    // single source of truth:
    //   - currentWifiNetwork: latched in onAvailable() when the new
    //     Network has TRANSPORT_WIFI && !TRANSPORT_VPN, cleared in
    //     onLost() when it matches — so a stale SSID can never carry
    //     over into a wrong rule decision after leaving a WiFi.
    //   - currentWifiSsid: written from caps.transportInfo in
    //     onCapabilitiesChanged() (unredacted via the flag); cleared
    //     together with currentWifiNetwork.
    // detectCurrentSsid() reads currentWifiSsid synchronously (no
    // polling, no "returned empty" attempts). A foreground fallback
    // via WifiManager.connectionInfo is used only when the latch is
    // empty AND the app is currently visible (e.g. first run, before
    // any callback has fired).
    @Volatile
    private var currentWifiNetwork: Network? = null
    @Volatile
    private var currentWifiSsid: String = ""

    // v0.9.14.71 fix: how many consecutive evaluator-skip cycles we
    // tolerate when SSID detection comes back empty AND no cached
    // SSID is available. After the threshold we force-evaluate
    // anyway, accepting that we'll evaluate the rules with ssid=""
    // — better than staying silently passive forever, which the
    // earlier code did in the "VPN up + no cache" sticky-skip path.
    // Three skips ≈ 90 s with the 30 s in-process tick, plenty of
    // time for SSID resolution to recover after a VPN-up flurry.
    private var indeterminateSkipCount: Int = 0
    private val maxIndeterminateSkips: Int = 3

    // Tracks the last applied rule resolution so the engine fires
    // its applier only on transition (= resolved target changed),
    // not on every tick. Without this guard the rules engine would
    // re-apply the same rule on every NetworkCallback event, which
    // for action=POOL/CONNECTION drives switchActivePool /
    // switchActiveConnection / requestDisconnect in a ping-pong
    // loop because tunnel-up itself produces a new network event.
    // Empty string = no rule currently in effect.
    private var lastRuleKey: String = ""

    // Fix 3 (v0.9.15.68) — single-consumer evaluation pipeline.
    // Every trigger (NetworkCallback x4, wake receiver, 10 s tick,
    // settings flow, VPN-status flow) used to scope.launch its own
    // evaluateCurrentNetwork() coroutine. On Dispatchers.Main they
    // don't run in parallel but DO interleave at suspend points
    // (rule resolve(), requestConnect/Disconnect), racing
    // lastRuleKey / indeterminateSkipCount / the
    // SSID cache → sporadic wrong decisions. A CONFLATED channel +
    // one consumer collapses a callback burst into a single, ordered
    // evaluation with the latest settled state. The channel is never
    // closed (singleton, restart-safe): on stop() the consumer drops
    // ticks via the `started` check and resumes on the next start().
    private val evalTrigger = Channel<Unit>(Channel.CONFLATED)
    @Volatile
    private var evalConsumerStarted = false

    /**
     * Start monitoring network changes. Safe to call multiple times.
     */
    fun start() {
        if (started) return
        started = true
        PrivycsLogger.d(TAG, "Starting network monitor")

        // Re-evaluate whenever the rule list changes — the user adds /
        // edits / deletes / reorders a rule, or the COD→rules migration
        // runs. Without this a rule edit only takes effect on the next
        // spontaneous network event.
        scope.launch {
            // rules is a StateFlow — already conflated + distinct, so
            // no distinctUntilChanged() (which the coroutines lib flags
            // as an error on a StateFlow).
            PrivycsApp.instance.networkRulesRepository.rules
                .collect {
                    PrivycsLogger.d(TAG, "Network rules changed, re-evaluating")
                    // Force a transition so an edited rule is applied
                    // immediately even if the network state is unchanged.
                    lastRuleKey = ""
                    // Editing a rule is an explicit user intent in its
                    // own right; a stale "I tapped Disconnect 5 s ago"
                    // stamp should not suppress the evaluation that runs
                    // as a direct consequence of the new rule.
                    AlwaysOnDetector.clearUserDisconnectStamp(context)
                    evaluateCurrentNetwork()
                }
        }

        // v1.0.5.10: Re-evaluate whenever the Auto-tunnel master toggle
        // (settings.networkRulesEnabled) flips. Without this, toggling
        // master OFF→ON did not apply any matching rule until the next
        // spontaneous network event (up to 10 s via the backstop tick).
        // The user-perceived bug: "I just turned Auto-tunnel ON but
        // nothing happened." Now master-toggle changes fire the eval
        // pipeline within the same 300 ms debounce as other triggers.
        // We map the settingsFlow to just the boolean and apply
        // distinctUntilChanged so other settings changes (gateway URL,
        // theme, etc.) don't spuriously re-fire the engine.
        scope.launch {
            PrivycsApp.instance.settingsRepository.settingsFlow
                .map { it.networkRulesEnabled }
                .distinctUntilChanged()
                .collect { enabled ->
                    PrivycsLogger.d(
                        TAG,
                        "Master toggle changed: networkRulesEnabled=$enabled, re-evaluating",
                    )
                    lastRuleKey = ""
                    AlwaysOnDetector.clearUserDisconnectStamp(context)
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
                    PrivycsLogger.d(TAG, "VPN transitioned to disconnected, re-evaluating rules")
                    // Force a rule re-apply so a connect-type rule
                    // reconnects after a manual disconnect (subject to
                    // the manual-disconnect cooldown inside the applier).
                    lastRuleKey = ""
                    evaluateCurrentNetwork()
                }
                wasConnected = status.connected
            }
        }

        // v0.9.15.70 — Callback-driven SSID state machine. On Android 12+
        // the OS redacts SSID in every poll API (WifiManager.connection-
        // Info, ConnectivityManager.getNetworkCapabilities) when the app
        // is in the background, EVEN with ACCESS_BACKGROUND_LOCATION.
        // The unredacted SSID is only delivered through a callback
        // registered with FLAG_INCLUDE_LOCATION_INFO (set at registration
        // below on API 31+). We therefore make the callback the single
        // source of truth: latch the current WiFi Network + SSID here;
        // clear them in onLost when the latched network goes away. The
        // evaluator and detectCurrentSsid() read the latch directly —
        // no polling, no "returned empty" attempts, no stale-cache risk
        // when leaving a WiFi.
        val updateWifiLatchFromCaps = { network: Network, caps: NetworkCapabilities ->
            try {
                if (caps.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) &&
                    !caps.hasTransport(NetworkCapabilities.TRANSPORT_VPN)) {
                    currentWifiNetwork = network
                    if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
                        val info = caps.transportInfo as? android.net.wifi.WifiInfo
                        val raw = info?.ssid?.removeSurrounding("\"")
                        if (!raw.isNullOrEmpty() && raw != "<unknown ssid>") {
                            if (currentWifiSsid != raw) {
                                PrivycsLogger.d(TAG, "WiFi SSID latched -> ${PrivycsLogger.redactSsid(raw)} (network $network)")
                            }
                            currentWifiSsid = raw
                        }
                        // If raw is empty here, caps were redacted (rare
                        // when registered with FLAG_INCLUDE_LOCATION_INFO
                        // and ACCESS_BACKGROUND_LOCATION granted; happens
                        // briefly during a transition before the proper
                        // caps event arrives). Keep the previous SSID
                        // until either a non-empty value lands or onLost
                        // for this very Network clears it.
                    }
                }
            } catch (_: Exception) { /* never let the callback throw */ }
        }

        val isWifiNonVpn = { network: Network ->
            val c = try {
                connectivityManager.getNetworkCapabilities(network)
            } catch (_: Exception) {
                null
            }
            c != null &&
                c.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) &&
                !c.hasTransport(NetworkCapabilities.TRANSPORT_VPN)
        }

        val onAvailableImpl: (Network) -> Unit = { network ->
            PrivycsLogger.d(TAG, "Network available: $network")
            // Latch the new WiFi Network the moment it appears. The SSID
            // arrives shortly after via onCapabilitiesChanged; until
            // then currentWifiSsid stays empty (the indeterminate-skip
            // guard handles the brief window).
            if (isWifiNonVpn(network)) {
                currentWifiNetwork = network
            }
            evaluateCurrentNetwork()
        }
        val onLostImpl: (Network) -> Unit = { network ->
            // Do NOT hard-code "no network" here. Android frequently calls
            // onLost for the outgoing default network a few milliseconds
            // BEFORE onAvailable fires for the new default (typical on
            // WiFi→Mobile handover). Hard-coding "none" created a window
            // where the UI showed "No Network" and the auto-connect
            // evaluator saw state=none, tore down the VPN, and only then
            // noticed the new network had arrived.
            PrivycsLogger.d(TAG, "Network lost: $network — re-evaluating current state")
            // v0.9.15.70 — clear the WiFi latch if this is the network
            // we were tracking. Without this, the cached SSID would
            // carry over into the next WiFi (or worse, into a hotel /
            // foreign WLAN), producing wrong rule decisions until a new
            // unredacted SSID arrives.
            if (network == currentWifiNetwork) {
                if (currentWifiSsid.isNotEmpty()) {
                    PrivycsLogger.d(TAG, "WiFi SSID latch cleared (network $network lost)")
                }
                currentWifiNetwork = null
                currentWifiSsid = ""
            }
            evaluateCurrentNetwork()
        }
        val onCapsImpl: (Network, NetworkCapabilities) -> Unit = { network, caps ->
            PrivycsLogger.d(TAG, "Network capabilities changed: $network")
            updateWifiLatchFromCaps(network, caps)
            evaluateCurrentNetwork()
        }
        val onLinkImpl: (Network, android.net.LinkProperties) -> Unit = { network, _ ->
            PrivycsLogger.d(TAG, "Network link properties changed: $network")
            evaluateCurrentNetwork()
        }

        val callback: ConnectivityManager.NetworkCallback =
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
                object : ConnectivityManager.NetworkCallback(
                    ConnectivityManager.NetworkCallback.FLAG_INCLUDE_LOCATION_INFO,
                ) {
                    override fun onAvailable(network: Network) = onAvailableImpl(network)
                    override fun onLost(network: Network) = onLostImpl(network)
                    override fun onCapabilitiesChanged(
                        network: Network,
                        networkCapabilities: NetworkCapabilities,
                    ) = onCapsImpl(network, networkCapabilities)
                    override fun onLinkPropertiesChanged(
                        network: Network,
                        linkProperties: android.net.LinkProperties,
                    ) = onLinkImpl(network, linkProperties)
                }
            } else {
                object : ConnectivityManager.NetworkCallback() {
                    override fun onAvailable(network: Network) = onAvailableImpl(network)
                    override fun onLost(network: Network) = onLostImpl(network)
                    override fun onCapabilitiesChanged(
                        network: Network,
                        networkCapabilities: NetworkCapabilities,
                    ) = onCapsImpl(network, networkCapabilities)
                    override fun onLinkPropertiesChanged(
                        network: Network,
                        linkProperties: android.net.LinkProperties,
                    ) = onLinkImpl(network, linkProperties)
                }
            }

        // Register-then-store: only set networkCallback AFTER the
        // system call returns successfully so a failed registration
        // doesn't leave a stale reference that stop() would later
        // try to unregister (system would throw IAE because nothing
        // was registered). Conversely, a successful registration
        // always pairs with a matching unregister in stop(). This
        // closes the leak the audit flagged: failed start() left the
        // field set but no backing registration.
        //
        // VPN-bypass callback selection — see class kdoc for the
        // full rationale. The two API paths are observation-only
        // (no network is forcibly kept up); the older path attaches
        // its filter via NetworkRequest.NET_CAPABILITY_NOT_VPN.
        try {
            // VPN-bypass via NetworkRequest (works on all API levels
            // 26+). Originally v0.9.14.71 split this into a
            // registerSystemDefaultNetworkCallback path for API 31+
            // — but that method is @SystemApi(MODULE_LIBRARIES) and
            // not callable from regular apps even with compileSdk=34
            // (compile failed on .71-.72 with "Unresolved reference").
            // The NetworkRequest variant is observation-only,
            // unprivileged, and gets us the same outcome: callbacks
            // fire on every non-VPN physical transport change, even
            // while a VPN tunnel is the system default.
            // v0.9.14.88: NET_CAPABILITY_INTERNET dropped from the
            // request. The capability is added by the system AFTER
            // captive-portal validation completes (5-30 s after the
            // physical Wi-Fi association). With INTERNET in the
            // filter, our onAvailable / onCapabilitiesChanged is
            // delayed by that whole validation window — visible to
            // the user as "VPN doesn't react when phone enters home
            // Wi-Fi until ~30 s later" and indistinguishable from
            // Doze deferral. Without INTERNET we get the callback at
            // physical association time; the rules engine still
            // works because we only need the SSID/transport to
            // resolve a rule, not Internet reachability.
            val req = NetworkRequest.Builder()
                .addCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN)
                .addTransportType(NetworkCapabilities.TRANSPORT_WIFI)
                .addTransportType(NetworkCapabilities.TRANSPORT_CELLULAR)
                .addTransportType(NetworkCapabilities.TRANSPORT_ETHERNET)
                .build()
            connectivityManager.registerNetworkCallback(req, callback)
            networkCallback = callback

            // Runtime BroadcastReceiver for instant-wake triggers.
            // The system delivers Intent.ACTION_SCREEN_ON the moment
            // the display turns on (instant — there is NO Doze
            // deferral on this broadcast). CONNECTIVITY_ACTION is the
            // legacy connectivity-change pathway and sometimes
            // arrives before the NetworkCallback in the small window
            // where the system has associated with a Wi-Fi but not
            // yet resolved the captive-portal probe. Both call back
            // into evaluateCurrentNetwork() so the rules engine
            // re-runs with fresh state.
            try {
                val r = object : android.content.BroadcastReceiver() {
                    override fun onReceive(context: Context, intent: android.content.Intent) {
                        val action = intent.action
                        PrivycsLogger.d(TAG, "Wake receiver fired: $action")

                        // v1.0.5.9: WifiManager.NETWORK_STATE_CHANGED_ACTION
                        // fast-path. This broadcast arrives within
                        // ~100-300ms of Wi-Fi association — well before
                        // ConnectivityManager.NetworkCallback fires on
                        // throttling OEMs (Samsung / Xiaomi / Oppo can
                        // delay the callback by 5-8s). It also carries
                        // EXTRA_WIFI_INFO with the live SSID in the same
                        // delivery, so we don't have to wait for the
                        // separate onCapabilitiesChanged callback to
                        // latch the SSID — we set the latch directly
                        // here. Net effect: rule-driven disconnect/
                        // connect on a Wi-Fi-SSID-match collapses from
                        // 5-10 s to sub-second.
                        if (action == android.net.wifi.WifiManager.NETWORK_STATE_CHANGED_ACTION) {
                            try {
                                @Suppress("DEPRECATION")
                                val netInfo = intent.getParcelableExtra<android.net.NetworkInfo>(
                                    android.net.wifi.WifiManager.EXTRA_NETWORK_INFO
                                )
                                if (netInfo?.isConnected == true) {
                                    @Suppress("DEPRECATION")
                                    val wifiInfo = intent.getParcelableExtra<android.net.wifi.WifiInfo>(
                                        android.net.wifi.WifiManager.EXTRA_WIFI_INFO
                                    )
                                    val rawSsid = wifiInfo?.ssid
                                    if (!rawSsid.isNullOrEmpty() && rawSsid != "<unknown ssid>") {
                                        // SSID arrives quoted: strip
                                        // leading + trailing quotes if
                                        // present, leave non-quoted as-is.
                                        val cleaned = if (rawSsid.length >= 2 &&
                                            rawSsid.first() == '"' && rawSsid.last() == '"'
                                        ) {
                                            rawSsid.substring(1, rawSsid.length - 1)
                                        } else rawSsid
                                        currentWifiSsid = cleaned
                                        PrivycsLogger.d(
                                            TAG,
                                            "Wifi fast-path: SSID latched from broadcast",
                                        )
                                    }
                                } else if (netInfo?.isConnectedOrConnecting == false) {
                                    // Wi-Fi gone — clear the SSID latch
                                    // so the next rule evaluation does
                                    // not match a stale SSID.
                                    currentWifiSsid = ""
                                }
                            } catch (e: Throwable) {
                                PrivycsLogger.w(TAG, "Wifi fast-path latch failed: ${e.message}")
                                // Fall through; evaluation still runs.
                            }
                        }

                        evaluateCurrentNetwork()
                    }
                }
                val filter = android.content.IntentFilter().apply {
                    addAction(android.content.Intent.ACTION_SCREEN_ON)
                    @Suppress("DEPRECATION")
                    addAction(android.net.ConnectivityManager.CONNECTIVITY_ACTION)
                    // v1.0.5.9: Wi-Fi-association fast-path.
                    addAction(android.net.wifi.WifiManager.NETWORK_STATE_CHANGED_ACTION)
                }
                if (android.os.Build.VERSION.SDK_INT >= 33) {
                    context.registerReceiver(
                        r,
                        filter,
                        Context.RECEIVER_NOT_EXPORTED,
                    )
                } else {
                    @Suppress("UnspecifiedRegisterReceiverFlag")
                    context.registerReceiver(r, filter)
                }
                wakeReceiver = r
            } catch (e: Exception) {
                PrivycsLogger.e(TAG, "Failed to register wake receiver", e)
            }
        } catch (e: Exception) {
            PrivycsLogger.e(TAG, "Failed to register network callback", e)
            // started flag must be reset too so a retry can
            // attempt registration; otherwise started=true with
            // no callback wedges the monitor in a half-up state.
            started = false
            return
        }

        // Fix 3 — single consumer for the conflated eval pipeline.
        // Launched once for the process lifetime (the channel is
        // never closed); guarded so repeated start()/stop() cycles
        // don't spawn duplicate consumers on the same channel.
        if (!evalConsumerStarted) {
            evalConsumerStarted = true
            scope.launch {
                for (unused in evalTrigger) {
                    // Debounce: a WiFi↔Mobile / WiFi off→on handover
                    // emits onLost/onAvailable/onCaps/onLink in a
                    // sub-second burst — collapse it into ONE
                    // evaluation against the settled state. CONFLATED
                    // means extra triggers during the delay coalesce
                    // to a single pending one.
                    kotlinx.coroutines.delay(300)
                    if (!started) continue
                    try {
                        runEvaluation()
                    } catch (e: Exception) {
                        PrivycsLogger.e(TAG, "Evaluation failed", e)
                    }
                }
            }
        }

        // In-process backstop tick (v0.9.14.71). NetworkCallback is
        // the primary fast path; this 10 s tick catches the cases
        // where the callback doesn't fire — Doze mode batching, OEM
        // power-management throttling, and the rare missed event
        // during Wi-Fi/Cellular handover under load. The
        // PrivycsVpnService that hosts NetworkMonitor is a foreground
        // service while a tunnel is up, so the OS doesn't pause this
        // coroutine. AutoTunnelWorker (15-min WorkManager) remains
        // the long-term safety net for after-process-death scenarios.
        // v0.9.14.96: tightened from 30 s → 10 s per user request
        // ("ich brauche aber schnellere action als 60s"). Battery
        // cost is one Coroutine-delay every 10 s plus the
        // evaluateCurrentNetwork() call (~5 ms CPU, no I/O on the
        // happy path) — negligible. The win: when the OEM defers
        // our NetworkCallback for >10 s but our process is alive,
        // the SSID-in-except-list disconnect now fires within 10 s
        // instead of 30 s.
        scope.launch {
            while (started) {
                kotlinx.coroutines.delay(10_000)
                if (!started) break
                evaluateCurrentNetwork()
            }
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
        PrivycsLogger.d(TAG, "Stopping network monitor")

        networkCallback?.let {
            try {
                connectivityManager.unregisterNetworkCallback(it)
            } catch (e: Exception) {
                PrivycsLogger.e(TAG, "Failed to unregister network callback", e)
            }
        }
        networkCallback = null

        wakeReceiver?.let {
            try {
                context.unregisterReceiver(it)
            } catch (e: Exception) {
                PrivycsLogger.e(TAG, "Failed to unregister wake receiver", e)
            }
        }
        wakeReceiver = null
    }

    /**
     * Evaluate the current network state against connect-on-demand rules
     * and trigger VPN connect/disconnect as needed.
     */
    private fun evaluateCurrentNetwork() {
        // Fix 3: hand off to the conflated pipeline instead of
        // launching a fresh, racing coroutine per trigger.
        evalTrigger.trySend(Unit)
    }

    private suspend fun runEvaluation() {
        // v1.0.5: master on/off — when the NetworkRules engine is
        // disabled via the master toggle in NetworkRulesScreen, no
        // rule may trigger any auto-connect/-disconnect. Bypass the
        // full evaluation pipeline here for the smallest possible
        // surface. The user can still manually Connect/Disconnect via
        // the Connect screen — that path doesn't go through this
        // function. Matches Desktop's `network_rules_enabled` gate.
        if (!PrivycsApp.instance.settingsRepository.getSettingsBlocking().networkRulesEnabled) {
            // v1.0.5.25: don't just bail — propagate the OFF state
            // into _networkState so every reader of networkState.value
            // (ConnectScreen post-disconnect orchestration,
            // VpnPauseTimer expiry, post-sinkhole COD resume etc.)
            // sees shouldConnect=false instead of inheriting whatever
            // the LAST eval-while-ON computed. Without this, a fresh
            // master-OFF toggle leaves a stale shouldConnect=true in
            // the flow and downstream readers keep firing reconnects
            // even though the engine itself is paused.
            _networkState.value = NetworkState(
                networkType = _networkState.value.networkType,
                ssid = _networkState.value.ssid,
                shouldConnect = false,
                ruleMatch = "Auto-tunnel master OFF",
            )
            return
        }
        val networkType = detectNetworkType()
        // v0.9.15.70 — single SSID source: the callback-driven latch.
        // detectCurrentSsid() reads currentWifiSsid (set in
        // onCapabilitiesChanged, cleared in onLost) — no polling, no
        // stale value leaking into a different WiFi.
        val effectiveSsid = if (networkType == "wifi") detectCurrentSsid() else ""

        // v1.0.5.3: boot-time SSID conservatism. On app start the
        // SSID latch is empty until the first onCapabilitiesChanged
        // delivers it; resolving rules against an empty SSID while
        // an SSID-rule exists silently treats a trusted-WiFi as
        // "any WiFi" and fires a connect that the very next eval
        // (~1-3s later, once SSID arrives) tears back down —
        // visible "connect-then-disconnect" flicker on every app
        // open. Skip the entire pipeline until SSID resolves.
        // Mirrors evaluateRulesNow()'s guard so all rule-driven
        // entry points behave identically.
        if (networkType == "wifi" && effectiveSsid.isEmpty()) {
            val hasSsidRule = PrivycsApp.instance.networkRulesRepository.rules.value.any {
                it.matchType == RuleMatchType.SSID_EXACT ||
                    it.matchType == RuleMatchType.SSID_PATTERN
            }
            if (hasSsidRule) {
                PrivycsLogger.d(
                    TAG,
                    "Skipping evaluate: WiFi with SSID-rule but SSID not yet latched",
                )
                return
            }
        }

        // Stability guard: while the VPN is up AND the SSID is still
        // unresolved (background redaction not yet lifted by a
        // callback), skip rather than act on an unknown SSID — acting
        // could tear down the tunnel on a trusted WiFi we just haven't
        // identified yet. Capped at maxIndeterminateSkips so a genuine
        // WiFi→Mobile move is not skipped forever.
        val vpnManagerForGuard = VpnServiceManager.getInstance(context)
        val indeterminate = vpnManagerForGuard.isConnected && networkType == "wifi" &&
            effectiveSsid.isEmpty()
        if (indeterminate) {
            if (indeterminateSkipCount < maxIndeterminateSkips) {
                indeterminateSkipCount++
                PrivycsLogger.d(
                    TAG,
                    "Skipping evaluate: VPN up but SSID indeterminate — " +
                        "skip $indeterminateSkipCount/$maxIndeterminateSkips",
                )
                return
            }
            PrivycsLogger.w(
                TAG,
                "Indeterminate-skip threshold reached, evaluating with empty ssid",
            )
        }
        indeterminateSkipCount = 0

        val ssid = effectiveSsid
        val bssid = detectCurrentBssid()

        // The rule list is the single source of truth — the legacy
        // "simple COD" was migrated into rules (see
        // NetworkRulesRepository.migrateLegacyCod). Walk it in priority
        // order; the first matching rule wins.
        val resolution = PrivycsApp.instance.networkRulesRepository
            .resolve(networkType, ssid, bssid)

        val wantsVpn = resolution is RuleResolution.ConnectActive ||
            resolution is RuleResolution.Pool ||
            resolution is RuleResolution.Connection
        _networkState.value = NetworkState(
            networkType = networkType,
            ssid = ssid,
            shouldConnect = wantsVpn,
            ruleMatch = describeResolution(resolution, networkType, ssid),
        )

        // NoMatch — no rule covers this network. The engine takes no
        // action; the VPN stays in whatever state the user left it.
        if (resolution is RuleResolution.NoMatch) {
            lastRuleKey = ""
            return
        }

        // Apply when the resolved rule changed (key != lastRuleKey) OR
        // when the VPN is settled in the wrong state — down while a
        // connect-rule matches, or up while a NoVpn rule matches — and
        // nothing is in flight. The settled-wrong path re-asserts a
        // rule after a manual disconnect once the cooldown passes, and
        // retries a connect that failed; lastRuleKey alone would skip
        // those. While a connect IS in flight (isConnecting) we do NOT
        // re-fire — that is the ping-pong guard.
        val key = resolutionKey(resolution)
        val vpnManager = VpnServiceManager.getInstance(context)
        val settledWrong = if (resolution is RuleResolution.NoVpn) {
            vpnManager.isConnected
        } else {
            !vpnManager.isConnected && !vpnManager.isConnecting.value
        }
        if (key != lastRuleKey || settledWrong) {
            lastRuleKey = key
            PrivycsLogger.i(TAG, "rule -> $key (settledWrong=$settledWrong)")
            applyRuleResolution(resolution)
        }
    }

    /** Stable identity of a resolution, for the transition guard. */
    private fun resolutionKey(r: RuleResolution): String = when (r) {
        is RuleResolution.NoMatch -> ""
        is RuleResolution.NoVpn -> "no_vpn:"
        is RuleResolution.ConnectActive -> "connect_active:"
        is RuleResolution.Pool -> "pool:${r.poolId}"
        is RuleResolution.Connection -> "connection:${r.connectionId}"
    }

    /**
     * Human-readable description of the engine's decision — fed into
     * the NetworkState the UI banner reads.
     */
    private fun describeResolution(
        r: RuleResolution,
        networkType: String,
        ssid: String,
    ): String {
        val net = when {
            networkType == "wifi" && ssid.isNotEmpty() -> "WiFi \"$ssid\""
            networkType == "wifi" -> "WiFi"
            networkType == "none" -> "no network"
            else -> networkType
        }
        return when (r) {
            is RuleResolution.NoMatch -> "$net — no rule matches"
            is RuleResolution.NoVpn -> "$net — rule: disconnect (no VPN)"
            is RuleResolution.ConnectActive -> "$net — rule: connect"
            is RuleResolution.Pool -> "$net — rule: switch pool"
            is RuleResolution.Connection -> "$net — rule: switch connection"
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
     * Detect the current physical network transport, ignoring the
     * VPN itself. v0.9.14.71 fix: previous version queried
     * `cm.activeNetwork` which becomes the VPN once the tunnel is
     * up, and the VPN's NetworkCapabilities don't reliably inherit
     * the underlying transport bits across all OEMs/Android versions.
     * Result was on-demand mis-firing for users whose VPN was up.
     *
     * New strategy: walk all networks, skip anything that has
     * TRANSPORT_VPN, return the first non-VPN network with INTERNET
     * cap. Picks WiFi over cellular over ethernet by simple iteration
     * order — typically Android lists the system default first.
     */
    private fun detectNetworkType(): String {
        val networks = try {
            connectivityManager.allNetworks
        } catch (e: Exception) {
            PrivycsLogger.w(TAG, "Failed to enumerate networks: ${e.message}")
            return "none"
        }
        // 1. VPN down (or not the system default): the system default
        //    IS the real physical transport — classify it directly.
        val activeCaps = connectivityManager.activeNetwork?.let {
            connectivityManager.getNetworkCapabilities(it)
        }
        if (activeCaps != null &&
            !activeCaps.hasTransport(NetworkCapabilities.TRANSPORT_VPN)
        ) {
            classifyTransport(activeCaps)?.let { return it }
        }

        // 2. VPN IS the system default (tunnel up). Do NOT pick "the
        //    first non-VPN entry in allNetworks" — that list's order is
        //    unspecified, and on a Mobile→WiFi handover while the
        //    tunnel is up the cellular network lingers for a while as
        //    the VPN's backup transport. The old iteration-order pick
        //    then returned "mobile" while the user was actually on
        //    WiFi: the status stuck on "connected via Mobile" and the
        //    SSID/except rule never ran because the network was never
        //    classified wifi. Only a COD off/on re-register "fixed" it
        //    (by which point the lingering cellular net was gone).
        //
        //    Resolve DETERMINISTICALLY instead: collect every non-VPN
        //    INTERNET transport currently present and rank
        //    ethernet > wifi > mobile, so a present WiFi wins over a
        //    lingering cellular link after the handover. (NetworkCap-
        //    abilities.underlyingNetworks would be the "authoritative"
        //    source but is not resolvable against this module's SDK
        //    classpath — this ranking is the robust, SDK-safe answer
        //    and is correct for the phone handover case the bug is
        //    about.)
        var hasWifi = false
        var hasCellular = false
        var hasEthernet = false
        for (n in networks) {
            val c = connectivityManager.getNetworkCapabilities(n) ?: continue
            if (c.hasTransport(NetworkCapabilities.TRANSPORT_VPN)) continue
            // Fix 1 (v0.9.15.68) — NO NET_CAPABILITY_INTERNET filter.
            // A freshly-associated WiFi has no INTERNET cap until
            // captive-portal validation completes (5-30 s). With the
            // VPN up, activeNetwork IS the VPN, so step 1 above is
            // ALWAYS skipped and this loop is the only classifier —
            // re-imposing the INTERNET filter here delayed the
            // trusted-WiFi disconnect by the whole validation window
            // (exactly the symptom v0.9.14.88 fixed at the callback
            // layer by dropping INTERNET from the NetworkRequest, then
            // silently reintroduced here). Rules need the
            // SSID/transport, not Internet reachability — mirror the
            // callback's own filter. A leaving WiFi lingering without
            // INTERNET self-corrects on the next onLost/onCaps tick.
            when {
                c.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) -> hasWifi = true
                c.hasTransport(NetworkCapabilities.TRANSPORT_CELLULAR) -> hasCellular = true
                c.hasTransport(NetworkCapabilities.TRANSPORT_ETHERNET) -> hasEthernet = true
            }
        }
        return when {
            hasEthernet -> "ethernet"
            hasWifi -> "wifi"
            hasCellular -> "mobile"
            else -> "none"
        }
    }

    private fun classifyTransport(c: NetworkCapabilities): String? {
        return when {
            c.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) -> "wifi"
            c.hasTransport(NetworkCapabilities.TRANSPORT_CELLULAR) -> "mobile"
            c.hasTransport(NetworkCapabilities.TRANSPORT_ETHERNET) -> "ethernet"
            else -> null
        }
    }

    /**
     * Detect the current WiFi SSID.
     * v0.9.15.70 — Returns the SSID we are CURRENTLY connected to,
     * read from the callback-driven latch (currentWifiSsid). On
     * Android 12+ in the background the latch is the ONLY reliable
     * source: every poll API (WifiManager.connectionInfo,
     * ConnectivityManager.getNetworkCapabilities().transportInfo)
     * returns "<unknown ssid>" / "" outside a callback context even
     * with ACCESS_BACKGROUND_LOCATION granted. The latch is written
     * from the FLAG_INCLUDE_LOCATION_INFO callback in
     * onCapabilitiesChanged and cleared in onLost when the latched
     * Network goes away — so we can never serve a stale SSID after
     * leaving a WiFi.
     *
     * Foreground fallback: WifiManager.connectionInfo returns the
     * real SSID when the app is in the foreground (not subject to
     * background redaction). Used only when the latch is empty —
     * e.g. very first launch before any callback fires, or while
     * the BootReceiver runs.
     */
    @Suppress("DEPRECATION")
    private fun detectCurrentSsid(): String {
        val latched = currentWifiSsid
        if (latched.isNotEmpty()) return latched
        // Foreground fallback. Avoid burning ACCESS_FINE_LOCATION
        // checks when the call would be redacted anyway: only attempt
        // WifiManager.connectionInfo when we have NO latched SSID. In
        // background this still returns "" / "<unknown ssid>"; that's
        // fine — we return "" and the indeterminate-skip guard waits
        // for the callback.
        return try {
            val ssid = wifiManager.connectionInfo?.ssid?.removeSurrounding("\"") ?: return ""
            if (ssid.isEmpty() || ssid == "<unknown ssid>") "" else ssid
        } catch (_: SecurityException) {
            ""
        } catch (_: Exception) {
            ""
        }
    }

    /**
     * Apply a rule resolution. Connect-type resolutions route through
     * ConnectCoordinator with the ON_DEMAND source so they inherit the
     * same gating (Kill Switch, Always-On pause, preemption) as a
     * manual tap. The CONNECT_ACTIVE path additionally honours the
     * manual- and on-demand-disconnect cooldowns, reproducing the
     * legacy "simple COD" connect behaviour. Each branch is idempotent
     * — already-in-desired-state is a no-op — so re-invocation by the
     * settled-wrong retry path in runEvaluation is safe.
     */
    private suspend fun applyRuleResolution(resolution: RuleResolution) {
        val app = PrivycsApp.instance
        val vpnManager = VpnServiceManager.getInstance(context)
        when (resolution) {
            is RuleResolution.NoVpn -> {
                if (vpnManager.isConnected) {
                    PrivycsLogger.i(TAG, "rule -> NO_VPN, disconnecting")
                    com.privycs.vpn.util.ConnectCoordinator.requestDisconnect(
                        context,
                        com.privycs.vpn.util.ConnectCoordinator.IntentSource.ON_DEMAND,
                    )
                    // Suppress the teardown-flap reconnect — see
                    // ON_DEMAND_DISCONNECT_COOLDOWN_MS.
                    AlwaysOnDetector.stampOnDemandDisconnect(context)
                }
            }
            is RuleResolution.ConnectActive -> {
                if (vpnManager.isConnected || vpnManager.isConnecting.value) return
                // Manual-disconnect cooldown — honour a recent user tap.
                if (AlwaysOnDetector.wasRecentlyManuallyDisconnected(
                        context, MANUAL_DISCONNECT_COOLDOWN_MS,
                    )
                ) {
                    PrivycsLogger.i(
                        TAG,
                        "rule -> CONNECT_ACTIVE suppressed: manual-disconnect cooldown",
                    )
                    return
                }
                // On-demand-disconnect cooldown — bridge the SSID-flap
                // window after a rule-driven disconnect.
                if (AlwaysOnDetector.wasRecentlyOnDemandDisconnected(
                        context, ON_DEMAND_DISCONNECT_COOLDOWN_MS,
                    )
                ) {
                    PrivycsLogger.i(
                        TAG,
                        "rule -> CONNECT_ACTIVE suppressed: on-demand-disconnect cooldown",
                    )
                    return
                }
                // Pool-active wins — getActive() is null when a pool
                // owns the user's active slot.
                val poolReg = app.poolRepository.registry.value
                val activePool = poolReg.pools.firstOrNull { it.id == poolReg.activeId }
                if (poolReg.activeId.isNotEmpty() && activePool != null) {
                    val r = com.privycs.vpn.util.ConnectCoordinator.requestPoolConnect(
                        context,
                        com.privycs.vpn.util.ConnectCoordinator.IntentSource.ON_DEMAND,
                        activePool.id,
                        activePool.name,
                    )
                    PrivycsLogger.d(TAG, "rule -> CONNECT_ACTIVE pool -> $r")
                    return
                }
                val connection = app.connectionRepository.getActive()
                if (connection == null) {
                    PrivycsLogger.d(
                        TAG,
                        "rule -> CONNECT_ACTIVE but no active connection or pool",
                    )
                    return
                }
                val r = com.privycs.vpn.util.ConnectCoordinator.requestConnect(
                    context,
                    com.privycs.vpn.util.ConnectCoordinator.IntentSource.ON_DEMAND,
                    connection,
                )
                PrivycsLogger.d(TAG, "rule -> CONNECT_ACTIVE connection -> $r")
            }
            is RuleResolution.Pool -> {
                val poolReg = app.poolRepository.registry.value
                val pool = poolReg.pools.firstOrNull { it.id == resolution.poolId }
                if (pool == null) {
                    PrivycsLogger.w(TAG, "rule -> POOL ${resolution.poolId} but pool not found")
                    return
                }
                if (poolReg.activeId == pool.id && vpnManager.isConnected) {
                    return // already active and connected
                }
                PrivycsLogger.i(TAG, "rule -> POOL ${pool.name}")
                vpnManager.switchActivePool(pool.id)
            }
            is RuleResolution.Connection -> {
                val conn = app.connectionRepository.getById(resolution.connectionId)
                if (conn == null) {
                    PrivycsLogger.w(
                        TAG,
                        "rule -> CONNECTION ${resolution.connectionId} but not found",
                    )
                    return
                }
                if (app.connectionRepository.activeId == conn.id && vpnManager.isConnected) {
                    return
                }
                PrivycsLogger.i(TAG, "rule -> CONNECTION ${conn.name}")
                vpnManager.switchActiveConnection(conn.id)
            }
            is RuleResolution.NoMatch -> Unit
        }
    }

    /**
     * Detect the current Wi-Fi BSSID (access-point MAC). Used by
     * the Phase-3 BSSID-match rule type to defend against SSID
     * spoofing - someone naming their hotspot "HomeWifi" at the
     * airport would otherwise pass an SSID-only trust check.
     *
     * Requires ACCESS_FINE_LOCATION (which we already use for
     * SSID detection on Android 8+). Returns "" when not on Wi-Fi
     * or when BSSID cannot be determined. Lower-cased for
     * case-insensitive matching against rule MAC strings.
     */
    @Suppress("DEPRECATION")
    private fun detectCurrentBssid(): String {
        // Same VPN-aware pattern as detectCurrentSsid: prefer
        // TransportInfo on API 29+ so an active VPN doesn't blank
        // out the BSSID (used by Network Rules' BSSID-match path).
        return try {
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
                for (n in connectivityManager.allNetworks) {
                    val c = connectivityManager.getNetworkCapabilities(n) ?: continue
                    if (c.hasTransport(NetworkCapabilities.TRANSPORT_VPN)) continue
                    if (!c.hasTransport(NetworkCapabilities.TRANSPORT_WIFI)) continue
                    val info = c.transportInfo as? android.net.wifi.WifiInfo ?: continue
                    val bssid = info.bssid ?: continue
                    if (bssid == "02:00:00:00:00:00" || bssid.isBlank()) continue
                    return bssid.lowercase()
                }
            }
            val wifiInfo = wifiManager.connectionInfo
            val bssid = wifiInfo?.bssid ?: return ""
            // Android sometimes returns "02:00:00:00:00:00" when
            // Location permission is granted but the system has
            // anonymised the MAC (e.g. when MAC randomisation is
            // active and the underlying call doesn't have full
            // permission yet). Treat that as "unknown".
            if (bssid == "02:00:00:00:00:00" || bssid.isBlank()) "" else bssid.lowercase()
        } catch (e: SecurityException) {
            ""
        } catch (e: Exception) {
            ""
        }
    }

    /**
     * Force a re-evaluation from outside. Call this after the user changes
     * connect-on-demand settings so the state/rule-match text and the auto
     * connect decision refresh immediately instead of waiting for the next
     * network event.
     */
    fun reevaluate() {
        PrivycsLogger.d(TAG, "Manual re-evaluate requested")
        evaluateCurrentNetwork()
    }

    /**
     * Synchronous one-shot rule check — does the current network
     * resolve to a connect right now? Returns true iff a connect
     * intent SHOULD fire. Does NOT fire any intent or touch state.
     * Used by BootReceiver to gate boot-time auto-connect.
     */
    fun evaluateRulesNow(): Boolean {
        val networkType = detectNetworkType()
        val effectiveSsid = if (networkType == "wifi") detectCurrentSsid() else ""
        // Boot conservatism: the SSID latch is empty until the first
        // callback fires. If any SSID-matching rule exists, do NOT
        // decide on an unidentified WiFi at boot — the live monitor
        // corrects once the callback lands.
        if (networkType == "wifi" && effectiveSsid.isEmpty()) {
            val hasSsidRule = PrivycsApp.instance.networkRulesRepository.rules.value.any {
                it.matchType == RuleMatchType.SSID_EXACT ||
                    it.matchType == RuleMatchType.SSID_PATTERN
            }
            if (hasSsidRule) return false
        }
        val bssid = detectCurrentBssid()
        val resolution = PrivycsApp.instance.networkRulesRepository
            .resolve(networkType, effectiveSsid, bssid)
        return resolution is RuleResolution.ConnectActive ||
            resolution is RuleResolution.Pool ||
            resolution is RuleResolution.Connection
    }
}
