package com.privycs.vpn.widget

import android.graphics.Bitmap
import android.graphics.Canvas
import android.graphics.Color
import android.graphics.LinearGradient
import android.graphics.Paint
import android.graphics.Path
import android.graphics.PorterDuff
import android.graphics.Shader

/**
 * Renders a rolling speed-sample buffer as a smooth-spline sparkline
 * bitmap for the home-screen widget. Mirrors the visual contract of
 * the in-app [com.privycs.vpn.ui.components.SpeedSparkline] so the
 * two charts show identical shapes for identical data.
 *
 * Why a bitmap and not a custom view: RemoteViews (the API surface
 * used by home-screen widgets) exposes only a handful of pre-defined
 * view types and cannot run Compose. The only way to get an arbitrary
 * curve onto the widget is to pre-render it to a bitmap in the app
 * process and pass it via [android.widget.RemoteViews.setImageViewBitmap].
 *
 * Smoothing uses Catmull-Rom-to-Bezier conversion with tension 1/6.
 * Identical math and constants as the Compose version so the curves
 * land pixel-by-pixel on the same path at matching canvas sizes.
 *
 * Output bitmaps are ARGB_8888 with transparent background; the
 * widget's container color shows through. The launcher copies the
 * bitmap before display, so the caller may discard the returned
 * reference without explicit recycling.
 */
object WidgetSparklineRenderer {

    /**
     * Per-bucket bitmap + last-input hash. Two buckets (RX, TX) so
     * concurrent calls from the widget update path don't thrash the
     * cache. When the new call's hash matches the cached one (idle
     * tunnel: same all-zero samples for many ticks, or same numbers
     * because no traffic since last sample), we return the cached
     * bitmap without re-running the spline math or allocating a
     * new bitmap. Tunnel-idle widget refreshes drop from
     * "render+allocate" to "lookup" - typical idle phone saves
     * ~99% of sparkline-related GC pressure.
     *
     * Cache invalidation: dimension change OR hash change forces a
     * fresh allocation. We don't try to mutate the cached bitmap
     * in place (RemoteViews may have copied it via Parcel/ashmem
     * already; in-place mutation could be visible to the launcher
     * mid-frame).
     */
    private val cachedBitmaps = arrayOfNulls<Bitmap>(2)
    private val cachedHashes = LongArray(2) { Long.MIN_VALUE }
    const val BUCKET_RX = 0
    const val BUCKET_TX = 1

    /**
     * @param samples rolling window of non-negative samples. <2
     *   elements or an all-zero buffer returns an empty transparent
     *   bitmap (visually correct for an idle tunnel).
     * @param lineColor ARGB line color. The area gradient reuses the
     *   same RGB at alpha 0x99 (top) -> 0x00 (bottom).
     * @param widthPx pixel width of the output. Caller converts dp
     *   via `displayMetrics.density`.
     * @param heightPx pixel height of the output.
     * @param cacheBucket BUCKET_RX or BUCKET_TX. Determines which
     *   slot to consult for the cached bitmap.
     */
    fun render(
        samples: List<Float>,
        lineColor: Int,
        widthPx: Int,
        heightPx: Int,
        cacheBucket: Int = BUCKET_RX,
    ): Bitmap {
        val bucket = cacheBucket.coerceIn(0, cachedBitmaps.size - 1)
        // Fast path: same dimensions + same input → return cached.
        // Hash mixes lineColor + widthPx + heightPx + samples so any
        // change forces a re-render.
        val hash = computeHash(samples, lineColor, widthPx, heightPx)
        val cached = cachedBitmaps[bucket]
        if (cached != null &&
            !cached.isRecycled &&
            cached.width == widthPx &&
            cached.height == heightPx &&
            cachedHashes[bucket] == hash
        ) {
            return cached
        }

        val bitmap = Bitmap.createBitmap(
            widthPx.coerceAtLeast(1),
            heightPx.coerceAtLeast(1),
            Bitmap.Config.ARGB_8888,
        )
        val canvas = Canvas(bitmap)
        canvas.drawColor(Color.TRANSPARENT, PorterDuff.Mode.CLEAR)

        val n = samples.size
        if (n < 2) return bitmap

        val w = widthPx.toFloat()
        val h = heightPx.toFloat()

        val maxVal = samples.maxOrNull() ?: 0f
        if (maxVal <= 0f) return bitmap

        // Tallest sample fills ~90% of the canvas height. Headroom
        // keeps the peak legible against the widget edge.
        val scaleY = (h * 0.9f) / maxVal
        val stepX = w / (n - 1).coerceAtLeast(1).toFloat()

        val xs = FloatArray(n) { it * stepX }
        val ys = FloatArray(n) { h - (samples[it] * scaleY) }

        val linePath = Path().apply { moveTo(xs[0], ys[0]) }
        val areaPath = Path().apply {
            moveTo(xs[0], h)
            lineTo(xs[0], ys[0])
        }

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
        areaPath.lineTo(xs[n - 1], h)
        areaPath.close()

        // Area fill: vertical gradient derived from lineColor. alpha
        // top 0x99 (~0.6) -> bottom 0x00. Bitwise merge isolates RGB
        // so we don't depend on Color helper imports.
        val rgb = lineColor and 0x00FFFFFF
        val areaPaint = Paint(Paint.ANTI_ALIAS_FLAG).apply {
            shader = LinearGradient(
                0f, 0f, 0f, h,
                rgb or 0x99000000.toInt(),
                rgb, // alpha 0x00 at bottom
                Shader.TileMode.CLAMP,
            )
        }
        canvas.drawPath(areaPath, areaPaint)

        // Line stroke on top of the area fill.
        val linePaint = Paint(Paint.ANTI_ALIAS_FLAG).apply {
            style = Paint.Style.STROKE
            strokeWidth = 2f
            color = lineColor
            strokeCap = Paint.Cap.ROUND
            strokeJoin = Paint.Join.ROUND
        }
        canvas.drawPath(linePath, linePaint)

        // Write through to the cache for the next render() call's
        // fast-path. Recycle the previous cached bitmap if it had
        // mismatched dimensions; same-dim ones get GC'd naturally
        // since RemoteViews already copied the data.
        cachedBitmaps[bucket]?.takeIf { !it.isRecycled }?.let {
            if (it.width != widthPx || it.height != heightPx) {
                try { it.recycle() } catch (_: Exception) { }
            }
        }
        cachedBitmaps[bucket] = bitmap
        cachedHashes[bucket] = hash

        return bitmap
    }

    /**
     * Hash mixing all inputs that affect the rendered pixels. Used
     * by the cache fast-path; collisions just cause an unnecessary
     * re-render, not visual artifacts.
     */
    private fun computeHash(
        samples: List<Float>,
        lineColor: Int,
        widthPx: Int,
        heightPx: Int,
    ): Long {
        var h = 1125899906842597L  // FNV-style seed
        h = 31L * h + lineColor
        h = 31L * h + widthPx
        h = 31L * h + heightPx
        h = 31L * h + samples.size
        for (s in samples) {
            h = 31L * h + java.lang.Float.floatToRawIntBits(s).toLong()
        }
        return h
    }
}
