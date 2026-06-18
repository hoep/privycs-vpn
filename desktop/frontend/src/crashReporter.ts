import * as Sentry from '@sentry/vue'
import type { App } from 'vue'
import type { Router } from 'vue-router'

// v1.0.7 — Vue-side crash reporting to self-hosted Bugsink.
//
// Mirrors the Go-side init in crash_reporter.go with the same
// privacy-first guarantees:
//
//   - Opt-in only. main.ts gates this initCrashReporter call on
//     settings.crash_reports_enabled. If the user toggles off
//     at runtime, the SDK's client is closed via close().
//
//   - Anonymous user.id = install UUID generated server-side by
//     the Go side. Never linked to API key / license key / email.
//
//   - beforeSend strips: SSIDs, API keys, license keys, public
//     IPs (RFC1918 / loopback / ULA / link-local kept for local-
//     network debug), user-path segments, HTTP request bodies.
//
//   - Session replay DISABLED. Don't init the @sentry/replay
//     package; explicit comment so future me / reviewer remembers
//     why.
//
//   - Wails IPC errors are dropped because the Go side already
//     captures them with proper context. We catch the same errors
//     in Vue land too — dedup is the responsibility of Bugsink's
//     issue-grouping.

// Project-2 DSN on the Bugsink backend (Vue is treated as a
// separate "project" in Bugsink so frontend-only issues are
// grouped distinctly from Go panics). Public token, OK to ship.
const SENTRY_DSN = 'https://940cdbe1f2944090ae2480cb75c601b7@crashes.privycs.com/1'

// v1.0.7: SDK initialised state. Allows runtime toggle-off without
// reloading the page.
let initialised = false

export function initCrashReporter(app: App, router: Router, installUUID: string): void {
  if (initialised) return
  if (!installUUID) {
    // Refuse to init without a stable user-id; the Go side is
    // responsible for generating it before the Vue layer runs.
    // eslint-disable-next-line no-console
    console.warn('CrashReporter: no install UUID — refusing init')
    return
  }

  Sentry.init({
    app,
    dsn: SENTRY_DSN,
    release: `privycs-vpn-frontend@${(window as any).__APP_VERSION__ || 'dev'}`,
    environment: (window as any).__APP_VERSION__ === 'dev' ? 'dev' : 'production',

    // Always send error events; we already gate via opt-in.
    sampleRate: 1.0,

    // No performance/traces. We're not paying tx/span overhead for
    // a desktop UI.
    tracesSampleRate: 0,

    // No session replay. Replay would screenshot the DOM at error
    // time which could leak SSIDs, API keys in input fields,
    // license keys, etc. Explicit disable so a future SDK default
    // change can't accidentally turn it on.
    replaysSessionSampleRate: 0,
    replaysOnErrorSampleRate: 0,

    // Vue-specific integration. trackComponents=false because we
    // don't want component names spilling into error events; the
    // call-site filename + line is enough.
    integrations: [
      Sentry.vueIntegration({
        app,
        tracingOptions: { trackComponents: false },
      }),
    ],

    // Drop the SDK's automatic breadcrumbs that can leak route URLs.
    // Allowed: console, dom (click events), fetch (timing only).
    // Filtered: navigation (route URLs).
    beforeBreadcrumb(breadcrumb) {
      if (!breadcrumb) return breadcrumb
      if (breadcrumb.type === 'navigation') return null
      if (breadcrumb.message) {
        breadcrumb.message = redactString(breadcrumb.message)
      }
      if (breadcrumb.data) {
        for (const k of Object.keys(breadcrumb.data)) {
          const v = breadcrumb.data[k]
          if (typeof v === 'string') {
            breadcrumb.data[k] = redactString(v)
          }
        }
      }
      return breadcrumb
    },

    // Top-level PII redaction pass on every outgoing event.
    beforeSend(event) {
      if (!event) return event
      // Strip top-level message.
      if (event.message) event.message = redactString(event.message)
      // Exception values + frame filenames.
      if (event.exception?.values) {
        for (const ex of event.exception.values) {
          if (ex.value) ex.value = redactString(ex.value)
          if (ex.stacktrace?.frames) {
            for (const f of ex.stacktrace.frames) {
              if (f.filename) f.filename = redactPath(f.filename)
              if (f.abs_path) f.abs_path = redactPath(f.abs_path)
            }
          }
        }
      }
      // Drop request body + headers (they can hold the API key
      // in an Authorization header if a fetch span leaked it).
      if (event.request) {
        event.request = undefined
      }
      // Scrub tags + extra.
      if (event.tags) {
        for (const k of Object.keys(event.tags)) {
          const v = event.tags[k]
          if (typeof v === 'string') event.tags[k] = redactString(v)
        }
      }
      if (event.extra) {
        for (const k of Object.keys(event.extra)) {
          const v = event.extra[k]
          if (typeof v === 'string') event.extra[k] = redactString(v)
        }
      }
      // server_name is browser-side; nothing to redact.
      return event
    },

    // Cap breadcrumbs so a chatty session can't ship megabytes of
    // logs on a single crash event.
    maxBreadcrumbs: 50,
  })

  // Anonymous user id + minimal tags. NEVER set email / username.
  Sentry.setUser({ id: installUUID })
  Sentry.setTag('surface', 'frontend')
  Sentry.setTag('os.family', getOSFamilyFromUA())

  // Vue Router error capture. router.onError is the official hook
  // for navigation failures; we forward to Sentry.
  router.onError((err) => {
    Sentry.captureException(err)
  })

  initialised = true
}

// Public API for the settings toggle. Off-state forces the SDK
// client to flush + drain so any pending crash events get sent if
// the user is toggling off AFTER a crash — then close().
export async function closeCrashReporter(): Promise<void> {
  if (!initialised) return
  const client = Sentry.getClient()
  if (client) {
    await client.flush(2000)
    await client.close(2000)
  }
  initialised = false
}

// ---- redaction helpers ---------------------------------------

const reAPIKey = /\b[A-Za-z0-9_-]{32,}={0,2}\b/g
const reIPv4 = /\b(\d{1,3}\.){3}\d{1,3}\b/g
const reIPv6Global = /\b[23][0-9a-fA-F]{3}(:[0-9a-fA-F]{0,4}){1,7}\b/g
const reUserPathUnix = /(\/(Users|home))\/[^/\s:"]+/g
const reUserPathWin = /(C:\\Users\\)[^\\/\s:"]+/gi
// Wi-Fi SSID — strip the value after an ssid key (ssid=Foo, "ssid":"Foo",
// SSID: Foo). Mirrors Android reSSIDKv + the Go backend; the privacy policy
// claims SSIDs are stripped (audit finding 2026-06-18).
const reSSID = /(ssid["\s=:]+)[^\s,"]+/gi

function redactString(s: string): string {
  if (!s || s.length < 7) return s
  return (
    s
      .replace(reSSID, '$1<redacted-ssid>')
      .replace(reAPIKey, '<redacted-token>')
      .replace(reIPv6Global, '<redacted-ipv6>')
      .replace(reIPv4, (ip) => (isPrivateIPv4(ip) ? ip : '<redacted-ipv4>'))
      .replace(reUserPathUnix, '$1/<user>')
      .replace(reUserPathWin, '$1<user>\\')
  )
}

function redactPath(p: string): string {
  if (!p) return p
  return p.replace(reUserPathUnix, '$1/<user>').replace(reUserPathWin, '$1<user>\\')
}

function isPrivateIPv4(ip: string): boolean {
  const parts = ip.split('.').map((n) => parseInt(n, 10))
  if (parts.length !== 4) return false
  const [a, b] = parts
  if (a === 10 || a === 127) return true
  if (a === 169 && b === 254) return true
  if (a === 172 && b >= 16 && b <= 31) return true
  if (a === 192 && b === 168) return true
  return false
}

function getOSFamilyFromUA(): string {
  const ua = navigator.userAgent.toLowerCase()
  if (ua.includes('mac')) return 'darwin'
  if (ua.includes('win')) return 'windows'
  if (ua.includes('linux')) return 'linux'
  return 'unknown'
}
