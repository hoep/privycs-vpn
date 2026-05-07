package com.privycs.vpn.widget

import android.app.PendingIntent
import android.appwidget.AppWidgetManager
import android.appwidget.AppWidgetProvider
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.util.Log
import android.widget.RemoteViews
import android.widget.Toast
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import androidx.core.content.ContextCompat
import com.privycs.vpn.MainActivity
import com.privycs.vpn.R
import com.privycs.vpn.data.models.VpnProtocol
import com.privycs.vpn.service.VpnServiceManager
import com.privycs.vpn.util.AlwaysOnDetector
import com.privycs.vpn.util.SpeedTracker

/**
 * Home-screen widget - Connect-screen mirror (v2).
 *
 * Composition matches the app's ConnectScreen as a dominant-anchor
 * layout: header with connection name + server endpoint, big
 * circular toggle button (active protocol icon when connected, grey
 * Privycs logo when idle) that fills the remaining vertical space,
 * then protocol switcher row, then traffic row with download +
 * upload sparklines and values.
 *
 * Interaction:
 * - Tap anywhere on the big circle → connect/disconnect toggle.
 *   Click target is the entire 68dp circle area (the FrameLayout
 *   container) so launchers cannot route mis-hits to the root
 *   background and lose the tap.
 * - Tap a protocol icon → switch active protocol. Failures (no
 *   active connection, missing config for that protocol) surface
 *   as Toast so the user sees why nothing happened.
 * - Tap on body (outside the circle + protocol row) → open app.
 *
 * State propagation: PrivycsVpnService fires sendStatusUpdate() on
 * every lifecycle transition and every traffic-polling tick (~2s).
 * The widget pulls live server endpoint + speed samples from the
 * shared service / SpeedTracker state on each refresh rather than
 * serialising them through Bundle extras every tick.
 */
class VpnWidget : AppWidgetProvider() {

    companion object {
        private const val TAG = "VpnWidget"
        const val ACTION_TOGGLE = "com.privycs.vpn.widget.TOGGLE"
        const val ACTION_STATUS_CHANGED = "com.privycs.vpn.widget.STATUS_CHANGED"
        const val ACTION_SWITCH_PROTOCOL = "com.privycs.vpn.widget.SWITCH_PROTOCOL"
        const val EXTRA_CONNECTED = "connected"
        const val EXTRA_CONNECTION_NAME = "connection_name"
        const val EXTRA_PROTOCOL = "protocol"
        const val EXTRA_UPTIME = "uptime"
        const val EXTRA_TARGET_PROTOCOL = "target_protocol"

        private const val ALWAYS_ON_WIDGET_PAUSE_MINUTES = 15

        // Application-lifetime scope for the widget's post-disconnect
        // COD re-check coroutine. Lives beyond onReceive's return so
        // the 400ms delay + reconnect call can complete even after the
        // broadcast receiver has exited.
        private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main)

        // Sparkline bitmap logical resolution. ImageView scaleType
        // fitXY scales it to the actual on-screen size; render at a
        // size that stays crisp without being wasteful.
        // Half the resolution we used to use (was 360x60). The
        // launcher upscales the bitmap to fit the ImageView's
        // actual pixel size, and at typical widget sizes (~80-120
        // dp wide for the sparkline cell) 180x30 is already at or
        // above 1:1 pixel mapping. Halving cuts bitmap memory by
        // 75% (4 bytes per pixel x 4x fewer pixels) and trims the
        // Catmull-Rom spline math by the same factor on each tick.
        private const val SPARKLINE_WIDTH_PX = 180
        private const val SPARKLINE_HEIGHT_PX = 30

        // Brand colours for the two traffic curves - green down,
        // blue up. Chosen to stay legible against both light and
        // dark widget backgrounds without a theme-aware swap.
        private const val SPARKLINE_RX_COLOR = 0xFF4ADE80.toInt()
        private const val SPARKLINE_TX_COLOR = 0xFF60A5FA.toInt()

        /**
         * Send a broadcast to refresh all VPN widgets. Called from
         * PrivycsVpnService whenever state changes (including
         * per-tick traffic samples).
         */
        fun sendStatusUpdate(
            context: Context,
            connected: Boolean,
            connectionName: String = "",
            protocol: String = "",
            uptime: Long = 0L,
        ) {
            val intent = Intent(context, VpnWidget::class.java).apply {
                action = ACTION_STATUS_CHANGED
                // setPackage restricts delivery to receivers in our own
                // app package. The intent is already explicit (targets
                // VpnWidget::class.java) so other apps cannot intercept,
                // but Aikido's pattern-matcher flags any sendBroadcast
                // without setPackage as risky. Defense-in-depth +
                // explicit-policy clarification. v0.9.14.10.
                setPackage(context.packageName)
                putExtra(EXTRA_CONNECTED, connected)
                putExtra(EXTRA_CONNECTION_NAME, connectionName)
                putExtra(EXTRA_PROTOCOL, protocol)
                putExtra(EXTRA_UPTIME, uptime)
            }
            context.sendBroadcast(intent)
        }
    }

    override fun onUpdate(
        context: Context,
        appWidgetManager: AppWidgetManager,
        appWidgetIds: IntArray,
    ) {
        val manager = try {
            VpnServiceManager.getInstance(context)
        } catch (_: Exception) {
            null
        }
        val st = manager?.status?.value
        val connected = st?.connected ?: false
        val connectionName = st?.connectionName ?: ""
        val protocol = st?.activeProtocol?.label ?: ""
        val uptime = st?.uptime ?: 0L
        val serverEndpoint = st?.serverEndpoint ?: ""
        val localAddress = st?.localAddress ?: ""
        val rxBytes = st?.rxBytes ?: 0L
        val txBytes = st?.txBytes ?: 0L

        for (appWidgetId in appWidgetIds) {
            updateWidgetWithStatus(
                context, appWidgetManager, appWidgetId,
                connected, connectionName, protocol, uptime, serverEndpoint,
                localAddress, rxBytes, txBytes,
            )
        }
    }

    override fun onReceive(context: Context, intent: Intent) {
        super.onReceive(context, intent)

        when (intent.action) {
            ACTION_TOGGLE -> handleToggle(context)

            ACTION_SWITCH_PROTOCOL -> {
                val target = intent.getStringExtra(EXTRA_TARGET_PROTOCOL) ?: return
                handleProtocolSwitch(context, target)
            }

            ACTION_STATUS_CHANGED -> {
                val connected = intent.getBooleanExtra(EXTRA_CONNECTED, false)
                val connectionName = intent.getStringExtra(EXTRA_CONNECTION_NAME) ?: ""
                val protocol = intent.getStringExtra(EXTRA_PROTOCOL) ?: ""
                val uptime = intent.getLongExtra(EXTRA_UPTIME, 0L)

                // serverEndpoint, localAddress, rx/tx totals are not in
                // the broadcast intent; read them live from the service
                // so every widget row stays accurate even when the
                // sender doesn't attach them. The traffic samples come
                // through SpeedTracker globals anyway.
                val st = try {
                    VpnServiceManager.getInstance(context).status.value
                } catch (_: Exception) {
                    null
                }
                val serverEndpoint = st?.serverEndpoint ?: ""
                val localAddress = st?.localAddress ?: ""
                val rxBytes = st?.rxBytes ?: 0L
                val txBytes = st?.txBytes ?: 0L

                val appWidgetManager = AppWidgetManager.getInstance(context)
                val widgetIds = appWidgetManager.getAppWidgetIds(
                    ComponentName(context, VpnWidget::class.java)
                )
                for (widgetId in widgetIds) {
                    updateWidgetWithStatus(
                        context, appWidgetManager, widgetId,
                        connected, connectionName, protocol, uptime, serverEndpoint,
                        localAddress, rxBytes, txBytes,
                    )
                }
            }
        }
    }

    private fun handleToggle(context: Context) {
        Log.d(TAG, "Widget toggle tapped")
        try {
            val manager = VpnServiceManager.getInstance(context)

            // If a manual pause is active, a widget tap is treated
            // as "resume now": cancel the pause timer and fire a
            // USER-source connect so ConnectCoordinator's gate lets
            // it through (see Gate 3 there).
            if (com.privycs.vpn.util.VpnPauseTimer.isPausedNow()) {
                com.privycs.vpn.util.VpnPauseTimer.cancel()
                // Pool-active wins; same dead-end fix as everywhere
                // else: getActive() is null when a pool is the
                // user's selection, so without this branch the
                // widget's resume-now would silently no-op for
                // pool users.
                val poolReg = com.privycs.vpn.PrivycsApp.instance
                    .poolRepository.registry.value
                val activePoolId = poolReg.activeId
                val activePool = poolReg.pools.firstOrNull { it.id == activePoolId }
                if (activePoolId.isNotEmpty() && activePool != null) {
                    scope.launch {
                        try {
                            com.privycs.vpn.util.ConnectCoordinator.requestPoolConnect(
                                context,
                                com.privycs.vpn.util.ConnectCoordinator.IntentSource.USER,
                                activePoolId,
                                activePool.name,
                            )
                        } catch (e: Exception) {
                            Log.w(TAG, "Resume-now pool connect failed", e)
                        }
                    }
                    return
                }
                val conn = com.privycs.vpn.PrivycsApp.instance.connectionRepository.getActive()
                if (conn == null) {
                    showToast(context, context.getString(R.string.widget_toast_no_active_connection))
                    return
                }
                // Coordinator.requestConnect is suspend and may
                // perform DataStore reads + service intents that
                // can take 100s of ms. Running it via runBlocking
                // on the BroadcastReceiver thread risked an ANR
                // when the coordinator's mutex was held by an
                // ongoing service operation - the receiver only
                // has a 10s budget. Dispatch on the app-level
                // scope so onReceive returns immediately.
                scope.launch {
                    try {
                        com.privycs.vpn.util.ConnectCoordinator.requestConnect(
                            context,
                            com.privycs.vpn.util.ConnectCoordinator.IntentSource.USER,
                            conn,
                        )
                    } catch (e: Exception) {
                        Log.w(TAG, "Resume-now connect failed", e)
                    }
                }
                return
            }

            if (manager.isConnected) {
                if (AlwaysOnDetector.detected.value) {
                    AlwaysOnDetector.pauseFor(context, ALWAYS_ON_WIDGET_PAUSE_MINUTES)
                    Log.i(TAG, "Widget toggle with Always-On: pausing for $ALWAYS_ON_WIDGET_PAUSE_MINUTES min")
                }

                // Direct disconnect mirrors what ConnectScreen does -
                // bypass ConnectCoordinator for the disconnect side so
                // the service teardown is immediate. The follow-up
                // reconnect is also a direct call to avoid the
                // coordinator's "disconnect in progress" gate that
                // was silently rejecting our ON_DEMAND reconnects
                // from the widget in earlier versions.
                manager.disconnect()

                // Schedule COD re-evaluation on the app-level scope
                // so it survives after onReceive returns. Without
                // this, the broadcast receiver is torn down and the
                // delayed reconnect never fires.
                scope.launch {
                    try {
                        val settings = com.privycs.vpn.PrivycsApp.instance
                            .settingsRepository.getSettingsBlocking()
                        if (!settings.connectOnDemand.enabled) return@launch
                        delay(400)
                        val nm = com.privycs.vpn.service.NetworkMonitor.getInstance(context)
                        nm.reevaluate()
                        val ns = nm.networkState.value
                        if (!ns.shouldConnect || manager.isConnected) return@launch
                        Log.i(
                            TAG,
                            "On-demand reconnect after widget disconnect (${ns.ruleMatch})",
                        )
                        manager.connect()
                    } catch (e: Exception) {
                        Log.w(TAG, "COD re-evaluate after widget disconnect failed", e)
                    }
                }
            } else {
                // Pool mode wins over single-connection mode when both
                // are configured. The user explicitly chose a pool as
                // the active target on the Connect screen, so the
                // widget tap must trigger the pool's pick-and-connect
                // path - NOT the legacy single connection. Falls back
                // to the single connection only when no pool is active.
                val poolRegistry = com.privycs.vpn.PrivycsApp.instance
                    .poolRepository.registry.value
                val activePoolId = poolRegistry.activeId
                val activePool = poolRegistry.pools.firstOrNull { it.id == activePoolId }
                if (activePoolId.isNotEmpty() && activePool != null) {
                    // Route pool through ConnectCoordinator so that
                    // the same gates (Kill Switch sinkhole, system-
                    // revoke cooldown, Always-On / manual pause)
                    // and serialisation apply as for single
                    // connection. Pre-Coordinator-pool-aware code
                    // fired ACTION_POOL_CONNECT here directly,
                    // which silently bypassed every gate and was
                    // the same kind of dead-end as the COD-pool
                    // bug NetworkMonitor used to have.
                    scope.launch {
                        try {
                            com.privycs.vpn.util.ConnectCoordinator.requestPoolConnect(
                                context,
                                com.privycs.vpn.util.ConnectCoordinator.IntentSource.WIDGET,
                                activePoolId,
                                activePool.name,
                            )
                        } catch (e: Exception) {
                            Log.w(TAG, "Widget pool connect failed", e)
                        }
                    }
                    return
                }

                val connection = com.privycs.vpn.PrivycsApp.instance.connectionRepository.getActive()
                if (connection == null) {
                    Log.w(TAG, "Widget toggle: no active connection or pool to connect to")
                    showToast(context, context.getString(R.string.widget_toast_no_active_connection))
                    return
                }
                // Same rationale as above: dispatch on app scope
                // instead of runBlocking on the receiver thread to
                // avoid ANR.
                scope.launch {
                    try {
                        com.privycs.vpn.util.ConnectCoordinator.requestConnect(
                            context,
                            com.privycs.vpn.util.ConnectCoordinator.IntentSource.WIDGET,
                            connection,
                        )
                    } catch (e: Exception) {
                        Log.w(TAG, "Widget connect failed", e)
                    }
                }
            }
        } catch (e: Exception) {
            Log.e(TAG, "Toggle failed", e)
            showToast(context, context.getString(R.string.widget_toast_toggle_failed))
        }
    }

    /**
     * Protocol switch from the widget. Gives the user Toast
     * feedback for every outcome - success, no connection, missing
     * protocol config, runtime error - so a silent no-op can't
     * happen anymore.
     */
    private fun handleProtocolSwitch(context: Context, targetProtocolStr: String) {
        Log.d(TAG, "Widget protocol switch requested: $targetProtocolStr")
        val target = VpnProtocol.fromString(targetProtocolStr) ?: run {
            Log.w(TAG, "Unknown protocol string from widget: $targetProtocolStr")
            showToast(context, context.getString(R.string.widget_toast_unknown_protocol))
            return
        }
        try {
            val manager = VpnServiceManager.getInstance(context)
            val active = com.privycs.vpn.PrivycsApp.instance.connectionRepository.getActive()
            if (active == null) {
                showToast(context, context.getString(R.string.widget_toast_no_active_connection))
                return
            }
            if (!active.hasProtocol(target)) {
                showToast(
                    context,
                    context.getString(R.string.widget_toast_protocol_missing, target.label),
                )
                return
            }
            // Already active? Nothing to do but tell the user.
            if (manager.status.value.activeProtocol == target) {
                showToast(
                    context,
                    context.getString(R.string.widget_toast_protocol_already_active, target.label),
                )
                return
            }
            manager.switchProtocol(target)
            showToast(
                context,
                context.getString(R.string.widget_toast_protocol_switching, target.label),
            )
        } catch (e: Exception) {
            Log.e(TAG, "Protocol switch failed", e)
            showToast(context, context.getString(R.string.widget_toast_protocol_switch_failed))
        }
    }

    private fun showToast(context: Context, msg: String) {
        Toast.makeText(context, msg, Toast.LENGTH_SHORT).show()
    }

    private fun updateWidgetWithStatus(
        context: Context,
        appWidgetManager: AppWidgetManager,
        appWidgetId: Int,
        connected: Boolean,
        connectionName: String,
        protocol: String,
        uptime: Long,
        serverEndpoint: String,
        localAddress: String,
        rxBytes: Long,
        txBytes: Long,
    ) {
        val views = RemoteViews(context.packageName, R.layout.widget_vpn)
        val activeProtocol = VpnProtocol.fromString(protocol)

        // KillSwitchManager drives the widget's "danger" state. When
        // SINKHOLE is active, the VPN is down but the block-all tun fd
        // is in place - the user should see the block state, not a
        // grey "disconnected" mask. We read the StateFlow value
        // directly since AppWidgetProvider is a broadcast receiver
        // (no lifecycle scope for collect).
        val killSwitchSinkhole = com.privycs.vpn.util.KillSwitchManager
            .state.value == com.privycs.vpn.util.KillSwitchManager.State.SINKHOLE

        // --- Section 1: Big circular status button ---
        val statusColor = ContextCompat.getColor(
            context,
            when {
                killSwitchSinkhole -> R.color.widget_status_kill_switch
                connected -> R.color.widget_status_connected
                else -> R.color.widget_status_disconnected
            },
        )

        // Circle backdrop tint via colorFilter (SRC_IN, so the oval
        // shape is fully repainted with the tint colour).
        views.setInt(R.id.widget_button_bg, "setColorFilter", statusColor)

        // Halo glow behind the circle. Visible only when the tunnel
        // is up AND the Kill Switch is not in sinkhole - sinkhole
        // already gets a red disk + Kill Switch icon, adding a green
        // halo would be visually contradictory. Disconnected state
        // also stays glow-less so the widget reads as "off".
        views.setViewVisibility(
            R.id.widget_button_halo,
            if (connected && !killSwitchSinkhole) android.view.View.VISIBLE
            else android.view.View.GONE,
        )

        // Resolve the icon in the circle. Sinkhole wins over everything
        // else - the user must see it's the Kill-Switch shield, not
        // the protocol icon. Otherwise: live active protocol when
        // connected, saved active protocol when disconnected (so the
        // circle stays informative even before they reconnect).
        val displayProtocol = activeProtocol
            ?: com.privycs.vpn.PrivycsApp.instance.connectionRepository
                .getActive()?.activeProtocol
        val iconRes = when {
            killSwitchSinkhole -> R.drawable.ic_kill_switch_sinkhole
            displayProtocol == VpnProtocol.WIREGUARD -> R.drawable.ic_protocol_wireguard
            displayProtocol == VpnProtocol.OPENVPN -> R.drawable.ic_protocol_openvpn
            displayProtocol == VpnProtocol.IPSEC -> R.drawable.ic_protocol_strongswan
            else -> R.drawable.ic_privycs_logo
        }
        views.setImageViewResource(R.id.widget_button_icon, iconRes)

        // --- Section 2: Uptime (big monospace) ---
        views.setTextViewText(
            R.id.widget_uptime,
            when {
                killSwitchSinkhole -> context.getString(R.string.widget_status_kill_switch_active)
                connected -> formatUptimeClock(uptime)
                else -> context.getString(R.string.widget_status_disconnected)
            },
        )
        views.setTextColor(R.id.widget_uptime, statusColor)

        // --- Section 3: Connection name (+ chevron is in XML, decorative) ---
        // Pool-aware label. When a pool is the active selection the
        // widget should communicate that explicitly - both so the user
        // understands a tap will go through the pool pick-and-connect
        // path AND so they don't get confused by an unfamiliar member
        // hostname when the round-robin scheduler has rotated.
        // Connected:    "<member-name> · <pool-name>"
        // Disconnected: "Pool: <pool-name>"
        // Single-conn:  unchanged
        val poolRegistry = com.privycs.vpn.PrivycsApp.instance
            .poolRepository.registry.value
        val activePool = poolRegistry.pools.firstOrNull { it.id == poolRegistry.activeId }
        val displayName = when {
            activePool != null && connected && connectionName.isNotBlank() ->
                "$connectionName · ${activePool.name}"
            activePool != null ->
                context.getString(
                    R.string.widget_pool_label_disconnected,
                    activePool.name
                )
            connectionName.isNotBlank() -> connectionName
            else -> context.getString(R.string.app_name)
        }
        views.setTextViewText(R.id.widget_connection_name, displayName)

        // --- Section 4: Protocol pills ---
        // Pool-aware: hide the whole pill row when a pool is active.
        // Pools mix members with different protocols, so the
        // single-protocol picker UI is meaningless for them.
        // Mirrors the same gate on ConnectScreen (`activePool ==
        // null` requirement on the protocol-badges row). Hiding
        // the row also gives the connect button slightly more
        // vertical space, which is the "bigger / clearer" UX win
        // requested for pool users.
        views.setViewVisibility(
            R.id.widget_protocol_pills_row,
            if (activePool != null) android.view.View.GONE
            else android.view.View.VISIBLE,
        )

        // Hide pills for protocols the active connection does NOT
        // have configured. Without this gate the widget always shows
        // all three pills regardless of the connection's actual
        // protocol set, so a connection with only WireGuard renders
        // greyed-out IPSec + OpenVPN pills the user can't switch to.
        // Mirrors the Connect screen's `availableProtocols()`-driven
        // ProtocolBadges row. Pool-active path already hid the whole
        // row above; this is the single-connection refinement.
        val configuredProtocols = com.privycs.vpn.PrivycsApp.instance
            .connectionRepository.getActive()?.availableProtocols() ?: emptyList()
        views.setViewVisibility(
            R.id.widget_protocol_wg,
            if (configuredProtocols.contains(VpnProtocol.WIREGUARD)) android.view.View.VISIBLE else android.view.View.GONE,
        )
        views.setViewVisibility(
            R.id.widget_protocol_ipsec,
            if (configuredProtocols.contains(VpnProtocol.IPSEC)) android.view.View.VISIBLE else android.view.View.GONE,
        )
        views.setViewVisibility(
            R.id.widget_protocol_ovpn,
            if (configuredProtocols.contains(VpnProtocol.OPENVPN)) android.view.View.VISIBLE else android.view.View.GONE,
        )
        setProtocolPillState(
            context, views, VpnProtocol.WIREGUARD,
            R.id.widget_protocol_wg, R.id.widget_protocol_wg_icon,
            R.id.widget_protocol_wg_label, activeProtocol == VpnProtocol.WIREGUARD,
        )
        setProtocolPillState(
            context, views, VpnProtocol.IPSEC,
            R.id.widget_protocol_ipsec, R.id.widget_protocol_ipsec_icon,
            R.id.widget_protocol_ipsec_label, activeProtocol == VpnProtocol.IPSEC,
        )
        setProtocolPillState(
            context, views, VpnProtocol.OPENVPN,
            R.id.widget_protocol_ovpn, R.id.widget_protocol_ovpn_icon,
            R.id.widget_protocol_ovpn_label, activeProtocol == VpnProtocol.OPENVPN,
        )

        // --- Section 4b: Status label inside the circle.
        //
        // The Connect-screen screenshot shows "Connected" inside the
        // green disc as a white label beneath the icon. We mirror that
        // ONLY in the connected state. In disconnected / sinkhole
        // states the widget_uptime field directly below the circle
        // already carries the same status text (grey / red), so
        // duplicating it inside the disc would print "Kill Switch
        // Active" twice on screen. Hide the inner label in those
        // states; the icon expands into the freed space because of
        // weight=1 on the icon ImageView.
        if (connected && !killSwitchSinkhole) {
            views.setViewVisibility(R.id.widget_button_label, android.view.View.VISIBLE)
            views.setTextViewText(
                R.id.widget_button_label,
                context.getString(R.string.widget_status_connected),
            )
        } else {
            views.setViewVisibility(R.id.widget_button_label, android.view.View.GONE)
        }

        // --- Section 5: Endpoint (centered below pills) ---
        // Compose a richer location line: "<flag> <ip> · <city>, <country>"
        // when we have all the pieces, falling back gracefully to
        // just the IP, then to "not connected".
        //
        // We read VpnStatus directly here to pick up the pool-aware
        // fields (activeMemberCountry / activeMemberName) without
        // having to extend updateWidgetWithStatus's signature.
        // SingleConnection path: country/member name come empty so
        // we parse them from connectionName which often follows the
        // same "<cc>-<city3>-<n>" pattern (Mullvad-style).
        val endpointText = if (connected && serverEndpoint.isNotBlank()) {
            val st = try {
                VpnServiceManager.getInstance(context).status.value
            } catch (_: Exception) { null }
            val cc = st?.activeMemberCountry.orEmpty().ifBlank {
                // Fallback for single-connection path: parse cc
                // from the connection name's first segment.
                connectionName.split("-").firstOrNull().orEmpty()
            }
            val nameForCity = st?.activeMemberName.orEmpty().ifBlank { connectionName }
            val flag = com.privycs.vpn.data.PoolHostnameLabels.flagEmojiFromCode(cc)
            val city = com.privycs.vpn.data.PoolHostnameLabels.cityFromHostname(nameForCity)
            val country = com.privycs.vpn.data.PoolHostnameLabels.countryNameFromCode(cc)
            buildString {
                if (flag.isNotEmpty()) append(flag).append("  ")
                append(serverEndpoint)
                val locTail = when {
                    city.isNotEmpty() && country.isNotEmpty() -> "$city, $country"
                    city.isNotEmpty() -> city
                    country.isNotEmpty() -> country
                    else -> ""
                }
                if (locTail.isNotEmpty()) append(" · ").append(locTail)
            }
        } else {
            context.getString(R.string.widget_endpoint_not_connected)
        }
        views.setTextViewText(R.id.widget_endpoint_center, endpointText)

        // --- Section 6: Traffic cards (Download / Upload) ---
        val rxHistory = SpeedTracker.rxSpeedHistory.value
        val txHistory = SpeedTracker.txSpeedHistory.value
        views.setImageViewBitmap(
            R.id.widget_sparkline_rx,
            WidgetSparklineRenderer.render(
                rxHistory, SPARKLINE_RX_COLOR, SPARKLINE_WIDTH_PX, SPARKLINE_HEIGHT_PX,
                cacheBucket = WidgetSparklineRenderer.BUCKET_RX,
            ),
        )
        views.setImageViewBitmap(
            R.id.widget_sparkline_tx,
            WidgetSparklineRenderer.render(
                txHistory, SPARKLINE_TX_COLOR, SPARKLINE_WIDTH_PX, SPARKLINE_HEIGHT_PX,
                cacheBucket = WidgetSparklineRenderer.BUCKET_TX,
            ),
        )
        views.setTextViewText(R.id.widget_rx_total, formatBytes(rxBytes))
        views.setTextViewText(R.id.widget_tx_total, formatBytes(txBytes))
        views.setTextViewText(
            R.id.widget_rx_value,
            SpeedTracker.formatSpeed(SpeedTracker.latestRxBps()),
        )
        views.setTextViewText(
            R.id.widget_tx_value,
            SpeedTracker.formatSpeed(SpeedTracker.latestTxBps()),
        )

        // --- Section 7: VPN IP row ---
        views.setTextViewText(
            R.id.widget_vpn_ip,
            if (connected && localAddress.isNotBlank()) localAddress else "—",
        )

        // --- Section 8: Endpoint row ---
        views.setTextViewText(
            R.id.widget_endpoint_value,
            if (connected && serverEndpoint.isNotBlank()) serverEndpoint else "—",
        )

        // --- Click targets ---

        // Body tap opens the app. Attached to root background so
        // taps outside the circle + protocol row + traffic row
        // stay as "open app" affordance.
        val openAppIntent = Intent(context, MainActivity::class.java).apply {
            flags = Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TOP
        }
        views.setOnClickPendingIntent(
            android.R.id.background,
            PendingIntent.getActivity(
                context, 0, openAppIntent,
                PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
            ),
        )

        // Big circle = toggle. Click target is the entire
        // FrameLayout container so a mis-hit within the 68dp circle
        // area can't be routed to the root by the launcher.
        views.setOnClickPendingIntent(
            R.id.widget_button_container,
            PendingIntent.getBroadcast(
                context, appWidgetId,
                Intent(context, VpnWidget::class.java).apply { action = ACTION_TOGGLE },
                PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
            ),
        )

        // Protocol switcher clicks - unique PendingIntent per
        // (widget, protocol) via bit-packed requestCode AND a
        // per-target URI so Android doesn't dedupe.
        views.setOnClickPendingIntent(
            R.id.widget_protocol_wg,
            protocolSwitchPendingIntent(context, appWidgetId, VpnProtocol.WIREGUARD),
        )
        views.setOnClickPendingIntent(
            R.id.widget_protocol_ovpn,
            protocolSwitchPendingIntent(context, appWidgetId, VpnProtocol.OPENVPN),
        )
        views.setOnClickPendingIntent(
            R.id.widget_protocol_ipsec,
            protocolSwitchPendingIntent(context, appWidgetId, VpnProtocol.IPSEC),
        )

        appWidgetManager.updateAppWidget(appWidgetId, views)
    }

    /**
     * Match the Connect-screen ProtocolBadges visual: when active,
     * the pill takes a per-protocol brand-colour fill at low alpha
     * (0x33 / ~20%) plus a 1dp stroke at slightly higher alpha
     * (0x4D / ~30%), and the icon + label are tinted to the full-
     * strength brand colour. When inactive, the neutral grey
     * inactive drawable is used and the icon/label render in the
     * default secondary text colour.
     *
     * Per-protocol active drawables are needed because RemoteViews
     * cannot apply a colorFilter to a shape drawable's solid/stroke
     * channels independently - we need three separate fully-baked
     * drawables.
     */
    private fun setProtocolPillState(
        context: Context,
        views: RemoteViews,
        protocol: VpnProtocol,
        pillId: Int,
        iconId: Int,
        labelId: Int,
        active: Boolean,
    ) {
        val bg = if (active) {
            when (protocol) {
                VpnProtocol.WIREGUARD -> R.drawable.widget_protocol_button_active_wg
                VpnProtocol.IPSEC -> R.drawable.widget_protocol_button_active_ipsec
                VpnProtocol.OPENVPN -> R.drawable.widget_protocol_button_active_ovpn
            }
        } else {
            R.drawable.widget_protocol_button_inactive
        }
        views.setInt(pillId, "setBackgroundResource", bg)

        // Brand colours - mirrors WireGuardRed / IpSecBlue /
        // OpenVpnOrange in ui/theme/Theme.kt. Hardcoded here rather
        // than loaded from colors.xml because they're already
        // fixed-string brand identifiers - no theme-aware swap.
        val brand = when (protocol) {
            VpnProtocol.WIREGUARD -> 0xFF88171A.toInt()
            VpnProtocol.IPSEC -> 0xFF2563EB.toInt()
            VpnProtocol.OPENVPN -> 0xFFEA7E20.toInt()
        }
        val inactiveTint = ContextCompat.getColor(context, R.color.widget_text_secondary)

        val tint = if (active) brand else inactiveTint
        views.setInt(iconId, "setColorFilter", tint)
        views.setTextColor(labelId, tint)
    }

    private fun protocolSwitchPendingIntent(
        context: Context,
        appWidgetId: Int,
        target: VpnProtocol,
    ): PendingIntent {
        val requestCode = (appWidgetId shl 2) or target.ordinal
        val intent = Intent(context, VpnWidget::class.java).apply {
            action = ACTION_SWITCH_PROTOCOL
            putExtra(EXTRA_TARGET_PROTOCOL, target.name.lowercase())
            data = android.net.Uri.parse("privycs-widget://switch/$appWidgetId/${target.name.lowercase()}")
        }
        return PendingIntent.getBroadcast(
            context, requestCode, intent,
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
        )
    }

    /**
     * HH:MM:SS uptime for the big widget clock. Always fixed-width
     * so the monospace layout doesn't jitter as digits grow.
     */
    private fun formatUptimeClock(seconds: Long): String {
        val s = if (seconds < 0) 0L else seconds
        val hours = s / 3600
        val minutes = (s % 3600) / 60
        val secs = s % 60
        return String.format("%02d:%02d:%02d", hours, minutes, secs)
    }

    /**
     * Human-readable byte total for RX/TX card values. Uses base-2
     * (KiB/MiB) binning since VPN stats are kernel counters, not
     * marketing megabits. String formatted with a decimal comma on
     * de-DE locale matches the in-app Connect screen formatting.
     */
    private fun formatBytes(bytes: Long): String {
        if (bytes < 1024) return "$bytes B"
        val units = arrayOf("KB", "MB", "GB", "TB")
        var v = bytes.toDouble() / 1024.0
        var idx = 0
        while (v >= 1024.0 && idx < units.size - 1) {
            v /= 1024.0
            idx++
        }
        return String.format("%.1f %s", v, units[idx])
    }
}
