package com.privycs.vpn.util

import android.content.Context
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow

/**
 * Heuristic detector and pause controller for Android system-level
 * Always-On VPN.
 *
 * Android has no public API to query Always-On status (the
 * Settings.Global.ALWAYS_ON_VPN_APP slot is system-access-only). We
 * infer it by timing correlation: if our VpnService is woken with a
 * null intent (handleAlwaysOnReconnect path) within a short window
 * after the user explicitly tapped Disconnect in our UI, the only
 * mechanism that could have caused that wake-up is the OS Always-On
 * + START_STICKY auto-respawn - the system would not otherwise
 * re-start our service right after we asked it to stop.
 *
 * Once detected, the flag persists in SharedPreferences across
 * process death so the UI immediately shows the correct
 * "managed-by-system" disconnect flow on next launch without
 * requiring another detection round.
 *
 * The pause mechanism lets the user temporarily defeat Always-On
 * auto-reconnect: UI stamps pauseUntilMs = now + X minutes,
 * handleAlwaysOnReconnect returns early without reconnecting while
 * pauseUntilMs > now. When the flag expires, the next Always-On
 * trigger reconnects normally.
 */
object AlwaysOnDetector {

    private const val PREF_NAME = "privycs_always_on"
    private const val KEY_LAST_USER_DISCONNECT = "last_user_disconnect_ms"
    private const val KEY_DETECTED = "always_on_detected"
    private const val KEY_PAUSE_UNTIL = "pause_until_ms"
    private const val KEY_LAST_SYSTEM_REVOKE = "last_system_revoke_ms"

    // NetworkMonitor must not fire on-demand reconnect for this long
    // after the OS revokes our VPN permission. The in-flight service
    // teardown (WireGuard goroutines + GoBackend close + TUN fd
    // release) runs asynchronously and can take up to ~4s. Firing a
    // new connect before it completes produces a multi-tunnel race
    // that manifests as "Failed to write packet to TUN device:
    // input/output error" plus a keepalive storm.
    const val SYSTEM_REVOKE_COOLDOWN_MS = 5_000L

    // 3 s is long enough to bridge the service teardown + system
    // START_STICKY respawn delay (typically ~200-800 ms observed)
    // but short enough that a genuine user-initiated connect after
    // a disconnect does not falsely trigger detection.
    private const val DETECTION_WINDOW_MS = 3_000L

    private val _detected = MutableStateFlow(false)
    val detected: StateFlow<Boolean> = _detected.asStateFlow()

    /** Called from PrivycsApp.onCreate to load the persisted flag. */
    fun init(ctx: Context) {
        val prefs = ctx.getSharedPreferences(PREF_NAME, Context.MODE_PRIVATE)
        _detected.value = prefs.getBoolean(KEY_DETECTED, false)
    }

    /**
     * Stamp the current time as the last user-initiated disconnect.
     * Called from VpnServiceManager.disconnect() BEFORE the intent is
     * sent so the timestamp lands before START_STICKY can wake us.
     */
    fun stampUserDisconnect(ctx: Context) {
        ctx.getSharedPreferences(PREF_NAME, Context.MODE_PRIVATE)
            .edit()
            .putLong(KEY_LAST_USER_DISCONNECT, System.currentTimeMillis())
            .apply()
    }

    /**
     * Returns true if the user manually disconnected within
     * [cooldownMs] of now. NetworkMonitor checks this before
     * issuing an on-demand reconnect so a tap-Disconnect doesn't
     * get instantly undone by the next network-change event when
     * the rule still matches the current network.
     *
     * Without this gate, the user-perceived sequence was:
     *   tap Disconnect -> VPN goes down -> 100-500ms later it
     *   comes back up automatically. The on-demand auto-reconnect
     *   was correct from a rule-matching perspective but ignored
     *   user intent.
     */
    fun wasRecentlyManuallyDisconnected(ctx: Context, cooldownMs: Long): Boolean {
        val prefs = ctx.getSharedPreferences(PREF_NAME, Context.MODE_PRIVATE)
        val lastUserDc = prefs.getLong(KEY_LAST_USER_DISCONNECT, 0L)
        if (lastUserDc <= 0L) return false
        return (System.currentTimeMillis() - lastUserDc) < cooldownMs
    }

    /**
     * Clear the last-user-disconnect timestamp so the cooldown gate
     * stops blocking evaluations from this point on. Called from
     * NetworkMonitor whenever the COD settings flow re-emits: a
     * user toggling Connect-on-Demand (or changing trigger / SSID
     * rules) is an explicit fresh intent that should not be
     * suppressed by an older "I tapped Disconnect" stamp. Without
     * this clear, a user who manually disconnected then 5 seconds
     * later toggled COD on would see "on-demand reconnect
     * suppressed: manual disconnect within 30s cooldown" and
     * silently no-op for the rest of the cooldown window.
     */
    fun clearUserDisconnectStamp(ctx: Context) {
        ctx.getSharedPreferences(PREF_NAME, Context.MODE_PRIVATE)
            .edit()
            .remove(KEY_LAST_USER_DISCONNECT)
            .apply()
    }

    /**
     * Called from PrivycsVpnService.handleAlwaysOnReconnect. If a user
     * disconnect happened within DETECTION_WINDOW_MS, we are on
     * Always-On. Persists the flag and flips the StateFlow for any
     * UI observers.
     */
    fun onAlwaysOnReconnectTriggered(ctx: Context): Boolean {
        val prefs = ctx.getSharedPreferences(PREF_NAME, Context.MODE_PRIVATE)
        val lastUserDc = prefs.getLong(KEY_LAST_USER_DISCONNECT, 0L)
        val now = System.currentTimeMillis()
        val justDisconnected = lastUserDc > 0 && (now - lastUserDc) < DETECTION_WINDOW_MS
        if (justDisconnected && !prefs.getBoolean(KEY_DETECTED, false)) {
            prefs.edit().putBoolean(KEY_DETECTED, true).apply()
            _detected.value = true
        }
        return justDisconnected
    }

    /**
     * Set a pause window during which handleAlwaysOnReconnect must
     * NOT reconnect. UI calls this BEFORE sending ACTION_DISCONNECT
     * so the flag is already in place when the OS-triggered
     * auto-respawn arrives.
     */
    fun pauseFor(ctx: Context, minutes: Int) {
        val until = System.currentTimeMillis() + minutes * 60_000L
        ctx.getSharedPreferences(PREF_NAME, Context.MODE_PRIVATE)
            .edit()
            .putLong(KEY_PAUSE_UNTIL, until)
            .apply()
    }

    /** Returns true if the pause window is still in effect. */
    fun isPausedNow(ctx: Context): Boolean {
        val until = ctx.getSharedPreferences(PREF_NAME, Context.MODE_PRIVATE)
            .getLong(KEY_PAUSE_UNTIL, 0L)
        return until > System.currentTimeMillis()
    }

    /** Returns the absolute ms timestamp until which pause is active, or 0. */
    fun pauseUntilMs(ctx: Context): Long {
        return ctx.getSharedPreferences(PREF_NAME, Context.MODE_PRIVATE)
            .getLong(KEY_PAUSE_UNTIL, 0L)
    }

    /** Clear the pause flag (e.g., when user taps Connect again). */
    fun clearPause(ctx: Context) {
        ctx.getSharedPreferences(PREF_NAME, Context.MODE_PRIVATE)
            .edit()
            .remove(KEY_PAUSE_UNTIL)
            .apply()
    }

    /**
     * Called from PrivycsVpnService.onRevoke (system tore down our
     * VPN permission, e.g., user disabled Always-On or another VPN
     * app took over). Timestamp feeds the SYSTEM_REVOKE_COOLDOWN_MS
     * gate in NetworkMonitor so on-demand auto-reconnect does not
     * fight an in-flight service teardown.
     */
    fun stampSystemRevoke(ctx: Context) {
        ctx.getSharedPreferences(PREF_NAME, Context.MODE_PRIVATE)
            .edit()
            .putLong(KEY_LAST_SYSTEM_REVOKE, System.currentTimeMillis())
            .apply()
    }

    /** True if a system-initiated VPN revoke happened within the cooldown window. */
    fun isInSystemRevokeCooldown(ctx: Context): Boolean {
        val last = ctx.getSharedPreferences(PREF_NAME, Context.MODE_PRIVATE)
            .getLong(KEY_LAST_SYSTEM_REVOKE, 0L)
        return last > 0 && (System.currentTimeMillis() - last) < SYSTEM_REVOKE_COOLDOWN_MS
    }
}
