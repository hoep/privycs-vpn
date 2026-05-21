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
 *     setAndAllowWhileIdle is the right primitive — inexact (needs
 *     no alarm permission) yet still fires in Doze (limited to once
 *     per ~9min there, but rotation is rarely faster anyway) and
 *     respects Battery-Saver. The user can opt into "ignore battery
 *     optimizations" if they want sub-9min rotation in Doze (we
 *     prompt on first activate).
 */
class PoolRotationScheduler(private val context: Context) {

    private val alarmManager = context.getSystemService(Context.ALARM_SERVICE) as AlarmManager
    private val powerManager = context.getSystemService(Context.POWER_SERVICE) as PowerManager

    /**
     * Monotonic sequence number for arm() calls. Bumped on every
     * arm; embedded in the alarm Intent's extras as EXTRA_SEQ.
     * The receiving service compares incoming-seq vs latest-arm-seq
     * (loaded from process-shared state) and DROPS stale intents.
     *
     * Use case: user manually triggers rotation (tap Connect on a
     * pool) just as the scheduled rotation timer fires. Two
     * intents land in the BroadcastReceiver queue back-to-back;
     * without de-duplication they'd both run pickAndConnect on
     * the same pool, creating a teardown/bringUp/teardown collision
     * that can leave the tunnel in a half-up state. With the
     * sequence guard the older one is silently dropped.
     */
    @Volatile
    private var armSequence: Long = 0L

    /**
     * Cancel all pending pool alarms. Call when the active pool
     * is deactivated or the user switches to a non-pool connection.
     *
     * Note: PendingIntent matching ignores extras - only the action
     * + requestCode + flags identify a slot. So calling cancel with
     * a poolId-less Intent matches whatever poolId-bearing Intent
     * was previously armed for the same requestCode.
     */
    fun cancelAll() {
        alarmManager.cancel(prewarmIntent())
        alarmManager.cancel(rotateIntent())
        // Bump sequence: any in-flight intent firing AFTER this
        // cancel (e.g. queued before cancel reached the OS) carries
        // the old seq number and will be dropped by the handler's
        // seq-check.
        armSequence += 1
        latestArmSequence.set(armSequence)
        Log.d(TAG, "alarms cancelled (seq now $armSequence)")
    }

    /**
     * Returns the interval in ms that the scheduler will ACTUALLY
     * use given the current power state. The UI countdown reads
     * this so the displayed "next rotation in X" matches reality
     * when battery-saver doubled the schedule.
     */
    fun effectiveIntervalMs(intervalMin: Int): Long {
        var ms = intervalMin.toLong() * 60 * 1000L
        if (powerManager.isPowerSaveMode) ms *= 2
        return ms
    }

    /**
     * True if battery-saver doubled the rotation interval. UI
     * surfaces this as a hint badge so the user understands why
     * rotation is slower than configured.
     */
    val isBatterySaverActive: Boolean
        get() = powerManager.isPowerSaveMode

    /**
     * Arm both pre-warm and rotate alarms for the next cycle.
     * intervalMs is the full rotation interval; pre-warm fires
     * 60s before rotation, rotation at interval-end.
     *
     * If idle-aware is on AND the device is in interactive use
     * (screen on or recently used), we still arm — Doze coordination
     * happens via setAndAllowWhileIdle which handles the deferral
     * semantics for us.
     *
     * Battery-saver halves rotation frequency: in saver mode, the
     * actual scheduled interval is doubled. Prevents pool churn
     * draining the user's last 5%.
     */
    fun arm(poolId: String, intervalMin: Int) {
        val seq: Long
        // Single critical section for the seq + currentPoolId +
        // currentIntervalMin tuple so the battery-saver receiver
        // (which reads them) cannot observe a partial update where
        // armSequence already shows the new value but currentPoolId
        // still points at the previous pool. @Volatile alone gives
        // per-field visibility but not multi-field atomicity.
        synchronized(this) {
            armSequence += 1
            seq = armSequence
            latestArmSequence.set(seq)
            currentPoolId = poolId
            currentIntervalMin = intervalMin
        }
        ensureBatterySaverReceiverRegistered()

        var effectiveInterval = intervalMin.toLong() * 60 * 1000L
        if (powerManager.isPowerSaveMode) {
            effectiveInterval *= 2
            Log.d(TAG, "battery-saver active: doubling rotation interval to ${effectiveInterval}ms")
        }

        val now = System.currentTimeMillis()
        val rotateAt = now + effectiveInterval
        val preWarmAt = rotateAt - PRE_WARM_LEAD_MS

        val canPreWarm = preWarmAt > now + 5_000

        if (canPreWarm) {
            scheduleAlarm(preWarmAt, prewarmIntent(poolId, seq))
        }
        scheduleAlarm(rotateAt, rotateIntent(poolId, seq))

        Log.i(TAG, "armed pool=$poolId seq=$seq rotateAt=${rotateAt - now}ms preWarmAt=${if (canPreWarm) "${preWarmAt - now}ms" else "skipped"}")
    }

    private fun scheduleAlarm(triggerAtMs: Long, pi: PendingIntent) {
        // v0.9.15.75: setAndAllowWhileIdle (inexact). It needs NO
        // alarm permission — unlike setExact* / setExactAndAllowWhileIdle,
        // which on Android 12+ require SCHEDULE_EXACT_ALARM (not auto-
        // granted for apps targeting 14+) or the Play-restricted
        // USE_EXACT_ALARM. Pool rotation tolerates the OS batching
        // window: the interval is minutes and the receiver re-arms the
        // next cycle on fire, so drift never compounds. Still fires in
        // Doze (≈once per 9 min cap there).
        alarmManager.setAndAllowWhileIdle(AlarmManager.RTC_WAKEUP, triggerAtMs, pi)
    }

    private fun prewarmIntent(poolId: String? = null, seq: Long = 0L): PendingIntent {
        val intent = Intent(context, PoolAlarmReceiver::class.java).apply {
            action = ACTION_PRE_WARM
            poolId?.let { putExtra(EXTRA_POOL_ID, it) }
            putExtra(EXTRA_ARM_SEQ, seq)
        }
        return PendingIntent.getBroadcast(
            context, REQ_PREWARM, intent,
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
        )
    }

    private fun rotateIntent(poolId: String? = null, seq: Long = 0L): PendingIntent {
        val intent = Intent(context, PoolAlarmReceiver::class.java).apply {
            action = ACTION_ROTATE
            poolId?.let { putExtra(EXTRA_POOL_ID, it) }
            putExtra(EXTRA_ARM_SEQ, seq)
        }
        return PendingIntent.getBroadcast(
            context, REQ_ROTATE, intent,
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
        )
    }

    /**
     * Battery-saver state change handler. When isPowerSaveMode
     * flips, arm() needs to be re-issued because the EFFECTIVE
     * interval (which doubles in saver mode) was baked into the
     * already-scheduled alarm. Without re-arming, a saver-toggle
     * mid-cycle leaves the next rotation tick at an interval
     * that no longer matches the user's setting + saver state.
     *
     * Registered lazily on first arm() and re-uses the same
     * receiver instance until cancelAll() is called (we don't
     * unregister there because the receiver is cheap to keep
     * around and re-arming when a future pool starts).
     */
    private var batterySaverReceiver: android.content.BroadcastReceiver? = null
    // Volatile so the battery-saver BroadcastReceiver thread sees
    // the latest values written by arm() (which runs on whichever
    // coroutine called it). Without volatile, JIT could keep these
    // in a register on the writer thread and the reader sees
    // stale "" / 0 from the previous boot, re-arming with empty
    // poolId or zero interval.
    @Volatile private var currentPoolId: String? = null
    @Volatile private var currentIntervalMin: Int = 0

    private fun ensureBatterySaverReceiverRegistered() {
        if (batterySaverReceiver != null) return
        batterySaverReceiver = object : android.content.BroadcastReceiver() {
            override fun onReceive(c: android.content.Context, i: Intent) {
                // Snapshot both fields under the same lock arm()
                // uses so the pair is consistent (no torn read of
                // poolId from cycle N + interval from cycle N+1).
                val pool: String
                val interval: Int
                synchronized(this@PoolRotationScheduler) {
                    pool = currentPoolId ?: return
                    interval = currentIntervalMin
                }
                Log.i(TAG, "battery-saver state changed - re-arming pool=$pool")
                arm(pool, interval)
            }
        }
        try {
            val filter = android.content.IntentFilter(
                PowerManager.ACTION_POWER_SAVE_MODE_CHANGED
            )
            // RECEIVER_NOT_EXPORTED on T+ (ContextCompat does the
            // right thing on older API levels). System broadcasts
            // are exempt from the export requirement but the API
            // still requires the flag.
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
                context.registerReceiver(
                    batterySaverReceiver, filter,
                    Context.RECEIVER_NOT_EXPORTED
                )
            } else {
                context.registerReceiver(batterySaverReceiver, filter)
            }
        } catch (e: Exception) {
            Log.w(TAG, "battery-saver receiver register failed: ${e.message}")
            batterySaverReceiver = null
        }
    }

    /**
     * Tear down the battery-saver receiver if it was registered.
     * Idempotent. Called from PrivycsVpnService.onDestroy so the
     * receiver doesn't leak across service-respawn cycles - Android
     * logs IntentReceiverLeaked when a service is destroyed without
     * unregistering all receivers it registered.
     *
     * Earlier comment in this file claimed "we don't unregister
     * because the receiver is cheap to keep around" - that was wrong:
     * the leak is loud (visible in logcat as a stack trace + warning)
     * AND the receiver references the Service via its closure-over
     * `this@PoolRotationScheduler`, so the dead Service instance
     * can't be GC'd until the Application process exits.
     */
    fun unregisterBatterySaverReceiver() {
        val r = batterySaverReceiver ?: return
        try {
            context.unregisterReceiver(r)
        } catch (e: Exception) {
            // Already-unregistered or never-fully-registered are
            // both expected race outcomes. Don't crash the service
            // teardown for it.
            Log.d(TAG, "battery-saver unregister: ${e.message}")
        } finally {
            batterySaverReceiver = null
        }
    }

    companion object {
        private const val TAG = "PoolScheduler"

        const val ACTION_PRE_WARM = "com.privycs.vpn.action.POOL_PRE_WARM"
        const val ACTION_ROTATE = "com.privycs.vpn.action.POOL_ROTATE"
        const val EXTRA_POOL_ID = "pool_id"
        const val EXTRA_ARM_SEQ = "arm_seq"

        private const val REQ_PREWARM = 0xA001
        private const val REQ_ROTATE = 0xA002

        const val PRE_WARM_LEAD_MS = 60_000L

        /**
         * Process-shared latest sequence. Reused across
         * scheduler instances (e.g. transient instances from
         * PoolRepository.delete()) so the receiver-side seq
         * check is consistent regardless of who armed.
         */
        val latestArmSequence = java.util.concurrent.atomic.AtomicLong(0L)
    }
}
