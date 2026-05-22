package com.privycs.vpn.util

import android.content.Context
import android.widget.Toast
import com.privycs.vpn.PrivycsApp
import com.privycs.vpn.R

/**
 * Pro-tier feature gate — the single check every gated action calls.
 *
 * Returns true when the action is allowed: the user has Privycs Pro,
 * OR gating is globally disabled. When a Free user is gated it returns
 * false and shows a toast pointing at Settings → Privycs Pro.
 *
 * Gating is master-switched by [com.privycs.vpn.data.EntitlementRepository.GATING_ENABLED].
 * While that flag is false (the test-track default) this always returns
 * true — the gates are wired but dormant. Flipping the flag for the
 * production release is what arms them.
 *
 * Usage at a gate site:
 *     if (!proGateAllowed(context)) return   // Free user blocked
 */
fun proGateAllowed(context: Context): Boolean {
    if (PrivycsApp.instance.entitlementRepository.isUnlocked()) return true
    // Post to the main looper — a gate can be hit from a non-UI
    // coroutine (e.g. AddPoolHost.runImport), and a Toast must be
    // shown on the main thread.
    android.os.Handler(android.os.Looper.getMainLooper()).post {
        Toast.makeText(
            context,
            context.getString(R.string.pro_gate_toast),
            Toast.LENGTH_LONG,
        ).show()
    }
    return false
}
