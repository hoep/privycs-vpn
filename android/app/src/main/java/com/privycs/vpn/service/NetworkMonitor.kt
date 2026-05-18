package com.privycs.vpn.service

import android.content.Context
import android.net.ConnectivityManager
import android.net.Network
import android.net.NetworkCapabilities
import android.net.NetworkRequest
import android.net.wifi.WifiManager
import android.os.Build
import com.privycs.vpn.PrivycsApp
import com.privycs.vpn.data.models.ConnectOnDemandSettings
import com.privycs.vpn.util.AlwaysOnDetector
import com.privycs.vpn.util.PrivycsLogger
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.channels.Channel
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

    // Edge-trigger state. Only used on the DISCONNECT side so that a
    // flipping rule does not keep issuing redundant disconnect calls while
    // the VPN is already torn down. The connect side intentionally does
    // NOT require a transition: if on-demand rules match and the VPN is
    // off, we connect immediately, even 2 seconds after the user manually
    // disconnected. That is the whole point of on-demand - the "VPN will
    // connect" banner has to be true, not aspirational.
    private var lastShouldConnect: Boolean? = null

    // Sticky SSID cache: once we resolve the current WiFi SSID to a
    // real name, we remember it across evaluations. This is critical
    // because Android's WifiManager.connectionInfo can start returning
    // <unknown ssid> or empty once a VPN becomes the active transport,
    // and naive re-evaluation in "except" mode then flips shouldConnect
    // between true ("cannot determine SSID -> connect") and false
    // ("SSID in except list") depending on which resolution the OS
    // felt like giving us that tick. The flip-flop drove a disconnect
    // /reconnect oscillation after a manual OpenVPN connect on a
    // trusted-except WiFi. Cleared on network-type change so a real
    // WiFi switch picks up the new SSID instead of reusing the old.
    private var lastResolvedSsid: String = ""
    private var lastResolvedNetworkType: String = ""

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
    // (settingsFlow.first(), requestConnect/Disconnect), racing
    // lastShouldConnect / lastRuleKey / indeterminateSkipCount / the
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

        // Re-evaluate when on-demand settings change (SSID list, mode,
        // trigger). Without this the user's rule edits only take effect on
        // the next spontaneous network event.
        scope.launch {
            PrivycsApp.instance.settingsRepository.settingsFlow
                .map { it.connectOnDemand }
                .distinctUntilChanged()
                .collect {
                    PrivycsLogger.d(TAG, "Connect-on-demand settings changed, re-evaluating")
                    // Force edge transition on settings change so a rule flip
                    // is applied immediately even if the network state is
                    // unchanged.
                    lastShouldConnect = null
                    // Clear the manual-disconnect cooldown stamp.
                    // Toggling COD or editing rules is an explicit
                    // user intent in its own right; a stale "I tapped
                    // Disconnect 5 seconds ago" stamp should not
                    // silently suppress the evaluation that runs as
                    // a direct consequence of the new settings.
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
                    PrivycsLogger.d(TAG, "VPN transitioned to disconnected, re-evaluating on-demand rules")
                    lastShouldConnect = null // force edge so connect branch fires
                    evaluateCurrentNetwork()
                }
                wasConnected = status.connected
            }
        }

        // Doze-resilient SSID hook (v0.9.15.11). The callback's
        // onCapabilitiesChanged receives unredacted WifiInfo *only if*
        // the callback was registered with FLAG_INCLUDE_LOCATION_INFO
        // (API 31+). Without the flag, Android 12+ redacts SSID/BSSID
        // to "<unknown ssid>" whenever the app is in the background —
        // and Doze counts as background even for foreground-services.
        // Below we register the callback with the flag on API 31+, and
        // here we pull the SSID straight out of the capabilities the
        // system just handed us. That value populates lastResolvedSsid
        // BEFORE evaluateCurrentNetwork() runs, so rule evaluation has
        // a fresh SSID without going through getNetworkCapabilities()
        // (which DOES NOT honour the flag and stays redacted in Doze).
        val cacheSsidFromCaps = { caps: NetworkCapabilities ->
            try {
                if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q &&
                    caps.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) &&
                    !caps.hasTransport(NetworkCapabilities.TRANSPORT_VPN)) {
                    val info = caps.transportInfo as? android.net.wifi.WifiInfo
                    val raw = info?.ssid?.removeSurrounding("\"")
                    if (!raw.isNullOrEmpty() && raw != "<unknown ssid>") {
                        // Fix 2 (v0.9.15.68) — only refresh the sticky
                        // cache from an UNAMBIGUOUS WiFi environment.
                        // During WiFi→WiFi / off→on, both the outgoing
                        // and incoming network briefly emit caps
                        // events; writing the cache from the OUTGOING
                        // one poisons every later cache-fallback
                        // evaluation with the previous network's name.
                        // (With background-location granted,
                        // currentWifiSsids() sees both → size>1 → skip;
                        // without it the set is redacted/empty → size<=1
                        // → accept the callback's own unredacted SSID,
                        // which is the most-current we can know.)
                        val visible = currentWifiSsids()
                        if (visible.size <= 1) {
                            if (lastResolvedSsid != raw) {
                                PrivycsLogger.d(TAG, "Callback SSID refresh -> \"$raw\"")
                            }
                            lastResolvedSsid = raw
                            lastResolvedNetworkType = "wifi"
                        } else {
                            PrivycsLogger.d(
                                TAG,
                                "Skipping cache refresh: ${visible.size} WiFi networks visible (transition)"
                            )
                        }
                    }
                }
            } catch (_: Exception) { /* never let the callback throw */ }
        }

        val onAvailableImpl: (Network) -> Unit = { network ->
            PrivycsLogger.d(TAG, "Network available: $network")
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
            evaluateCurrentNetwork()
        }
        val onCapsImpl: (Network, NetworkCapabilities) -> Unit = { network, caps ->
            PrivycsLogger.d(TAG, "Network capabilities changed: $network")
            cacheSsidFromCaps(caps)
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
                        PrivycsLogger.d(
                            TAG,
                            "Wake receiver fired: ${intent.action}",
                        )
                        evaluateCurrentNetwork()
                    }
                }
                val filter = android.content.IntentFilter().apply {
                    addAction(android.content.Intent.ACTION_SCREEN_ON)
                    @Suppress("DEPRECATION")
                    addAction(android.net.ConnectivityManager.CONNECTIVITY_ACTION)
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
            val settings = PrivycsApp.instance.settingsRepository.settingsFlow.first()
            val codSettings = settings.connectOnDemand

            val networkType = detectNetworkType()
            val rawSsid = detectCurrentSsid()

            // Sticky SSID fallback. Android's WifiManager can start
            // returning "" or <unknown ssid> once a VPN is the active
            // transport on the device; we keep the last real SSID
            // around and use it when the fresh detection came back
            // empty, so "except" and "only" rules stay stable across
            // the VPN-up/down flicker. Cleared on genuine network-
            // type change (wifi -> mobile / mobile -> none / etc.)
            // so a real network switch picks up the new SSID.
            if (rawSsid.isNotEmpty()) {
                lastResolvedSsid = rawSsid
                lastResolvedNetworkType = networkType
            } else if (networkType == "none") {
                // Drop the sticky SSID ONLY when connectivity is
                // genuinely gone — NOT on a wifi->mobile transport
                // flip. The old "networkType != lastResolvedNetworkType"
                // wipe is the root cause of the multiply-reported
                // "trusted-WiFi except-rule not applied after WiFi
                // off->on while the VPN is up" bug: the cache is
                // consumed only when networkType == "wifi" (see
                // effectiveSsid below), so keeping it across the
                // cellular leg has ZERO effect on the mobile-side
                // decision, but lets the except/only rule still
                // resolve against the last real SSID on WiFi-return —
                // where the live SSID is OS-redacted in the background
                // on Android 12+ without ACCESS_BACKGROUND_LOCATION.
                // A genuinely different WiFi overwrites the cache via
                // the rawSsid.isNotEmpty() branch the moment the SSID
                // is readable (foreground, or background once the
                // Part-2 background-location grant lands).
                lastResolvedSsid = ""
                lastResolvedNetworkType = networkType
            }
            val effectiveSsid = if (rawSsid.isEmpty() && networkType == "wifi") {
                lastResolvedSsid.also {
                    if (it.isNotEmpty()) {
                        PrivycsLogger.d(TAG, "SSID detection returned empty, using cached \"$it\"")
                    }
                }
            } else rawSsid

            // Cache hit = the live SSID was OS-redacted and we fell
            // back to lastResolvedSsid. Decision is still valid; the
            // text just must not claim it as the *current* network.
            val ssidFromCache = rawSsid.isEmpty() && networkType == "wifi" &&
                effectiveSsid.isNotEmpty()

            val (shouldConnect, ruleMatch) = evaluateRules(
                codSettings, networkType, effectiveSsid, ssidFromCache
            )

            // Stability guard: while VPN is up AND SSID detection is
            // still uncertain (empty raw AND no cache), be cautious —
            // acting on "safety-first shouldConnect=true" plus the
            // previous "correctly detected in-except false" created
            // the disconnect/reconnect oscillation reported after
            // manual OpenVPN connect on trusted-except WiFi.
            //
            // BUT: v0.9.14.71 limits how many consecutive ticks the
            // skip can fire. After maxIndeterminateSkips we evaluate
            // anyway with ssid="" — the rule outcome may be
            // suboptimal for one tick, but the alternative (silent
            // forever-skip) is worse: user moves WiFi → Mobile under
            // VPN, the rule no longer applies (mobile not in WiFi
            // SSID-list), but we never act on it.
            val vpnManagerForGuard = VpnServiceManager.getInstance(context)
            val indeterminate = vpnManagerForGuard.isConnected && networkType == "wifi" &&
                rawSsid.isEmpty() && lastResolvedSsid.isEmpty()
            if (indeterminate) {
                if (indeterminateSkipCount < maxIndeterminateSkips) {
                    indeterminateSkipCount++
                    PrivycsLogger.d(
                        TAG,
                        "Skipping evaluate: VPN up but SSID indeterminate (no cache) — " +
                            "skip $indeterminateSkipCount/$maxIndeterminateSkips"
                    )
                    return
                }
                PrivycsLogger.w(
                    TAG,
                    "Indeterminate-skip threshold reached, evaluating with empty ssid (forcing decision)"
                )
            }
            indeterminateSkipCount = 0

            // Use effective SSID from here on for state reporting so
            // the UI banner reflects the value we actually decided on.
            val ssid = effectiveSsid
            val bssid = detectCurrentBssid()

            val newState = NetworkState(
                networkType = networkType,
                ssid = ssid,
                shouldConnect = shouldConnect,
                ruleMatch = ruleMatch
            )
            _networkState.value = newState

            // Phase 2: Per-network rules engine. When the user has
            // defined any network rule, the rules engine becomes
            // authoritative for the connect lifecycle. Walk rules
            // in priority order, first match wins:
            //   - NoVpn: if connected, disconnect; else stay down.
            //   - Pool/Connection: switch target if different;
            //     otherwise leave as-is.
            //   - NoMatch (no rules OR none matched): fall through
            //     to the legacy COD logic below for backwards-compat.
            val ruleResolution = PrivycsApp.instance.networkRulesRepository
                .resolve(networkType, ssid, bssid)
            if (ruleResolution !is com.privycs.vpn.data.models.RuleResolution.NoMatch) {
                // Transition guard: same rule applied as last tick =
                // skip. Otherwise the engine drives switch/disconnect
                // in a loop because tunnel-up itself fires another
                // NetworkCallback event which re-evaluates and
                // re-applies. Reset on NoMatch so the next match
                // fires applier even if it lands on the same target.
                val key = when (ruleResolution) {
                    is com.privycs.vpn.data.models.RuleResolution.NoVpn -> "no_vpn:"
                    is com.privycs.vpn.data.models.RuleResolution.Pool ->
                        "pool:${ruleResolution.poolId}"
                    is com.privycs.vpn.data.models.RuleResolution.Connection ->
                        "connection:${ruleResolution.connectionId}"
                    else -> ""
                }
                if (key != lastRuleKey) {
                    lastRuleKey = key
                    PrivycsLogger.i(TAG, "rule transition -> $key")
                    applyRuleResolution(ruleResolution)
                }
                return
            } else {
                // No rule matched - reset transition key so the
                // next match fires applier even if same as before.
                lastRuleKey = ""
            }

            if (!codSettings.enabled) {
                PrivycsLogger.d(TAG, "Connect on demand disabled, skipping action")
                lastShouldConnect = null
                return
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
                // Manual-disconnect cooldown. If the user tapped
                // Disconnect within MANUAL_DISCONNECT_COOLDOWN_MS,
                // honour their intent and skip the auto-reconnect.
                // Without this gate, on-demand fires within ~500ms
                // of a manual disconnect and the user sees their
                // tap silently undone.
                if (com.privycs.vpn.util.AlwaysOnDetector
                        .wasRecentlyManuallyDisconnected(
                            context, MANUAL_DISCONNECT_COOLDOWN_MS
                        )
                ) {
                    PrivycsLogger.i(
                        TAG,
                        "on-demand reconnect suppressed: manual disconnect within ${MANUAL_DISCONNECT_COOLDOWN_MS / 1000}s cooldown"
                    )
                    return
                }

                // On-demand-disconnect cooldown (v0.9.15.24). After
                // we ourselves fired a rule-triggered disconnect,
                // suppress the next on-demand reconnect for a few
                // seconds so the teardown's transient
                // NetworkCapabilities events don't flip the SSID
                // cache and re-trigger via the "Cannot determine
                // SSID, connecting" fallback. See
                // AlwaysOnDetector.stampOnDemandDisconnect for the
                // full loop pattern.
                if (com.privycs.vpn.util.AlwaysOnDetector
                        .wasRecentlyOnDemandDisconnected(
                            context, ON_DEMAND_DISCONNECT_COOLDOWN_MS
                        )
                ) {
                    PrivycsLogger.i(
                        TAG,
                        "on-demand reconnect suppressed: on-demand disconnect within ${ON_DEMAND_DISCONNECT_COOLDOWN_MS / 1000}s cooldown — waiting for SSID stabilisation"
                    )
                    return
                }

                // Pool-active wins. When the user's active selection
                // is a Pool we have explicitly cleared
                // connectionRepository.activeId (so the Connect
                // screen does not show a stale single-connection
                // name underneath the pool card). That means
                // getActive() returns null in the pool case, and
                // before this branch existed COD silently early-
                // returned with "no active connection, skipping"
                // for pool users.
                //
                // Pool now flows through ConnectCoordinator
                // .requestPoolConnect() with the same gate set as
                // single-connection: Kill Switch sinkhole, system-
                // revoke cooldown, Always-On pause, manual pause,
                // and the manual-disconnect cooldown above. The
                // Coordinator fires ACTION_POOL_CONNECT internally
                // when accepted.
                val poolReg = PrivycsApp.instance.poolRepository.registry.value
                val activePoolId = poolReg.activeId
                val activePool = poolReg.pools.firstOrNull { it.id == activePoolId }
                if (activePoolId.isNotEmpty() && activePool != null) {
                    val poolResult = com.privycs.vpn.util.ConnectCoordinator.requestPoolConnect(
                        context,
                        com.privycs.vpn.util.ConnectCoordinator.IntentSource.ON_DEMAND,
                        activePoolId,
                        activePool.name,
                    )
                    PrivycsLogger.d(
                        TAG,
                        "on-demand requestPoolConnect -> $poolResult (poolId=$activePoolId rules=$ruleMatch)"
                    )
                    return
                }

                // Single-connection path. All gating + serialisation
                // happens inside ConnectCoordinator: system-revoke
                // cooldown, always-on pause flag, preemption by
                // USER-source intents, duplicate-connect guard
                // while Connecting is in flight. We just hand off
                // the intent with the ON_DEMAND source tag and
                // trust the coordinator's decision.
                val connection = PrivycsApp.instance.connectionRepository.getActive()
                if (connection == null) {
                    PrivycsLogger.d(TAG, "Rules match but no active connection or pool, skipping")
                    return
                }
                val result = com.privycs.vpn.util.ConnectCoordinator.requestConnect(
                    context,
                    com.privycs.vpn.util.ConnectCoordinator.IntentSource.ON_DEMAND,
                    connection,
                )
                PrivycsLogger.d(TAG, "on-demand requestConnect -> $result (rules=$ruleMatch)")
            } else if (!shouldConnect && vpnManager.isConnected) {
                // Drop the `&& transitioned` gate that lived here: if
                // we're connected and the rules say we shouldn't be,
                // always issue a disconnect - not only on the flip
                // from shouldConnect=true. Without this, a user who
                // connects manually while already sitting inside an
                // except-list SSID stays connected forever because
                // the rule state is stable-false across polls (no
                // transition event), even though the rule clearly
                // says "disconnect here". requestDisconnect is
                // idempotent (returns AlreadyIdle if tunnel is
                // already down), so re-firing is free.
                // v0.9.14.96: log the result so a disconnect that
                // didn't actually tear down the tunnel can be
                // diagnosed (the new desync-safety in
                // ConnectCoordinator.requestDisconnect logs WARN on
                // the desync path).
                val r = com.privycs.vpn.util.ConnectCoordinator.requestDisconnect(
                    context,
                    com.privycs.vpn.util.ConnectCoordinator.IntentSource.ON_DEMAND,
                )
                PrivycsLogger.i(TAG, "on-demand disconnect requested ($ruleMatch) -> $r")
                com.privycs.vpn.util.EventNotifier.codDisconnected(context, ruleMatch)
                // Stamp the on-demand-disconnect so the connect-side
                // gate above suppresses the immediate reconnect from
                // the teardown's transient NetworkCapabilities events.
                com.privycs.vpn.util.AlwaysOnDetector.stampOnDemandDisconnect(context)
            } else if (transitioned) {
                PrivycsLogger.d(TAG, "Rules transitioned but already in desired state: $ruleMatch")
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
     * Requires ACCESS_FINE_LOCATION permission on Android 8+.
     * Returns empty string if not on WiFi or SSID cannot be determined.
     *
     * v0.9.14.71 strategy:
     *   1. API 29+: prefer NetworkCapabilities.transportInfo on the
     *      first non-VPN WiFi network. transportInfo carries a
     *      WifiInfo even when VPN is the system default — the path
     *      WifiManager.connectionInfo can't honour that on Android
     *      10+ once VPN is active and starts returning empty/
     *      "<unknown ssid>".
     *   2. Fallback: WifiManager.connectionInfo (legacy API 26-28).
     *   3. Cleaner: the sticky-cache lastResolvedSsid in
     *      evaluateCurrentNetwork() runs as a third layer.
     */
    /**
     * Distinct, non-empty SSIDs of all currently-present non-VPN
     * WiFi networks. Fix 2 (v0.9.15.68): the old detectCurrentSsid()
     * returned the FIRST WiFi in connectivityManager.allNetworks,
     * whose order is UNSPECIFIED — during a WiFi→WiFi / off→on
     * transition allNetworks momentarily holds BOTH the outgoing
     * (still carrying its stale WifiInfo) and the incoming network,
     * so "first" could be the network the user just left. Returning
     * the full set lets the caller treat size>1 as "ambiguous, do
     * not assert an SSID" instead of guessing wrong.
     */
    @Suppress("DEPRECATION")
    private fun currentWifiSsids(): List<String> {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.Q) {
            // Legacy path (API 26-28): WifiManager is reliable here
            // (the VPN-blanks-it problem is Android 10+).
            val s = try {
                wifiManager.connectionInfo?.ssid?.removeSurrounding("\"")
            } catch (_: Exception) {
                null
            }
            return if (!s.isNullOrEmpty() && s != "<unknown ssid>") listOf(s) else emptyList()
        }
        val out = LinkedHashSet<String>()
        try {
            for (n in connectivityManager.allNetworks) {
                val c = connectivityManager.getNetworkCapabilities(n) ?: continue
                if (c.hasTransport(NetworkCapabilities.TRANSPORT_VPN)) continue
                if (!c.hasTransport(NetworkCapabilities.TRANSPORT_WIFI)) continue
                val info = c.transportInfo as? android.net.wifi.WifiInfo ?: continue
                val raw = info.ssid?.removeSurrounding("\"") ?: continue
                if (raw.isNotEmpty() && raw != "<unknown ssid>") out.add(raw)
            }
        } catch (e: SecurityException) {
            PrivycsLogger.w(TAG, "No location permission for SSID detection", e)
        } catch (e: Exception) {
            PrivycsLogger.e(TAG, "Failed to enumerate WiFi SSIDs", e)
        }
        return out.toList()
    }

    private fun detectCurrentSsid(): String {
        val ssids = currentWifiSsids()
        return when {
            ssids.size == 1 -> ssids[0]
            ssids.isEmpty() -> ""
            else -> {
                // Ambiguous: WiFi→WiFi / off→on in flight. Returning
                // "" (rather than an arbitrary, possibly-outgoing
                // name) hands the decision to the sticky cache +
                // indeterminate-skip guard, which ride out the
                // transition until exactly one WiFi remains.
                PrivycsLogger.w(
                    TAG,
                    "SSID ambiguous: ${ssids.size} WiFi networks visible — deferring to sticky cache"
                )
                ""
            }
        }
    }

    /**
     * Apply a non-NoMatch rule resolution. Drives target switching
     * via the existing switchActivePool / switchActiveConnection
     * machinery so we get the same disconnect-wait-reconnect grace
     * pattern, Coordinator gating, and tentative-status propagation
     * that manual switches use.
     *
     * Honours the manual-disconnect cooldown and Always-On pause
     * by routing through the Coordinator (via switch helpers,
     * vpnManager.connect, requestDisconnect). USER source preserves
     * the same gate semantics as a tap.
     */
    private suspend fun applyRuleResolution(
        resolution: com.privycs.vpn.data.models.RuleResolution,
    ) {
        val app = PrivycsApp.instance
        val vpnManager = VpnServiceManager.getInstance(context)
        when (resolution) {
            is com.privycs.vpn.data.models.RuleResolution.NoVpn -> {
                if (vpnManager.isConnected) {
                    PrivycsLogger.i(TAG, "rule -> NO_VPN, disconnecting")
                    com.privycs.vpn.util.ConnectCoordinator.requestDisconnect(
                        context,
                        com.privycs.vpn.util.ConnectCoordinator.IntentSource.ON_DEMAND,
                    )
                    // Same cooldown stamp as the legacy COD disconnect
                    // branch — see line 691 / ON_DEMAND_DISCONNECT_COOLDOWN_MS.
                    com.privycs.vpn.util.AlwaysOnDetector.stampOnDemandDisconnect(context)
                }
            }
            is com.privycs.vpn.data.models.RuleResolution.Pool -> {
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
            is com.privycs.vpn.data.models.RuleResolution.Connection -> {
                val conn = app.connectionRepository.getById(resolution.connectionId)
                if (conn == null) {
                    PrivycsLogger.w(TAG, "rule -> CONNECTION ${resolution.connectionId} but not found")
                    return
                }
                val currentActive = app.connectionRepository.activeId
                if (currentActive == conn.id && vpnManager.isConnected) {
                    return
                }
                PrivycsLogger.i(TAG, "rule -> CONNECTION ${conn.name}")
                vpnManager.switchActiveConnection(conn.id)
            }
            else -> Unit
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
     * Evaluate connect-on-demand rules against current network state.
     * Returns a pair of (shouldConnect, ruleDescription).
     */
    private fun evaluateRules(
        settings: ConnectOnDemandSettings,
        networkType: String,
        ssid: String,
        // v0.9.15.x — true when `ssid` did not come from a live read
        // but from the sticky cache (lastResolvedSsid) because the OS
        // redacted the background SSID. The decision below is unchanged
        // (the cached name is still a real, last-seen network), but the
        // human-readable text must NOT assert it as the *current*
        // network — otherwise a move between two exception-list WLANs
        // shows the previous WLAN's name in the notification/banner
        // ("display glitch" report 2026-05-18). We label it "last-known"
        // so the text is honest about the uncertainty.
        ssidIsStale: Boolean = false
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

        // Honest network label: when the SSID is the cached last-known
        // one (OS-redacted live read), say so instead of asserting it
        // as the current network. Decision predicates below keep using
        // raw `ssid` — only the display string changes.
        val netName = if (ssidIsStale) "last-known WiFi \"$ssid\"" else "WiFi \"$ssid\""

        // For WiFi, evaluate SSID rules
        return when (settings.ssidMode) {
            "all" -> {
                // Explicit parentheses: the previous version had a Kotlin operator-precedence
                // bug where " (all SSIDs)" was only appended when ssid was empty.
                val ssidPart = if (ssid.isNotEmpty()) {
                    if (ssidIsStale) " (last-known \"$ssid\")" else " \"$ssid\""
                } else ""
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
                        Pair(true, "$netName matches the allowed list")
                    else ->
                        Pair(false, "$netName is not in the allowed list")
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
                        Pair(false, "$netName is in the exception list")
                    else ->
                        Pair(true, "$netName is not in the exception list")
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
        PrivycsLogger.d(TAG, "Manual re-evaluate requested")
        evaluateCurrentNetwork()
    }

    /**
     * Synchronous one-shot rule check - does the current network
     * environment satisfy the COD rules? Returns true if a connect
     * intent SHOULD fire right now, false otherwise.
     *
     * Does NOT fire any intent, does NOT update lastShouldConnect,
     * does NOT touch the state flow. Pure read-only evaluation.
     *
     * Used by BootReceiver to gate the boot-time auto-connect: when
     * COD is enabled the user expects boot connect ONLY when rules
     * currently match. The async evaluateCurrentNetwork() path
     * cannot serve this because BootReceiver may finish before the
     * Main-dispatcher launch runs, in which case the process can be
     * killed before the eval lands.
     */
    fun evaluateRulesNow(settings: ConnectOnDemandSettings): Boolean {
        val networkType = detectNetworkType()
        val rawSsid = detectCurrentSsid()
        val effectiveSsid = if (rawSsid.isEmpty() && networkType == "wifi") {
            lastResolvedSsid
        } else {
            rawSsid
        }
        // Fix 4 (v0.9.15.68) — boot-path conservatism. At boot
        // lastResolvedSsid is empty and the background SSID is
        // redacted, so an except/only rule with an unresolved SSID
        // would hit the "cannot determine SSID → connect" default
        // (evaluateRules except branch) and auto-connect on a
        // possibly-trusted WiFi at every reboot. The live monitor
        // corrects within seconds once the SSID resolves; until
        // then, do NOT auto-connect on an unidentified WiFi at boot.
        if (networkType == "wifi" && effectiveSsid.isEmpty() &&
            (settings.ssidMode == "except" || settings.ssidMode == "only")
        ) {
            return false
        }
        val (shouldConnect, _) = evaluateRules(settings, networkType, effectiveSsid)
        return shouldConnect
    }
}
