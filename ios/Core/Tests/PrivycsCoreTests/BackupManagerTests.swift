import XCTest
@testable import PrivycsCore

final class BackupManagerTests: XCTestCase {

    // Standard PBKDF2-HMAC-SHA256 known-answer vectors. If these pass, the
    // pure-Swift PBKDF2 derives the SAME key as Java's PBKDF2WithHmacSHA256
    // — the prerequisite for cross-platform backup restore with Android.
    func testPBKDF2KAT_oneIteration() {
        let dk = PBKDF2.deriveSHA256(password: "password", salt: Array("salt".utf8),
                                     iterations: 1, keyLength: 32)
        XCTAssertEqual(hex(dk),
            "120fb6cffcf8b32c43e7225256c4f837a86548c92ccc35480805987cb70be17b")
    }

    func testPBKDF2KAT_twoIterations() {
        let dk = PBKDF2.deriveSHA256(password: "password", salt: Array("salt".utf8),
                                     iterations: 2, keyLength: 32)
        XCTAssertEqual(hex(dk),
            "ae4d0c95af6b46d32d0adff928f06dd02a303f8ef3c251dfd6e2d85a95474c43")
    }

    func testBackupRoundTrip() throws {
        let payload = BackupManager.Payload(
            connections: BackupManager.ConnectionRegistry(connections: [
                SavedConnection(id: "c1", name: "Home", protocols: [
                    ProtocolConfig(id: "p1", protocol: .wireguard, filename: "wg.conf",
                                   serverAddress: "h:51820")
                ], activeConfigID: "p1")
            ], activeId: "c1"),
            settings: .default,
            pools: BackupManager.PoolFile(pools: [], activeId: ""),
            networkRules: [NetworkRule(id: "r1", matchType: .any, action: .noVpn)]
        )
        let blob = try BackupManager.export(payload, password: "secret123")
        let decoded = try BackupManager.decrypt(blob, password: "secret123")
        XCTAssertEqual(decoded.connections.connections.first?.id, "c1")
        XCTAssertEqual(decoded.connections.connections.first?.protocols.first?.serverAddress, "h:51820")
        XCTAssertEqual(decoded.networkRules.first?.id, "r1")
    }

    func testWrongPassphraseFails() throws {
        let payload = BackupManager.Payload(
            connections: BackupManager.ConnectionRegistry(),
            settings: .default
        )
        let blob = try BackupManager.export(payload, password: "right")
        XCTAssertThrowsError(try BackupManager.decrypt(blob, password: "wrong"))
    }

    func testEnvelopeShape() throws {
        let blob = try BackupManager.export(
            BackupManager.Payload(connections: BackupManager.ConnectionRegistry(), settings: .default),
            password: "p")
        let json = String(data: blob, encoding: .utf8) ?? ""
        XCTAssertTrue(json.contains("\"salt\""))
        XCTAssertTrue(json.contains("\"iv\""))
        XCTAssertTrue(json.contains("\"data\""))
        XCTAssertTrue(json.contains("\"version\""))
    }

    private func hex(_ b: [UInt8]) -> String { b.map { String(format: "%02x", $0) }.joined() }
}
