import XCTest
@testable import PrivycsCore

final class OVPNCompatTests: XCTestCase {
    let pre = OVPNCompatPreprocessor()

    func testScriptHooksStripped() {
        let raw = """
        remote vpn.example.com 1194
        script-security 2
        up /tmp/up.sh
        down /tmp/down.sh
        cipher AES-256-GCM
        """
        let result = pre.preprocess(raw)
        XCTAssertFalse(result.cleanedConfig.contains("script-security"))
        XCTAssertFalse(result.cleanedConfig.contains("/tmp/up.sh"))
        XCTAssertFalse(result.cleanedConfig.contains("/tmp/down.sh"))
        XCTAssertTrue(result.warnings.contains(.scriptHooksStripped))
    }

    func testPluginsStripped() {
        let raw = """
        remote vpn.example.com 1194
        plugin /usr/lib/openvpn/plugin.so
        """
        let result = pre.preprocess(raw)
        XCTAssertFalse(result.cleanedConfig.contains("plugin"))
        XCTAssertTrue(result.warnings.contains(.pluginsStripped))
    }

    func testCompLzoNormalized() {
        let raw = """
        remote vpn.example.com 1194
        comp-lzo
        """
        let result = pre.preprocess(raw)
        XCTAssertTrue(result.cleanedConfig.contains("compress lzo"))
        XCTAssertFalse(result.cleanedConfig.contains("comp-lzo"))
        XCTAssertTrue(result.warnings.contains(.deprecatedCompressDirective))
    }

    func testInlineBlocksPreserved() {
        let raw = """
        remote vpn.example.com 1194
        <cert>
        -----BEGIN CERTIFICATE-----
        MIIDXTCC...
        -----END CERTIFICATE-----
        </cert>
        cipher AES-256-GCM
        """
        let result = pre.preprocess(raw)
        XCTAssertTrue(result.cleanedConfig.contains("BEGIN CERTIFICATE"))
        XCTAssertTrue(result.cleanedConfig.contains("</cert>"))
    }

    func testWeakCipherWarning() {
        let raw = """
        remote vpn.example.com 1194
        cipher BF-CBC
        """
        let result = pre.preprocess(raw)
        XCTAssertTrue(result.cleanedConfig.contains("cipher BF-CBC")) // not stripped, only flagged
        let weakWarning = result.warnings.first { warning in
            if case .weakCipherDetected = warning { return true }
            return false
        }
        XCTAssertNotNil(weakWarning)
    }

    func testCommentsPreserved() {
        let raw = """
        # My config
        remote vpn.example.com 1194
        ; semicolon comment
        cipher AES-256-GCM
        """
        let result = pre.preprocess(raw)
        XCTAssertTrue(result.cleanedConfig.contains("# My config"))
        XCTAssertTrue(result.cleanedConfig.contains("; semicolon comment"))
    }

    func testClientCertNotRequiredStripped() {
        let raw = """
        remote vpn.example.com 1194
        client-cert-not-required
        cipher AES-256-GCM
        """
        let result = pre.preprocess(raw)
        XCTAssertFalse(result.cleanedConfig.contains("client-cert-not-required"))
    }
}
