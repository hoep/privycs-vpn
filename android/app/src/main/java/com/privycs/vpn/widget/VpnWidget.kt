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
        private const val SPARKLINE_WIDTH_PX = 360
        private const val SPARKLINE_HEIGHT_PX = 60

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

        for (appWidgetId in appWidgetIds) {
            updateWidgetWithStatus(
                context, appWidgetManager, appWidgetId,
                connected, connectionName, protocol, uptime, serverEndpoint,
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

                // serverEndpoint is not in the broadcast intent;
                // read it live from the service so the header stays
                // accurate even when the sender doesn't attach it.
                val serverEndpoint = try {
                    VpnServiceManager.getInstance(context).status.value.serverEndpoint
                } catch (_: Exception) {
                    ""
                }

                val appWidgetManager = AppWidgetManager.getInstance(context)
                val widgetIds = appWidgetManager.getAppWidgetIds(
                    ComponentName(context, VpnWidget::class.java)
                )
                for (widgetId in widgetIds) {
                    updateWidgetWithStatus(
                        context, appWidgetManager, widgetId,
                        connected, connectionName, protocol, uptime, serverEndpoint,
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
                val conn = com.privycs.vpn.PrivycsApp.instance.connectionRepository.getActive()
                if (conn == null) {
                    showToast(context, context.getString(R.string.widget_toast_no_active_connection))
                    return
                }
                kotlinx.coroutines.runBlocking<Unit> {
                    com.privycs.vpn.util.ConnectCoordinator.requestConnect(
                        context,
                        com.privycs.vpn.util.ConnectCoordinator.IntentSource.USER,
                        conn,
                    )
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
                val connection = com.privycs.vpn.PrivycsApp.instance.connectionRepository.getActive()
                if (connection == null) {
                    Log.w(TAG, "Widget toggle: no active connection to connect to")
                    showToast(context, context.getString(R.string.widget_toast_no_active_connection))
                    return
                }
                kotlinx.coroutines.runBlocking {
                    com.privycs.vpn.util.ConnectCoordinator.requestConnect(
                        context,
                        com.privycs.vpn.util.ConnectCoordinator.IntentSource.WIDGET,
                        connection,
                    )
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
    ) {
        val views = RemoteViews(context.packageName, R.layout.widget_vpn)
        val activeProtocol = VpnProtocol.fromString(protocol)

        // --- Section 1: Header ---
        views.setTextViewText(
            R.id.widget_header_name,
            connectionName.ifBlank { context.getString(R.string.app_name) },
        )
        views.setTextViewText(
            R.id.widget_header_endpoint,
            if (connected && serverEndpoint.isNotBlank()) serverEndpoint
            else context.getString(R.string.widget_endpoint_not_connected),
        )

        // --- Section 2: Big circular connect button ---
        val statusColor = ContextCompat.getColor(
            context,
            if (connected) R.color.widget_status_connected
            else R.color.widget_status_disconnected,
        )

        // Circle backdrop tint via colorFilter (SRC_IN, so the oval
        // shape is fully repainted with the tint colour).
        views.setInt(R.id.widget_button_bg, "setColorFilter", statusColor)

        // Resolve the protocol to show in the button. When connected
        // we trust the live VpnServiceManager status. When disconnected
        // we fall back to the active connection's saved activeProtocol
        // so the user still sees "WG" / "OVPN" / "IPSec" - the icon
        // reflects what they'd connect to, not a generic shield.
        val displayProtocol = activeProtocol
            ?: com.privycs.vpn.PrivycsApp.instance.connectionRepository
                .getActive()?.activeProtocol
        val iconRes = when (displayProtocol) {
            VpnProtocol.WIREGUARD -> R.drawable.ic_protocol_wireguard
            VpnProtocol.OPENVPN -> R.drawable.ic_protocol_openvpn
            VpnProtocol.IPSEC -> R.drawable.ic_protocol_strongswan
            null -> R.drawable.ic_privycs_logo // no connections at all
        }
        views.setImageViewResource(R.id.widget_button_icon, iconRes)

        views.setTextViewText(
            R.id.widget_button_label,
            if (connected) {
                "${context.getString(R.string.widget_status_connected)} ${formatUptime(uptime)}".trim()
            } else {
                context.getString(R.string.widget_status_disconnected)
            },
        )
        views.setTextColor(R.id.widget_button_label, statusColor)

        // --- Section 3: Protocol switcher ---
        setProtocolButtonState(views, R.id.widget_protocol_wg, activeProtocol == VpnProtocol.WIREGUARD)
        setProtocolButtonState(views, R.id.widget_protocol_ovpn, activeProtocol == VpnProtocol.OPENVPN)
        setProtocolButtonState(views, R.id.widget_protocol_ipsec, activeProtocol == VpnProtocol.IPSEC)

        // --- Section 4: Traffic sparklines + values ---
        val rxHistory = SpeedTracker.rxSpeedHistory.value
        val txHistory = SpeedTracker.txSpeedHistory.value
        views.setImageViewBitmap(
            R.id.widget_sparkline_rx,
            WidgetSparklineRenderer.render(
                rxHistory, SPARKLINE_RX_COLOR, SPARKLINE_WIDTH_PX, SPARKLINE_HEIGHT_PX,
            ),
        )
        views.setImageViewBitmap(
            R.id.widget_sparkline_tx,
            WidgetSparklineRenderer.render(
                txHistory, SPARKLINE_TX_COLOR, SPARKLINE_WIDTH_PX, SPARKLINE_HEIGHT_PX,
            ),
        )
        views.setTextViewText(
            R.id.widget_rx_value,
            SpeedTracker.formatSpeed(SpeedTracker.latestRxBps()),
        )
        views.setTextViewText(
            R.id.widget_tx_value,
            SpeedTracker.formatSpeed(SpeedTracker.latestTxBps()),
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

    private fun setProtocolButtonState(
        views: RemoteViews,
        viewId: Int,
        active: Boolean,
    ) {
        val bg = if (active) R.drawable.widget_protocol_button_active
        else R.drawable.widget_protocol_button_inactive
        views.setInt(viewId, "setBackgroundResource", bg)
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

    private fun formatUptime(seconds: Long): String {
        if (seconds <= 0) return ""
        val hours = seconds / 3600
        val minutes = (seconds % 3600) / 60
        val secs = seconds % 60
        return when {
            hours > 0 -> String.format("%dh %02dm", hours, minutes)
            minutes > 0 -> String.format("%dm %02ds", minutes, secs)
            else -> String.format("%ds", secs)
        }
    }
}
