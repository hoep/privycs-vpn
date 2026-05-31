import XCTest
@testable import PrivycsCore

final class NetworkRulesEngineTests: XCTestCase {
    let engine = NetworkRulesEngine()

    func testMasterOffReturnsNoMatch() {
        let rule = NetworkRule(
            id: "r1",
            match: NetworkRule.Match(networkType: .wifi),
            action: .disconnect
        )
        let state = NetworkState(networkType: .wifi, ssid: "Home")
        let result = engine.evaluate(rules: [rule], state: state, masterEnabled: false)
        XCTAssertNil(result.matchedRule)
        XCTAssertEqual(result.action, .keepAsIs)
    }

    func testFirstMatchingRuleWins() {
        let r1 = NetworkRule(
            id: "r1", name: "wifi any",
            match: NetworkRule.Match(networkType: .wifi),
            action: .connectToPool(poolID: "pool-eu")
        )
        let r2 = NetworkRule(
            id: "r2", name: "always",
            match: NetworkRule.Match(networkType: .any),
            action: .disconnect
        )
        let state = NetworkState(networkType: .wifi, ssid: "Cafe")
        let result = engine.evaluate(rules: [r1, r2], state: state, masterEnabled: true)
        XCTAssertEqual(result.matchedRule?.id, "r1")
        XCTAssertEqual(result.action, .connectToPool(poolID: "pool-eu"))
    }

    func testSSIDOnlyMatchesListedSSID() {
        let rule = NetworkRule(
            id: "r1",
            match: NetworkRule.Match(networkType: .wifi, ssidMode: .only, ssidList: ["Home", "Office"]),
            action: .disconnect
        )
        XCTAssertEqual(
            engine.evaluate(
                rules: [rule],
                state: NetworkState(networkType: .wifi, ssid: "Home"),
                masterEnabled: true
            ).matchedRule?.id,
            "r1"
        )
        XCTAssertNil(
            engine.evaluate(
                rules: [rule],
                state: NetworkState(networkType: .wifi, ssid: "Cafe"),
                masterEnabled: true
            ).matchedRule
        )
    }

    func testSSIDExceptDoesNOTMatchListedSSID() {
        let rule = NetworkRule(
            id: "r1",
            match: NetworkRule.Match(networkType: .wifi, ssidMode: .except, ssidList: ["Trusted"]),
            action: .keepAsIs
        )
        XCTAssertNil(
            engine.evaluate(
                rules: [rule],
                state: NetworkState(networkType: .wifi, ssid: "Trusted"),
                masterEnabled: true
            ).matchedRule
        )
        XCTAssertEqual(
            engine.evaluate(
                rules: [rule],
                state: NetworkState(networkType: .wifi, ssid: "Public"),
                masterEnabled: true
            ).matchedRule?.id,
            "r1"
        )
    }

    func testDisabledRuleSkipped() {
        let rule = NetworkRule(
            id: "r1",
            match: NetworkRule.Match(networkType: .any),
            action: .disconnect,
            enabled: false
        )
        let state = NetworkState(networkType: .wifi, ssid: "x")
        let result = engine.evaluate(rules: [rule], state: state, masterEnabled: true)
        XCTAssertNil(result.matchedRule)
    }

    func testNonWifiNetworkIgnoresSSIDMode() {
        let rule = NetworkRule(
            id: "r1",
            match: NetworkRule.Match(networkType: .mobile, ssidMode: .only, ssidList: ["Home"]),
            action: .disconnect
        )
        let state = NetworkState(networkType: .mobile, ssid: "")
        let result = engine.evaluate(rules: [rule], state: state, masterEnabled: true)
        XCTAssertEqual(result.matchedRule?.id, "r1")
    }
}
