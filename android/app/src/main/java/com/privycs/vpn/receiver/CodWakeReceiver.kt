package com.privycs.vpn.receiver

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.util.Log

/**
 * Process-wake-on-network-change receiver, fired by a
 * `PendingIntent`-bound `ConnectivityManager.NetworkCallback`
 * registered persistently in `CodWakeRegistrar`.
 *
 * Why this exists separately from the runtime [NetworkMonitor]
 * NetworkCallback: the runtime callback is registered from
 * NetworkMonitor.start() which lives inside our application
 * process. When aggressive OEM battery-killers (Xiaomi MIUI,
 * Samsung One UI, Huawei EMUI, Oppo, Vivo) terminate our
 * foreground service even with `stopWithTask=false`, the
 * runtime callback dies with the process and we miss every
 * subsequent network change until the user opens the app
 * (which re-spawns the process). User-reported symptom: "VPN
 * stays connected for 5 minutes on except-SSID until I open
 * the app — then it disconnects instantly". Process was dead;
 * opening it triggered onCreate → NetworkMonitor.start() →
 * evaluateCurrentNetwork() → fires the long-overdue disconnect.
 *
 * The `PendingIntent`-based NetworkCallback (API 31+) instead
 * delivers an `Intent` to a Manifest-registered receiver, which
 * Android can target even when our process is dead — it spawns
 * a fresh process to handle the broadcast. We then re-init
 * `NetworkMonitor` and run a single evaluation, which trips
 * the same on-demand connect/disconnect path the live tick
 * would have. Result: ≤2 s reaction even after process kill.
 *
 * On API < 31 this receiver is a no-op (registration is
 * skipped in CodWakeRegistrar). Older Android relies on the
 * runtime NetworkCallback + foreground service surviving.
 */
class CodWakeReceiver : BroadcastReceiver() {

    companion object {
        private const val TAG = "CodWakeReceiver"
    }

    override fun onReceive(context: Context, intent: Intent) {
        Log.i(TAG, "fired: action=${intent.action} extras=${intent.extras?.keySet()}")
        try {
            val nm = com.privycs.vpn.service.NetworkMonitor
                .getInstance(context.applicationContext)
            // Idempotent — start() returns early if already running.
            nm.start()
            // Force a single evaluation immediately. The runtime
            // tick (10 s) would catch this within 10 s anyway IF
            // it's running, but the whole point of this code path
            // is the case where the tick ISN'T running because the
            // process was just spawned for this broadcast.
            nm.reevaluate()
        } catch (t: Throwable) {
            Log.e(TAG, "evaluate after wake failed", t)
        }
    }
}
