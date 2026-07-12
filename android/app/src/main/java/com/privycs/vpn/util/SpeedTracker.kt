package com.privycs.vpn.util

import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow

/**
 * Singleton that derives per-second RX/TX speed values from successive
 * byte counters and maintains a rolling history suitable for a
 * sparkline chart.
 *
 * The caller (VpnServiceManager) feeds in cumulative byte counts and a
 * `connected` flag on every status poll. SpeedTracker:
 *
 *  - Computes deltas against the previous sample.
 *  - Divides by elapsed wall-clock seconds so backgrounded apps or
 *    skipped polls do not distort the speed.
 *  - Clamps negatives to zero (counter reset / reconnect race).
 *  - Resets history on disconnect so the sparkline flatlines instead
 *    of carrying stale spikes into the next session.
 *
 * History length is 30 samples = 60 s at 2 s poll interval: short
 * enough to react visibly to bursts, long enough to read trends.
 */
object SpeedTracker {

    private const val HISTORY_LEN = 30

    /**
     * Ignore samples arriving sooner than this after the previous one. The service
     * polls once a second; anything faster is an off-cadence push from a state
     * listener whose byte delta is ~0 and would only inject a false 0 B/s.
     */
    private const val MIN_SAMPLE_INTERVAL_MS = 250L

    /**
     * Consecutive below-baseline samples after which a counter regression is taken as
     * a genuine reset (tunnel restart / protocol switch) rather than a transient
     * backend hiccup, and the lower value is accepted as the new baseline.
     */
    private const val REGRESSION_TOLERANCE = 3

    private val zeros = List(HISTORY_LEN) { 0f }
    private val _rxSpeedHistory = MutableStateFlow(zeros)
    private val _txSpeedHistory = MutableStateFlow(zeros)

    /** Public read-only views for UI collection. */
    val rxSpeedHistory: StateFlow<List<Float>> = _rxSpeedHistory.asStateFlow()
    val txSpeedHistory: StateFlow<List<Float>> = _txSpeedHistory.asStateFlow()

    @Volatile private var lastRxBytes: Long = 0L
    @Volatile private var lastTxBytes: Long = 0L
    @Volatile private var lastSampleAtMs: Long = 0L
    /** Consecutive samples whose counters sat below the baseline. */
    @Volatile private var regressionStreak: Int = 0

    /**
     * Feed a new sample. `connected=false` resets the history and the
     * internal baseline so the next connect starts clean.
     */
    @Synchronized
    fun record(rxBytes: Long, txBytes: Long, connected: Boolean) {
        if (!connected) {
            if (_rxSpeedHistory.value.any { it != 0f } || _txSpeedHistory.value.any { it != 0f }) {
                _rxSpeedHistory.value = zeros
                _txSpeedHistory.value = zeros
            }
            lastRxBytes = 0L
            lastTxBytes = 0L
            lastSampleAtMs = 0L
            regressionStreak = 0
            return
        }

        val now = System.currentTimeMillis()
        if (lastSampleAtMs == 0L) {
            // First sample of this connected session establishes
            // baseline without producing a spike.
            lastRxBytes = rxBytes
            lastTxBytes = txBytes
            lastSampleAtMs = now
            return
        }

        // A counter regression is a backend hiccup, not negative traffic. The byte
        // readers can briefly report a lower total: OpenVpnTunnel takes max() over
        // three independent sources and the winner can switch, IpSecTunnel's
        // TrafficStats resets across a rekey, WG reports 0 between handshakes.
        // Clamping the negative delta to 0 — all we used to do — is not enough: the
        // baseline was still overwritten with the LOWER value, so the next honest
        // reading measured against a too-low baseline and came out as a spike. That
        // is the 0 / spike / 0 / spike the speed readout showed.
        //
        // Such a dip lasts one tick and the counter then returns to its true value,
        // so ignore it and keep the last good baseline. A GENUINE reset (tunnel
        // restarted, protocol switched) looks identical for one tick but PERSISTS,
        // and ignoring that forever would peg the readout at 0 until the next
        // disconnect. Tell them apart by whether it sticks.
        if (rxBytes < lastRxBytes || txBytes < lastTxBytes) {
            regressionStreak++
            if (regressionStreak >= REGRESSION_TOLERANCE) {
                lastRxBytes = rxBytes
                lastTxBytes = txBytes
                lastSampleAtMs = now
                regressionStreak = 0
            }
            _rxSpeedHistory.value = (_rxSpeedHistory.value.drop(1) + 0f)
            _txSpeedHistory.value = (_txSpeedHistory.value.drop(1) + 0f)
            return
        }
        regressionStreak = 0

        // The service polls once a second, but record() is also reached from the
        // OpenVPN/IPSec state listeners and the pool broadcasts. Such an off-cadence
        // call re-reads counters that have not moved: delta 0 over a few ms, which
        // lands a false 0 B/s at the head of the ring and makes the readout blink.
        if (now - lastSampleAtMs < MIN_SAMPLE_INTERVAL_MS) return

        val elapsedSec = maxOf(0.001f, (now - lastSampleAtMs) / 1000f)
        val rxSpeed = maxOf(0f, (rxBytes - lastRxBytes) / elapsedSec)
        val txSpeed = maxOf(0f, (txBytes - lastTxBytes) / elapsedSec)

        _rxSpeedHistory.value = (_rxSpeedHistory.value.drop(1) + rxSpeed)
        _txSpeedHistory.value = (_txSpeedHistory.value.drop(1) + txSpeed)

        lastRxBytes = rxBytes
        lastTxBytes = txBytes
        lastSampleAtMs = now
    }

    /** Latest sample, convenience for UI speed label. */
    fun latestRxBps(): Float = _rxSpeedHistory.value.lastOrNull() ?: 0f
    fun latestTxBps(): Float = _txSpeedHistory.value.lastOrNull() ?: 0f

    /** Bytes-per-second formatter matching the desktop formatSpeed(). */
    fun formatSpeed(bps: Float): String {
        return when {
            bps < 1f -> "0 B/s"
            bps < 1024f -> "${bps.toInt()} B/s"
            bps < 1024f * 1024f -> "%.1f KB/s".format(bps / 1024f)
            bps < 1024f * 1024f * 1024f -> "%.1f MB/s".format(bps / 1024f / 1024f)
            else -> "%.1f GB/s".format(bps / 1024f / 1024f / 1024f)
        }
    }
}
