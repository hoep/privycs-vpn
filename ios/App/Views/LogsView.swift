import SwiftUI
import UIKit
import PrivycsCore

/// Live tunnel-log viewer — port of Android's LogsScreen. Reads the
/// shared App-Group log file (written by app + PacketTunnelProvider).
/// The file can be up to 256 KB; reading + rendering all of it on the
/// main thread froze the screen ("logs don't load"), so we read off the
/// main actor and show only the most recent `shown` lines, with a
/// "Load earlier entries" button to reveal older ones on demand.
struct LogsView: View {
    @State private var text = ""
    @State private var loading = true
    @State private var hasMore = false
    @State private var shown = 400
    /// While true, new content auto-scrolls to the bottom (live tail).
    /// Turned off once the user loads earlier entries so it doesn't yank
    /// them back down.
    @State private var stickToBottom = true
    private let ticker = Timer.publish(every: 2, on: .main, in: .common).autoconnect()

    private static let pageLines = 400

    var body: some View {
        ScrollViewReader { proxy in
            ScrollView {
                if loading {
                    ProgressView().padding(.top, 48).frame(maxWidth: .infinity)
                } else {
                    VStack(alignment: .leading, spacing: 0) {
                        if hasMore {
                            Button {
                                stickToBottom = false
                                shown += LogsView.pageLines
                                Task { await reload() }
                            } label: {
                                Label("Load earlier entries", systemImage: "arrow.up.circle")
                                    .font(.caption)
                            }
                            .buttonStyle(.borderless)
                            .frame(maxWidth: .infinity)
                            .padding(.vertical, 10)
                        }
                        Text(text.isEmpty ? String(localized: "No log entries yet.") : text)
                            .font(.system(size: 11, design: .monospaced))
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .textSelection(.enabled)
                            .padding(12)
                            .id("logbottom")
                    }
                }
            }
            .background(PrivycsColor.background)
            .onChange(of: text) { _ in
                if stickToBottom { withAnimation { proxy.scrollTo("logbottom", anchor: .bottom) } }
            }
        }
        .navigationTitle("Logs")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .navigationBarTrailing) {
                Menu {
                    Button {
                        UIPasteboard.general.string = PrivycsLog.read()   // full log
                    } label: { Label("Copy", systemImage: "doc.on.doc") }
                    Button(role: .destructive) {
                        PrivycsLog.clear(); text = ""; hasMore = false
                    } label: { Label("Clear", systemImage: "trash") }
                } label: {
                    Image(systemName: "ellipsis.circle")
                }
            }
        }
        .task { await reload() }
        .onReceive(ticker) { _ in Task { await reload() } }
    }

    /// Read + tail the log off the main actor, then publish on main.
    private func reload() async {
        let limit = shown
        let result = await Task.detached(priority: .utility) { () -> (text: String, more: Bool) in
            let full = PrivycsLog.read()
            let lines = full.split(separator: "\n", omittingEmptySubsequences: false)
            let tail = lines.suffix(limit).joined(separator: "\n")
            return (tail, lines.count > limit)
        }.value
        if result.text != text { text = result.text }
        hasMore = result.more
        if loading { loading = false }
    }
}
