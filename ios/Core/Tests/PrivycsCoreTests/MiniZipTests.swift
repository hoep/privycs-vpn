import XCTest
@testable import PrivycsCore

/// Verifies the dependency-free ZIP reader + the pool-config extraction path
/// that replaced the flaky ZIPFoundation call ("keine gültige config").
///
/// The fixture is a STORED (uncompressed) archive so the parsing is exercised
/// on Linux too, where Apple's `Compression` framework (used for deflate
/// entries) is unavailable. It holds two `.conf` configs + one `readme.txt`.
final class MiniZipTests: XCTestCase {

    // pool_stored.zip — `de-ber-01.conf`, `us-nyc-02.conf`, `readme.txt` (STORED).
    private let storedZipB64 = "UEsDBBQAAAAAAP2uxVyH9SpVhAAAAIQAAAAOAAAAZGUtYmVyLTAxLmNvbmZbSW50ZXJmYWNlXQpQcml2YXRlS2V5ID0gQUFBQQpBZGRyZXNzID0gMTAuMC4wLjIvMzIKW1BlZXJdClB1YmxpY0tleSA9IEJCQkIKRW5kcG9pbnQgPSBkZTEuZXhhbXBsZS5jb206NTE4MjAKQWxsb3dlZElQcyA9IDAuMC4wLjAvMApQSwMEFAAAAAAA/a7FXHNIg+GEAAAAhAAAAA4AAAB1cy1ueWMtMDIuY29uZltJbnRlcmZhY2VdClByaXZhdGVLZXkgPSBDQ0NDCkFkZHJlc3MgPSAxMC4wLjAuMy8zMgpbUGVlcl0KUHVibGljS2V5ID0gRERERApFbmRwb2ludCA9IHVzMi5leGFtcGxlLmNvbTo1MTgyMApBbGxvd2VkSVBzID0gMC4wLjAuMC8wClBLAwQUAAAAAAD9rsVcYvC/QQoAAAAKAAAACgAAAHJlYWRtZS50eHRpZ25vcmUgbWUKUEsBAhQDFAAAAAAA/a7FXIf1KlWEAAAAhAAAAA4AAAAAAAAAAAAAAIABAAAAAGRlLWJlci0wMS5jb25mUEsBAhQDFAAAAAAA/a7FXHNIg+GEAAAAhAAAAA4AAAAAAAAAAAAAAIABsAAAAHVzLW55Yy0wMi5jb25mUEsBAhQDFAAAAAAA/a7FXGLwv0EKAAAACgAAAAoAAAAAAAAAAAAAAIABYAEAAHJlYWRtZS50eHRQSwUGAAAAAAMAAwCwAAAAkgEAAAAA"

    private func storedZip() -> Data {
        Data(base64Encoded: storedZipB64)!
    }

    func testLooksLikeZip() {
        XCTAssertTrue(MiniZip.looksLikeZip(storedZip()))
        XCTAssertFalse(MiniZip.looksLikeZip(Data("[Interface]\nPrivateKey = x\n".utf8)))
        XCTAssertFalse(MiniZip.looksLikeZip(Data()))
    }

    func testMiniZipReadsAllEntries() {
        let entries = MiniZip.entries(storedZip())
        let names = Set(entries.map { $0.name })
        XCTAssertEqual(names, ["de-ber-01.conf", "us-nyc-02.conf", "readme.txt"])
        let de = entries.first { $0.name == "de-ber-01.conf" }
        XCTAssertNotNil(de)
        let text = String(decoding: de!.data, as: UTF8.self)
        XCTAssertTrue(text.contains("Endpoint = de1.example.com:51820"), "stored entry content mismatch: \(text)")
    }

    func testExtractZipFiltersToConfigsOnly() {
        // readme.txt must be dropped; both .conf files kept with full content.
        let configs = PoolImporter.extractZip(storedZip())
        XCTAssertEqual(configs.count, 2)
        XCTAssertEqual(Set(configs.map { $0.filename }), ["de-ber-01.conf", "us-nyc-02.conf"])
        XCTAssertTrue(configs.allSatisfy { $0.content.contains("[Interface]") })
    }

    func testExtractConfigsDetectsZipByMagicEvenWithoutZipExtension() {
        // The file importer can hand back a URL without a `.zip` suffix — magic
        // bytes must still route it through the archive reader.
        let configs = PoolImporter.extractConfigs(fromFileData: storedZip(), filename: "download")
        XCTAssertEqual(configs.count, 2)
    }

    func testExtractConfigsLooseSingleConfig() {
        let raw = "[Interface]\nPrivateKey = x\n[Peer]\nEndpoint = a.example.com:51820\n"
        let configs = PoolImporter.extractConfigs(fromFileData: Data(raw.utf8), filename: "fr-par-9.conf")
        XCTAssertEqual(configs.count, 1)
        XCTAssertEqual(configs.first?.filename, "fr-par-9.conf")
    }

    func testMakeMembersFromExtractedConfigs() {
        let configs = PoolImporter.extractZip(storedZip())
        let members = PoolImporter.makeMembers(configs)
        XCTAssertEqual(members.count, 2)
        // Filename `de-ber-01` → country parsed from the `de-` prefix.
        XCTAssertTrue(members.contains { $0.country == "DE" })
    }
}
