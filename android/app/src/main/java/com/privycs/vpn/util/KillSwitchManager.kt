package com.privycs.vpn.util

import android.util.Log
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow

/**
 * In-memory state machine for the app-level Kill Switch.
 *
 * Three states:
 *   - IDLE      No enforcement. Either the setting is off, or
 *               the setting is on but the user has never completed
 *               a successful connect in this process (armed flag
 *               resets on process death - we don't block traffic
 *               at boot just because the toggle is saved "on").
 *   - ARMED     A user-initiated connect has succeeded this
 *               session. Tunnel may be up or down; on an
 *               unexpected drop the sinkhole engages.
 *   - SINKHOLE  Tunnel dropped without a user-initiated
 *               disconnect. PrivycsVpnService runs a block-all
 *               route via VpnService.Builder so no traffic
 *               escapes until the user either reconnects or
 *               toggles the Kill Switch off.
 *
 * Transitions:
 *   IDLE     -> ARMED     user-initiated connect succeeds
 *   ARMED    -> IDLE      user-initiated disconnect, or toggle OFF
 *   ARMED    -> SINKHOLE  unexpected tunnel drop, AND system
 *                         Always-On VPN is NOT active (Always-On
 *                         defers to the OS mechanism instead)
 *   SINKHOLE -> ARMED     successful reconnect
 *   SINKHOLE -> IDLE      user toggles KS off or manually
 *                         disconnects via the app's settings
 *
 * Design notes:
 *   - Session-scoped, not persistent. On process death or boot
 *     the state resets to IDLE even if the user had Kill Switch
 *     saved "on". This matches the design intent: the switch is
 *     "disconnect safety net" not "boot-time traffic block".
 *   - The actual sinkhole tun fd is established by
 *     PrivycsVpnService when it observes state transitioning to
 *     SINKHOLE. This class only tracks the intent.
 */
object KillSwitchManager {
    private const val TAG = "KillSwitchManager"

    enum class State { IDLE, ARMED, SINKHOLE }

    private val _state = MutableStateFlow(State.IDLE)
    val state: StateFlow<State> = _state.asStateFlow()

    /** True iff the current state is ARMED or SINKHOLE. */
    fun isArmed(): Boolean = _state.value != State.IDLE

    /** True iff the sinkhole tunnel is (or should be) running. */
    fun isSinkholeActive(): Boolean = _state.value == State.SINKHOLE

    /**
     * Move to ARMED after a user-initiated connect succeeds, or
     * after a successful reconnect out of sinkhole mode. Only
     * called when the Kill Switch setting is enabled; otherwise
     * the caller should skip arming entirely.
     */
    fun arm() {
        when (_state.value) {
            State.IDLE -> {
                _state.value = State.ARMED
                Log.i(TAG, "armed (first successful connect)")
            }
            State.SINKHOLE -> {
                _state.value = State.ARMED
                Log.i(TAG, "armed (sinkhole released via reconnect)")
            }
            State.ARMED -> {
                // no-op: reconnect while already armed
            }
        }
    }

    /**
     * Move to IDLE on user-initiated disconnect or on the user
     * toggling the Kill Switch setting off. Never called by
     * tunnel-drop detection.
     */
    fun disarm() {
        if (_state.value != State.IDLE) {
            Log.i(TAG, "disarmed (was ${_state.value})")
            _state.value = State.IDLE
        }
    }

    /**
     * Engage the sinkhole state after the tunnel drops without a
     * user-initiated disconnect. Caller is responsible for
     * checking Always-On-VPN is not active (we defer to the OS
     * in that case rather than running a second sinkhole service).
     */
    fun engageSinkhole(reason: String = "") {
        if (_state.value == State.ARMED) {
            _state.value = State.SINKHOLE
            Log.i(TAG, "sinkhole engaged: $reason")
        } else {
            Log.d(TAG, "engageSinkhole ignored: current state is ${_state.value}")
        }
    }

    /**
     * Force sinkhole from ANY state (including IDLE). Used in two
     * places where the user intent is clear even though the tunnel
     * was never armed this session:
     *
     * - User enables Kill Switch while disconnected with a configured
     *   connection present. Industry-standard "hardcore" kill switch
     *   semantics: block immediately, unblock only when the user
     *   either connects or disables KS.
     *
     * - User manually disconnects an active tunnel while Kill Switch
     *   is enabled. Same industry-standard: the user enabled KS
     *   specifically to prevent unprotected traffic; disconnecting
     *   is another form of "tunnel down with KS on", so block.
     */
    fun forceSinkhole(reason: String = "") {
        if (_state.value != State.SINKHOLE) {
            val previous = _state.value
            _state.value = State.SINKHOLE
            Log.i(TAG, "sinkhole forced (was $previous): $reason")
        }
    }

    /**
     * Leave the sinkhole state back to IDLE. Used when the user
     * toggles Kill Switch off while the sinkhole is running.
     */
    fun releaseSinkholeToIdle() {
        if (_state.value == State.SINKHOLE) {
            Log.i(TAG, "sinkhole released to idle (user disarmed during sinkhole)")
            _state.value = State.IDLE
        }
    }
}
