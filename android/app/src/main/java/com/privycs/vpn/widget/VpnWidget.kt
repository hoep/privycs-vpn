package com.privycs.vpn.widget

import android.app.PendingIntent
import android.appwidget.AppWidgetManager
import android.appwidget.AppWidgetProvider
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.util.Log
import android.widget.RemoteViews
import androidx.core.content.ContextCompat
import com.privycs.vpn.MainActivity
import com.privycs.vpn.R
import com.privycs.vpn.service.PrivycsVpnService
import com.privycs.vpn.service.VpnServiceManager
import com.privycs.vpn.util.AlwaysOnDetector

/**
 * Home-screen widget showing VPN connection status.
 *
 * Render contract (v0.9.3.12+):
 * - Root view uses @android:id/background so the Android 12+ launcher
 *   applies its widget chrome (rounded corners, Material You tint)
 *   to our own background drawable instead of overlaying its own.
 * - Colors are resolved from @color/widget_* tokens which have
 *   light/dark variants in values-night, so RemoteViews (which run
 *   in the launcher's resource context) render correctly on both
 *   themes without needing runtime theme-attr lookup.
 * - Always-On: when Android's system Always-On VPN is active and
 *   the widget's toggle would disconnect, we honor the existing
 *   app-level pause flag via AlwaysOnDetector.pauseFor() for a
 *   safe default pause window - otherwise the launcher's tap fires
 *   a stopSelf which the OS undoes within 1s, and the widget looks
 *   broken exactly like the in-app disconnect did pre-0.9.3.11.
 *
 * Status propagation: PrivycsVpnService fires sendStatusUpdate() at
 * every lifecycle transition (connect success, state change,
 * disconnect, polling loop), so the widget refreshes at most 500ms
 * after the tunnel's state actually changes.
 */
class VpnWidget : AppWidgetProvider() {

    companion object {
        private const val TAG = "VpnWidget"
        const val ACTION_TOGGLE = "com.privycs.vpn.widget.TOGGLE"
        const val ACTION_STATUS_CHANGED = "com.privycs.vpn.widget.STATUS_CHANGED"
        const val EXTRA_CONNECTED = "connected"
        const val EXTRA_CONNECTION_NAME = "connection_name"
        const val EXTRA_PROTOCOL = "protocol"
        const val EXTRA_UPTIME = "uptime"

        // Default pause window when user hits the widget toggle while
        // Always-On is active. 15 min balances "I need net access for
        // a quick task" (5 min might be too short) against "I forgot
        // and want VPN back soon" (60 min is too lax).
        private const val ALWAYS_ON_WIDGET_PAUSE_MINUTES = 15

        /**
         * Send a broadcast to update all VPN widgets with current status.
         * Call this from PrivycsVpnService whenever status changes.
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
        // VpnServiceManager so the first frame shows the truth, not
        // a hardcoded "Disconnected". Matches behavior users expect
        // when placing a widget while the tunnel is already up via
        // always-on.
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
                // Always-On guard: a raw disconnect is neutered by the
                // OS auto-respawn, so instead set a 15min pause flag
                // and fire the disconnect. The service's
                // handleAlwaysOnReconnect honors the flag and skips
                // the reconnect until the timer expires.
                if (AlwaysOnDetector.detected.value) {
                    AlwaysOnDetector.pauseFor(context, ALWAYS_ON_WIDGET_PAUSE_MINUTES)
                    Log.i(TAG, "Widget toggle with Always-On: pausing for $ALWAYS_ON_WIDGET_PAUSE_MINUTES min")
                }
                manager.disconnect()
            } else {
                manager.connect()
            }
        } catch (e: Exception) {
            Log.e(TAG, "Toggle failed", e)
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

        // Status label + color
        if (connected) {
            views.setTextViewText(
                R.id.widget_status,
                context.getString(R.string.widget_status_connected),
            )
            views.setTextColor(
                R.id.widget_status,
                ContextCompat.getColor(context, R.color.widget_status_connected),
            )
            views.setTextViewText(
                R.id.widget_connection_name,
                connectionName.ifBlank { context.getString(R.string.app_name) },
            )
            views.setTextViewText(R.id.widget_uptime, formatUptime(uptime))
            views.setTextViewText(
                R.id.widget_toggle_button,
                context.getString(R.string.widget_disconnect),
            )
        } else {
            views.setTextViewText(
                R.id.widget_status,
                context.getString(R.string.widget_status_disconnected),
            )
            views.setTextColor(
                R.id.widget_status,
                ContextCompat.getColor(context, R.color.widget_status_disconnected),
            )
            views.setTextViewText(
                R.id.widget_connection_name,
                connectionName.ifBlank { context.getString(R.string.app_name) },
            )
            views.setTextViewText(R.id.widget_uptime, "")
            views.setTextViewText(
                R.id.widget_toggle_button,
                context.getString(R.string.widget_connect),
            )
        }

        // Tap anywhere on the widget body (excluding the toggle pill)
        // opens the app. We wire it on the root-background view so
        // taps on the icon + status + connection name all route to
        // MainActivity, while the toggle button has its own tap
        // target attached separately below.
        val openAppIntent = Intent(context, MainActivity::class.java).apply {
            flags = Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TOP
        }
        val openAppPending = PendingIntent.getActivity(
            context, 0, openAppIntent,
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
        )
        views.setOnClickPendingIntent(android.R.id.background, openAppPending)

        // Toggle VPN on button tap. Unique requestCode per widget
        // instance so multiple widgets on the same home screen get
        // their own PendingIntent slot (Android docs: PendingIntents
        // with equal Intent extras + action coalesce, widgetId in
        // requestCode prevents that).
        val toggleIntent = Intent(context, VpnWidget::class.java).apply {
            action = ACTION_TOGGLE
        }
        val togglePending = PendingIntent.getBroadcast(
            context, appWidgetId, toggleIntent,
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
        )
        views.setOnClickPendingIntent(R.id.widget_toggle_button, togglePending)

        appWidgetManager.updateAppWidget(appWidgetId, views)
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
