import XCTest
@testable import PrivycsCore

/// The gateway's numeric config id is unique only WITHIN a protocol — its API
/// addresses configs as `<protocol>-<id>`. Keying a SwiftUI list on that number
/// gives duplicate identities, and SwiftUI answers by swapping row content around
/// and dropping rows while scrolling; a lookup by it returns whichever config comes
/// first, which on tvOS meant picking OpenVPN and connecting IPSec.
final class RemoteConfigEntryIdentityTests: XCTestCase {

    private func decode(_ json: String) throws -> RemoteConfigEntry {
        try JSONDecoder().decode(RemoteConfigEntry.self, from: Data(json.utf8))
    }

    /// The wire format is unchanged: the gateway still sends `"id"`, and it still
    /// lands in `configID` — which is what the download URL must use.
    func testDecodesGatewayNumberIntoConfigID() throws {
        let e = try decode(#"{"id": 42, "peer_name": "laptop", "protocol": "openvpn", "interface_name": "tun0", "vpn_ip": "10.0.0.2"}"#)

        XCTAssertEqual(e.configID, 42)
        XCTAssertEqual(e.peerName, "laptop")
        XCTAssertEqual(e.protocolRaw, "openvpn")
    }

    func testSameNumberDifferentProtocolsAreDistinctIdentities() throws {
        let ipsec = try decode(#"{"id": 2, "peer_name": "Shielded", "protocol": "ipsec"}"#)
        let openvpn = try decode(#"{"id": 2, "peer_name": "Shielded", "protocol": "openvpn"}"#)

        XCTAssertEqual(ipsec.configID, openvpn.configID, "precondition: the gateway really does reuse the number across protocols")
        XCTAssertNotEqual(ipsec.id, openvpn.id, "identities must differ, or a list keyed on them shuffles its rows")
        XCTAssertEqual(ipsec.id, "ipsec-2")
        XCTAssertEqual(openvpn.id, "openvpn-2")
    }

    /// A whole page of configs must produce a page of unique identities — the exact
    /// invariant SwiftUI's ForEach relies on.
    func testIdentitiesAreUniqueAcrossAMixedConfigList() throws {
        let entries = try [
            #"{"id": 2, "protocol": "ipsec", "peer_name": "Shielded"}"#,
            #"{"id": 2, "protocol": "openvpn", "peer_name": "Shielded"}"#,
            #"{"id": 2, "protocol": "wireguard", "peer_name": "Shielded"}"#,
            #"{"id": 28, "protocol": "openvpn", "peer_name": "laptop"}"#,
            #"{"id": 28, "protocol": "wireguard", "peer_name": "laptop"}"#,
        ].map(decode)

        XCTAssertEqual(Set(entries.map(\.id)).count, entries.count, "duplicate identity in the list")
    }

    /// Round-trips through the cache TVAppState keeps in UserDefaults.
    func testSurvivesAnEncodeDecodeRoundTrip() throws {
        let original = try decode(#"{"id": 7, "peer_name": "tv", "protocol": "wireguard", "interface_name": "wg0", "obfuscation_enabled": true}"#)

        let restored = try JSONDecoder().decode(
            RemoteConfigEntry.self,
            from: JSONEncoder().encode(original)
        )

        XCTAssertEqual(restored, original)
        XCTAssertEqual(restored.id, "wireguard-7")
        XCTAssertEqual(restored.protocol, .amneziawg, "obfuscated WireGuard reads as AmneziaWG")
    }
}
