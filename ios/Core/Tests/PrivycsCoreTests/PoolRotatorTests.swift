import XCTest
@testable import PrivycsCore

final class PoolRotatorTests: XCTestCase {

    // Regions are the Android `regionForCountry` labels — round-robin is
    // region-aware (policy raw value "round-robin-region"), so the region
    // string, not the city, decides the rotation order.
    let de = PoolMember(id: "m1", name: "DE-Frankfurt", country: "DE", region: "Europe",
                        index: 0, protocol: .wireguard)
    let at = PoolMember(id: "m2", name: "AT-Vienna", country: "AT", region: "Europe",
                        index: 1, protocol: .wireguard)
    let us = PoolMember(id: "m3", name: "US-NYC", country: "US", region: "North America",
                        index: 2, protocol: .wireguard)
    let jp = PoolMember(id: "m4", name: "JP-Tokyo", country: "JP", region: "Asia-Pacific",
                        index: 3, protocol: .wireguard)

    func testRoundRobinFirstPickIsLowestId() {
        // No cursor and no user country → first region alphabetically
        // ("Europe"), lowest id inside it.
        let pool = Pool(id: "p", name: "test", policy: .roundRobin, members: [us, at, de])
        let r = PoolRotator()
        let result = r.pick(from: pool)
        XCTAssertEqual(result?.member.id, "m1")
    }

    func testRoundRobinAlternatesRegionsAndCyclesWithin() {
        // Android parity: advance to the NEXT region each pick, and inside a
        // region advance that region's own cursor. Europe = {m1,m2},
        // North America = {m3} → m1, m3, m2, m3, m1 …
        var pool = Pool(id: "p", name: "test", policy: .roundRobin, members: [de, at, us])
        let r = PoolRotator()
        var seq: [String] = []
        for _ in 0..<5 {
            let step = r.pick(from: pool)!
            seq.append(step.member.id)
            pool = step.updatedPool
        }
        XCTAssertEqual(seq, ["m1", "m3", "m2", "m3", "m1"])
        // Single-member regions don't burn cursor state (Android short-circuit).
        XCTAssertEqual(pool.rotation?.regionCursors["Europe"], "m1")
        XCTAssertNil(pool.rotation?.regionCursors["North America"])
    }

    func testRoundRobinFirstPickPrefersHomeRegion() {
        let pool = Pool(id: "p", name: "test", policy: .roundRobin, members: [de, at, us])
        let r = PoolRotator()
        XCTAssertEqual(r.pick(from: pool, userCountry: "US")?.member.id, "m3")
        XCTAssertEqual(r.pick(from: pool, userCountry: "DE")?.member.id, "m1")
    }

    func testRoundRobinResumesAfterDeletedCursorMember() {
        // Cursor points at "b", which no longer exists. Restarting at index 0
        // would re-serve "a" immediately; resume at the insertion point.
        let members = [
            PoolMember(id: "a", name: "a", country: "DE", region: "Europe", index: 0, protocol: .wireguard),
            PoolMember(id: "c", name: "c", country: "DE", region: "Europe", index: 1, protocol: .wireguard),
            PoolMember(id: "d", name: "d", country: "DE", region: "Europe", index: 2, protocol: .wireguard),
        ]
        let picked = PoolRotator.pickRoundRobin(eligible: members, lastMemberID: "a",
                                                userCountry: "", cursors: ["Europe": "b"])
        XCTAssertEqual(picked?.member.id, "c")
        XCTAssertEqual(picked?.cursors["Europe"], "c")
    }

    func testRegionCursorsSurviveJSONRoundTrip() throws {
        var pool = Pool(id: "p", name: "test", policy: .roundRobin, members: [de, at, us])
        pool = PoolRotator().pick(from: pool)!.updatedPool
        let back = try JSONDecoder().decode(Pool.self, from: JSONEncoder().encode(pool))
        XCTAssertEqual(back.rotation?.regionCursors, pool.rotation?.regionCursors)
    }

    func testRegionForCountryMatchesAndroidLabels() {
        XCTAssertEqual(PoolRegions.regionForCountry("ch"), "Europe")
        XCTAssertEqual(PoolRegions.regionForCountry("BR"), "South America")
        XCTAssertEqual(PoolRegions.regionForCountry("AE"), "Middle East")
        XCTAssertEqual(PoolRegions.regionForCountry("KZ"), "Asia-Pacific")
        XCTAssertEqual(PoolRegions.regionForCountry("ZZ"), "Other")
        // Stored region wins over the country-derived one.
        XCTAssertEqual(PoolRegions.region(of: jp), "Asia-Pacific")
    }

    func testGeoNearestSameCountryWins() {
        let pool = Pool(id: "p", name: "test", policy: .geoNearest, members: [us, jp, de])
        let r = PoolRotator()
        let result = r.pick(from: pool, userCountry: "DE")
        XCTAssertEqual(result?.member.country, "DE")
    }

    func testGeoNearestFallbackToContinent() {
        // No DE-member; user from DE → expect an EU country (AT)
        let pool = Pool(id: "p", name: "test", policy: .geoNearest, members: [us, jp, at])
        let r = PoolRotator()
        let result = r.pick(from: pool, userCountry: "DE")
        XCTAssertEqual(result?.member.country, "AT")
    }

    func testRestrictRegionsFilter() {
        // Pool has DE+US+JP members; restrict to ["DE"] only
        let pool = Pool(
            id: "p",
            name: "test",
            policy: .roundRobin,
            members: [us, jp, de],
            restrictRegions: ["DE"]
        )
        let r = PoolRotator()
        let result = r.pick(from: pool)
        XCTAssertEqual(result?.member.country, "DE")
    }

    func testEmptyPoolReturnsNil() {
        let pool = Pool(id: "p", name: "empty", policy: .random, members: [])
        let r = PoolRotator()
        XCTAssertNil(r.pick(from: pool))
    }

    func testRotationStateUpdatesCursor() {
        var pool = Pool(
            id: "p", name: "test", policy: .roundRobin, members: [de, at],
            rotation: PoolRotation(intervalSeconds: 0, nextRotationAt: 0)
        )
        let r = PoolRotator()
        let first = r.pick(from: pool)!
        pool = first.updatedPool
        XCTAssertEqual(pool.rotation?.lastUsedMemberID, "m1")
        XCTAssertEqual(pool.activeMemberID, "m1")
    }
}
