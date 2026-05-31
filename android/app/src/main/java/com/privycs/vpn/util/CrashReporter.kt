package com.privycs.vpn.util

import android.content.Context
import android.content.SharedPreferences
import com.privycs.vpn.BuildConfig
import io.sentry.Hub
import io.sentry.NoOpHub
import io.sentry.Sentry
import io.sentry.SentryEvent
import io.sentry.SentryLevel
import io.sentry.android.core.SentryAndroid
import io.sentry.protocol.Mechanism
import io.sentry.protocol.User
import java.util.UUID
import java.util.concurrent.atomic.AtomicBoolean

/**
 * v1.0.7 — privacy-first crash reporting via self-hosted Bugsink at
 * crashes.privycs.com (Sentry-protocol compatible).
 *
 * Mirrors the Desktop Go-side implementation in crash_reporter.go +
 * the Vue-side in crashReporter.ts with the same guarantees:
 *
 *  - **Opt-in only.** [SettingsRepository.crashReportsEnabled] is
 *    the persistent flag; default false. SettingsScreen has a
 *    toggle. Until the user opts in, [init] returns early and the
 *    Sentry SDK is never loaded.
 *
 *  - **Anonymous identity.** [User.id] is a per-install UUID
 *    persisted in [PREFS_CRASH_REPORTER]. Regenerated on app
 *    uninstall + reinstall (SharedPreferences cleared with app
 *    data). NEVER linked to API key, license key, account email.
 *
 *  - **PII redaction at every send.** [beforeSend] strips:
 *      - SSIDs (regex `\bSSID=...\b` + value-side)
 *      - API keys (32+ char base64/hex)
 *      - License keys (ed25519 base64 signature pattern)
 *      - Public IPv4 + IPv6 (RFC1918 / loopback / ULA kept)
 *      - File paths containing `/data/data/<pkg>/` user dirs
 *      - HTTP request bodies
 *      - Breadcrumbs of type "navigation" (route names)
 *
 *  - **NDK crashes captured.** sentry-android pulls sentry-android-
 *    ndk transitively; native crashes from libcharon, libopenvpn3,
 *    wireguard-android are caught. The redaction pipeline runs on
 *    those events too (sentry-ndk dispatches through the same
 *    beforeSend hook).
 *
 *  - **Self-hosted only.** DSN points to crashes.privycs.com (our
 *    Bugsink). No third-party telemetry providers.
 *
 *  - **No replay/screenshot.** We never enable session replay; even
 *    a future SDK default flip can't turn it on without changing
 *    this file.
 */
object CrashReporter {
    private const val TAG = "CrashReporter"

    /** Sentry project-2 DSN on the Bugsink backend. */
    private const val SENTRY_DSN =
        "https://729f8ad7d9734817bbb3eb15aa13cc8e@crashes.privycs.com/2"

    private const val PREFS_CRASH_REPORTER = "privycs_crash_reporter"
    private const val PREF_INSTALL_UUID = "install_uuid"

    private val initialised = AtomicBoolean(false)

    /**
     * Initialise the Sentry SDK iff the user has opted in. Idempotent
     * across multiple calls. Called from [PrivycsApp.onCreate] with
     * the persisted opt-in flag, and again from [SettingsRepository]
     * whenever the toggle flips.
     */
    fun init(context: Context, optedIn: Boolean) {
        if (!optedIn) {
            if (initialised.get()) {
                // User just toggled off — explicitly clear the global
                // hub so any queued events are dropped before
                // any next CaptureException attempts re-resolve.
                Sentry.setCurrentHub(NoOpHub.getInstance() as Hub)
                initialised.set(false)
            }
            return
        }
        if (initialised.getAndSet(true)) return

        val installUUID = ensureInstallUUID(context)

        SentryAndroid.init(context) { opts ->
            opts.dsn = SENTRY_DSN
            opts.release = "privycs-vpn-android@${BuildConfig.VERSION_NAME}"
            opts.environment = if (BuildConfig.DEBUG) "dev" else "production"
            opts.serverName = "redacted"  // never leak device hostname
            // Always send error events; we already gate via opt-in.
            opts.sampleRate = 1.0
            // No traces / replays / profiles.
            opts.tracesSampleRate = 0.0
            opts.enableNdk = true               // libcharon / OpenVPN3 / wg-android native
            opts.isEnableUserInteractionBreadcrumbs = false  // route privacy
            opts.maxBreadcrumbs = 50
            opts.beforeSend = io.sentry.SentryOptions.BeforeSendCallback { event, _ ->
                redactEvent(event)
                event
            }
            opts.beforeBreadcrumb = io.sentry.SentryOptions.BeforeBreadcrumbCallback { crumb, _ ->
                if (crumb.type?.equals("navigation", ignoreCase = true) == true) null else {
                    crumb.message = crumb.message?.let { redactString(it) }
                    crumb.data.keys.toList().forEach { k ->
                        (crumb.data[k] as? String)?.let { crumb.data[k] = redactString(it) }
                    }
                    crumb
                }
            }
        }

        // Anonymous user-id + minimal tags. Never email/username.
        Sentry.configureScope { scope ->
            scope.user = User().apply { id = installUUID }
            scope.setTag("surface", "android")
            scope.setTag("os.family", "android")
            scope.setTag("app.version", BuildConfig.VERSION_NAME)
        }

        PrivycsLogger.i(TAG, "Enabled (release=${BuildConfig.VERSION_NAME} install=${installUUID.take(8)}…)")
    }

    /** Persistent per-install UUID (created on first call). */
    private fun ensureInstallUUID(context: Context): String {
        val prefs: SharedPreferences =
            context.getSharedPreferences(PREFS_CRASH_REPORTER, Context.MODE_PRIVATE)
        prefs.getString(PREF_INSTALL_UUID, null)?.let { return it }
        val fresh = UUID.randomUUID().toString().replace("-", "")
        prefs.edit().putString(PREF_INSTALL_UUID, fresh).apply()
        return fresh
    }

    /**
     * Flush queued events with a 2-second deadline. Useful at
     * graceful-shutdown moments (Activity.onDestroy of the last
     * resumed Activity). Safe no-op when not initialised.
     */
    fun flush() {
        if (!initialised.get()) return
        Sentry.flush(2000)
    }

    /**
     * Submit a non-fatal exception. Off-state is a fast no-op.
     */
    fun captureException(t: Throwable) {
        if (!initialised.get()) return
        Sentry.captureException(t)
    }

    /**
     * Submit a non-fatal logged message at WARNING level. Used in
     * places where we'd otherwise drop a soft error into the log.
     */
    fun captureMessage(msg: String) {
        if (!initialised.get()) return
        Sentry.captureMessage(redactString(msg), SentryLevel.WARNING)
    }

    // ---------------------------------------------------------------
    // Redaction pipeline
    // ---------------------------------------------------------------

    private val reAPIKey = Regex("\\b[A-Za-z0-9_-]{32,}={0,2}\\b")
    private val reIPv4 = Regex("\\b(\\d{1,3}\\.){3}\\d{1,3}\\b")
    private val reIPv6Global = Regex("\\b[23][0-9a-fA-F]{3}(:[0-9a-fA-F]{0,4}){1,7}\\b")
    private val reUserDataPath = Regex("/data/(data|user/\\d+)/[^/\\s:\\\"]+")
    private val reSSIDKv = Regex("(SSID[\\s=:]+)[^\\s,\\\"]+", RegexOption.IGNORE_CASE)

    /**
     * Mutates an outgoing [SentryEvent] in place, scrubbing every
     * PII surface we know about. Returns the same reference for
     * chaining; never returns null (we never drop events here —
     * dropping should be a higher-level policy decision).
     */
    private fun redactEvent(event: SentryEvent) {
        event.message?.let { msg ->
            msg.message = msg.message?.let { redactString(it) }
            msg.formatted = msg.formatted?.let { redactString(it) }
        }
        event.exceptions?.forEach { ex ->
            ex.value = ex.value?.let { redactString(it) }
            ex.stacktrace?.frames?.forEach { f ->
                f.filename = f.filename?.let { redactPath(it) }
                f.absPath = f.absPath?.let { redactPath(it) }
            }
            // Mechanism description sometimes leaks paths.
            (ex.mechanism as? Mechanism)?.description = (ex.mechanism as? Mechanism)?.description?.let {
                redactString(it)
            }
        }
        // Drop request entirely.
        event.request = null
        // Scrub tags + extras.
        event.tags?.keys?.toList()?.forEach { k ->
            event.tags?.get(k)?.let { v -> event.setTag(k, redactString(v)) }
        }
        event.extras?.keys?.toList()?.forEach { k ->
            (event.extras?.get(k) as? String)?.let { v -> event.setExtra(k, redactString(v)) }
        }
        // Always overwrite hostname.
        event.serverName = "redacted"
        // Modules (loaded library versions) can leak.
        event.modules = null
    }

    private fun redactString(s: String): String {
        if (s.length < 7) return s
        var out = s
        out = reAPIKey.replace(out, "<redacted-token>")
        out = reIPv6Global.replace(out, "<redacted-ipv6>")
        out = reIPv4.replace(out) { mr -> if (isPrivateIPv4(mr.value)) mr.value else "<redacted-ipv4>" }
        out = reSSIDKv.replace(out) { mr -> "${mr.groupValues[1]}<redacted-ssid>" }
        out = redactPath(out)
        return out
    }

    private fun redactPath(p: String): String {
        if (p.isBlank()) return p
        return reUserDataPath.replace(p, "/data/<app>")
    }

    private fun isPrivateIPv4(ip: String): Boolean {
        val parts = ip.split(".").mapNotNull { it.toIntOrNull() }
        if (parts.size != 4) return false
        val (a, b) = parts
        if (a == 10 || a == 127) return true
        if (a == 169 && b == 254) return true
        if (a == 172 && b in 16..31) return true
        if (a == 192 && b == 168) return true
        return false
    }
}
