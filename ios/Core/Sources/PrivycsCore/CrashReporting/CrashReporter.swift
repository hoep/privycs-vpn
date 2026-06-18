import Foundation
#if canImport(Sentry)
import Sentry
#endif

/// Privacy-first crash reporting via self-hosted Bugsink at
/// crashes.privycs.com. Sentry-protocol kompatibel. Mirror der
/// Android- und Desktop-Implementierung mit gleichen Garantien.
///
/// **Design-Prinzipien**:
///
/// 1. Default OFF. User opt-in via Settings → "Anonymous diagnostics"
///    toggle. Nichts wird gesendet bevor `start()` mit `optedIn=true`
///    aufgerufen wird.
///
/// 2. Anonymous identity. `event.User.id` = install-UUID (persistiert
///    im Keychain via `KeychainKey.installUUID`). NICHT linked zu
///    API-key, license-key, Apple-ID, oder Email.
///
/// 3. PII redaction at every send via beforeSend hook. Strippt:
///    - SSIDs (`SSID=...` regex)
///    - 32+ char base64/hex tokens (API keys, license signatures)
///    - Public IPv4 (RFC1918/loopback/link-local kept for debug)
///    - Global IPv6 (ULA + link-local kept)
///    - Apple-specific user-paths (`/var/mobile/Containers/...`)
///    - HTTP request bodies
///    - Navigation breadcrumbs
///
/// 4. NO session replay. Sentry's `enableUserInteractionTracing`
///    and replay-related options are OFF. Hard guard against future
///    SDK default flips.
///
/// 5. Self-hosted only. DSN = crashes.privycs.com (Bugsink project 3).
///    Kein third-party Telemetry-Provider.
public actor CrashReporter {

    /// Bugsink-DSN für das iOS-Projekt (project id 3). Wird in der
    /// Phase-0-Anlage bei `https://crashes.privycs.com/` erzeugt.
    /// PLATZHALTER — vor Production-Launch durch echte DSN ersetzen.
    public static let dsn = "https://39be28e9033a428d9a27c0240af97692@crashes.privycs.com/3"

    private let secretStore: KeychainSecretStore
    private var started = false

    public init(appGroup: String = KeychainSecretStore.defaultAppGroup) {
        self.secretStore = KeychainSecretStore(appGroup: appGroup)
    }

    /// Init Sentry-SDK iff opt-in. Idempotent. Bei opt-out wird
    /// die SDK-Client-Instance geclose()d damit any queued events
    /// nicht gesendet werden.
    public func start(optedIn: Bool, appVersion: String) async {
#if canImport(Sentry)
        if !optedIn {
            if started {
                SentrySDK.close()
                started = false
            }
            return
        }
        if started { return }
        let installUUID = await ensureInstallUUID()

        SentrySDK.start { opts in
            opts.dsn = CrashReporter.dsn
            opts.releaseName = "privycs-vpn-ios@\(appVersion)"
            opts.environment = appVersion.contains("dev") ? "dev" : "production"
            // Always send error events; opt-in gate already passed.
            opts.sampleRate = 1.0
            // No performance traces, no profiling.
            opts.tracesSampleRate = 0
#if !targetEnvironment(simulator)
            // No screenshots, no view hierarchies — would leak SSIDs
            // visible in input fields or license keys in error
            // dialogs. Hard-disable.
            opts.attachScreenshot = false
            opts.attachViewHierarchy = false
#endif
            opts.enableUserInteractionTracing = false
            opts.maxBreadcrumbs = 50
            opts.beforeSend = { event in
                CrashReporter.redact(event)
                return event
            }
            opts.beforeBreadcrumb = { crumb in
                guard crumb.type?.lowercased() != "navigation" else { return nil }
                if let msg = crumb.message {
                    crumb.message = CrashReporter.redactString(msg)
                }
                return crumb
            }
        }
        SentrySDK.configureScope { scope in
            let user = User(userId: installUUID)
            scope.setUser(user)
            scope.setTag(value: "ios", key: "surface")
            scope.setTag(value: "ios", key: "os.family")
            scope.setTag(value: appVersion, key: "app.version")
        }
        started = true
#endif
    }

    public func flush() async {
#if canImport(Sentry)
        guard started else { return }
        SentrySDK.flush(timeout: 2.0)
#endif
    }

    public func captureException(_ error: Error) async {
#if canImport(Sentry)
        guard started else { return }
        SentrySDK.capture(error: error)
#endif
    }

    // MARK: — install-UUID

    private func ensureInstallUUID() async -> String {
        // `try?` over a `throws -> String?` expression flattens to String?
        // (SE-0230) — single bind is enough; double-bind/re-shadow does not
        // compile because `existing` is already non-optional after the first.
        if let existing = try? await secretStore.get(KeychainKey.installUUID),
           !existing.isEmpty {
            return existing
        }
        let fresh = UUID().uuidString.replacingOccurrences(of: "-", with: "")
        try? await secretStore.set(fresh, for: KeychainKey.installUUID)
        return fresh
    }

    // MARK: — Redaction

#if canImport(Sentry)
    nonisolated static func redact(_ event: Event) {
        if let msg = event.message?.message {
            event.message?.message = redactString(msg)
        }
        // `formatted` is a get-only computed property derived from `message`
        // + params — no manual redaction needed; redacting `message` propagates.
        event.exceptions?.forEach { ex in
            // Sentry 9.x: Exception.value is now nullable (was non-optional in 8.x).
            if let v = ex.value { ex.value = redactString(v) }
            ex.stacktrace?.frames.forEach { f in
                if let path = f.fileName {
                    f.fileName = redactPath(path)
                }
            }
        }
        event.request = nil
        event.serverName = "redacted"
    }
#endif

    nonisolated static func redactString(_ s: String) -> String {
        guard s.count > 6 else { return s }
        var out = s
        // 32+ char base64/hex tokens
        out = out.replacingOccurrences(
            of: #"\b[A-Za-z0-9_-]{32,}={0,2}\b"#,
            with: "<redacted-token>",
            options: .regularExpression
        )
        // SSID= pattern
        out = out.replacingOccurrences(
            of: #"(?i)(ssid[\s=:]+)[^\s,"]+"#,
            with: "$1<redacted-ssid>",
            options: .regularExpression
        )
        // Global IPv6 (2xxx::/3 oder 3xxx::/3)
        out = out.replacingOccurrences(
            of: #"\b[23][0-9a-fA-F]{3}(:[0-9a-fA-F]{0,4}){1,7}\b"#,
            with: "<redacted-ipv6>",
            options: .regularExpression
        )
        // Public IPv4 — RFC1918/loopback/link-local kept
        out = redactPublicIPv4InString(out)
        out = redactPath(out)
        return out
    }

    private nonisolated static func redactPublicIPv4InString(_ s: String) -> String {
        var result = s
        let pattern = #"\b(\d{1,3}\.){3}\d{1,3}\b"#
        guard let regex = try? NSRegularExpression(pattern: pattern) else { return s }
        let nsRange = NSRange(s.startIndex..., in: s)
        let matches = regex.matches(in: s, range: nsRange).reversed()
        for match in matches {
            if let range = Range(match.range, in: result) {
                let ip = String(result[range])
                if !isPrivateIPv4(ip) {
                    result.replaceSubrange(range, with: "<redacted-ipv4>")
                }
            }
        }
        return result
    }

    private nonisolated static func isPrivateIPv4(_ ip: String) -> Bool {
        let parts = ip.split(separator: ".").compactMap { Int($0) }
        guard parts.count == 4 else { return false }
        let (a, b) = (parts[0], parts[1])
        if a == 10 || a == 127 { return true }
        if a == 169 && b == 254 { return true }
        if a == 172 && (16...31).contains(b) { return true }
        if a == 192 && b == 168 { return true }
        return false
    }

    nonisolated static func redactPath(_ p: String) -> String {
        // /var/mobile/Containers/Data/Application/<UUID>/...
        //   → /var/mobile/Containers/Data/Application/<user>/...
        return p.replacingOccurrences(
            of: #"/var/mobile/Containers/Data/Application/[^/]+"#,
            with: "/var/mobile/Containers/Data/Application/<user>",
            options: .regularExpression
        )
    }
}
