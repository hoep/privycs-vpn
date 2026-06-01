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
                } else {
                    let target = self.settings.tunnelHealthTarget.isEmpty
                        ? "1.1.1.1" : self.settings.tunnelHealthTarget
                    let dead = self.settings.tunnelHealthDeadThreshold > 0
                        ? self.settings.tunnelHealthDeadThreshold : 3
                    let ok = await AppState.reachable(host: target, timeout: 4)
                    fails = ok ? 0 : fails + 1
                    self.tunnelHealth = fails == 0 ? .healthy
                        : (fails >= dead ? .recovering : .degraded)
                }
                let interval = self.settings.tunnelHealthPingIntervalSec > 0
                    ? self.settings.tunnelHealthPingIntervalSec : 10
                try? await Task.sleep(nanoseconds: UInt64(interval) * 1_000_000_000)
            }
        }
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
