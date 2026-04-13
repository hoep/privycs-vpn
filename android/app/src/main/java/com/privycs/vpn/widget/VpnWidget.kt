package com.privycs.vpn.widget

import android.app.PendingIntent
import android.appwidget.AppWidgetManager
import android.appwidget.AppWidgetProvider
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.util.Log
import android.widget.RemoteViews
import com.privycs.vpn.MainActivity
import com.privycs.vpn.R
import com.privycs.vpn.service.PrivycsVpnService
import com.privycs.vpn.service.VpnServiceManager

/**
 * Home screen widget displaying VPN connection status.
 * Shows connection state, name, protocol badge, and uptime.
 * Tapping the widget opens the app; tapping the action button toggles connection.
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

        /**
         * Send a broadcast to update all VPN widgets with current status.
         * Call this from PrivycsVpnService when status changes.
         */
        fun sendStatusUpdate(
            context: Context,
            connected: Boolean,
            connectionName: String = "",
            protocol: String = "",
            uptime: Long = 0L
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
        appWidgetIds: IntArray
    ) {
        for (appWidgetId in appWidgetIds) {
            updateWidget(context, appWidgetManager, appWidgetId)
        }
    }

    override fun onReceive(context: Context, intent: Intent) {
        super.onReceive(context, intent)

        when (intent.action) {
            ACTION_TOGGLE -> {
                handleToggle(context)
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
                        connected, connectionName, protocol, uptime
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
                val intent = Intent(context, PrivycsVpnService::class.java).apply {
                    action = PrivycsVpnService.ACTION_DISCONNECT
                }
                context.startService(intent)
            } else {
                manager.connect()
            }
        } catch (e: Exception) {
            Log.e(TAG, "Toggle failed", e)
        }
    }

    private fun updateWidget(
        context: Context,
        appWidgetManager: AppWidgetManager,
        appWidgetId: Int
    ) {
        // Default state: disconnected
        updateWidgetWithStatus(
            context, appWidgetManager, appWidgetId,
            connected = false,
            connectionName = "",
            protocol = "",
            uptime = 0L
        )
    }

    private fun updateWidgetWithStatus(
        context: Context,
        appWidgetManager: AppWidgetManager,
        appWidgetId: Int,
        connected: Boolean,
        connectionName: String,
        protocol: String,
        uptime: Long
    ) {
        val views = RemoteViews(context.packageName, R.layout.widget_vpn)

        // Status text
        if (connected) {
            views.setTextViewText(R.id.widget_status, "Connected")
            views.setTextColor(R.id.widget_status, 0xFF4CAF50.toInt())
            views.setImageViewResource(R.id.widget_icon, android.R.drawable.ic_lock_lock)
            views.setTextViewText(R.id.widget_connection_name, connectionName.ifBlank { "Privycs VPN" })
            views.setTextViewText(R.id.widget_protocol, protocol.ifBlank { "" })
            views.setTextViewText(R.id.widget_uptime, formatUptime(uptime))
        } else {
            views.setTextViewText(R.id.widget_status, "Disconnected")
            views.setTextColor(R.id.widget_status, 0xFF9E9E9E.toInt())
            views.setImageViewResource(R.id.widget_icon, android.R.drawable.ic_lock_idle_lock)
            views.setTextViewText(R.id.widget_connection_name, "Privycs VPN")
            views.setTextViewText(R.id.widget_protocol, "")
            views.setTextViewText(R.id.widget_uptime, "")
        }

        // Open app on widget tap
        val openAppIntent = Intent(context, MainActivity::class.java)
        val openAppPending = PendingIntent.getActivity(
            context, 0, openAppIntent,
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT
        )
        views.setOnClickPendingIntent(R.id.widget_root, openAppPending)

        // Toggle VPN on action button tap
        val toggleIntent = Intent(context, VpnWidget::class.java).apply {
            action = ACTION_TOGGLE
        }
        val togglePending = PendingIntent.getBroadcast(
            context, 1, toggleIntent,
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT
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
