import WidgetKit
import SwiftUI
import AppIntents
import PrivycsCore

// MARK: - Timeline

struct PrivycsEntry: TimelineEntry {
    let date: Date
    let model: WidgetModel
}

struct PrivycsProvider: TimelineProvider {
    func placeholder(in context: Context) -> PrivycsEntry {
        PrivycsEntry(date: Date(), model: WidgetModel())
    }
    func getSnapshot(in context: Context, completion: @escaping (PrivycsEntry) -> Void) {
        completion(PrivycsEntry(date: Date(), model: WidgetModel.current()))
    }
    func getTimeline(in context: Context, completion: @escaping (Timeline<PrivycsEntry>) -> Void) {
        let model = WidgetModel.current()
        let entry = PrivycsEntry(date: Date(), model: model)
        // Refresh sooner while connected (live traffic moves) than when idle.
        let next = Date().addingTimeInterval(model.connected ? 60 : 900)
        completion(Timeline(entries: [entry], policy: .after(next)))
    }
}

// MARK: - Connect disc (mirrors the in-app ConnectButton / Android widget disc)

struct ConnectDisc: View {
    let model: WidgetModel
    var size: CGFloat = 96

    var body: some View {
        ZStack {
            // Disc background: teal gradient solid when connected, else a
            // transparent disc with a 2pt outline ring.
            if model.connected {
                Circle().fill(
                    LinearGradient(colors: [WColor.connected, WColor.hex(0x00A88C)],
                                   startPoint: .top, endPoint: .bottom)
                )
                // Glow ring — a 2pt stroked circle 4pt beyond the disc edge.
                Circle().strokeBorder(WColor.connected.opacity(0.45), lineWidth: 2).padding(-4)
            } else {
                Circle().strokeBorder(WColor.disconnected.opacity(0.6), lineWidth: 2)
            }
            VStack(spacing: 2) {
                Image(systemName: WProto.symbol(model.protocolRaw))
                    .font(.system(size: size * 0.34, weight: .semibold))
                    .foregroundStyle(model.connected ? WColor.iconConnected : WColor.iconIdle)
                Text(model.statusLabel)
                    .font(.system(size: size * 0.115, weight: .semibold))
                    .foregroundStyle(model.connected ? WColor.labelConnected : WColor.onSurfaceVariant)
                    .lineLimit(1)
                    .minimumScaleFactor(0.7)
            }
            .padding(.horizontal, 6)
        }
        .frame(width: size, height: size)
    }
}

// MARK: - Traffic sparkline (SwiftUI Canvas bars — ports WidgetSparklineRenderer)

struct Sparkline: View {
    let samples: [Double]
    let color: Color

    var body: some View {
        Canvas { ctx, size in
            guard !samples.isEmpty, let maxV = samples.max(), maxV > 0 else { return }
            let n = samples.count
            let slot = size.width / CGFloat(n)
            let barW = slot * 0.70                      // 70% bar / 30% gap (Android)
            for (i, v) in samples.enumerated() where v > 0 {
                let h = max(1.5, CGFloat(v) / CGFloat(maxV) * size.height * 0.9)  // 90% scale, 1.5pt floor
                let x = CGFloat(i) * slot + (slot - barW) / 2
                let rect = CGRect(x: x, y: size.height - h, width: barW, height: h)
                ctx.fill(Path(roundedRect: rect, cornerRadius: 1), with: .color(color))
            }
        }
    }
}

// MARK: - Protocol pill row (interactive — in-place switch for WG/AWG/OpenVPN)

struct ProtocolPills: View {
    let model: WidgetModel
    var body: some View {
        HStack(spacing: 6) {
            ForEach(model.availableProtocols, id: \.self) { raw in
                if TunnelProviderConfig.isInPlaceSwitchable(raw) {
                    Button(intent: SwitchProtocolIntent(protocolRaw: raw)) { pill(raw) }
                        .buttonStyle(.plain)
                } else {
                    // IPSec: not in-place switchable (IKEv2/cert path lives in
                    // the app) — a tap falls through to the widget's open-app URL.
                    pill(raw)
                }
            }
        }
    }

    @ViewBuilder private func pill(_ raw: String) -> some View {
        let active = (raw == model.protocolRaw)
        HStack(spacing: 4) {
            Image(systemName: WProto.symbol(raw)).font(.system(size: 9))
            Text(WProto.label(raw)).font(.system(size: 9, weight: .medium)).lineLimit(1)
        }
        .padding(.horizontal, 6).padding(.vertical, 4)
        .frame(maxWidth: .infinity)
        .foregroundStyle(active ? WProto.brand(raw) : WColor.onSurfaceVariant)
        .background(
            RoundedRectangle(cornerRadius: 7)
                .fill(active ? WProto.brand(raw).opacity(0.20) : Color.gray.opacity(0.12))
        )
    }
}

// MARK: - Traffic cell (label + total + speed + sparkline)

struct TrafficCell: View {
    let arrow: String
    let arrowColor: Color
    let total: Int64
    let speed: Int64
    let history: [Double]
    let sparkColor: Color
    let label: String

    var body: some View {
        VStack(alignment: .leading, spacing: 2) {
            HStack(spacing: 3) {
                Image(systemName: arrow).font(.system(size: 9, weight: .bold)).foregroundStyle(arrowColor)
                Text(label).font(.system(size: 9)).foregroundStyle(.secondary)
            }
            Text(WFormat.bytes(total)).font(.system(size: 12, weight: .semibold))
            HStack(spacing: 4) {
                Text(WFormat.speed(speed))
                    .font(.system(size: 9, design: .monospaced)).foregroundStyle(.secondary)
                Sparkline(samples: history, color: sparkColor).frame(width: 48, height: 14)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}

// MARK: - Compact widget (systemSmall — connect disc only, Android 2x2 parity)

struct CompactWidgetView: View {
    let entry: PrivycsEntry
    var body: some View {
        Button(intent: ToggleVPNIntent()) {
            ConnectDisc(model: entry.model, size: 92)
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
        .buttonStyle(.plain)
        .widgetURL(URL(string: "privycs://open"))
    }
}

// MARK: - Main widget (systemMedium/Large — disc + identity + pills + traffic)

struct MainWidgetView: View {
    @Environment(\.locale) private var locale
    let entry: PrivycsEntry

    var body: some View {
        let m = entry.model
        HStack(alignment: .top, spacing: 12) {
            Button(intent: ToggleVPNIntent()) { ConnectDisc(model: m, size: 84) }
                .buttonStyle(.plain)

            VStack(alignment: .leading, spacing: 4) {
                Text(m.displayName.isEmpty ? "Privycs VPN" : m.displayName)
                    .font(.system(size: 13, weight: .semibold)).lineLimit(1)

                if m.connected, !m.countryCode.isEmpty {
                    let line = "\(m.flag) \(m.countryName(locale))".trimmingCharacters(in: .whitespaces)
                    if !line.isEmpty {
                        Text(line).font(.system(size: 11)).foregroundStyle(.secondary).lineLimit(1)
                    }
                }

                if !m.availableProtocols.isEmpty { ProtocolPills(model: m) }

                Spacer(minLength: 2)

                HStack(spacing: 10) {
                    TrafficCell(arrow: "arrow.down", arrowColor: WColor.connected,
                                total: m.rxBytes, speed: m.rxSpeed, history: m.rxHistory,
                                sparkColor: WColor.sparkRx, label: String(localized: "Download"))
                    TrafficCell(arrow: "arrow.up", arrowColor: WColor.sparkTx,
                                total: m.txBytes, speed: m.txSpeed, history: m.txHistory,
                                sparkColor: WColor.sparkTx, label: String(localized: "Upload"))
                }
            }
        }
        .padding(12)
        .widgetURL(URL(string: "privycs://open"))
    }
}

// MARK: - Widget configurations + bundle

struct PrivycsCompactWidget: Widget {
    let kind = "PrivycsVPNCompactWidget"
    var body: some WidgetConfiguration {
        StaticConfiguration(kind: kind, provider: PrivycsProvider()) { entry in
            CompactWidgetView(entry: entry)
                .containerBackground(.fill.tertiary, for: .widget)
        }
        .configurationDisplayName("Privycs VPN — Connect")
        .description("One-tap connect / disconnect.")
        .supportedFamilies([.systemSmall])
    }
}

struct PrivycsStatusWidget: Widget {
    let kind = "PrivycsVPNStatusWidget"
    var body: some WidgetConfiguration {
        StaticConfiguration(kind: kind, provider: PrivycsProvider()) { entry in
            MainWidgetView(entry: entry)
                .containerBackground(.fill.tertiary, for: .widget)
        }
        .configurationDisplayName("Privycs VPN — Status")
        .description("Connection status, server and live traffic.")
        .supportedFamilies([.systemMedium, .systemLarge])
    }
}

@main
struct PrivycsWidgetBundle: WidgetBundle {
    var body: some Widget {
        PrivycsCompactWidget()
        PrivycsStatusWidget()
    }
}
