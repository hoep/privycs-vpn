package com.privycs.vpn.service

import android.app.AlarmManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.os.Build
import android.os.PowerManager
import android.util.Log
import com.privycs.vpn.receiver.PoolAlarmReceiver

/**
 * Schedules pool-rotation events via AlarmManager.
 *
 * Architecture decision (vs desktop's ticker goroutine):
 *
 *   Desktop runs `time.NewTicker(5s)` continuously and checks each
 *   tick whether `scheduledRotation` has been crossed. That's fine
 *   on Desktop where Goroutines are free, but on Android it would
 *   either:
 *     a) Use a Coroutine-with-delay-loop tied to the Service's
 *        lifecycle — wakes the CPU constantly + dies on Service-kill.
 *     b) Use WorkManager — minimum interval 15min, useless for
 *        pools with 5-min rotation.
 *
 *   Instead we schedule TWO precise alarms per cycle:
 *     PRE_WARM at scheduledRotation - 60s
 *     ROTATE at scheduledRotation
 *
 *   Between alarms NOTHING runs. AlarmManager wakes the CPU briefly,
 *   the BroadcastReceiver dispatches to the service, the work runs,
 *   the next alarm gets scheduled, the service idles or stops.
 *
 *   Doze + Battery-Saver:
 *     setExactAndAllowWhileIdle is the right primitive — fires even
 *     in Doze (limited to once per ~9min in Doze, but rotation is
 *     rarely faster than that anyway) and respects Battery-Saver.
 *     The user can opt into "ignore battery optimizations" if they
 *     want sub-9min rotation in Doze (we prompt on first activate).
 */
class PoolRotationScheduler(private val context: Context) {

    private val alarmManager = context.getSystemService(Context.ALARM_SERVICE) as AlarmManager
    private val powerManager = context.getSystemService(Context.POWER_SERVICE) as PowerManager

    /**
     * Cancel all pending pool alarms. Call when the active pool
     * is deactivated or the user switches to a non-pool connection.
     */
    fun cancelAll() {
        alarmManager.cancel(prewarmIntent())
        alarmManager.cancel(rotateIntent())
        Log.d(TAG, "alarms cancelled")
    }

    /**
     * Arm both pre-warm and rotate alarms for the next cycle.
     * intervalMs is the full rotation interval; pre-warm fires
     * 60s before rotation, rotation at interval-end.
     *
     * If idle-aware is on AND the device is in interactive use
     * (screen on or recently used), we still arm — Doze coordination
     * happens via setExactAndAllowWhileIdle which handles the
     * deferral semantics for us.
     *
     * Battery-saver halves rotation frequency: in saver mode, the
     * actual scheduled interval is doubled. Prevents pool churn
     * draining the user's last 5%.
     */
    fun arm(poolId: String, intervalMin: Int) {
        var effectiveInterval = intervalMin.toLong() * 60 * 1000L
        if (powerManager.isPowerSaveMode) {
            effectiveInterval *= 2
            Log.d(TAG, "battery-saver active: doubling rotation interval to ${effectiveInterval}ms")
        }

        val now = System.currentTimeMillis()
        val rotateAt = now + effectiveInterval
        val preWarmAt = rotateAt - PRE_WARM_LEAD_MS

        // Skip pre-warm entirely for short intervals where the lead
        // window would collide with the rotation moment. Mirrors
        // desktop's intervalSeconds <= preWarmLeadSeconds gate.
        val canPreWarm = preWarmAt > now + 5_000  // 5s safety margin

        if (canPreWarm) {
            scheduleExactAlarm(preWarmAt, prewarmIntent(poolId))
        }
        scheduleExactAlarm(rotateAt, rotateIntent(poolId))

        Log.i(TAG, "armed pool=$poolId rotateAt=${rotateAt - now}ms preWarmAt=${if (canPreWarm) "${preWarmAt - now}ms" else "skipped"}")
    }

    private fun scheduleExactAlarm(triggerAtMs: Long, pi: PendingIntent) {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.M) {
            // S+ requires SCHEDULE_EXACT_ALARM permission for setExact*.
            // setExactAndAllowWhileIdle does NOT need it (Doze-managed
            // imprecision in exchange for whitelist access). This is
            // the right tradeoff for pool rotation: ±9min in Doze is
            // acceptable, exact-when-active is what the user perceives.
            alarmManager.setExactAndAllowWhileIdle(AlarmManager.RTC_WAKEUP, triggerAtMs, pi)
        } else {
            alarmManager.setExact(AlarmManager.RTC_WAKEUP, triggerAtMs, pi)
        }
    }

    private fun prewarmIntent(poolId: String? = null): PendingIntent {
        val intent = Intent(context, PoolAlarmReceiver::class.java).apply {
            action = ACTION_PRE_WARM
            poolId?.let { putExtra(EXTRA_POOL_ID, it) }
        }
        return PendingIntent.getBroadcast(
            context, REQ_PREWARM, intent,
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
        )
    }

    private fun rotateIntent(poolId: String? = null): PendingIntent {
        val intent = Intent(context, PoolAlarmReceiver::class.java).apply {
            action = ACTION_ROTATE
            poolId?.let { putExtra(EXTRA_POOL_ID, it) }
        }
        return PendingIntent.getBroadcast(
            context, REQ_ROTATE, intent,
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
        )
    }

    companion object {
        private const val TAG = "PoolScheduler"

        const val ACTION_PRE_WARM = "com.privycs.vpn.action.POOL_PRE_WARM"
        const val ACTION_ROTATE = "com.privycs.vpn.action.POOL_ROTATE"
        const val EXTRA_POOL_ID = "pool_id"

        private const val REQ_PREWARM = 0xA001
        private const val REQ_ROTATE = 0xA002

        const val PRE_WARM_LEAD_MS = 60_000L
    }
}
