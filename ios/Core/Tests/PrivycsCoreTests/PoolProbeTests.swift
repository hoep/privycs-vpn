import XCTest
@testable import PrivycsCore

final class PoolProbeTests: XCTestCase {

    private func member(_ addr: String) -> PoolMember {
        var cfg = ProtocolConfig(id: "c", protocol: .wireguard, filename: "f.conf", configContent: "")
        cfg.serverAddress = addr
        return PoolMember(id: "m", name: "m", config: cfg, country: "DE", region: "Europe")
    }

    func testStripPortIfPresent() {
        XCTAssertEqual(PoolProbe.stripPortIfPresent("vpn.example.com:51820"), "vpn.example.com")
        XCTAssertEqual(PoolProbe.stripPortIfPresent("vpn.example.com"), "vpn.example.com")
        XCTAssertEqual(PoolProbe.stripPortIfPresent("[2001:db8::1]:51820"), "2001:db8::1")
        // A bare IPv6 literal must NOT be split at its last colon.
        XCTAssertEqual(PoolProbe.stripPortIfPresent("2001:db8::1"), "2001:db8::1")
        XCTAssertEqual(PoolProbe.stripPortIfPresent("  1.2.3.4:51820 "), "1.2.3.4")
        XCTAssertEqual(PoolProbe.stripPortIfPresent(""), "")
    }

    func testIsBareIP() {
        XCTAssertTrue(PoolProbe.isBareIP("1.2.3.4"))
        XCTAssertTrue(PoolProbe.isBareIP("2001:db8::1"))
        XCTAssertFalse(PoolProbe.isBareIP("vpn.example.com"))
        XCTAssertFalse(PoolProbe.isBareIP("1.2.3.4:51820"))
    }

    func testBareIPEndpointNeedsNoDNS() async {
        // No resolver involved → passes immediately, even offline.
        let why = await PoolProbe.probe(member: member("1.2.3.4:51820"))
        XCTAssertNil(why)
    }

    func testMissingEndpointFails() async {
        let why = await PoolProbe.probe(member: member(""))
        XCTAssertEqual(why, "probe: member has no endpoint")
    }

    func testUnresolvableHostFails() async {
        // .invalid is reserved by RFC 2606 — never resolves.
        let why = await PoolProbe.probe(member: member("no-such-host.invalid:51820"))
        XCTAssertNotNil(why)
    }
}
