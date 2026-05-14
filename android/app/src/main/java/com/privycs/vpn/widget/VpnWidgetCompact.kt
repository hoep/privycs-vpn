package com.privycs.vpn.widget

import android.app.PendingIntent
import android.appwidget.AppWidgetManager
import android.appwidget.AppWidgetProvider
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.widget.RemoteViews
import androidx.core.content.ContextCompat
import com.privycs.vpn.MainActivity
import com.privycs.vpn.R
import com.privycs.vpn.data.models.VpnProtocol
import com.privycs.vpn.service.VpnServiceManager

/**
 * 2x2 compact home-screen widget — connect button only.
 *
 * Strips the main VpnWidget down to just the big circular connect
 * button (state-aware tint, protocol icon, tap = toggle). Layout
 * shares the widget_button_* view-IDs with the main layout so the
 * render code is a near-byte copy of the main widget's "Section 1".
 *
 * Toggle wiring: the click PendingIntent targets [VpnWidget]'s
 * ACTION_TOGGLE handler, NOT this class. That keeps the toggle
 * logic (pause-handling, pool-vs-single routing, KS-sinkhole
 * refusal) centralised — both widgets dispatch into the same
 * code path. State-change broadcasts (ACTION_STATUS_CHANGED) are
 * received here independently so the compact widget redraws
 * alongside the main one.
 */
class VpnWidgetCompact : AppWidgetProvider() {

    override fun onUpdate(
        context: Context,
        appWidgetManager: AppWidgetManager,
        appWidgetIds: IntArray,
    ) {
        for (id in appWidgetIds) {
            renderCompact(context, appWidgetManager, id)
        }
    }

    override fun onReceive(context: Context, intent: Intent) {
        super.onReceive(context, intent)
        // STATUS_CHANGED is the same broadcast the main widget
        // consumes — service-emitted on every connect / disconnect /
        // state-transition tick. We re-render all compact instances
        // on each broadcast.
        if (intent.action == VpnWidget.ACTION_STATUS_CHANGED ||
            intent.action == AppWidgetManager.ACTION_APPWIDGET_UPDATE
        ) {
            val mgr = AppWidgetManager.getInstance(context)
            val ids = mgr.getAppWidgetIds(
                ComponentName(context, VpnWidgetCompact::class.java)
            )
            for (id in ids) {
                renderCompact(context, mgr, id)
            }
        }
    }

    private fun renderCompact(
        context: Context,
        appWidgetManager: AppWidgetManager,
        appWidgetId: Int,
    ) {
        val status = try {
            VpnServiceManager.getInstance(context).status.value
        } catch (_: Exception) {
            null
        }
        val connected = status?.connected ?: false
        val activeProtocol = status?.activeProtocol
        val killSwitchSinkhole = com.privycs.vpn.util.KillSwitchManager
            .state.value == com.privycs.vpn.util.KillSwitchManager.State.SINKHOLE

        val views = RemoteViews(context.packageName, R.layout.widget_vpn_compact)

        // ---- Connect button render (mirrors VpnWidget's Section 1) ----
        val statusColor = ContextCompat.getColor(
            context,
            when {
                killSwitchSinkhole -> R.color.widget_status_kill_switch
                connected -> R.color.widget_status_connected
                else -> R.color.widget_status_disconnected
            },
        )
        views.setInt(R.id.widget_button_bg, "setColorFilter", statusColor)

        views.setViewVisibility(
            R.id.widget_button_halo,
            if (connected && !killSwitchSinkhole) android.view.View.VISIBLE
            else android.view.View.GONE,
        )

        // Display the active protocol's icon when one is known, or
        // fall back to the connection-registry's saved active when
        // status is empty (disconnected state right after launch).
        // Same logic as the main widget's displayProtocol resolution.
        val displayProtocol = activeProtocol
            ?: com.privycs.vpn.PrivycsApp.instance.connectionRepository
                .getActive()?.activeProtocol
        val iconRes = when {
            killSwitchSinkhole -> R.drawable.ic_kill_switch_sinkhole
            // v0.9.15.25: all protocols render their mono/alpha
            // variant + state-driven tint (white connected, dark grey
            // disconnected), matching the in-app Connect-screen
            // formatting. See VpnWidget for the rationale.
            displayProtocol == VpnProtocol.AMNEZIAWG -> R.drawable.ic_protocol_amneziawg_mono
            displayProtocol == VpnProtocol.WIREGUARD -> R.drawable.ic_protocol_wireguard
            displayProtocol == VpnProtocol.OPENVPN -> R.drawable.ic_protocol_openvpn
            displayProtocol == VpnProtocol.IPSEC -> R.drawable.ic_protocol_strongswan
            else -> R.drawable.ic_privycs_logo
        }
        views.setImageViewResource(R.id.widget_button_icon, iconRes)
        val iconTint: Int = when {
            killSwitchSinkhole -> 0
            connected -> 0xFFFFFFFF.toInt()
            else -> 0xFF202124.toInt()
        }
        views.setInt(R.id.widget_button_icon, "setColorFilter", iconTint)

        // ---- Click targets ----
        // Body tap (everywhere on the widget) opens the main app.
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

        // Big circle = toggle. Click forwards to VpnWidget's
        // ACTION_TOGGLE handler so the toggle logic stays
        // centralised across both widget classes. Unique
        // requestCode per (widget, appWidgetId) so Android doesn't
        // collapse multiple compact widgets' PendingIntents.
        views.setOnClickPendingIntent(
            R.id.widget_button,
            PendingIntent.getBroadcast(
                context, appWidgetId,
                Intent(context, VpnWidget::class.java).apply { action = VpnWidget.ACTION_TOGGLE },
                PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
            ),
        )

        appWidgetManager.updateAppWidget(appWidgetId, views)
    }
}
