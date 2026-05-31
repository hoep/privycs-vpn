import Foundation

/// Persistente AppSettings-Storage via App-Group UserDefaults.
/// API-Key + License-Key landen NICHT hier sondern im Keychain
/// (siehe `KeychainSecretStore`); das Field `apiKey` in AppSettings
/// ist Session-Cache, immer leer im persisted-Set.
///
/// Mirror der Android `SettingsRepository` Semantik — DataStore →
/// hier UserDefaults — gleiche AsyncSequence-style observability
/// via AsyncStream.
public actor SettingsRepository {

    private let userDefaults: UserDefaults
    private let secretStore: KeychainSecretStore
    private let settingsKey = "privycs.settings.v1"
    private var changeContinuations: [UUID: AsyncStream<AppSettings>.Continuation] = [:]

    public init(
        appGroup: String = KeychainSecretStore.defaultAppGroup,
        secretStore: KeychainSecretStore? = nil
    ) {
        guard let suite = UserDefaults(suiteName: appGroup) else {
            self.userDefaults = .standard
            self.secretStore = secretStore ?? KeychainSecretStore(appGroup: appGroup)
            return
        }
        self.userDefaults = suite
        self.secretStore = secretStore ?? KeychainSecretStore(appGroup: appGroup)
    }

    /// Lädt aktuelle Settings + hydratiert API-Key aus Keychain.
    public func current() async throws -> AppSettings {
        var s: AppSettings
        if let data = userDefaults.data(forKey: settingsKey) {
            s = try JSONDecoder().decode(AppSettings.self, from: data)
        } else {
            s = .default
        }
        if let apiKey = try await secretStore.get(KeychainKey.apiKey) {
            s.apiKey = apiKey
        }
        return s
    }

    /// Speichert Settings. API-Key wird ins Keychain extrahiert
    /// und im persisted JSON auf "" gesetzt.
    public func save(_ settings: AppSettings) async throws {
        var stripped = settings
        if !stripped.apiKey.isEmpty {
            try await secretStore.set(stripped.apiKey, for: KeychainKey.apiKey)
        }
        stripped.apiKey = ""
        let data = try JSONEncoder().encode(stripped)
        userDefaults.set(data, forKey: settingsKey)
        // Notify observers — full hydrated state for convenience.
        var notify = stripped
        notify.apiKey = settings.apiKey
        for (_, c) in changeContinuations {
            c.yield(notify)
        }
    }

    /// AsyncStream der Settings-Veränderungen. Buffer 4 reicht für
    /// burst writes wie Migration; bei Backpressure droppen wir
    /// alte Events.
    public func observe() -> AsyncStream<AppSettings> {
        let id = UUID()
        return AsyncStream<AppSettings>(bufferingPolicy: .bufferingNewest(4)) { continuation in
            Task {
                let initial = (try? await self.current()) ?? .default
                continuation.yield(initial)
                await self.addContinuation(id: id, continuation: continuation)
            }
            continuation.onTermination = { _ in
                Task { await self.removeContinuation(id: id) }
            }
        }
    }

    // MARK: — Private continuation registry

    private func addContinuation(
        id: UUID,
        continuation: AsyncStream<AppSettings>.Continuation
    ) {
        changeContinuations[id] = continuation
    }

    private func removeContinuation(id: UUID) {
        changeContinuations.removeValue(forKey: id)
    }
}
