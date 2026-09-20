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
        now: Date = Date(),
        excludingMemberIDs: Set<String> = []
    ) -> (member: PoolMember, updatedPool: Pool)? {
        let eligible = filterEligible(pool: pool).filter { !excludingMemberIDs.contains($0.id) }
        guard !eligible.isEmpty else { return nil }

        let chosen: PoolMember
        var newCursors: [String: String]? = nil
        switch pool.policy {
        case .random:
            chosen = eligible.randomElement()!

        case .roundRobin:
            // Region round-robin — port of Android `PoolPicker.pickRoundRobin`.
            // The policy's wire value is literally "round-robin-region": walk
            // the regions in alphabetical order and, inside each, advance a
            // persisted per-region member cursor. A single global ring (what
            // iOS did before) revisits the same region repeatedly and leaks the
            // same exit region far more often than Android does.
            let rr = Self.pickRoundRobin(
                eligible: eligible,
                lastMemberID: pool.rotation?.lastUsedMemberID ?? "",
                userCountry: userCountry,
                cursors: pool.rotation?.regionCursors ?? [:]
            )
            guard let picked = rr else { return nil }
            chosen = picked.member
            newCursors = picked.cursors

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
        if let newCursors { rotation.regionCursors = newCursors }
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

    /// Region round-robin. Returns the picked member plus the updated cursor
    /// map (the caller persists it with the pool). Pure — no I/O, so the whole
    /// thing stays unit-testable, unlike Android's repo-bound version.
    static func pickRoundRobin(
        eligible: [PoolMember],
        lastMemberID: String,
        userCountry: String,
        cursors: [String: String]
    ) -> (member: PoolMember, cursors: [String: String])? {
        guard !eligible.isEmpty else { return nil }

        var byRegion: [String: [PoolMember]] = [:]
        for m in eligible { byRegion[PoolRegions.region(of: m), default: []].append(m) }
        let regions = byRegion.keys.sorted()
        guard !regions.isEmpty else { return nil }

        var cursors = cursors

        func pickFrom(_ region: String) -> PoolMember? {
            guard let members = byRegion[region]?.sorted(by: { $0.id < $1.id }),
                  !members.isEmpty else { return nil }
            // One member: the cursor would always re-pick it; don't churn state.
            if members.count == 1 { return members[0] }

            // Cursor priority: persisted per-region cursor → lastMemberID when
            // it lives in this region → start at 0.
            var cursor = cursors[region] ?? ""
            if cursor.isEmpty, !lastMemberID.isEmpty, members.contains(where: { $0.id == lastMemberID }) {
                cursor = lastMemberID
            }
            let startIdx: Int
            if cursor.isEmpty {
                startIdx = 0
            } else if let idx = members.firstIndex(where: { $0.id == cursor }) {
                startIdx = (idx + 1) % members.count
            } else {
                // The cursor's member was deleted or went unreachable mid-cycle.
                // Restarting at 0 would revisit the early ids out of order and
                // re-use the same exit sooner than one full cycle. Resume at the
                // insertion point instead (Android does the same via binarySearch).
                let insertion = members.firstIndex { $0.id > cursor } ?? members.count
                startIdx = insertion % members.count
            }
            let picked = members[startIdx]
            cursors[region] = picked.id
            return picked
        }

        func pickWithFallback(_ preferred: String?) -> PoolMember? {
            if let p = preferred, byRegion[p] != nil, let m = pickFrom(p) { return m }
            for r in regions { if let m = pickFrom(r) { return m } }
            return nil
        }

        // Single-region pool: just rotate the cursor inside it.
        if regions.count == 1 {
            guard let m = pickFrom(regions[0]) else { return nil }
            return (m, cursors)
        }
        // First pick ever: start in the user's home region when known.
        if lastMemberID.isEmpty {
            let home = userCountry.isEmpty ? nil : PoolRegions.regionForCountry(userCountry)
            guard let m = pickWithFallback(home) else { return nil }
            return (m, cursors)
        }
        // Otherwise advance to the region after the last pick's one.
        let lastRegion = eligible.first { $0.id == lastMemberID }.map { PoolRegions.region(of: $0) } ?? ""
        if lastRegion.isEmpty {
            let home = userCountry.isEmpty ? nil : PoolRegions.regionForCountry(userCountry)
            guard let m = pickWithFallback(home) else { return nil }
            return (m, cursors)
        }
        let idx = regions.firstIndex(of: lastRegion) ?? -1
        let next = idx >= 0 ? regions[(idx + 1) % regions.count] : regions[0]
        guard let m = pickWithFallback(next) else { return nil }
        return (m, cursors)
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
