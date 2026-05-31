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
    public static let version = "1.0.7-alpha"
    public static let appGroupIdentifier = KeychainSecretStore.defaultAppGroup
}
