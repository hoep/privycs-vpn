import XCTest
@testable import PrivycsCore

/// Verifies the IPv6 killswitch injector matches Android's
/// IpV6KillswitchInjector behaviour — the fix for "AmneziaWG has no IPv6".
final class IPv6KillswitchInjectorTests: XCTestCase {

    func testWireGuardV4OnlyGetsV6CatchAll() {
        let conf = """
        [Interface]
        PrivateKey = abc
        Address = 10.0.0.2/32

        [Peer]
        PublicKey = def
        Endpoint = vpn.example.com:51820
        AllowedIPs = 0.0.0.0/0
        PersistentKeepalive = 25
        """
        let r = IPv6KillswitchInjector.inject(conf, protocol: .wireguard)
        XCTAssertTrue(r.applied)
        XCTAssertTrue(r.patched.contains("AllowedIPs = 0.0.0.0/0, ::/0"))
    }

    func testAmneziaWGSharesWGGrammar() {
        let conf = """
        [Interface]
        PrivateKey = abc
        Jc = 4
        Address = 10.0.0.2/32

        [Peer]
        PublicKey = def
        AllowedIPs = 0.0.0.0/0
        """
        let r = IPv6KillswitchInjector.inject(conf, protocol: .amneziawg)
        XCTAssertTrue(r.applied)
        XCTAssertTrue(r.patched.contains(", ::/0"))
    }

    func testWireGuardAlreadyHasV6IsNoOp() {
        let conf = """
        [Peer]
        AllowedIPs = 0.0.0.0/0, ::/0
        """
        let r = IPv6KillswitchInjector.inject(conf, protocol: .wireguard)
        XCTAssertFalse(r.applied)
        XCTAssertEqual(r.patched, conf)
    }

    func testInterfaceAllowedIPsNotTouched() {
        // An AllowedIPs key outside the [Peer] section must be ignored —
        // only the peer's routing scope gets the v6 catch-all.
        let conf = """
        [Interface]
        Address = 10.0.0.2/32

        [Peer]
        AllowedIPs = 0.0.0.0/0
        """
        let r = IPv6KillswitchInjector.inject(conf, protocol: .wireguard)
        XCTAssertTrue(r.applied)
        XCTAssertEqual(r.patched.components(separatedBy: "::/0").count - 1, 1)
    }

    func testOpenVPNGetsV6Routes() {
        let conf = "remote vpn.example.com 1194\nredirect-gateway def1"
        let r = IPv6KillswitchInjector.inject(conf, protocol: .openvpn)
        XCTAssertTrue(r.applied)
        XCTAssertTrue(r.patched.contains("route-ipv6 ::/0"))
        XCTAssertTrue(r.patched.contains("redirect-gateway ipv6"))
    }

    func testOpenVPNAlreadyV6IsNoOp() {
        let conf = "remote vpn.example.com 1194\nroute-ipv6 ::/0"
        let r = IPv6KillswitchInjector.inject(conf, protocol: .openvpn)
        XCTAssertFalse(r.applied)
    }

    func testIPSecRemoteTsGetsV6() {
        let conf = "{\"remote\": {\"remote_ts\": [\"0.0.0.0/0\"]}}"
        let r = IPv6KillswitchInjector.inject(conf, protocol: .ipsec)
        XCTAssertTrue(r.applied)
        XCTAssertTrue(r.patched.contains("::/0"))
    }
}
