#if os(macOS)
import Foundation

/// macOS stand-in for the iOS `BackgroundRotation` (App/BackgroundRotation.swift,
/// excluded from the macOS target). BGTaskScheduler / the BackgroundTasks
/// framework is unavailable on native macOS, so background pool rotation is a
/// no-op here — the foreground rotation timer in AppState is the only path. A
/// running Mac app keeps the timer alive, which covers the typical desktop case.
enum BackgroundRotation {
    static let taskID = "com.privycs.vpn.poolrotate"

    /// Set by AppState; never invoked on macOS (no OS background task).
    nonisolated(unsafe) static var onRotate: (() async -> Void)?

    static func register() {}
    static func schedule(at date: Date) {}
    static func cancel() {}
}
#endif
