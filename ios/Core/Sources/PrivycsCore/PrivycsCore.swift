import Foundation

/// PrivycsCore — Shared business logic für Privycs VPN iOS App
/// und PacketTunnelProvider Network Extension.
///
/// Public API surface:
///   - **Models**: SavedConnection, ProtocolConfig, VpnProtocol,
///     Pool, PoolMember, PoolPolicy, PoolRotation, PoolSplitTunnel,
///     NetworkRule, NetworkState, AppSettings, VpnStatus
///   - **Storage**: KeychainSecretStore, ConnectionRepository,
///     SettingsRepository, PoolRepository, NetworkRulesRepository
///   - **Pool Logic**: PoolRotator, GeoNearestPicker
///   - **Network**: NetworkMonitor, NetworkRulesEngine
///   - **Crypto**: LicenseVerifier (ed25519 cross-platform)
///   - **Entitlement**: EntitlementRepository (StoreKit + License + Gateway)
///   - **Telemetry**: CrashReporter (opt-in self-hosted Bugsink)
///   - **I18n**: SupportedLocale, L10n
///
/// Multi-target consumers:
///   - App-Target — UI views + ViewModels
///   - PacketTunnelProvider-Target — tunnel-side state + storage access
///
/// Trennung App/Tunnel über App Group:
///   - UserDefaults(suiteName: "group.com.privycs.vpn")
///   - Keychain access group: "group.com.privycs.vpn"
///
/// Beide Targets müssen die `App Groups` capability aktivieren mit
/// dem gleichen identifier und müssen das gleiche Provisioning-
/// Profile mit `keychain-access-groups` entitlement haben.
public enum PrivycsCoreInfo {
    // Crash-report appVersion + fallback for the Settings version row (the row
    // itself prefers the bundle's CFBundleShortVersionString). Keep in sync
    // with MARKETING_VERSION in ios/project.yml on each release.
    public static let version = "1.0.9"
    public static let appGroupIdentifier = KeychainSecretStore.defaultAppGroup

    /// ed25519 license public-key hex, injected at build time via the
    /// app Info.plist key `LicensePublicKeyHex` (← `$(LICENSE_PUBLIC_KEY_HEX)`
    /// CI secret — the SAME key Android/Desktop embed). Empty when the
    /// secret isn't set, in which case the verifier fails closed.
    public static var licensePublicKeyHex: String {
        (Bundle.main.object(forInfoDictionaryKey: "LicensePublicKeyHex") as? String) ?? ""
    }
}
