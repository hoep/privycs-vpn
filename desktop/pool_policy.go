package main

import (
	"crypto/rand"
	"math/big"
	"sort"

	"github.com/hoep/privycs/desktop/geoip"
)

// Pick is the convenience wrapper that callers with a *PoolRegistry
// use. Threads the registry's runtime-state through so unreachable
// members are filtered.
func (r *PoolRegistry) Pick(p *Pool, userCountry, lastMemberID string) *PoolMember {
	var state *PoolStateRegistry
	if r != nil {
		state = r.state
	}
	return pickMemberInternal(state, p, userCountry, lastMemberID, nil)
}

// PickExcluding is the wrapper that retry loops use.
func (r *PoolRegistry) PickExcluding(p *Pool, userCountry, lastMemberID string, excludeIDs []string) *PoolMember {
	var state *PoolStateRegistry
	if r != nil {
		state = r.state
	}
	return pickMemberInternal(state, p, userCountry, lastMemberID, excludeIDs)
}

// PickMember runs the Pool's policy and returns the member that should
// be connected next. Convenience entry-point that treats all members
// as reachable (no state-registry consultation). Production code uses
// (*PoolRegistry).Pick() which threads the registry's state through.
// Tests use this form directly.
func PickMember(p *Pool, userCountry string, lastMemberID string) *PoolMember {
	return PickMemberExcluding(p, userCountry, lastMemberID, nil)
}

// PickMemberExcluding is the no-state-registry form. See registry.PickExcluding
// for the production form that consults runtime unreachable state.
func PickMemberExcluding(p *Pool, userCountry string, lastMemberID string, excludeIDs []string) *PoolMember {
	return pickMemberInternal(nil, p, userCountry, lastMemberID, excludeIDs)
}

// pickMemberInternal is the shared implementation. state may be nil
// (treats all members as reachable).
func pickMemberInternal(state *PoolStateRegistry, p *Pool, userCountry string, lastMemberID string, excludeIDs []string) *PoolMember {
	if p == nil {
		return nil
	}
	eligible := p.EligibleMembers(state)
	if len(excludeIDs) > 0 {
		filtered := make([]*PoolMember, 0, len(eligible))
		for _, m := range eligible {
			skip := false
			for _, id := range excludeIDs {
				if m.ID == id {
					skip = true
					break
				}
			}
			if !skip {
				filtered = append(filtered, m)
			}
		}
		eligible = filtered
	}
	if len(eligible) == 0 {
		return nil
	}

	switch p.Policy {
	case PolicyGeoNearest:
		return pickGeoNearest(eligible, effectiveCountry(p, userCountry))
	case PolicyRandom:
		return pickRandom(eligible)
	case PolicyRoundRobin:
		return pickRoundRobin(state, p, eligible, lastMemberID, effectiveCountry(p, userCountry))
	}

	// Unknown policy - degrade to random rather than refusing to connect.
	return pickRandom(eligible)
}

// effectiveCountry resolves the country that Geo-Nearest matches
// against. Manual override on the Pool wins over auto-detect.
func effectiveCountry(p *Pool, detectedCountry string) string {
	if p.CountryOverride != "" {
		return p.CountryOverride
	}
	return detectedCountry
}

// pickGeoNearest picks a member matching the user's country, with
// region fallback if no country match exists, and a final random
// fallback if neither country nor region match.
//
// "Geographically nearest" is a coarse approximation - we do not have
// pre-VPN latency data, and country-match is the strongest signal a
// pure offline lookup can give. A user in AT with a server in DE will
// get the DE server (region match) over a server in JP. A user in AT
// with no European servers in the pool still gets a connection (random
// fallback) rather than a refusal.
func pickGeoNearest(eligible []*PoolMember, userCountry string) *PoolMember {
	if len(eligible) == 0 {
		return nil
	}

	// Tier 1: exact country match.
	if userCountry != "" {
		matches := make([]*PoolMember, 0, len(eligible))
		for _, m := range eligible {
			if m.Country == userCountry {
				matches = append(matches, m)
			}
		}
		if len(matches) > 0 {
			return pickRandom(matches)
		}
	}

	// Tier 2: same region (continent-level proxy).
	if userCountry != "" {
		userRegion := geoip.Region(userCountry)
		matches := make([]*PoolMember, 0, len(eligible))
		for _, m := range eligible {
			if m.Region == userRegion {
				matches = append(matches, m)
			}
		}
		if len(matches) > 0 {
			return pickRandom(matches)
		}
	}

	// Tier 3: any eligible. The Pool may not contain a server in the
	// user's region at all - return SOMETHING rather than fail.
	return pickRandom(eligible)
}

// pickRandom returns a uniformly-random pick from the slice. Uses
// crypto/rand because math/rand without a seed produces the same
// sequence across runs, which would surprise users running policy=
// Random with a fresh app start.
func pickRandom(members []*PoolMember) *PoolMember {
	if len(members) == 0 {
		return nil
	}
	if len(members) == 1 {
		return members[0]
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(len(members))))
	if err != nil {
		// crypto/rand failure is pathological - fall back to first.
		return members[0]
	}
	return members[n.Int64()]
}

// pickRoundRobin advances to a member in a different region than the
// last one, then picks WITHIN that region using a per-region round-
// robin cursor (since v0.9.11.39). Earlier versions picked randomly
// within the chosen region; the change closes a privacy hole where
// the same exit IP could be re-picked within just a few rotations
// despite the pool having dozens of members in that region.
//
// Region order is alphabetical so the inter-region rotation is
// deterministic (useful for testing) and stable across saves.
//
// First-pick semantics: start at the user's home region (derived from
// userCountry) when known, so the very first connect is geographically
// sensible. Within the chosen region we honour the cursor -
// SortedByID order means the same member sequence across runs.
//
// Edge cases:
//   - Single-region pool (len(regions) == 1): degrade to "next member
//     in the only region" using the cursor.
//   - Single-member pool: returns the only member every time
//     (rotation effectively no-ops, but does not crash or loop).
//   - Last member deleted/marked unreachable since last pick: the
//     cursor falls through to "advance from the last persisted
//     position", which on a fresh pool means "start at index 0".
func pickRoundRobin(state *PoolStateRegistry, p *Pool, eligible []*PoolMember, lastMemberID string, userCountry string) *PoolMember {
	if len(eligible) == 0 {
		return nil
	}

	byRegion := groupByRegion(eligible)
	regions := sortedRegionKeys(byRegion)
	if len(regions) == 0 {
		return nil
	}

	pickCursorBased := func(region string) *PoolMember {
		members := byRegion[region]
		if len(members) == 0 {
			return nil
		}
		// Sort members by ID inside the region for deterministic
		// cursor advancement. Mutating the byRegion slice in place is
		// safe (it's a fresh slice from groupByRegion).
		sort.Slice(members, func(i, j int) bool {
			return members[i].ID < members[j].ID
		})
		// Cursor source priority: state-registry's persisted cursor,
		// else the lastMemberID arg (if it's in this region), else
		// empty (start at index 0). The arg-fallback keeps tests
		// without a state registry working with the historic
		// "advance from lastMemberID" semantics.
		cursor := ""
		if state != nil && p != nil {
			cursor = state.RegionCursor(p.ID, region)
		}
		if cursor == "" && lastMemberID != "" {
			for _, m := range members {
				if m.ID == lastMemberID {
					cursor = lastMemberID
					break
				}
			}
		}
		startIdx := 0
		if cursor != "" {
			for i, m := range members {
				if m.ID == cursor {
					startIdx = (i + 1) % len(members)
					break
				}
			}
		}
		picked := members[startIdx]
		if state != nil && p != nil {
			state.SetRegionCursor(p.ID, region, picked.ID)
		}
		return picked
	}

	// Single-region pool: cursor-only rotation within the one region.
	if len(regions) == 1 {
		return pickCursorBased(regions[0])
	}

	// First-time pick: start at the user's home region if available
	// in the pool, else the alphabetically-first region.
	if lastMemberID == "" {
		if userCountry != "" {
			homeRegion := geoip.Region(userCountry)
			if _, ok := byRegion[homeRegion]; ok {
				return pickCursorBased(homeRegion)
			}
		}
		return pickCursorBased(regions[0])
	}

	// Find the previous member's region.
	var lastRegion string
	for _, m := range eligible {
		if m.ID == lastMemberID {
			lastRegion = m.Region
			break
		}
	}
	if lastRegion == "" {
		// Last member is no longer eligible (deleted, marked unreachable).
		// Restart from the user's home region if known, else first region.
		if userCountry != "" {
			homeRegion := geoip.Region(userCountry)
			if _, ok := byRegion[homeRegion]; ok {
				return pickCursorBased(homeRegion)
			}
		}
		return pickCursorBased(regions[0])
	}

	// Advance to the next region alphabetically.
	for i, r := range regions {
		if r == lastRegion {
			next := (i + 1) % len(regions)
			return pickCursorBased(regions[next])
		}
	}
	return pickCursorBased(regions[0])
}

func groupByRegion(members []*PoolMember) map[string][]*PoolMember {
	out := make(map[string][]*PoolMember)
	for _, m := range members {
		r := m.Region
		if r == "" {
			r = "Other"
		}
		out[r] = append(out[r], m)
	}
	return out
}

func sortedRegionKeys(byRegion map[string][]*PoolMember) []string {
	keys := make([]string, 0, len(byRegion))
	for k := range byRegion {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
