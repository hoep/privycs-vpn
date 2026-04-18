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
    private val writeLock = Any()

    fun d(tag: String, msg: String) = log("DEBUG", tag, msg) { Log.d(tag, msg) }
    fun i(tag: String, msg: String) = log("INFO ", tag, msg) { Log.i(tag, msg) }
    fun w(tag: String, msg: String) = log("WARN ", tag, msg) { Log.w(tag, msg) }
    fun w(tag: String, msg: String, t: Throwable) = log("WARN ", tag, "$msg: ${t.message}") { Log.w(tag, msg, t) }
    fun e(tag: String, msg: String) = log("ERROR", tag, msg) { Log.e(tag, msg) }
    fun e(tag: String, msg: String, t: Throwable) = log("ERROR", tag, "$msg: ${t.message}") { Log.e(tag, msg, t) }

    private inline fun log(level: String, tag: String, msg: String, logcat: () -> Unit) {
        logcat()
        // The PrivycsApp context is guaranteed to be initialized before any
        // VPN event fires (Application.onCreate completes before the first
        // Activity). Guarding with `instance == null` check keeps us safe
        // if a static init ever races.
        val ctx: Context = try {
            PrivycsApp.instance
        } catch (_: UninitializedPropertyAccessException) {
            return
        }
        synchronized(writeLock) {
            try {
                val file = File(ctx.filesDir, LOG_FILE)
                if (file.exists() && file.length() > MAX_SIZE_BYTES) {
                    rotate(file)
                }
                PrintWriter(FileWriter(file, true)).use { out ->
                    val ts = dateFormat.format(Date())
                    out.println("$ts [$tag] $level $msg")
                }
            } catch (_: Exception) {
                // Never crash the app just because logging failed. logcat()
                // already captured it above.
            }
        }
    }

    /**
     * Tail-trim the log to the last KEEP_LAST_LINES lines.
     */
    private fun rotate(file: File) {
        try {
            val lines = file.readLines()
            val tail = if (lines.size > KEEP_LAST_LINES) lines.takeLast(KEEP_LAST_LINES) else lines
            file.writeText(tail.joinToString("\n") + "\n")
        } catch (_: Exception) {
            // If rotate fails, just nuke the file to avoid unbounded growth.
            try { file.writeText("") } catch (_: Exception) {}
        }
    }
}
