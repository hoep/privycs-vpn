import XCTest
@testable import PrivycsCore

final class PoolRotatorTests: XCTestCase {

    let de = PoolMember(id: "m1", name: "DE-Frankfurt", country: "DE", region: "Frankfurt",
                        latitude: 50.11, longitude: 8.68, index: 0, protocol: .wireguard)
    let at = PoolMember(id: "m2", name: "AT-Vienna", country: "AT", region: "Vienna",
                        latitude: 48.2, longitude: 16.4, index: 1, protocol: .wireguard)
    let us = PoolMember(id: "m3", name: "US-NYC", country: "US", region: "NYC",
                        latitude: 40.71, longitude: -74.0, index: 2, protocol: .wireguard)
    let jp = PoolMember(id: "m4", name: "JP-Tokyo", country: "JP", region: "Tokyo",
                        latitude: 35.68, longitude: 139.69, index: 3, protocol: .wireguard)

    func testFirstAvailablePicksFirstMember() {
        let pool = Pool(id: "p", name: "test", policy: .firstAvailable, members: [de, at, us])
        let r = PoolRotator()
        let result = r.pick(from: pool)
        XCTAssertEqual(result?.member.id, "m1")
    }

    func testRoundRobinAdvances() {
        var pool = Pool(id: "p", name: "test", policy: .roundRobin, members: [de, at, us])
        let r = PoolRotator()

        let first = r.pick(from: pool)!
        XCTAssertEqual(first.member.id, "m1")
        pool = first.updatedPool

        let second = r.pick(from: pool)!
        XCTAssertEqual(second.member.id, "m2")
        pool = second.updatedPool

        let third = r.pick(from: pool)!
        XCTAssertEqual(third.member.id, "m3")
        pool = third.updatedPool

        // Wrap-around
        let fourth = r.pick(from: pool)!
        XCTAssertEqual(fourth.member.id, "m1")
    }

    func testGeoNearestSameCountryWins() {
        let pool = Pool(id: "p", name: "test", policy: .geoNearest, members: [us, jp, de])
        let r = PoolRotator()
        let result = r.pick(from: pool, userCountry: "DE")
        XCTAssertEqual(result?.member.country, "DE")
    }

    func testGeoNearestFallbackToContinent() {
        // No DE-member; user from DE → expect EU country (AT)
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
            policy: .firstAvailable,
            members: [us, jp, de],
            restrictRegions: ["DE"]
        )
        let r = PoolRotator()
        let result = r.pick(from: pool)
        XCTAssertEqual(result?.member.country, "DE")
    }

    func testEmptyPoolReturnsNil() {
        let pool = Pool(id: "p", name: "empty", policy: .firstAvailable, members: [])
        let r = PoolRotator()
        XCTAssertNil(r.pick(from: pool))
    }

    func testRotationStateUpdatesLastUsedIndex() {
        var pool = Pool(
            id: "p", name: "test", policy: .roundRobin, members: [de, at],
            rotation: PoolRotation(intervalSeconds: 0, lastUsedIndex: -1, nextRotationAt: 0)
        )
        let r = PoolRotator()
        let first = r.pick(from: pool)!
        pool = first.updatedPool
        XCTAssertEqual(pool.rotation?.lastUsedIndex, 0)
        XCTAssertEqual(pool.activeMemberID, "m1")
    }
}
