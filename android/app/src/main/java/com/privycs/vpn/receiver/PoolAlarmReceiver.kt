package com.privycs.vpn.receiver

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.os.Build
import android.util.Log
import com.privycs.vpn.service.PoolRotationScheduler
import com.privycs.vpn.service.PrivycsVpnService

/**
 * Receives AlarmManager-fired pool rotation events.
 *
 * Lifecycle: BroadcastReceiver onReceive runs briefly (10s ANR
 * budget) on the main thread. We must NOT do work directly here;
 * we hand off to PrivycsVpnService.startService with an action.
 * The service then has its full lifecycle (foreground service +
 * coroutine scope) to do the pre-warm or rotation work.
 *
 * This receiver is registered in AndroidManifest.xml to survive
 * process death — the alarm fires even if the app process was
 * killed since the alarm was set, and the receiver re-spawns the
 * process for the duration of the work.
 */
class PoolAlarmReceiver : BroadcastReceiver() {

    override fun onReceive(context: Context, intent: Intent) {
        val poolId = intent.getStringExtra(PoolRotationScheduler.EXTRA_POOL_ID).orEmpty()
        Log.i(TAG, "alarm received: action=${intent.action} pool=$poolId")

        val serviceAction = when (intent.action) {
            PoolRotationScheduler.ACTION_PRE_WARM -> PrivycsVpnService.ACTION_POOL_PRE_WARM
            PoolRotationScheduler.ACTION_ROTATE -> PrivycsVpnService.ACTION_POOL_ROTATE
            else -> {
                Log.w(TAG, "unknown action: ${intent.action}")
                return
            }
        }

        val serviceIntent = Intent(context, PrivycsVpnService::class.java).apply {
            action = serviceAction
            putExtra(PoolRotationScheduler.EXTRA_POOL_ID, poolId)
        }

        // From Android 8.0+, background services can only be started
        // from the foreground. PoolAlarmReceiver IS a foreground
        // context (briefly) so startForegroundService is allowed
        // here, and the service must call startForeground() within
        // 5 seconds of being started this way.
        try {
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
                context.startForegroundService(serviceIntent)
            } else {
                context.startService(serviceIntent)
            }
        } catch (e: Exception) {
            // SecurityException or IllegalStateException can happen
            // if the system has restricted us. Loud log; the next
            // alarm cycle will retry.
            Log.e(TAG, "service start failed: ${e.message}", e)
        }
    }

    companion object {
        private const val TAG = "PoolAlarmReceiver"
    }
}
