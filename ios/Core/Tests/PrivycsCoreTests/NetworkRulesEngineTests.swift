import XCTest
@testable import PrivycsCore

final class NetworkRulesEngineTests: XCTestCase {
    let engine = NetworkRulesEngine()

    func testMasterOffReturnsNoMatch() {
        let rule = NetworkRule(id: "r1", matchType: .networkType, matchValue: "wifi", action: .noVpn)
        let state = NetworkState(networkType: .wifi, ssid: "Home")
        let result = engine.evaluate(rules: [rule], state: state, masterEnabled: false)
        XCTAssertNil(result.matchedRule)
    }

    func testFirstMatchingRuleWins() {
        let r1 = NetworkRule(id: "r1", name: "wifi any", matchType: .networkType,
                             matchValue: "wifi", action: .pool, targetId: "pool-eu")
        let r2 = NetworkRule(id: "r2", name: "always", matchType: .any, action: .noVpn)
        let state = NetworkState(networkType: .wifi, ssid: "Cafe")
        let result = engine.evaluate(rules: [r1, r2], state: state, masterEnabled: true)
        XCTAssertEqual(result.matchedRule?.id, "r1")
        XCTAssertEqual(result.matchedRule?.action, .pool)
        XCTAssertEqual(result.matchedRule?.targetId, "pool-eu")
    }

    func testSSIDExactMatches() {
        let rule = NetworkRule(id: "r1", matchType: .ssidExact, matchValue: "Home", action: .noVpn)
        XCTAssertEqual(
            engine.evaluate(rules: [rule],
                            state: NetworkState(networkType: .wifi, ssid: "home"),  // case-insensitive
                            masterEnabled: true).matchedRule?.id, "r1")
        XCTAssertNil(
            engine.evaluate(rules: [rule],
                            state: NetworkState(networkType: .wifi, ssid: "Cafe"),
                            masterEnabled: true).matchedRule)
    }

    func testSSIDPatternGlob() {
        let rule = NetworkRule(id: "r1", matchType: .ssidPattern, matchValue: "Cafe*", action: .noVpn)
        XCTAssertEqual(
            engine.evaluate(rules: [rule],
                            state: NetworkState(networkType: .wifi, ssid: "Cafe-Guest"),
                            masterEnabled: true).matchedRule?.id, "r1")
        XCTAssertNil(
            engine.evaluate(rules: [rule],
                            state: NetworkState(networkType: .wifi, ssid: "Home"),
                            masterEnabled: true).matchedRule)
    }

    func testNetworkTypeWifiMobileComposite() {
        let rule = NetworkRule(id: "r1", matchType: .networkType, matchValue: "wifi_mobile", action: .connectActive)
        XCTAssertNotNil(engine.evaluate(rules: [rule],
                        state: NetworkState(networkType: .wifi), masterEnabled: true).matchedRule)
        XCTAssertNotNil(engine.evaluate(rules: [rule],
                        state: NetworkState(networkType: .mobile), masterEnabled: true).matchedRule)
        XCTAssertNil(engine.evaluate(rules: [rule],
                        state: NetworkState(networkType: .ethernet), masterEnabled: true).matchedRule)
    }

    func testBssidMatch() {
        let rule = NetworkRule(id: "r1", matchType: .bssid, matchValue: "AA:BB:CC:DD:EE:FF", action: .noVpn)
        XCTAssertEqual(
            engine.evaluate(rules: [rule],
                            state: NetworkState(networkType: .wifi, ssid: "x", bssid: "aa:bb:cc:dd:ee:ff"),
                            masterEnabled: true).matchedRule?.id, "r1")
        XCTAssertNil(
            engine.evaluate(rules: [rule],
                            state: NetworkState(networkType: .wifi, ssid: "x", bssid: ""),
                            masterEnabled: true).matchedRule)
    }

    func testDisabledRuleSkipped() {
        let rule = NetworkRule(id: "r1", matchType: .any, action: .noVpn, enabled: false)
        let state = NetworkState(networkType: .wifi, ssid: "x")
        XCTAssertNil(engine.evaluate(rules: [rule], state: state, masterEnabled: true).matchedRule)
    }

    func testAnyDoesNotMatchOffline() {
        let rule = NetworkRule(id: "r1", matchType: .any, action: .connectActive)
        XCTAssertNil(engine.evaluate(rules: [rule], state: .none, masterEnabled: true).matchedRule)
        XCTAssertNotNil(engine.evaluate(rules: [rule],
                        state: NetworkState(networkType: .mobile), masterEnabled: true).matchedRule)
    }
}
