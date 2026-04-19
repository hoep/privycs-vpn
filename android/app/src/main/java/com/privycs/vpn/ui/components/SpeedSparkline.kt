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
 * Rudimentary sparkline: area-filled line chart without axes or
 * labels, scaled to the widest-seen value in the supplied buffer so
 * the curve uses the full card height. Matches the desktop
 * SpeedSparkline.vue visual so cross-device users see consistent UI.
 *
 * Implementation uses Compose Canvas directly rather than an ECharts
 * WebView because the 200 KB ECharts bundle plus WebView startup is
 * wildly disproportionate to the 30-sample sparkline we need, and
 * Canvas has zero runtime cost beyond the drawing primitives.
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

        // Scale so the tallest sample in the current window uses
        // ~90% of the card height. Keeping a small headroom makes
        // the peak readable against the card edge. If all zeros,
        // draw nothing (an empty card is visually correct for an
        // idle tunnel).
        val maxVal = data.maxOrNull() ?: 0f
        if (maxVal <= 0f) return@Canvas
        val scaleY = (h * 0.9f) / maxVal
        val stepX = w / (n - 1).coerceAtLeast(1).toFloat()

        // Build the line path, then close it down to the bottom so
        // the area fill has a baseline. Separate stroke and fill so
        // the line itself is visible over the alpha-gradient area.
        val linePath = Path()
        val areaPath = Path()
        data.forEachIndexed { i, v ->
            val x = i * stepX
            val y = h - (v * scaleY)
            if (i == 0) {
                linePath.moveTo(x, y)
                areaPath.moveTo(x, h) // start at bottom
                areaPath.lineTo(x, y)
            } else {
                linePath.lineTo(x, y)
                areaPath.lineTo(x, y)
            }
        }
        // Close the area path at the bottom-right so the fill
        // encloses the curve.
        areaPath.lineTo((n - 1) * stepX, h)
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
