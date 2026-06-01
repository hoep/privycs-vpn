import Foundation
import BackgroundTasks

/// Background pool rotation via BGTaskScheduler — best-effort iOS analogue
/// of Android's AlarmManager-driven PoolRotationScheduler. iOS only runs
/// the task opportunistically (no precise timing, tight budget), so this
/// supplements the foreground rotation timer rather than replacing it.
/// Documented limitation: rotation while backgrounded fires "eventually",
/// not exactly at the interval.
enum BackgroundRotation {
    static let taskID = "com.privycs.vpn.poolrotate"

    /// Set by AppState — performs one rotation when the OS runs the task.
    nonisolated(unsafe) static var onRotate: (() async -> Void)?

    /// Register the launch handler. MUST be called during app launch
    /// (PrivycsVPNApp.init), before the first scene is shown.
    static func register() {
        BGTaskScheduler.shared.register(forTaskWithIdentifier: taskID, using: nil) { task in
            let work = Task {
                await onRotate?()
                task.setTaskCompleted(success: true)
            }
            task.expirationHandler = {
                work.cancel()
                task.setTaskCompleted(success: false)
            }
        }
    }

    /// Request a background rotation no earlier than `date`.
    static func schedule(at date: Date) {
        let req = BGProcessingTaskRequest(identifier: taskID)
        req.requiresNetworkConnectivity = true
        req.requiresExternalPower = false
        req.earliestBeginDate = date
        try? BGTaskScheduler.shared.submit(req)
    }

    static func cancel() {
        BGTaskScheduler.shared.cancel(taskRequestWithIdentifier: taskID)
    }
}
