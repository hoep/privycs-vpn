import SwiftUI

struct HelpView: View {
    var body: some View {
        AdaptiveNavStack {
            List {
                Section("Documentation") {
                    NavigationLink {
                        MarkdownDocView(
                            url: URL(string: "https://www.privycs.com/docs/ios-client.md")!,
                            title: loc("User Guide"))
                    } label: {
                        Label("User Guide", systemImage: "book")
                    }
                    Link("Open in browser", destination: URL(string: "https://www.privycs.com/docs/ios-client")!)
                    Link("Privacy Policy", destination: URL(string: "https://www.privycs.com/docs/ios-client-privacy")!)
                }
                Section("Support") {
                    Link("Email Support", destination: URL(string: "mailto:support@privycs.com")!)
                    Link("Open Source", destination: URL(string: "https://github.com/hoep/privycs-vpn")!)
                }
                Section("Legal") {
                    NavigationLink {
                        OssLicensesView()
                    } label: {
                        Label(loc("Open-Source Licenses"), systemImage: "doc.text")
                    }
                    Link("License (AGPL-3.0)", destination: URL(string: "https://github.com/hoep/privycs-vpn/blob/main/LICENSE.AGPL-3.0.txt")!)
                }
            }
            .navigationTitle("Help")
        }
    }
}
