package com.privycs.vpn.widget

import android.app.PendingIntent
import android.appwidget.AppWidgetManager
import android.appwidget.AppWidgetProvider
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.graphics.Color
import android.util.Log
import android.widget.RemoteViews
import androidx.core.content.ContextCompat
import com.privycs.vpn.MainActivity
import com.privycs.vpn.R
import com.privycs.vpn.data.models.VpnProtocol
import com.privycs.vpn.service.VpnServiceManager
import com.privycs.vpn.util.AlwaysOnDetector
import com.privycs.vpn.util.SpeedTracker

/**
 * Home-screen widget that mirrors the app's Connect screen in
 * compact form:
 *
 *   +--------------------------------------+
 *   |  [LOGO]  Connected      12m 34s      |  <- section 1 (logo = toggle)
 *   |          HomeRouter                  |
 *   |--------------------------------------|
 *   |  [ WG ]  [ OVPN ]  [ IPSec ]         |  <- section 2 (protocol switch)
 *   |--------------------------------------|
 *   |  ↓ ▁▂▃▄▅▆▇        1.2 MB/s           |  <- section 3 (sparklines)
 *   |  ↑ ▁▂▃▄▅▆          512 KB/s          |
 *   +--------------------------------------+
 *
 * Interaction:
 * - Tap on the logo toggles connect / disconnect (same affordance
 *   as the big circular button in the app).
 * - Tap on a protocol icon switches the active protocol on the
 *   current connection. The active protocol gets a filled accent
 *   background; inactive ones sit on the surface-variant tone.
 * - Tap anywhere else on the widget body opens MainActivity.
 *
 * Update path:
 * - PrivycsVpnService calls [sendStatusUpdate] on every state
 *   transition and every traffic-polling tick (~2s). That broadcast
 *   triggers a full widget refresh including two freshly rendered
 *   sparkline bitmaps derived from SpeedTracker's rolling history.
 * - Sparkline bitmaps are rendered at a fixed logical size and then
 *   stretched via ImageView scaleType=fitXY; avoids having to know
 *   the user's current widget width at render time.
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

        // Default pause window when the user hits the widget toggle
        // while Always-On is active. See handleToggle for rationale.
        private const val ALWAYS_ON_WIDGET_PAUSE_MINUTES = 15

        // Sparkline bitmap logical size. The actual on-screen size
        // is driven by the ImageView layout (fitXY scales the bitmap
        // to fit); we render at a resolution that stays crisp on the
        // widest reasonable widget size without being wasteful.
        private const val SPARKLINE_WIDTH_PX = 360
        private const val SPARKLINE_HEIGHT_PX = 60

        // Line colors matching the in-app ConnectScreen design.
        // Green for RX (download), blue for TX (upload). Both tones
        // are chosen to stay legible against both light and dark
        // widget backgrounds without a theme-aware swap.
        private const val SPARKLINE_RX_COLOR = 0xFF4ADE80.toInt() // green-400
        private const val SPARKLINE_TX_COLOR = 0xFF60A5FA.toInt() // blue-400

        /**
         * Send a broadcast to update all VPN widgets with current
         * status. Called from PrivycsVpnService whenever state
         * changes (including per-tick traffic polls).
         *
         * Speed samples are not passed as extras; the widget reads
         * them directly from [SpeedTracker] at refresh time to avoid
         * serialising 30-element float lists through Bundle twice
         * per second.
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
        // On provider add / system refresh, pull current state from
        // the service so the first frame shows the truth.
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

        for (appWidgetId in appWidgetIds) {
            updateWidgetWithStatus(
                context, appWidgetManager, appWidgetId,
                connected, connectionName, protocol, uptime,
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

                val appWidgetManager = AppWidgetManager.getInstance(context)
                val widgetIds = appWidgetManager.getAppWidgetIds(
                    ComponentName(context, VpnWidget::class.java)
                )
                for (widgetId in widgetIds) {
                    updateWidgetWithStatus(
                        context, appWidgetManager, widgetId,
                        connected, connectionName, protocol, uptime,
                    )
                }
            }
        }
    }

    private fun handleToggle(context: Context) {
        Log.d(TAG, "Widget toggle tapped")
        try {
            val manager = VpnServiceManager.getInstance(context)
            if (manager.isConnected) {
                // Always-On guard: raw disconnect is neutered by the
                // OS auto-respawn, so set a pause flag and fire the
                // disconnect. The service's handleAlwaysOnReconnect
                // honors the flag and skips reconnect until expiry.
                if (AlwaysOnDetector.detected.value) {
                    AlwaysOnDetector.pauseFor(context, ALWAYS_ON_WIDGET_PAUSE_MINUTES)
                    Log.i(TAG, "Widget toggle with Always-On: pausing for $ALWAYS_ON_WIDGET_PAUSE_MINUTES min")
                }
                kotlinx.coroutines.runBlocking {
                    com.privycs.vpn.util.ConnectCoordinator.requestDisconnect(
                        context,
                        com.privycs.vpn.util.ConnectCoordinator.IntentSource.WIDGET,
                    )
                }
            } else {
                val connection = com.privycs.vpn.PrivycsApp.instance.connectionRepository.getActive()
                if (connection == null) {
                    Log.w(TAG, "Widget toggle: no active connection to connect to")
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
        }
    }

    /**
     * Tapping a protocol icon switches the current connection's
     * active protocol and, if currently connected, the tunnel
     * restarts on the new protocol. Matches the behaviour of the
     * in-app protocol switcher.
     */
    private fun handleProtocolSwitch(context: Context, targetProtocolStr: String) {
        Log.d(TAG, "Widget protocol switch requested: $targetProtocolStr")
        val target = VpnProtocol.fromString(targetProtocolStr) ?: run {
            Log.w(TAG, "Unknown protocol string from widget: $targetProtocolStr")
            return
        }
        try {
            val manager = VpnServiceManager.getInstance(context)
            manager.switchProtocol(target)
        } catch (e: Exception) {
            Log.e(TAG, "Protocol switch failed", e)
        }
    }

    private fun updateWidgetWithStatus(
        context: Context,
        appWidgetManager: AppWidgetManager,
        appWidgetId: Int,
        connected: Boolean,
        connectionName: String,
        protocol: String,
        uptime: Long,
    ) {
        val views = RemoteViews(context.packageName, R.layout.widget_vpn)

        // --- Section 1: Logo tint + status text + connection name + uptime ---
        val statusColor = ContextCompat.getColor(
            context,
            if (connected) R.color.widget_status_connected
            else R.color.widget_status_disconnected,
        )
        // Tint the logo ImageView via the legacy setColorFilter path
        // (available since API 7; RemoteViews.setColorStateList for
        // setImageTintList requires API 31 and minSdk is 26).
        views.setInt(R.id.widget_icon, "setColorFilter", statusColor)

        if (connected) {
            views.setTextViewText(
                R.id.widget_status,
                context.getString(R.string.widget_status_connected),
            )
            views.setTextColor(R.id.widget_status, statusColor)
            views.setTextViewText(R.id.widget_uptime, formatUptime(uptime))
        } else {
            views.setTextViewText(
                R.id.widget_status,
                context.getString(R.string.widget_status_disconnected),
            )
            views.setTextColor(R.id.widget_status, statusColor)
            views.setTextViewText(R.id.widget_uptime, "")
        }
        views.setTextViewText(
            R.id.widget_connection_name,
            connectionName.ifBlank { context.getString(R.string.app_name) },
        )

        // --- Section 2: Protocol switcher backgrounds ---
        val activeProtocol = VpnProtocol.fromString(protocol)
        setProtocolButtonState(views, R.id.widget_protocol_wg, activeProtocol == VpnProtocol.WIREGUARD)
        setProtocolButtonState(views, R.id.widget_protocol_ovpn, activeProtocol == VpnProtocol.OPENVPN)
        setProtocolButtonState(views, R.id.widget_protocol_ipsec, activeProtocol == VpnProtocol.IPSEC)

        // --- Section 3: Sparkline bitmaps + throughput values ---
        val rxHistory = SpeedTracker.rxSpeedHistory.value
        val txHistory = SpeedTracker.txSpeedHistory.value
        val rxBitmap = WidgetSparklineRenderer.render(
            rxHistory, SPARKLINE_RX_COLOR, SPARKLINE_WIDTH_PX, SPARKLINE_HEIGHT_PX,
        )
        val txBitmap = WidgetSparklineRenderer.render(
            txHistory, SPARKLINE_TX_COLOR, SPARKLINE_WIDTH_PX, SPARKLINE_HEIGHT_PX,
        )
        views.setImageViewBitmap(R.id.widget_sparkline_rx, rxBitmap)
        views.setImageViewBitmap(R.id.widget_sparkline_tx, txBitmap)
        views.setTextViewText(
            R.id.widget_rx_value,
            SpeedTracker.formatSpeed(SpeedTracker.latestRxBps()),
        )
        views.setTextViewText(
            R.id.widget_tx_value,
            SpeedTracker.formatSpeed(SpeedTracker.latestTxBps()),
        )

        // --- Click targets ---

        // Body tap opens the app. Attached to the root background
        // so any tap outside the icon / protocol row / stays as
        // "open app" affordance.
        val openAppIntent = Intent(context, MainActivity::class.java).apply {
            flags = Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TOP
        }
        val openAppPending = PendingIntent.getActivity(
            context, 0, openAppIntent,
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
        )
        views.setOnClickPendingIntent(android.R.id.background, openAppPending)

        // Logo = toggle. Unique requestCode per widget instance so
        // multiple widgets on the home screen each get their own
        // PendingIntent slot.
        val togglePending = PendingIntent.getBroadcast(
            context, appWidgetId,
            Intent(context, VpnWidget::class.java).apply { action = ACTION_TOGGLE },
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
        )
        views.setOnClickPendingIntent(R.id.widget_icon, togglePending)

        // Protocol switcher click targets. requestCode includes both
        // the widget id and a protocol-specific nibble so buttons on
        // the same widget don't coalesce into the same PendingIntent.
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
        // Bit-pack the widgetId with a per-protocol marker so each
        // (widget, protocol) pair has a unique request code. Shift
        // widgetId up by 2 bits and OR in the protocol ordinal.
        val requestCode = (appWidgetId shl 2) or target.ordinal
        val intent = Intent(context, VpnWidget::class.java).apply {
            action = ACTION_SWITCH_PROTOCOL
            putExtra(EXTRA_TARGET_PROTOCOL, target.name.lowercase())
            // Include widgetId + protocol in the intent data so
            // Android's PendingIntent deduplication treats them as
            // distinct (equal Intents + equal request codes coalesce).
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
