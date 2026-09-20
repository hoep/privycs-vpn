import Foundation

/// Android `ConnectOnDemandSettings` mirror. iOS drives on-demand through
/// NetworkRules (the COD settings were unified into rules), but the field is
/// part of the shared backup wire format — without it an Android export
/// round-trips through iOS with its COD configuration silently dropped.
public struct ConnectOnDemandSettings: Codable, Equatable, Hashable {
    public var enabled: Bool
    /// "wifi" / "mobile" / "wifi_mobile".
    public var trigger: String
    /// "all" / "only" / "except".
    public var ssidMode: String
    public var ssidList: [String]

    public init(enabled: Bool = false,
                trigger: String = "wifi_mobile",
                ssidMode: String = "all",
                ssidList: [String] = []) {
        self.enabled = enabled
        self.trigger = trigger
        self.ssidMode = ssidMode
        self.ssidList = ssidList
    }

    private enum CodingKeys: String, CodingKey {
        case enabled, trigger
        case ssidMode = "ssid_mode"
        case ssidList = "ssid_list"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        let d = ConnectOnDemandSettings()
        enabled = (try? c.decodeIfPresent(Bool.self, forKey: .enabled)) ?? d.enabled
        trigger = (try? c.decodeIfPresent(String.self, forKey: .trigger)) ?? d.trigger
        ssidMode = (try? c.decodeIfPresent(String.self, forKey: .ssidMode)) ?? d.ssidMode
        ssidList = (try? c.decodeIfPresent([String].self, forKey: .ssidList)) ?? d.ssidList
    }
}

/// Persistente App-Settings — 1:1 Mirror der Android `AppSettings`
/// data class. snake_case CodingKeys damit Export/Restore cross-
/// platform zum Backup-File-Format passt.
public struct AppSettings: Codable, Equatable, Hashable {
    /// Aktuell ausgewähltes Protokoll für die nächste Connect.
    /// Empty = "let the engine decide".
    public var activeProtocol: String

    /// IPv6-leak Kill-Switch on/off (immer on auf iOS in v1 —
    /// keine User-Disable, hardware-block via NEPacketTunnelProvider
    /// includedRoutes). Field kept for cross-platform schema parity.
    public var killSwitchEnabled: Bool

    /// Legacy "Connect at Launch". v1.0+ ersetzt durch
    /// NetworkRules + Auto-Tunnel-Master-Toggle, aber Field bleibt
    /// für Backward-Compat im Backup-Restore.
    public var autoConnectOnStart: Bool

    /// Auto-Tunnel master toggle. Wenn false: keine automatischen
    /// Verbindungen (Connect-on-Demand, Network Rules, Reconnect)
    /// — User-Connect/Disconnect bleibt manuell möglich.
    /// **Master-Switch** für die Engine. Default true.
    public var networkRulesEnabled: Bool

    /// Theme-Wahl. "system" / "dark" / "light".
    public var theme: String

    /// In-App Sprache. "" = Folge System-Default; ansonsten
    /// einer von "en" / "de" / "es" / "fr" / "it" / "pt".
    public var appLanguage: String

    /// User-konfigurierter DNS-Override. Whitespace-separated IPs.
    /// Leer = kein Override.
    public var dnsOverride: String

    /// Log-Level. "debug" / "info" / "warn" / "error".
    public var logLevel: String

    /// Privycs Gateway URL für Pro-Features (Configs-Pull, QR-Enroll).
    /// Leer = nicht konfiguriert.
    public var gatewayURL: String

    /// API-Key für Gateway-Authentication. NUR im Keychain
    /// persistiert; dieses Field hier ist Plaintext-Cache während
    /// der Session und immer leer im Backup-Export.
    public var apiKey: String

    /// Tunnel-Health Probe-Mode. "auto" / "always" / "off".
    public var tunnelHealthMode: String

    /// Tunnel-Health Target-IP für ICMP-Probe.
    /// Empty = built-in 1.1.1.1 default.
    public var tunnelHealthTarget: String

    /// v0.9.15.30 — Tunnel-Health ping cadence. 0 = Default
    /// (5 Sekunden interval × 2 dead-threshold = max 10s reaction).
    public var tunnelHealthPingIntervalSec: Int

    /// 0 = Default (2 consecutive missed pings = dead).
    public var tunnelHealthDeadThreshold: Int

    /// Protocol-Failover-Order. Empty = Default
    /// `[amneziawg, wireguard, openvpn, ipsec]`.
    public var protocolFailoverOrder: [VpnProtocol]

    /// v1.0.7 — anonymous crash-report opt-in. Default false.
    /// Bound zur "Anonymous diagnostics" Toggle in Settings.
    public var crashReportsEnabled: Bool

    /// v1.0.7 — anonymous per-install UUID — never linked zu
    /// API-Key / License / Account-Email. Stable across app
    /// restarts; regenerated bei Uninstall.
    public var installUUID: String

    /// True after at-rest encryption migration completed on this
    /// installation. Informational only; actual encryption-state
    /// gets detected via Keychain entry presence.
    public var encryptedAtRest: Bool

    /// Android Connect-on-Demand block. iOS has no separate COD UI (it was
    /// unified into NetworkRules), but the value is carried verbatim so a
    /// backup written on Android and restored on Android via iOS keeps it.
    public var connectOnDemand: ConnectOnDemandSettings

    /// Android `first_launch_completed` — onboarding state.
    public var firstLaunchCompleted: Bool

    /// Android `keep_monitor_alive` — keep the network monitor running.
    public var keepMonitorAlive: Bool

    public init(
        activeProtocol: String = "",
        killSwitchEnabled: Bool = true,
        autoConnectOnStart: Bool = false,
        networkRulesEnabled: Bool = false,
        theme: String = "system",
        appLanguage: String = "",
        dnsOverride: String = "",
        logLevel: String = "info",
        gatewayURL: String = "",
        apiKey: String = "",
        tunnelHealthMode: String = "auto",
        tunnelHealthTarget: String = "",
        tunnelHealthPingIntervalSec: Int = 10,
        tunnelHealthDeadThreshold: Int = 3,
        protocolFailoverOrder: [VpnProtocol] = [],
        crashReportsEnabled: Bool = false,
        installUUID: String = "",
        encryptedAtRest: Bool = false,
        connectOnDemand: ConnectOnDemandSettings = ConnectOnDemandSettings(),
        firstLaunchCompleted: Bool = false,
        keepMonitorAlive: Bool = false
    ) {
        self.activeProtocol = activeProtocol
        self.killSwitchEnabled = killSwitchEnabled
        self.autoConnectOnStart = autoConnectOnStart
        self.networkRulesEnabled = networkRulesEnabled
        self.theme = theme
        self.appLanguage = appLanguage
        self.dnsOverride = dnsOverride
        self.logLevel = logLevel
        self.gatewayURL = gatewayURL
        self.apiKey = apiKey
        self.tunnelHealthMode = tunnelHealthMode
        self.tunnelHealthTarget = tunnelHealthTarget
        self.tunnelHealthPingIntervalSec = tunnelHealthPingIntervalSec
        self.tunnelHealthDeadThreshold = tunnelHealthDeadThreshold
        self.protocolFailoverOrder = protocolFailoverOrder
        self.crashReportsEnabled = crashReportsEnabled
        self.installUUID = installUUID
        self.encryptedAtRest = encryptedAtRest
        self.connectOnDemand = connectOnDemand
        self.firstLaunchCompleted = firstLaunchCompleted
        self.keepMonitorAlive = keepMonitorAlive
    }

    private enum CodingKeys: String, CodingKey {
        case activeProtocol = "active_protocol"
        case killSwitchEnabled = "kill_switch_enabled"
        case autoConnectOnStart = "auto_connect_on_start"
        case networkRulesEnabled = "network_rules_enabled"
        case theme
        case appLanguage = "app_language"
        case dnsOverride = "dns_override"
        case logLevel = "log_level"
        case gatewayURL = "gateway_url"
        case apiKey = "api_key"
        case tunnelHealthMode = "tunnel_health_mode"
        case tunnelHealthTarget = "tunnel_health_target"
        case tunnelHealthPingIntervalSec = "tunnel_health_ping_interval_sec"
        case tunnelHealthDeadThreshold = "tunnel_health_dead_threshold"
        case protocolFailoverOrder = "protocol_failover_order"
        case crashReportsEnabled = "crash_reports_enabled"
        case installUUID = "install_uuid"
        case encryptedAtRest = "encrypted_at_rest"
        case connectOnDemand = "connect_on_demand"
        case firstLaunchCompleted = "first_launch_completed"
        case keepMonitorAlive = "keep_monitor_alive"
    }

    /// Tolerant decoder. The synthesized one throws `keyNotFound` for ANY
    /// missing key and Swift does not apply property defaults while decoding —
    /// so an Android export (which has no `log_level`, `app_language`,
    /// `install_uuid`, …) failed to decode and BackupManager silently fell
    /// back to `.default`, dropping EVERY restored setting. Each key now falls
    /// back to its own default instead.
    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        let d = AppSettings()
        func str(_ k: CodingKeys, _ fallback: String) -> String {
            (try? c.decodeIfPresent(String.self, forKey: k)) ?? fallback
        }
        func bool(_ k: CodingKeys, _ fallback: Bool) -> Bool {
            (try? c.decodeIfPresent(Bool.self, forKey: k)) ?? fallback
        }
        func int(_ k: CodingKeys, _ fallback: Int) -> Int {
            (try? c.decodeIfPresent(Int.self, forKey: k)) ?? fallback
        }
        activeProtocol = str(.activeProtocol, d.activeProtocol)
        killSwitchEnabled = bool(.killSwitchEnabled, d.killSwitchEnabled)
        autoConnectOnStart = bool(.autoConnectOnStart, d.autoConnectOnStart)
        networkRulesEnabled = bool(.networkRulesEnabled, d.networkRulesEnabled)
        theme = str(.theme, d.theme)
        appLanguage = str(.appLanguage, d.appLanguage)
        dnsOverride = str(.dnsOverride, d.dnsOverride)
        logLevel = str(.logLevel, d.logLevel)
        gatewayURL = str(.gatewayURL, d.gatewayURL)
        apiKey = str(.apiKey, d.apiKey)
        tunnelHealthMode = str(.tunnelHealthMode, d.tunnelHealthMode)
        tunnelHealthTarget = str(.tunnelHealthTarget, d.tunnelHealthTarget)
        tunnelHealthPingIntervalSec = int(.tunnelHealthPingIntervalSec, d.tunnelHealthPingIntervalSec)
        tunnelHealthDeadThreshold = int(.tunnelHealthDeadThreshold, d.tunnelHealthDeadThreshold)
        protocolFailoverOrder = (try? c.decodeIfPresent([VpnProtocol].self, forKey: .protocolFailoverOrder)) ?? d.protocolFailoverOrder
        crashReportsEnabled = bool(.crashReportsEnabled, d.crashReportsEnabled)
        installUUID = str(.installUUID, d.installUUID)
        encryptedAtRest = bool(.encryptedAtRest, d.encryptedAtRest)
        connectOnDemand = (try? c.decodeIfPresent(ConnectOnDemandSettings.self, forKey: .connectOnDemand)) ?? d.connectOnDemand
        firstLaunchCompleted = bool(.firstLaunchCompleted, d.firstLaunchCompleted)
        keepMonitorAlive = bool(.keepMonitorAlive, d.keepMonitorAlive)
    }

    /// Default-Werte für eine fresh installation.
    public static let `default` = AppSettings()
}
