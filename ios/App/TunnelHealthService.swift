import Foundation
import Network
import PrivycsCore

@MainActor
extension AppState {

    /// Reachability-based tunnel health monitor — Android TunnelHealthMonitor
    /// parity (HEALTHY / DEGRADED / RECOVERING). While connected it probes a
    /// target host on an interval; consecutive failures escalate the state.
    /// App-foreground only (a NetworkExtension has no app-side timer budget
    /// when backgrounded — documented iOS limitation, like pool rotation).
    func startHealthMonitor() {
        healthTask?.cancel()
        healthTask = Task { [weak self] in
            var fails = 0
            while !Task.isCancelled {
                guard let self else { return }
                let connected = self.status.connected
                let enabled = self.settings.tunnelHealthMode != "off"
                if !connected || !enabled {
                    if self.tunnelHealth != nil { self.tunnelHealth = nil }
                    fails = 0
                } else if !self.appActive {
                    // Foreground-only: don't probe while backgrounded (the app
                    // is suspended, so a probe would spuriously fail). Hold the
                    // last state; onScenePhase restarts us fresh on resume.
                } else {
                    let target = AppState.healthHost(self.settings.tunnelHealthTarget)
                    let dead = self.settings.tunnelHealthDeadThreshold > 0
                        ? self.settings.tunnelHealthDeadThreshold : 3
                    let ok = await AppState.reachable(host: target, timeout: 4)
                    // Live tunnel traffic is itself a positive health signal:
                    // don't report "recovering" while the tunnel is actively
                    // passing bytes just because an external probe target is
                    // unreachable (a wrong/blocked ping target was making the
                    // pill stick on "recovering").
                    let live = self.rxSpeed > 0 || self.txSpeed > 0
                    fails = (ok || live) ? 0 : fails + 1
                    // Grace: a single missed probe stays "healthy" (transient
                    // blips are common); 2+ misses → degraded; dead → recovering.
                    self.tunnelHealth = fails <= 1 ? .healthy
                        : (fails >= dead ? .recovering : .degraded)
                    // Shadow engine (v1.0.9): forward the health verdict.
                    self.engineShadow.observeHealth(self.tunnelHealth)
                    PrivycsLog.log("health: probe \(target):443 ok=\(ok) live=\(live) fails=\(fails)")
                }
                let interval = self.settings.tunnelHealthPingIntervalSec > 0
                    ? self.settings.tunnelHealthPingIntervalSec : 10
                try? await Task.sleep(nanoseconds: UInt64(interval) * 1_000_000_000)
            }
        }
    }

    /// Host portion of a health-probe target: strips any `:port` (and
    /// `[IPv6]:port` brackets) and defaults to 1.1.1.1 when empty. A wrong
    /// target (e.g. "host:51820" or a bare gateway) used to make every probe
    /// fail → the pill stuck on "recovering".
    nonisolated static func healthHost(_ raw: String) -> String {
        let t = raw.trimmingCharacters(in: .whitespaces)
        if t.isEmpty { return "1.1.1.1" }
        if t.hasPrefix("[") {                       // [IPv6]:port
            if let close = t.firstIndex(of: "]") {
                return String(t[t.index(after: t.startIndex)..<close])
            }
            return t
        }
        let parts = t.split(separator: ":")
        return parts.count == 2 ? String(parts[0]) : t   // host:port → host; bare IPv6 untouched
    }

    /// TCP-connect reachability probe (no raw-ICMP entitlement needed):
    /// true iff a connection to host:443 reaches `.ready` within `timeout`.
    nonisolated static func reachable(host: String, timeout: TimeInterval) async -> Bool {
        await withCheckedContinuation { (cont: CheckedContinuation<Bool, Never>) in
            let conn = NWConnection(host: NWEndpoint.Host(host), port: 443, using: .tcp)
            let once = ResumeOnce()
            func finish(_ ok: Bool) {
                once.run {
                    conn.cancel()
                    cont.resume(returning: ok)
                }
            }
            conn.stateUpdateHandler = { state in
                switch state {
                case .ready: finish(true)
                case .failed, .cancelled: finish(false)
                default: break
                }
            }
            conn.start(queue: .global())
            DispatchQueue.global().asyncAfter(deadline: .now() + timeout) { finish(false) }
        }
    }
}

/// Single-shot guard so the reachability continuation resumes exactly once
/// across the NWConnection state callbacks + the timeout.
private final class ResumeOnce: @unchecked Sendable {
    private let lock = NSLock()
    private var done = false
    func run(_ block: () -> Void) {
        lock.lock()
        let already = done
        done = true
        lock.unlock()
        if !already { block() }
    }
}
