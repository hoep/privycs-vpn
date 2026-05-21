package com.privycs.vpn.util

import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import androidx.core.app.NotificationCompat
import androidx.core.app.NotificationManagerCompat
import com.privycs.vpn.MainActivity
import com.privycs.vpn.R

/**
 * Event notifications (v1).
 *
 * Distinct from the ongoing foreground-service notification
 * (PrivycsApp.NOTIFICATION_CHANNEL_VPN) which reflects *current state*.
 * This object fires one-shot notifications on *transitions* the user
 * actually wants to know about, on three channels the user configures
 * in Android's per-app notification settings (Settings has a row that
 * deep-links there — there is intentionally no in-app per-event
 * toggle, the system channels own that):
 *
 *   - [CHANNEL_SECURITY]    HIGH    — kill-switch sinkhole (traffic
 *                                     blocked). Trust/leak-awareness.
 *   - [CHANNEL_STATUS]      DEFAULT — on-demand auto connect/disconnect
 *                                     and protocol failover.
 *   - [CHANNEL_DIAGNOSTICS] LOW     — verbose on-demand decision log.
 *                                     Off-by-default in practice: the
 *                                     channel is silent/min and the
 *                                     user can disable it outright.
 *
 * One stable notification id per channel: event notifications REPLACE
 * the previous one of the same channel (never stack) so a WiFi↔mobile
 * flap can't spam the shade.
 *
 * Every post is permission- and exception-safe: a missing
 * POST_NOTIFICATIONS grant (Android 13+) or a disabled channel must
 * never crash a background coroutine or a tunnel callback.
 */
object EventNotifier {
    const val CHANNEL_SECURITY = "events_security"
    const val CHANNEL_STATUS = "events_status"
    const val CHANNEL_DIAGNOSTICS = "events_diagnostics"

    private const val ID_SECURITY = 1001
    private const val ID_STATUS = 1002
    private const val ID_DIAGNOSTICS = 1003
    private const val ID_TUNNEL_DEGRADED = 1004

    // NOTE: Android locks a channel's importance to user control
    // after first creation — these levels only apply to fresh
    // installs (or after the user clears data). They are intentionally
    // unobtrusive: only the security channel may interrupt; status is
    // silent shade-only; diagnostics is minimised. Users tune further
    // in system per-app notification settings (Settings deep-link).
    /** Registered once from PrivycsApp.createNotificationChannels(). */
    fun createChannels(mgr: NotificationManager) {
        mgr.createNotificationChannel(
            NotificationChannel(
                CHANNEL_SECURITY,
                "Security alerts",
                NotificationManager.IMPORTANCE_HIGH,
            ).apply {
                description = "Kill switch / loss-of-protection alerts"
            },
        )
        mgr.createNotificationChannel(
            NotificationChannel(
                CHANNEL_STATUS,
                "Connection events",
                NotificationManager.IMPORTANCE_LOW,
            ).apply {
                description =
                    "On-demand auto connect/disconnect and protocol failover"
                setShowBadge(false)
            },
        )
        mgr.createNotificationChannel(
            NotificationChannel(
                CHANNEL_DIAGNOSTICS,
                "On-demand diagnostics",
                NotificationManager.IMPORTANCE_MIN,
            ).apply {
                description =
                    "Verbose on-demand decision log (disable this channel " +
                    "if you don't want it)"
                setShowBadge(false)
            },
        )
    }

    private fun contentIntent(ctx: Context): PendingIntent {
        val i = Intent(ctx, MainActivity::class.java).apply {
            flags = Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TOP
        }
        return PendingIntent.getActivity(
            ctx,
            0,
            i,
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
        )
    }

    private fun post(
        ctx: Context,
        id: Int,
        channel: String,
        title: String,
        text: String,
        ongoing: Boolean = false,
    ) {
        try {
            val nm = NotificationManagerCompat.from(ctx)
            if (!nm.areNotificationsEnabled()) return
            val n = NotificationCompat.Builder(ctx, channel)
                .setSmallIcon(R.drawable.ic_privycs_shield)
                .setContentTitle(title)
                .setContentText(text)
                .setStyle(NotificationCompat.BigTextStyle().bigText(text))
                .setContentIntent(contentIntent(ctx))
                .setAutoCancel(!ongoing)
                .setOngoing(ongoing)
                .setOnlyAlertOnce(true)
                .build()
            nm.notify(id, n)
        } catch (_: SecurityException) {
            // POST_NOTIFICATIONS not granted (API 33+) — silent no-op.
        } catch (_: Exception) {
            // Never let a notification failure break the caller
            // (background coroutine / tunnel callback).
        }
    }

    private fun cancel(ctx: Context, id: Int) {
        try {
            NotificationManagerCompat.from(ctx).cancel(id)
        } catch (_: Exception) {
        }
    }

    // ---- security ----
    fun sinkholeEngaged(ctx: Context) = post(
        ctx,
        ID_SECURITY,
        CHANNEL_SECURITY,
        "Kill switch active",
        "All traffic is blocked until the VPN reconnects — you are not " +
            "leaking, but you are also offline.",
        ongoing = true,
    )

    fun sinkholeCleared(ctx: Context) = cancel(ctx, ID_SECURITY)

    // ---- status (COD + failover share ID_STATUS → mutually replacing) ----
    fun codConnected(ctx: Context, reason: String) = post(
        ctx,
        ID_STATUS,
        CHANNEL_STATUS,
        "Auto-connected",
        reason.ifBlank { "Connect-on-demand rule matched this network." },
    )

    fun codDisconnected(ctx: Context, reason: String) = post(
        ctx,
        ID_STATUS,
        CHANNEL_STATUS,
        "Auto-disconnected",
        reason.ifBlank { "Trusted network — VPN not needed here." },
    )

    fun failover(ctx: Context, text: String) = post(
        ctx,
        ID_STATUS,
        CHANNEL_STATUS,
        "Switched connection",
        text,
    )

    // ---- security: tunnel up but not passing traffic ----
    // v0.9.15.74 (audit item C). A USER-initiated tunnel the health
    // monitor has declared dead (consecutive ping failures). Auto-
    // recovery is deliberately OFF for USER tunnels — the user
    // controls their own tunnel — but without this notification they
    // would sit "connected" with no traffic and no signal. HIGH-
    // importance channel: a loss-of-protection condition the user
    // must see. Own id (not ID_SECURITY) so it coexists with a
    // kill-switch alert instead of replacing it.
    fun tunnelDegraded(ctx: Context) = post(
        ctx,
        ID_TUNNEL_DEGRADED,
        CHANNEL_SECURITY,
        "VPN not passing traffic",
        "The tunnel is connected but no traffic is getting through. " +
            "Open Privycs VPN and reconnect.",
    )

    fun tunnelDegradedCleared(ctx: Context) = cancel(ctx, ID_TUNNEL_DEGRADED)

    // ---- diagnostics (opt-in via system channel) ----
    fun diagnostics(ctx: Context, text: String) = post(
        ctx,
        ID_DIAGNOSTICS,
        CHANNEL_DIAGNOSTICS,
        "On-demand",
        text,
    )
}
