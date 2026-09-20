import Foundation

/// Geo-Nearest member picker — Android `PoolPicker.pickGeoNearest`
/// semantics (region-based, no coordinates):
///
/// 1. Members in the user's exact country → pick a RANDOM one.
/// 2. Else members in the user's REGION (Android `regionForCountry`, see
///    `PoolRegions`) → pick a RANDOM one.
/// 3. Else any eligible member at random.
///
/// Random tie-break within each cohort matches Android (SecureRandom),
/// so the same user in the same country spreads load instead of always
/// hitting member[0]. (Android `PoolMember` carries no lat/lon, so the
/// earlier iOS haversine path was a divergence — removed for parity.)
public enum GeoNearestPicker {

    public static func pick(
        from members: [PoolMember],
        userCountry: String,
        userLatLon: (Double, Double)? = nil   // accepted for source compat; unused (region-based)
    ) -> PoolMember? {
        guard !members.isEmpty else { return nil }

        let uc = userCountry.uppercased()

        // 1. Same country → random within cohort.
        if !uc.isEmpty {
            let same = members.filter { $0.country.uppercased() == uc }
            if let m = same.randomElement() { return m }

            // 2. Same REGION → random within cohort. Region comes from the
            //    member's stored `region` (what Android wrote at import),
            //    falling back to its country — same buckets, same labels.
            let userRegion = PoolRegions.regionForCountry(uc)
            let cohort = members.filter { PoolRegions.region(of: $0) == userRegion }
            if let m = cohort.randomElement() { return m }
        }

        // 3. Any eligible member at random.
        return members.randomElement()
    }
}
