package com.privycs.vpn.util

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test

/**
 * Pins the throughput tracker against the "0 B/s → 17 kB/s → 0 B/s" flapping the
 * speed readout showed on every platform.
 *
 * SpeedTracker is an object (singleton) and times itself off the wall clock, so
 * each test resets it via a disconnect and paces samples past MIN_SAMPLE_INTERVAL_MS.
 */
class SpeedTrackerTest {

    /** Comfortably above MIN_SAMPLE_INTERVAL_MS (250 ms), short enough to keep tests quick. */
    private val tickMs = 300L

    @Before
    fun reset() {
        SpeedTracker.record(0, 0, connected = false)
    }

    @Test
    fun `steady traffic produces a steady non-zero speed`() {
        var rx = 0L
        SpeedTracker.record(rx, 0, connected = true)   // baseline

        val speeds = mutableListOf<Float>()
        repeat(4) {
            Thread.sleep(tickMs)
            rx += 10_000
            SpeedTracker.record(rx, 0, connected = true)
            speeds += SpeedTracker.latestRxBps()
        }

        // Every sample must be non-zero: a constant byte rate must never read 0 B/s.
        assertTrue("expected no zero samples, got $speeds", speeds.all { it > 0f })
    }

    @Test
    fun `an off-cadence sample does not inject a false zero`() {
        SpeedTracker.record(0, 0, connected = true)
        Thread.sleep(tickMs)
        SpeedTracker.record(10_000, 0, connected = true)
        val honest = SpeedTracker.latestRxBps()
        assertTrue("baseline sample should be > 0", honest > 0f)

        // A state listener pushes the SAME counters microseconds later. Before the
        // MIN_SAMPLE_INTERVAL guard this landed a 0 B/s at the head of the ring.
        SpeedTracker.record(10_000, 0, connected = true)

        assertEquals("off-cadence re-read must not change the readout", honest, SpeedTracker.latestRxBps(), 0.01f)
    }

    @Test
    fun `a transient counter dip neither reads negative nor spikes on recovery`() {
        SpeedTracker.record(100_000, 0, connected = true)  // baseline
        Thread.sleep(tickMs)
        SpeedTracker.record(110_000, 0, connected = true)
        val steady = SpeedTracker.latestRxBps()

        // One bad reading: the counter dips (rekey / reader switch), then recovers.
        Thread.sleep(tickMs)
        SpeedTracker.record(0, 0, connected = true)
        assertEquals("a dip reads as 0 B/s, never negative", 0f, SpeedTracker.latestRxBps(), 0.01f)

        Thread.sleep(tickMs)
        SpeedTracker.record(120_000, 0, connected = true)
        val recovered = SpeedTracker.latestRxBps()

        // The old code re-baselined to the dip (0), so the recovery billed 120_000
        // bytes to one interval — a spike of roughly an order of magnitude. Measured
        // against the preserved baseline it must stay in the same ballpark as steady.
        assertTrue(
            "recovery must not spike (steady=$steady recovered=$recovered)",
            recovered < steady * 3f
        )
    }

    @Test
    fun `a persistent counter reset is eventually adopted as the new baseline`() {
        SpeedTracker.record(1_000_000, 0, connected = true)  // baseline: high counter
        Thread.sleep(tickMs)
        SpeedTracker.record(1_010_000, 0, connected = true)

        // Tunnel restarts: counters legitimately begin again near zero and STAY low.
        // Ignoring the regression forever would peg the readout at 0 for good.
        var rx = 0L
        repeat(REGRESSION_TOLERANCE) {
            Thread.sleep(tickMs)
            rx += 5_000
            SpeedTracker.record(rx, 0, connected = true)
        }

        // Baseline adopted — traffic on the restarted tunnel registers again.
        Thread.sleep(tickMs)
        rx += 5_000
        SpeedTracker.record(rx, 0, connected = true)

        assertTrue(
            "after a persistent reset the tracker must recover, got ${SpeedTracker.latestRxBps()}",
            SpeedTracker.latestRxBps() > 0f
        )
    }

    private companion object {
        /** Mirrors SpeedTracker.REGRESSION_TOLERANCE (private). */
        const val REGRESSION_TOLERANCE = 3
    }
}
