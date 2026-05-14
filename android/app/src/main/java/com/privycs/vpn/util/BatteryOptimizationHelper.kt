package com.privycs.vpn.util

import android.annotation.SuppressLint
import android.app.Activity
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.os.PowerManager
import android.provider.Settings

/**
 * Battery-optimization exemption helper (v0.9.15.24).
 *
 * Why this exists: Connect-on-Demand needs to detect WiFi changes
 * reliably even while the device is in Doze / idle. Foreground
 * services alone are NOT sufficient — Android defers
 * NetworkCallback delivery (including the runtime callback inside
 * NetworkMonitor AND the PendingIntent-based CodWakeReceiver) to
 * the ~15 min maintenance window unless the app is on the user's
 * battery-optimization-exemption list.
 *
 * The exemption is a legitimate Google Play use case for VPN apps
 * with auto-connect-on-network-rules behaviour. Without it the
 * user-reported symptom is "phone joins except-list WiFi while
 * pocket-idle, VPN stays connected, only disconnects after wake."
 * With it, NetworkCallback fires real-time and the rule kicks in
 * within seconds.
 *
 * UX: caller (SettingsScreen) checks isIgnoringBatteryOptimizations
 * when the user toggles COD on. If false, fires the system dialog
 * via openBatteryOptimizationSettings. The user makes the call —
 * we don't auto-confirm or coerce. Once granted, the OS persists
 * the exemption across reboots until the user manually revokes it.
 */
object BatteryOptimizationHelper {

    /**
     * True iff this app is on the user's battery-optimization-
     * exemption list. False means Doze rules apply to us (deferred
     * NetworkCallbacks, deferred broadcasts, deferred alarms).
     */
    fun isIgnoringBatteryOptimizations(ctx: Context): Boolean {
        return try {
            val pm = ctx.getSystemService(Context.POWER_SERVICE) as PowerManager
            pm.isIgnoringBatteryOptimizations(ctx.packageName)
        } catch (_: Throwable) {
            // Pre-API-23 doesn't have the API; treat as exempt
            // since Doze restrictions don't apply there either.
            true
        }
    }

    /**
     * Open the system "Battery optimization exemption" dialog for
     * THIS app. User taps "Allow" → exemption granted. Activity
     * context required because the system dialog is an Activity.
     *
     * The intent uses ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS
     * which Android opens with a single-app confirmation prompt —
     * specifically not the full battery-optimization list which
     * would require the user to find our app among dozens of
     * entries. Google Play permits this intent for documented
     * VPN-auto-connect / health-monitoring / always-on use cases;
     * Privycs VPN's Connect-on-Demand falls inside that policy
     * scope.
     *
     * lint suppress: the lint rule is overly conservative and
     * flags every BatteryNotLowConstraint use as "may be removed";
     * we have a clear use case + Play policy fit so the suppression
     * is appropriate.
     */
    @SuppressLint("BatteryLife")
    fun openBatteryOptimizationDialog(activity: Activity) {
        val intent = Intent(Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS).apply {
            data = Uri.parse("package:${activity.packageName}")
        }
        try {
            activity.startActivity(intent)
        } catch (_: Throwable) {
            // Fall back to the full battery-optimization list. Some
            // OEM ROMs strip the per-package dialog; the list view
            // is the universally-supported fallback.
            try {
                activity.startActivity(Intent(Settings.ACTION_IGNORE_BATTERY_OPTIMIZATION_SETTINGS))
            } catch (_: Throwable) {
                /* Nothing we can do — user navigates manually via
                   system Settings → Battery → Battery optimization. */
            }
        }
    }
}
