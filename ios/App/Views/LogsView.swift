import SwiftUI
import UIKit
import PrivycsCore

/// Live tunnel-log viewer — port of Android's LogsScreen. Reads the
/// shared App-Group log file (written by app + PacketTunnelProvider),
/// polls for changes, and offers copy + clear.
struct LogsView: View {
    @State private var text = ""
    private let ticker = Timer.publish(every: 1.5, on: .main, in: .common).autoconnect()

    var body: some View {
        ScrollViewReader { proxy in
            ScrollView {
                Text(text.isEmpty ? "No log entries yet." : text)
                    .font(.system(size: 11, design: .monospaced))
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .textSelection(.enabled)
                    .padding(12)
                    .id("logbottom")
            }
            .background(PrivycsColor.background)
            .onChange(of: text) { _, _ in
                withAnimation { proxy.scrollTo("logbottom", anchor: .bottom) }
            }
        }
        .navigationTitle("Logs")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .topBarTrailing) {
                Menu {
                    Button {
                        UIPasteboard.general.string = text
                    } label: { Label("Copy", systemImage: "doc.on.doc") }
                    Button(role: .destructive) {
                        PrivycsLog.clear(); text = ""
                    } label: { Label("Clear", systemImage: "trash") }
                } label: {
                    Image(systemName: "ellipsis.circle")
                }
            }
        }
        .onAppear { text = PrivycsLog.read() }
        .onReceive(ticker) { _ in
            let fresh = PrivycsLog.read()
            if fresh != text { text = fresh }
        }
    }
}
