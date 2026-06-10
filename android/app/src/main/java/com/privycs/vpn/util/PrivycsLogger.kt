package com.privycs.vpn.util

import android.content.Context
import android.util.Log
import com.privycs.vpn.PrivycsApp
import java.io.File
import java.io.FileWriter
import java.io.PrintWriter
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale
import java.util.concurrent.Executors
import java.util.concurrent.ThreadFactory

/**
 * Tee logger: writes every line to Android logcat AND to a rotating log
 * file the in-app LogsScreen can display. Without this the LogsScreen
 * only ever showed "No log file found" because nothing in the app
 * actually wrote to filesDir/privycs-vpn.log (all log calls went straight
 * to logcat, invisible to the app).
 *
 * Format per line (matches what LogsScreen's highlight regex expects):
 *   2026-04-18 14:35:22.123 [TAG] INFO message
 *
 * File is capped at ~200 KB; we keep the tail 500 lines to match the
 * LogsScreen truncation. Writes are synchronized so service-thread +
 * UI-thread callers do not interleave.
 */
object PrivycsLogger {
    private const val LOG_FILE = "privycs-vpn.log"
    private const val MAX_SIZE_BYTES = 200_000L
    private const val KEEP_LAST_LINES = 500
    private val dateFormat = SimpleDateFormat("yyyy-MM-dd HH:mm:ss.SSS", Locale.US)

    // Single-thread executor that owns ALL file I/O (append + rotate).
    // Moving the FileWriter/rotate off the caller thread fixes the
    // synchronous-disk-I/O-on-the-caller problem: d/i/w/e used to open,
    // write and close a FileWriter (and occasionally readLines()+rewrite
    // the whole file in rotate) under a global lock on whatever thread
    // called them — including the per-second service poll loop and the
    // Compose UI thread. The executor's FIFO queue preserves log
    // ordering (one consumer thread, tasks run in submit order), and
    // dateFormat is only ever touched on this one thread so it stays
    // de-facto single-threaded (SimpleDateFormat is not thread-safe).
    // The timestamp is captured at CALL time (see log()) and only
    // FORMATTED here, so reordering relative to call order cannot happen.
    // Daemon thread so it never blocks process exit; the OS flushes the
    // FileWriter on close in each task, so a process kill loses at most
    // the entries still queued — same crash-window as the old
    // append-and-close-per-line design.
    private val writeExecutor = Executors.newSingleThreadExecutor(
        ThreadFactory { r ->
            Thread(r, "PrivycsLogger-io").apply { isDaemon = true }
        }
    )

    fun d(tag: String, msg: String) = log("DEBUG", tag, msg) { Log.d(tag, msg) }
    fun i(tag: String, msg: String) = log("INFO ", tag, msg) { Log.i(tag, msg) }
    fun w(tag: String, msg: String) = log("WARN ", tag, msg) { Log.w(tag, msg) }
    fun w(tag: String, msg: String, t: Throwable) = log("WARN ", tag, "$msg: ${t.message}") { Log.w(tag, msg, t) }
    fun e(tag: String, msg: String) = log("ERROR", tag, msg) { Log.e(tag, msg) }
    fun e(tag: String, msg: String, t: Throwable) = log("ERROR", tag, "$msg: ${t.message}") { Log.e(tag, msg, t) }

    /**
     * Redact a Wi-Fi SSID for logging (v0.9.15.74 — B-5/privacy).
     * SSIDs are personal data; this log file is shown in-app
     * (LogsScreen) and included in bug-report exports, so the raw
     * name must not land in it. Keeps the first character + length
     * so a developer can still tell two networks apart.
     */
    fun redactSsid(ssid: String): String = when {
        ssid.isEmpty() -> "<none>"
        ssid.length == 1 -> "* (1)"
        else -> "${ssid.first()}… (${ssid.length} chars)"
    }

    /**
     * Redact every double-quoted substring of a free-form string
     * (e.g. an SSID embedded in a rule-match message) for logging,
     * leaving the surrounding text intact for diagnostics.
     */
    fun redactQuoted(s: String): String =
        s.replace(Regex("\"[^\"]*\""), "\"…\"")

    private inline fun log(level: String, tag: String, msg: String, logcat: () -> Unit) {
        logcat()
        val ctx: Context = try {
            PrivycsApp.instance
        } catch (_: UninitializedPropertyAccessException) {
            Log.w("PrivycsLogger",
                "PrivycsApp.instance not initialized yet; skipping log file write for [$tag] $msg")
            return
        }
        // Capture the wall-clock at CALL time so the file line's timestamp
        // reflects when the caller logged, not when the I/O thread happens
        // to drain the queue. Everything else (formatting + the actual
        // FileWriter) runs on the single-thread executor below, off the
        // caller thread, in submit order.
        val now = Date()
        val filesDir = ctx.filesDir
        try {
            writeExecutor.execute { writeLine(filesDir, now, level, tag, msg) }
        } catch (e: java.util.concurrent.RejectedExecutionException) {
            // Executor shut down (process tearing down). Drop quietly —
            // logcat already has the line.
            Log.w("PrivycsLogger", "log write rejected (executor down): $msg")
        }
    }

    /**
     * Runs on the single I/O thread. Appends one line and rotates when
     * the file outgrows MAX_SIZE_BYTES. No external lock needed: the
     * single-thread executor serialises every call here.
     */
    private fun writeLine(filesDir: File, ts: Date, level: String, tag: String, msg: String) {
        try {
            val file = File(filesDir, LOG_FILE)
            if (file.exists() && file.length() > MAX_SIZE_BYTES) {
                rotate(file)
            }
            PrintWriter(FileWriter(file, true)).use { out ->
                out.println("${dateFormat.format(ts)} [$tag] $level $msg")
            }
        } catch (e: Exception) {
            // Log to logcat so `adb logcat -s PrivycsLogger` reveals
            // WHY the file write failed (permissions, full disk, etc.)
            // - silent failure was exactly what left the in-app Logs
            // screen empty in v0.9.1.5 / v0.9.1.6.
            Log.e("PrivycsLogger",
                "Failed to append to $filesDir/$LOG_FILE: ${e.message}", e)
        }
    }

    /**
     * Tail-trim the log to the last KEEP_LAST_LINES lines.
     *
     * Atomic-rename strategy: write the tail to a sibling temp file,
     * then rename(2) onto the original. Pre-fix this used
     * `file.writeText(...)` which truncates the file in place — if
     * the process is killed mid-write the file ends up half-empty
     * AND the in-flight log call's PrintWriter has already opened
     * the (now-truncated) file in append mode at the OLD position,
     * so the next entry lands at a weird offset and subsequent
     * readers see garbage. The user-reported symptom was "logs
     * werden nicht kontinuierlich upgedatet" after the file passed
     * 200 KB — rotation kicked in, the file got mangled, the
     * LogsScreen viewer kept showing pre-rotation entries.
     *
     * Atomic rename is the standard fix: only at the end-of-rename
     * does the filename point to the truncated content; any reader
     * mid-rotation sees either the old or the new file, never a
     * partial mix.
     */
    private fun rotate(file: File) {
        try {
            val lines = file.readLines()
            val tail = if (lines.size > KEEP_LAST_LINES) lines.takeLast(KEEP_LAST_LINES) else lines
            val tmp = File(file.parentFile, "${file.name}.tmp")
            tmp.writeText(tail.joinToString("\n") + "\n")
            if (!tmp.renameTo(file)) {
                // renameTo can fail on some filesystems; fall back
                // to in-place truncate. Worse atomicity but better
                // than leaving the .tmp orphan.
                file.writeText(tmp.readText())
                tmp.delete()
            }
        } catch (e: Exception) {
            Log.e("PrivycsLogger", "Rotate failed: ${e.message}", e)
            // Last-resort: truncate so the file doesn't grow
            // unbounded. We accept losing the last 500 lines over
            // losing all future writes.
            try { file.writeText("") } catch (_: Exception) {}
        }
    }
}
