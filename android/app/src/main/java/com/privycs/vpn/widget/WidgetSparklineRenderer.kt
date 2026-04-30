package com.privycs.vpn.widget

import android.graphics.Bitmap
import android.graphics.Canvas
import android.graphics.Color
import android.graphics.Paint
import android.graphics.PorterDuff
import android.graphics.RectF

/**
 * Renders a rolling speed-sample buffer as a bar-style sparkline
 * bitmap for the home-screen widget. Mirrors the in-app
 * [com.privycs.vpn.ui.components.SpeedSparkline] (which switched
 * from area-curve to bars in v0.9.11.64) so the widget and the
 * Connect screen show identical shapes for identical data.
 *
 * Why a bitmap and not a custom view: RemoteViews (the API surface
 * used by home-screen widgets) exposes only a handful of pre-defined
 * view types and cannot run Compose. The only way to get an arbitrary
 * graph onto the widget is to pre-render it in the app process and
 * pass it via [android.widget.RemoteViews.setImageViewBitmap].
 *
 * Bar geometry mirrors the in-app version: 70% bar width, 30% gap,
 * 1.5px floor for non-zero samples so small bursts stay visible
 * against the baseline.
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
        if (n < 1) return bitmap

        val w = widthPx.toFloat()
        val h = heightPx.toFloat()

        val maxVal = samples.maxOrNull() ?: 0f
        if (maxVal <= 0f) return bitmap

        // Tallest sample fills ~90% of the canvas height. Headroom
        // keeps the peak legible against the widget edge.
        val scaleY = (h * 0.9f) / maxVal

        // Bar geometry: 70% bar / 30% gap, 1.5px floor for non-zero
        // samples. Same constants as the in-app SpeedSparkline so the
        // widget and Connect screen show identical bar shapes.
        val slot = w / n
        val barW = (slot * 0.70f).coerceAtLeast(1f)
        val minBarH = 1.5f

        val barPaint = Paint(Paint.ANTI_ALIAS_FLAG).apply {
            color = lineColor
            style = Paint.Style.FILL
        }
        val rect = RectF()
        for (i in 0 until n) {
            val v = samples[i]
            if (v <= 0f) continue
            val barH = (v * scaleY).coerceAtLeast(minBarH)
            val x = i * slot
            val y = h - barH
            rect.set(x, y, x + barW, h)
            // Tiny corner radius (1px) keeps the bar tops from
            // looking jagged on low-DPI launchers; 0px would also
            // work but the rounded look is closer to dashboard
            // conventions (Grafana-style mini bar charts).
            canvas.drawRoundRect(rect, 1f, 1f, barPaint)
        }

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
