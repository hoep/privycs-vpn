import SwiftUI

/// Open-source license credits — port of Android's OssLicensesScreen.
/// GPL-3.0 compliance: lists bundled components, their purpose and
/// license, plus the source-code link.
struct OssLicensesView: View {
    private struct Component: Identifiable {
        let id = UUID()
        let name: String
        let purpose: String
        let license: String
    }

    private let components: [Component] = [
        .init(name: "WireGuardKit", purpose: "WireGuard tunnel (wg-go)", license: "MIT"),
        .init(name: "AmneziaWG (amneziawg-go)", purpose: "DPI-resistant WireGuard variant", license: "MIT"),
        .init(name: "OpenVPNAdapter / OpenVPN3", purpose: "OpenVPN tunnel", license: "AGPL-3.0"),
        .init(name: "swift-crypto", purpose: "ed25519 license verification", license: "Apache-2.0"),
        .init(name: "GRDB.swift", purpose: "Local SQLite storage", license: "MIT"),
        .init(name: "swift-collections", purpose: "OrderedSet (pool rotation)", license: "Apache-2.0"),
        .init(name: "ZIPFoundation", purpose: "Pool .zip import", license: "MIT"),
    ]

    var body: some View {
        List {
            Section {
                ForEach(components) { c in
                    VStack(alignment: .leading, spacing: 2) {
                        Text(c.name).font(.system(size: 15, weight: .semibold))
                        Text(c.purpose).font(.system(size: 12)).foregroundStyle(.secondary)
                        Text(c.license).font(.system(size: 11, weight: .medium))
                            .foregroundStyle(PrivycsColor.teal)
                    }
                    .padding(.vertical, 2)
                }
            } header: {
                Text("Bundled components")
            } footer: {
                Text("Privycs VPN is licensed under GPL-3.0. Full license texts ship with the source.")
            }

            Section {
                VStack(alignment: .leading, spacing: 4) {
                    Text("IP to Country Lite by db-ip.com (CC BY 4.0)")
                        .font(.system(size: 13))
                    Link("db-ip.com/db/lite.php", destination: URL(string: "https://db-ip.com/db/lite.php")!)
                        .font(.system(size: 12))
                    Link("creativecommons.org/licenses/by/4.0", destination: URL(string: "https://creativecommons.org/licenses/by/4.0/")!)
                        .font(.system(size: 12))
                }
                .padding(.vertical, 2)
            } header: {
                Text("Geolocation data")
            } footer: {
                Text("The bundled IP→country database (country.mmdb) resolves server locations to flags entirely on-device. No IP is sent anywhere.")
            }

            Section("Source code") {
                Link(destination: URL(string: "https://github.com/hoep/privycs-vpn")!) {
                    Label("github.com/hoep/privycs-vpn", systemImage: "chevron.left.forwardslash.chevron.right")
                }
            }
        }
        .navigationTitle("Open Source Licenses")
        .navigationBarTitleDisplayMode(.inline)
    }
}
