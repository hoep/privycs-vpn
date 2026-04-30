package com.privycs.vpn.ui.components

import androidx.compose.foundation.Canvas
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.unit.dp

/**
 * Bar-style sparkline. One vertical bar per sample, scaled to the
 * widest-seen value in the supplied buffer so the tallest peak uses
 * ~90% of the card height.
 *
 * Why bars instead of a smooth area-curve: VPN traffic is bursty
 * (page load, video chunk, idle, page load). A continuous curve
 * smooths individual bursts into a misleading "shape"; bars
 * preserve sample-discrete resolution so the user can see "X
 * spikes in the last minute" at a glance. Matches the convention
 * of dashboard tools (Grafana, DataDog, Tailscale) for
 * throughput indicators.
 *
 * Edge handling:
 *   - All-zero buffer: draws nothing (idle tunnel - visually correct).
 *   - Single sample: draws one bar.
 *   - Min-bar-height for non-zero samples: 1.5px so bursts that are
 *     small but present do not vanish at the bottom edge.
 *   - Gap between bars: 30% of bar width so they read as discrete
 *     samples instead of a continuous fill.
 */
@Composable
fun SpeedSparkline(
    data: List<Float>,
    color: Color,
    modifier: Modifier = Modifier,
) {
    Canvas(
        modifier = modifier
            .fillMaxWidth()
            .height(24.dp)
    ) {
        if (data.isEmpty()) return@Canvas

        val w = size.width
        val h = size.height
        val n = data.size

        val maxVal = data.maxOrNull() ?: 0f
        if (maxVal <= 0f) return@Canvas

        // Bar geometry. slot = total horizontal pixels for one
        // sample (bar + right-side gap). Last bar's gap goes
        // unused so the rightmost bar lines up with the right
        // edge instead of leaving an awkward void.
        //
        // Bar width = slot * 0.7, gap width = slot * 0.3 - the
        // 30% gap convention mentioned above. At very narrow
        // widths (Compose-side density makes this rare) the bar
        // floors to 1px.
        val slot = w / n
        val barW = (slot * 0.70f).coerceAtLeast(1f)
        val scaleY = (h * 0.90f) / maxVal
        // 1.5px floor for any non-zero sample so a tiny burst
        // (e.g. a few KB during otherwise-idle period) still
        // shows visually instead of vanishing at the baseline.
        val minBarH = 1.5f

        for (i in 0 until n) {
            val v = data[i]
            if (v <= 0f) continue
            val barH = (v * scaleY).coerceAtLeast(minBarH)
            // Compose Canvas Y origin is top-left; bars grow up
            // from the baseline.
            val x = i * slot
            val y = h - barH

            drawRect(
                color = color,
                topLeft = Offset(x, y),
                size = Size(barW, barH),
            )
        }
    }
}
