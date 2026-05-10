package com.privycs.vpn.util

import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.net.ConnectivityManager
import android.net.NetworkCapabilities
import android.net.NetworkRequest
import android.os.Build
import android.util.Log
import com.privycs.vpn.receiver.CodWakeReceiver

/**
 * Registers a process-death-surviving NetworkCallback that
 * fires a `PendingIntent` on every change to the underlying
 * (non-VPN) physical network. The PendingIntent targets
 * [CodWakeReceiver], a Manifest-registered BroadcastReceiver,
 * which Android can launch by spawning a fresh process if our
 * own process was terminated by the OEM battery killer.
 *
 * Without this, on aggressive OEMs (Xiaomi MIUI, Samsung One
 * UI, Huawei EMUI, Oppo, Vivo) a Wi-Fi change while the user's
 * phone was idle in their pocket would NOT trigger our COD
 * disconnect — our process was dead. User-reported v0.9.14.96
 * test: "5 min on except-SSID, no disconnect, opened app and
 * disconnect fired immediately."
 *
 * Available on API 31 (Android 12) and above. On older Android
 * we fall back to the runtime NetworkCallback in NetworkMonitor
 * and the foreground service surviving. minSdk = 26 so a small
 * fraction of users on API 26-30 still rely on the older path.
 */
object CodWakeRegistrar {

    private const val TAG = "CodWakeRegistrar"

    /**
     * Idempotent — calling this multiple times re-registers
     * with the same PendingIntent slot, which the framework
     * coalesces (FLAG_UPDATE_CURRENT). Safe to call from
     * Application.onCreate AND from BootReceiver.
     */
    fun register(context: Context) {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.S) {
            Log.d(TAG, "skipping (API ${Build.VERSION.SDK_INT} < 31)")
            return
        }
        val cm = context.applicationContext
            .getSystemService(Context.CONNECTIVITY_SERVICE) as ConnectivityManager
        val intent = Intent(context.applicationContext, CodWakeReceiver::class.java)
        // FLAG_MUTABLE required on API 31+ for callback-style
        // PendingIntents that Android needs to fill in extras
        // (the Network ref + capability change reason).
        val pi = PendingIntent.getBroadcast(
            context.applicationContext,
            REQUEST_CODE,
            intent,
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_MUTABLE,
        )
        // Filter on NOT_VPN so the callback fires for the
        // underlying physical-transport changes, NOT for our own
        // VPN tun coming up/down. Same logic as NetworkMonitor's
        // runtime callback. We deliberately do NOT add
        // NET_CAPABILITY_INTERNET — that requires captive-portal
        // validation, adding 5-30 s of delay before the callback
        // fires.
        val req = NetworkRequest.Builder()
            .addCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN)
            .addTransportType(NetworkCapabilities.TRANSPORT_WIFI)
            .addTransportType(NetworkCapabilities.TRANSPORT_CELLULAR)
            .addTransportType(NetworkCapabilities.TRANSPORT_ETHERNET)
            .build()
        try {
            cm.registerNetworkCallback(req, pi)
            Log.i(TAG, "registered PendingIntent-based NetworkCallback (process-death-surviving)")
        } catch (t: Throwable) {
            Log.e(TAG, "registration failed", t)
        }
    }

    private const val REQUEST_CODE = 0xC0DEC0DE.toInt()
}
