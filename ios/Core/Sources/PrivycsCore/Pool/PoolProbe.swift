import Foundation
#if canImport(Glibc)
import Glibc
#else
import Darwin
#endif

/// Pre-connect reachability probe for a pool member — port of Android's
/// `PoolProbe.probeMember` (used by `PoolConnector.preWarm`).
///
/// It answers one cheap question before we touch the live tunnel: does this
/// member's endpoint still resolve? Rotation without it tears a working tunnel
/// down for a member that may be dead, and the user sits disconnected until the
/// retry loop finds a live one. A bare IP endpoint needs no DNS and passes
/// immediately, exactly as on Android.
///
/// Returns nil when the member looks usable, or a short reason string.
public enum PoolProbe {

    /// DNS timeout per member. Android: PROBE_DNS_TIMEOUT_MS = 2s.
    public static let dnsTimeout: TimeInterval = 2

    public static func probe(member: PoolMember) async -> String? {
        let endpoint = member.config.serverAddress
        if endpoint.trimmingCharacters(in: .whitespaces).isEmpty {
            return "probe: member has no endpoint"
        }
        let host = stripPortIfPresent(endpoint)
        if host.isEmpty { return "probe: empty host" }
        // Bare IP → nothing to resolve.
        if isBareIP(host) { return nil }
        return await resolve(host: host, timeout: dnsTimeout)
    }

    /// Resolves `host`, returning nil on success or a reason on failure.
    /// getaddrinfo has no cancellation, so it runs on its own thread and the
    /// caller gives up after `timeout` — a stalled resolver must not hold up a
    /// rotation.
    static func resolve(host: String, timeout: TimeInterval) async -> String? {
        await withCheckedContinuation { (cont: CheckedContinuation<String?, Never>) in
            let done = Lock()
            func finish(_ result: String?) {
                if done.claim() { cont.resume(returning: result) }
            }
            Thread.detachNewThread {
                var hints = addrinfo()
                hints.ai_family = AF_UNSPEC
                #if canImport(Glibc)
                // Glibc types SOCK_STREAM as __socket_type, Darwin as Int32.
                hints.ai_socktype = Int32(SOCK_STREAM.rawValue)
                #else
                hints.ai_socktype = SOCK_STREAM
                #endif
                var res: UnsafeMutablePointer<addrinfo>?
                let rc = getaddrinfo(host, nil, &hints, &res)
                defer { if res != nil { freeaddrinfo(res) } }
                if rc != 0 || res == nil {
                    // Deliberately host-free: the endpoint can be a private
                    // hostname and these strings reach the log + the UI.
                    finish("probe: dns failed")
                } else {
                    finish(nil)
                }
            }
            DispatchQueue.global().asyncAfter(deadline: .now() + timeout) {
                finish("probe: dns timeout")
            }
        }
    }

    /// Strips a `host:port` endpoint down to the host. Mirrors the Android and
    /// desktop helpers of the same name.
    public static func stripPortIfPresent(_ s: String) -> String {
        let trimmed = s.trimmingCharacters(in: .whitespaces)
        if trimmed.isEmpty { return "" }
        // Bare IP literal (v4 or v6, no port).
        if isBareIP(trimmed) { return trimmed }
        // Bracketed IPv6, with or without port: "[2001:db8::1]:51820".
        if trimmed.hasPrefix("["), let close = trimmed.firstIndex(of: "]") {
            return String(trimmed[trimmed.index(after: trimmed.startIndex)..<close])
        }
        // Hostname with port: split at the last colon when the tail is numeric.
        if let colon = trimmed.lastIndex(of: ":") {
            let tail = trimmed[trimmed.index(after: colon)...]
            if !tail.isEmpty, tail.allSatisfy(\.isNumber) {
                return String(trimmed[trimmed.startIndex..<colon])
            }
        }
        return trimmed
    }

    /// True for an IPv4 or IPv6 literal.
    public static func isBareIP(_ s: String) -> Bool {
        var v4 = in_addr()
        if inet_pton(AF_INET, s, &v4) == 1 { return true }
        var v6 = in6_addr()
        return inet_pton(AF_INET6, s, &v6) == 1
    }

    /// One-shot guard so the resolver thread and the timeout can race without
    /// resuming the continuation twice.
    final class Lock: @unchecked Sendable {
        private var taken = false
        private let mutex = NSLock()
        func claim() -> Bool {
            mutex.lock(); defer { mutex.unlock() }
            if taken { return false }
            taken = true
            return true
        }
    }
}
