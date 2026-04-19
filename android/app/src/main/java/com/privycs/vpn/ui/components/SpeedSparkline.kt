package com.privycs.vpn.ui.components

import androidx.compose.foundation.Canvas
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.Path
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.unit.dp

/**
 * Rudimentary sparkline: area-filled smooth-spline chart without axes
 * or labels, scaled to the widest-seen value in the supplied buffer so
 * the curve uses the full card height. Matches the desktop
 * SpeedSparkline.vue visual (echarts `smooth: true`) so cross-device
 * users see consistent UI.
 *
 * Smoothing uses Catmull-Rom-to-Bezier conversion: for each segment
 * (P1, P2) we look at neighbors (P0, P3) and compute two control
 * points so the resulting cubic Bezier passes through P1 and P2 with
 * tangents that depend on the surrounding samples. Result is the same
 * organic curve look as echarts without pulling in a chart library.
 *
 * Implementation stays on Compose Canvas (not a WebView hosting
 * echarts) because ~200 KB of JS + browser startup is hugely
 * disproportionate to a 30-sample sparkline, and Canvas's draw cost
 * for a few dozen cubicTo calls is negligible.
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
        if (data.size < 2) return@Canvas

        val w = size.width
        val h = size.height
        val n = data.size

        // Scale so the tallest sample in the current window uses
        // ~90% of the card height. Small headroom keeps the peak
        // readable against the card edge. All-zero buffer draws
        // nothing (visually correct for an idle tunnel).
        val maxVal = data.maxOrNull() ?: 0f
        if (maxVal <= 0f) return@Canvas
        val scaleY = (h * 0.9f) / maxVal
        val stepX = w / (n - 1).coerceAtLeast(1).toFloat()

        // Pre-compute (x, y) for every sample so the spline builder
        // can index neighbors without repeating the scale math.
        val xs = FloatArray(n) { it * stepX }
        val ys = FloatArray(n) { h - (data[it] * scaleY) }

        // Catmull-Rom to cubic Bezier conversion. For segment P1->P2:
        //   C1 = P1 + (P2 - P0) / 6
        //   C2 = P2 - (P3 - P1) / 6
        // Tension factor 1/6 matches echarts' default smoothing.
        // At the edges we mirror the endpoint so the spline has zero
        // tangent there (clean "roll-off" instead of over-shooting).
        val linePath = Path()
        val areaPath = Path()
        linePath.moveTo(xs[0], ys[0])
        areaPath.moveTo(xs[0], h)          // area baseline starts at bottom-left
        areaPath.lineTo(xs[0], ys[0])      // rise to first sample

        val tension = 1f / 6f
        for (i in 0 until n - 1) {
            val x0 = if (i == 0) xs[i] else xs[i - 1]
            val y0 = if (i == 0) ys[i] else ys[i - 1]
            val x1 = xs[i]
            val y1 = ys[i]
            val x2 = xs[i + 1]
            val y2 = ys[i + 1]
            val x3 = if (i + 2 >= n) xs[i + 1] else xs[i + 2]
            val y3 = if (i + 2 >= n) ys[i + 1] else ys[i + 2]

            val c1x = x1 + (x2 - x0) * tension
            val c1y = y1 + (y2 - y0) * tension
            val c2x = x2 - (x3 - x1) * tension
            val c2y = y2 - (y3 - y1) * tension

            linePath.cubicTo(c1x, c1y, c2x, c2y, x2, y2)
            areaPath.cubicTo(c1x, c1y, c2x, c2y, x2, y2)
        }
        // Close the area path along the baseline.
        areaPath.lineTo(xs[n - 1], h)
        areaPath.close()

        drawPath(
            path = areaPath,
            brush = Brush.verticalGradient(
                colors = listOf(color.copy(alpha = 0.6f), color.copy(alpha = 0f)),
                startY = 0f,
                endY = h,
            ),
        )
        drawPath(
            path = linePath,
            color = color,
            style = Stroke(width = 2f),
        )
    }
}
