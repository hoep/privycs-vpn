import XCTest
@testable import PrivycsCore

final class PoolImporterTests: XCTestCase {

    func testParseCountry() {
        XCTAssertEqual(PoolImporter.parseCountry("DE-Frankfurt-01.conf"), "DE")
        XCTAssertEqual(PoolImporter.parseCountry("us-nyc-wg.ovpn"), "US")
        XCTAssertEqual(PoolImporter.parseCountry("server.conf"), "")          // no cc segment
        XCTAssertEqual(PoolImporter.parseCountry("usa-x.conf"), "")           // 3 letters ≠ cc
    }

    func testIsConfigFile() {
        XCTAssertTrue(PoolImporter.isConfigFile("a.conf"))
        XCTAssertTrue(PoolImporter.isConfigFile("a.OVPN"))
        XCTAssertTrue(PoolImporter.isConfigFile("a.sswan"))
        XCTAssertFalse(PoolImporter.isConfigFile("readme.txt"))
        XCTAssertFalse(PoolImporter.isConfigFile("a.zip"))
    }

    func testMakeMembers() {
        let configs = [
            PoolImporter.ExtractedConfig(
                filename: "DE-fra-wg.conf",
                content: "[Interface]\nPrivateKey = x\n[Peer]\nEndpoint = de.example:51820\n"),
            PoolImporter.ExtractedConfig(
                filename: "US-nyc.ovpn",
                content: "client\nremote us.example 1194\n"),
        ]
        let m = PoolImporter.makeMembers(configs)
        XCTAssertEqual(m.count, 2)
        XCTAssertEqual(m[0].country, "DE")
        XCTAssertEqual(m[0].protocol, .wireguard)
        XCTAssertEqual(m[1].country, "US")
        XCTAssertEqual(m[1].protocol, .openvpn)
        // ids unique, indices sequential
        XCTAssertNotEqual(m[0].id, m[1].id)
    }
}
