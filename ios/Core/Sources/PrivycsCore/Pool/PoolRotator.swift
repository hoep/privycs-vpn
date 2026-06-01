import Foundation

/// Stateless Pool-Member-Picker. Mirror der Android `PoolRotator`
/// Logik: gegeben ein Pool + Policy + (optional) NowReference → gibt
/// das zu connectende Member zurück. Side-effect-frei; der Caller
/// persistiert die Pool-Mutation (lastUsedIndex etc.) selbst.
public struct PoolRotator: Sendable {

    public init() {}

    /// Wählt das nächste Member im Pool gemäß der konfigurierten
    /// Policy. Nil wenn der Pool leer oder alle Members durch
    /// restrict-regions ausgeschlossen sind.
    ///
    /// - Parameter pool: zu rotierende Pool. `pool.policy` entscheidet.
    /// - Parameter userCountry: ISO 3166-1 Alpha-2 der detektierten
    ///   User-Country (für GeoNearest). Empty = unbekannt → fallback.
    /// - Parameter userLatLon: User-Koordinaten für Distance-Sort
    ///   (für GeoNearest). Nil wenn nicht verfügbar.
    /// - Returns: das gewählte Member + ein neuer Pool-State mit
    ///   updated `lastUsedIndex` (für Round-Robin) und
    ///   `nextRotationAt` (für periodische Rotation).
    public func pick(
        from pool: Pool,
        userCountry: String = "",
        userLatLon: (Double, Double)? = nil,
        now: Date = Date()
    ) -> (member: PoolMember, updatedPool: Pool)? {
        let eligible = filterEligible(pool: pool)
        guard !eligible.isEmpty else { return nil }

        let chosen: PoolMember
        switch pool.policy {
        case .random:
            chosen = eligible.randomElement()!

        case .roundRobin:
            // Member-id cursor (Android per-region semantics): sort by id
            // for a stable ring, advance past the last-used member. Robust
            // to member add/remove (unlike a positional index).
            let ring = eligible.sorted { $0.id < $1.id }
            let lastID = pool.rotation?.lastUsedMemberID ?? ""
            if let prevIdx = ring.firstIndex(where: { $0.id == lastID }) {
                chosen = ring[(prevIdx + 1) % ring.count]
            } else {
                chosen = ring[0]
            }

        case .geoNearest:
            chosen = GeoNearestPicker.pick(
                from: eligible,
                userCountry: userCountry,
                userLatLon: userLatLon
            ) ?? eligible[0]
        }

        var updated = pool
        var rotation = updated.rotation ?? PoolRotation()
        rotation.lastUsedMemberID = chosen.id
        if rotation.intervalSeconds > 0 {
            rotation.nextRotationAt = Int64(
                now.addingTimeInterval(TimeInterval(rotation.intervalSeconds))
                    .timeIntervalSince1970
            )
        }
        updated.rotation = rotation
        updated.activeMemberID = chosen.id
        return (chosen, updated)
    }

    /// Filter helper — wirft Members raus die durch
    /// `restrictRegions` ausgeschlossen sind. Empty-Liste = no
    /// restriction.
    public func filterEligible(pool: Pool) -> [PoolMember] {
        if pool.restrictRegions.isEmpty {
            return pool.members
        }
        let allowed = Set(pool.restrictRegions.map { $0.lowercased() })
        return pool.members.filter { allowed.contains($0.country.lowercased()) }
    }
}
