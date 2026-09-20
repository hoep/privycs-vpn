import XCTest
@testable import PrivycsCore

final class ModelsCodableTests: XCTestCase {

    func testSavedConnectionRoundTrip() throws {
        let conn = SavedConnection(
            id: "abc-123",
            name: "My Home VPN",
            protocols: [
                ProtocolConfig(
                    id: "wg-1",
                    protocol: .wireguard,
                    filename: "home.conf",
                    nickname: "Home",
                    configContent: "[Interface]\nPrivateKey = X...\n",
                    serverAddress: "1.2.3.4:51820"
                ),
            ],
            activeConfigID: "wg-1",
            protocolFailoverOrder: [.wireguard, .openvpn],
            dnsOverride: "1.1.1.1",
            verified: true,
            lastConnectedAt: Date(timeIntervalSince1970: 1735680000)
        )
        let data = try JSONEncoder().encode(conn)
        let decoded = try JSONDecoder().decode(SavedConnection.self, from: data)
        XCTAssertEqual(decoded, conn)
    }

    func testJSONFieldsAreSnakeCase() throws {
        let conn = SavedConnection(
            id: "x",
            name: "n",
            protocols: [],
            activeConfigID: "y",
            protocolFailoverOrder: [.amneziawg],
            dnsOverride: "",
            verified: false,
            lastConnectedAt: nil
        )
        let data = try JSONEncoder().encode(conn)
        let json = String(data: data, encoding: .utf8)!
        XCTAssertTrue(json.contains("\"active_config_id\""), "Expected snake_case key 'active_config_id' in:\n\(json)")
        XCTAssertTrue(json.contains("\"protocol_failover_order\""), "Expected snake_case key 'protocol_failover_order'")
        XCTAssertTrue(json.contains("\"dns_override\""), "Expected snake_case key 'dns_override'")
        XCTAssertFalse(json.contains("\"activeConfigID\""), "camelCase key leaked: \(json)")
    }

    func testNetworkRuleRoundTrip() throws {
        let rules: [NetworkRule] = [
            NetworkRule(id: "r1", priority: 0, matchType: .any, action: .noVpn),
            NetworkRule(id: "r2", priority: 1, matchType: .networkType, matchValue: "wifi_mobile", action: .connectActive),
            NetworkRule(id: "r3", priority: 2, matchType: .ssidPattern, matchValue: "Cafe*", action: .connection, targetId: "conn-42", name: "cafes"),
            NetworkRule(id: "r4", priority: 3, matchType: .bssid, matchValue: "AA:BB:CC:DD:EE:FF", action: .pool, targetId: "pool-99"),
        ]
        for rule in rules {
            let data = try JSONEncoder().encode(rule)
            let decoded = try JSONDecoder().decode(NetworkRule.self, from: data)
            XCTAssertEqual(decoded, rule, "Round-trip mismatch for rule: \(rule.id)")
        }
        // Wire-key check: the Android @SerialName keys must be present.
        let data = try JSONEncoder().encode(rules[2])
        let json = String(data: data, encoding: .utf8) ?? ""
        XCTAssertTrue(json.contains("\"match_type\""))
        XCTAssertTrue(json.contains("\"match_value\""))
        XCTAssertTrue(json.contains("\"target_id\""))
    }

    func testPoolRoundTrip() throws {
        let pool = Pool(
            id: "pool-1",
            name: "EU Pool",
            policy: .geoNearest,
            members: [
                PoolMember(
                    id: "m1",
                    name: "DE-Frankfurt",
                    country: "DE",
                    region: "Frankfurt",
                    index: 0,
                    protocol: .wireguard,
                    configContent: "",
                    serverAddress: "frankfurt.privycs.example:51820"
                ),
            ],
            rotation: PoolRotation(intervalSeconds: 3600, lastUsedIndex: 0, nextRotationAt: 1735690000),
            activeMemberID: "m1",
            splitTunnel: PoolSplitTunnel(mode: .excludeListed, cidrs: ["192.168.0.0/16"]),
            restrictRegions: ["DE", "AT"],
            countryOverride: "",
            dnsOverride: ""
        )
        let data = try JSONEncoder().encode(pool)
        let decoded = try JSONDecoder().decode(Pool.self, from: data)
        XCTAssertEqual(decoded, pool)
    }

    func testAppSettingsDefaultsAndRoundTrip() throws {
        let s = AppSettings.default
        XCTAssertTrue(s.killSwitchEnabled)
        XCTAssertTrue(s.networkRulesEnabled)
        XCTAssertEqual(s.theme, "system")
        XCTAssertFalse(s.crashReportsEnabled)

        let data = try JSONEncoder().encode(s)
        let decoded = try JSONDecoder().decode(AppSettings.self, from: data)
        XCTAssertEqual(decoded, s)
    }

    func testAppSettingsDecodesAndroidExportWithoutIOSKeys() throws {
        // An Android settings blob: no log_level / app_language / install_uuid,
        // but connect_on_demand / first_launch_completed / keep_monitor_alive.
        // The synthesized decoder threw keyNotFound here, and BackupManager
        // then fell back to .default — silently dropping EVERY restored setting.
        let json = """
        {"active_protocol":"wireguard","kill_switch_enabled":false,"theme":"dark",
         "dns_override":"9.9.9.9","gateway_url":"https://gw.example.com","api_key":"",
         "connect_on_demand":{"enabled":true,"trigger":"wifi","ssid_mode":"only","ssid_list":["A","B"]},
         "network_rules_enabled":true,"first_launch_completed":true,"keep_monitor_alive":true,
         "tunnel_health_ping_interval_sec":10,"tunnel_health_dead_threshold":3}
        """
        let s = try JSONDecoder().decode(AppSettings.self, from: Data(json.utf8))
        XCTAssertEqual(s.theme, "dark")
        XCTAssertEqual(s.dnsOverride, "9.9.9.9")
        XCTAssertEqual(s.gatewayURL, "https://gw.example.com")
        XCTAssertTrue(s.networkRulesEnabled)
        XCTAssertTrue(s.firstLaunchCompleted)
        XCTAssertTrue(s.keepMonitorAlive)
        XCTAssertEqual(s.connectOnDemand.trigger, "wifi")
        XCTAssertEqual(s.connectOnDemand.ssidMode, "only")
        XCTAssertEqual(s.connectOnDemand.ssidList, ["A", "B"])
        // Keys the blob didn't carry fall back to their own defaults.
        XCTAssertEqual(s.logLevel, AppSettings.default.logLevel)
    }

    func testAppSettingsRoundTripKeepsEveryField() throws {
        var s = AppSettings.default
        s.connectOnDemand = ConnectOnDemandSettings(enabled: true, trigger: "mobile",
                                                    ssidMode: "except", ssidList: ["X"])
        s.firstLaunchCompleted = true
        s.keepMonitorAlive = true
        let back = try JSONDecoder().decode(AppSettings.self, from: JSONEncoder().encode(s))
        XCTAssertEqual(back, s)
    }

    func testTunnelHealthDefaultsMatchAndroid() {
        // The runtime already used 10/3 via `> 0` fallbacks; now the exported
        // values say so too, so a backup carries the real cadence.
        XCTAssertEqual(AppSettings.default.tunnelHealthPingIntervalSec, 10)
        XCTAssertEqual(AppSettings.default.tunnelHealthDeadThreshold, 3)
    }
}
