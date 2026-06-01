import Foundation

/// Persistierte Pro-Tier Entitlement-State. Drei mögliche
/// Quellen:
///   1. StoreKit 2 In-App Purchase (`com.privycs.vpn.pro_lifetime`)
///   2. ed25519-signed License-Key importiert vom User (LemonSqueezy
///      Cross-Platform-Bundle)
///   3. Gateway-issued License via Privycs-Subscription auf
///      privycs.com (Pro-Tier via Pull from Gateway)
///
/// Alle drei werden über die gleiche `EntitlementState` Surface
/// dargestellt, sodass die UI nicht wissen muss welcher Pfad benutzt
/// wurde. `proGateAllowed()` ist der zentrale Gate für die 6
/// Feature-Flags (analog Android).
///
/// **GATING_ENABLED Flag** ist DORMANT-Mode (false) bis Production-
/// Launch — alle proGateAllowed() Aufrufe returnen true, also
/// kein User wird gegated bevor Pricing live ist.
public actor EntitlementRepository {

    /// Master-Flag. Setze auf true beim Production-Launch (v1.0.x
    /// post-Apple-Review-Pass).
    public static let gatingEnabled = false

    private let userDefaults: UserDefaults
    private let secretStore: KeychainSecretStore
    private let licenseVerifier: LicenseVerifier
    private let stateKey = "privycs.entitlement.v1"

    public init(
        appGroup: String = KeychainSecretStore.defaultAppGroup,
        secretStore: KeychainSecretStore? = nil,
        licenseVerifier: LicenseVerifier? = nil
    ) {
        guard let suite = UserDefaults(suiteName: appGroup) else {
            self.userDefaults = .standard
            self.secretStore = secretStore ?? KeychainSecretStore(appGroup: appGroup)
            self.licenseVerifier = licenseVerifier ?? LicenseVerifier()
            return
        }
        self.userDefaults = suite
        self.secretStore = secretStore ?? KeychainSecretStore(appGroup: appGroup)
        self.licenseVerifier = licenseVerifier ?? LicenseVerifier()
    }

    /// Lädt persistierten State. Falls eine License-Key im Keychain
    /// liegt, wird sie reverified — eine Lizenz die seit dem
    /// letzten Boot expired ist wird hier abgewiesen.
    public func currentState() async throws -> EntitlementState {
        var state: EntitlementState
        if let data = userDefaults.data(forKey: stateKey),
           let s = try? JSONDecoder().decode(EntitlementState.self, from: data) {
            state = s
        } else {
            state = EntitlementState()
        }
        // Reverify the license key from keychain — guards against
        // backup-restore on a clock-rolled-back device, malformed
        // tampering etc.
        // `try?` flattens Optionals (SE-0230) — get throws -> String? yields String? not String??
        if let key = try? await secretStore.get(KeychainKey.proLicense) {
            do {
                let payload = try licenseVerifier.verify(key)
                if LicenseVerifier.unlocksIOS(payload) {
                    state.licenseSku = payload.sku
                    state.licenseEmailHash = payload.buyerEmailHash ?? ""
                    state.licenseValid = true
                } else {
                    state.licenseValid = false
                }
            } catch {
                state.licenseValid = false
            }
        }
        return state
    }

    /// Speichert den State. License-Key wird parallel ins Keychain
    /// geschrieben (im state-JSON nur Metadata, nicht der raw key).
    public func setState(_ state: EntitlementState, licenseKey: String? = nil) async throws {
        if let licenseKey {
            if licenseKey.isEmpty {
                try await secretStore.delete(KeychainKey.proLicense)
            } else {
                try await secretStore.set(licenseKey, for: KeychainKey.proLicense)
            }
        }
        let data = try JSONEncoder().encode(state)
        userDefaults.set(data, forKey: stateKey)
    }

    /// Zentraler Gate-Check. Aufruf-Sites siehe Android
    /// `proGateAllowed()` für die 6 Feature-Bereiche.
    public func proGateAllowed() async -> Bool {
        if !EntitlementRepository.gatingEnabled { return true }
        guard let state = try? await currentState() else { return false }
        return state.hasActivePro()
    }

    /// Imports eine ed25519-signed License-Key. Verifiziert sofort.
    /// Wirft LicenseError wenn invalide. Bei Erfolg wird der Key
    /// ins Keychain + Metadata in State geschrieben.
    public func importLicenseKey(_ key: String) async throws -> LicensePayload {
        let payload = try licenseVerifier.verify(key)
        guard LicenseVerifier.unlocksIOS(payload) else {
            throw EntitlementError.tierDoesNotUnlockIOS(payload.sku)
        }
        var state = (try? await currentState()) ?? EntitlementState()
        state.licenseSku = payload.sku
        state.licenseEmailHash = payload.buyerEmailHash ?? ""
        state.licenseValid = true
        state.licenseImportedAt = Int64(Date().timeIntervalSince1970)
        try await setState(state, licenseKey: key)
        return payload
    }

    /// Wird vom StoreKit-Observer aufgerufen wenn ein In-App
    /// Purchase als entitled bestätigt wurde.
    public func markStoreKitEntitled(_ productID: String) async throws {
        var state = (try? await currentState()) ?? EntitlementState()
        state.storeKitProductID = productID
        state.storeKitEntitled = true
        state.storeKitEntitledAt = Int64(Date().timeIntervalSince1970)
        try await setState(state, licenseKey: nil)
    }

    /// Reset alles entitlement-related. Wird vom "Reset App" Flow
    /// aufgerufen.
    public func wipe() async throws {
        userDefaults.removeObject(forKey: stateKey)
        try await secretStore.delete(KeychainKey.proLicense)
    }
}

public struct EntitlementState: Codable, Equatable, Sendable {
    public var storeKitProductID: String
    public var storeKitEntitled: Bool
    public var storeKitEntitledAt: Int64
    /// SKU of the imported license (privycs_pro_desktop | privycs_pro_bundle_all).
    public var licenseSku: String
    /// SHA-256 hash of the buyer email (the signer never ships the raw
    /// email — only its hash). Display-only.
    public var licenseEmailHash: String
    public var licenseValid: Bool
    public var licenseImportedAt: Int64

    public init(
        storeKitProductID: String = "",
        storeKitEntitled: Bool = false,
        storeKitEntitledAt: Int64 = 0,
        licenseSku: String = "",
        licenseEmailHash: String = "",
        licenseValid: Bool = false,
        licenseImportedAt: Int64 = 0
    ) {
        self.storeKitProductID = storeKitProductID
        self.storeKitEntitled = storeKitEntitled
        self.storeKitEntitledAt = storeKitEntitledAt
        self.licenseSku = licenseSku
        self.licenseEmailHash = licenseEmailHash
        self.licenseValid = licenseValid
        self.licenseImportedAt = licenseImportedAt
    }

    public func hasActivePro() -> Bool {
        if storeKitEntitled { return true }
        if licenseValid { return true }
        return false
    }

    private enum CodingKeys: String, CodingKey {
        case storeKitProductID = "store_kit_product_id"
        case storeKitEntitled = "store_kit_entitled"
        case storeKitEntitledAt = "store_kit_entitled_at"
        case licenseSku = "license_sku"
        case licenseEmailHash = "license_email_hash"
        case licenseValid = "license_valid"
        case licenseImportedAt = "license_imported_at"
    }
}

public enum EntitlementError: Error, Equatable {
    case tierDoesNotUnlockIOS(String)

    public var localizedDescription: String {
        switch self {
        case .tierDoesNotUnlockIOS(let sku):
            return "License '\(sku)' does not include iOS. Need the cross-platform bundle."
        }
    }
}
