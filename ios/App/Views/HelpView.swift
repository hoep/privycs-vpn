import SwiftUI

struct HelpView: View {
    var body: some View {
        NavigationStack {
            List {
                Section("Documentation") {
                    Link("User Guide", destination: URL(string: "https://www.privycs.com/docs/ios-client")!)
                    Link("FAQ", destination: URL(string: "https://www.privycs.com/faq")!)
                    Link("Privacy Policy", destination: URL(string: "https://www.privycs.com/privacy")!)
                }
                Section("Support") {
                    Link("Email Support", destination: URL(string: "mailto:support@privycs.com")!)
                    Link("Open Source", destination: URL(string: "https://github.com/hoep/privycs-vpn")!)
                }
            }
            .navigationTitle("Help")
        }
    }
}
