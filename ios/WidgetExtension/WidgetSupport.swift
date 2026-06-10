import SwiftUI
import PrivycsCore

// MARK: - Colors
//
// The widget can't import the app's PrivycsTheme (App target), so it keeps
// its own copy of the few colors it needs — values mirror the Android
// widget colors.xml + the in-app PrivycsColor 1:1.

enum WColor {
    static func hex(_ v: UInt32) -> Color {
        Color(.sRGB,
              red: Double((v >> 16) & 0xFF) / 255,
              green: Double((v >> 8) & 0xFF) / 255,
              blue: Double(v & 0xFF) / 255,
              opacity: 1)
    }
    static let connected = hex(0x00CDAB)          // teal (widget_status_connected accent)
    static let disconnected = hex(0x9AA0A6)        // widget_status_disconnected
    static let onSurfaceVariant = hex(0x9CA3AF)
    static let iconConnected = Color.white
    static let iconIdle = hex(0x9CA3AF)
    static let labelConnected = Color.white.opacity(0.9) // #E6FFFFFF
    static let sparkRx = hex(0x4ADE80)             // RX green
    static let sparkTx = hex(0x60A5FA)             // TX blue
    // Protocol brand colors (mirror Theme.kt / PrivycsColor)
    static let wireguard = hex(0x88171A)
    static let openvpn = hex(0xEA7E20)
    static let ipsec = hex(0x2563EB)
    static let amneziawg = hex(0x6366F1)
}

// MARK: - Protocol helpers (self-contained — app theme not importable)

enum WProto {
    static func brand(_ raw: String) -> Color {
        switch raw {
        case "wireguard": return WColor.wireguard
        case "openvpn":   return WColor.openvpn
        case "ipsec":     return WColor.ipsec
        case "amneziawg": return WColor.amneziawg
        default:          return WColor.connected
        }
    }
    /// Brand-glyph asset name. The 4 protocol logos are now bundled into the
    /// widget's OWN asset catalog (WidgetExtension/Assets.xcassets), mirroring
    /// the in-app ConnectButton (proto.assetName) and the Android widget's
    /// per-protocol drawables. nil for unknown → caller falls back to the SF
    /// Symbol below.
    static func assetName(_ raw: String) -> String? {
        switch raw {
        case "wireguard": return "ic_protocol_wireguard"
        case "openvpn":   return "ic_protocol_openvpn"
        case "ipsec":     return "ic_protocol_strongswan"
        case "amneziawg": return "ic_protocol_amneziawg"
        default:          return nil
        }
    }
    /// SF Symbol fallback — only for an unknown/empty protocol now that the
    /// brand glyphs are bundled with the widget.
    static func symbol(_ raw: String) -> String {
        switch raw {
        case "wireguard": return "bolt.shield.fill"
        case "openvpn":   return "lock.shield.fill"
        case "ipsec":     return "key.horizontal.fill"
        case "amneziawg": return "waveform.path.ecg.rectangle.fill"
        default:          return "shield.fill"
        }
    }
    static func label(_ raw: String) -> String {
        VpnProtocol(rawValue: raw)?.displayName ?? raw
    }
}

/// Protocol glyph for the widget: the bundled brand logo when the protocol is
/// known (template-rendered so the caller's foregroundStyle tints it, like the
/// in-app ConnectButton + the Android widget), else the SF Symbol fallback.
/// Resizable — the caller sets the frame.
struct WProtoIcon: View {
    let raw: String
    var body: some View {
        Group {
            if let asset = WProto.assetName(raw) {
                Image(asset).renderingMode(.template).resizable().scaledToFit()
            } else {
                Image(systemName: WProto.symbol(raw)).resizable().scaledToFit()
            }
        }
    }
}

// MARK: - Merged model
//
// Resolves the widget's view of state by MERGING the two App-Group stores:
//   • WidgetSnapshot      — app-written: identity, available-protocol list,
//                           pool flag, pause state, speed history.
//   • TunnelStatsSnapshot — tunnel-written: live connected / rx / tx /
//                           endpoint, refreshed even when the app is dead.
// Live tunnel wins for connected-state + traffic + endpoint; the app
// snapshot supplies everything the tunnel can't know (names, country,
// pills, sparkline history).

struct WidgetModel {
    var connected = false
    var paused = false
    var protocolRaw = ""
    var availableProtocols: [String] = []
    var isPool = false
    var connectionName = ""
    var poolName = ""
    var memberName = ""
    var countryCode = ""
    var serverEndpoint = ""
    var localAddress = ""
    var rxBytes: Int64 = 0
    var txBytes: Int64 = 0
    var rxSpeed: Int64 = 0
    var txSpeed: Int64 = 0
    var rxHistory: [Double] = []
    var txHistory: [Double] = []
    var connectedAtEpoch: Int64 = 0

    static func current() -> WidgetModel {
        var m = WidgetModel()
        if let s = WidgetSnapshotStore.read() {
            m.connected = s.connected
            m.paused = s.paused
            m.protocolRaw = s.protocolRaw
            m.availableProtocols = s.availableProtocols
            m.isPool = s.isPool
            m.connectionName = s.connectionName
            m.poolName = s.poolName
            m.memberName = s.memberName
            m.countryCode = s.countryCode
            m.serverEndpoint = s.serverEndpoint
            m.localAddress = s.localAddress
            m.rxBytes = s.rxBytes
            m.txBytes = s.txBytes
            m.rxSpeed = s.rxSpeed
            m.txSpeed = s.txSpeed
            m.rxHistory = s.rxHistory
            m.txHistory = s.txHistory
            m.connectedAtEpoch = s.connectedAtEpoch
        }
        // Live tunnel snapshot is authoritative for the volatile fields.
        if let l = TunnelStatsStore.read() {
            m.connected = l.connected
            m.rxBytes = l.rxBytes
            m.txBytes = l.txBytes
            if !l.serverEndpoint.isEmpty { m.serverEndpoint = l.serverEndpoint }
            if !l.localAddress.isEmpty { m.localAddress = l.localAddress }
            if !l.protocolRaw.isEmpty { m.protocolRaw = l.protocolRaw }
            if l.connectedAtEpoch > 0 { m.connectedAtEpoch = l.connectedAtEpoch }
        }
        return m
    }

    /// Status text inside the connect disc — Android parity (Connected /
    /// Paused / Connect).
    var statusLabel: String {
        if connected { return String(localized: "Connected") }
        if paused { return String(localized: "Paused") }
        return String(localized: "Connect")
    }

    var flag: String { PoolHostnameLabels.flagEmoji(countryCode) }
    func countryName(_ locale: Locale) -> String {
        PoolHostnameLabels.countryNameFromCode(countryCode, locale: locale)
    }

    /// Display name for the active selection (Android block-1 right column).
    var displayName: String {
        if isPool {
            if connected, !memberName.isEmpty { return "\(memberName) · \(poolName)" }
            return poolName
        }
        return connectionName
    }
}

// MARK: - Formatters (mirror Android formatBytes / formatSpeed, base-1024)

enum WFormat {
    static func bytes(_ b: Int64) -> String {
        let units = ["B", "KB", "MB", "GB", "TB"]
        var v = Double(max(0, b)); var i = 0
        while v >= 1024 && i < units.count - 1 { v /= 1024; i += 1 }
        return i == 0 ? "\(Int(v)) \(units[i])" : String(format: "%.1f %@", v, units[i])
    }
    static func speed(_ bps: Int64) -> String {
        let units = ["B/s", "KB/s", "MB/s", "GB/s"]
        var v = Double(max(0, bps)); var i = 0
        while v >= 1024 && i < units.count - 1 { v /= 1024; i += 1 }
        return i == 0 ? "\(Int(v)) \(units[i])" : String(format: "%.1f %@", v, units[i])
    }
}
