package main

import (
	"crypto/rand"
	"math/big"
	"sort"

	"github.com/hoep/privycs/desktop/geoip"
)

// PickMember runs the Pool's policy and returns the member that should
// be connected next. lastMemberID is the previously-active member's ID
// (relevant for Round-Robin's diversity logic, ignored by other
// policies). userCountry is the detected/configured user country (only
// consumed by Geo-Nearest). Returns nil if no eligible member exists.
//
// The function is intentionally a pure dispatcher with no side effects
// - it does not mark the picked member as ActiveMemberID, does not
// touch network state, does not log. The caller (App) is responsible
// for those.
func PickMember(p *Pool, userCountry string, lastMemberID string) *PoolMember {
	if p == nil {
		return nil
	}
	eligible := p.EligibleMembers()
	if len(eligible) == 0 {
		return nil
	}

	switch p.Policy {
	case PolicyGeoNearest:
		return pickGeoNearest(eligible, effectiveCountry(p, userCountry))
	case PolicyRandom:
		return pickRandom(eligible)
	case PolicyRoundRobin:
		return pickRoundRobin(eligible, lastMemberID, effectiveCountry(p, userCountry))
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
// last one. The region order is alphabetical so the rotation is
// deterministic (useful for testing) but stable across saves.
//
// Within the chosen region we still pick randomly - over many ticks
// this gives a pseudo-equal distribution over members of that region,
// without the bookkeeping of remembering per-region cursors.
//
// First-pick semantics: start at the user's home region (derived from
// userCountry) when known, so the very first connect is geographically
// sensible. Without this, alphabetical sort puts "Africa" first and a
// user in AT would land on a Nigerian server before rotation kicks in.
// Subsequent picks alphabetic-rotate from there.
//
// If the pool has only a single region (so "different region" cannot
// be satisfied), we degrade to "different member from same region" so
// rotation visibly does something.
func pickRoundRobin(eligible []*PoolMember, lastMemberID string, userCountry string) *PoolMember {
	if len(eligible) == 0 {
		return nil
	}

	byRegion := groupByRegion(eligible)
	regions := sortedRegionKeys(byRegion)
	if len(regions) == 0 {
		return nil
	}

	// First-time pick: start at the user's home region if we have a
	// country and that region is represented in the pool. Otherwise
	// fall back to the alphabetically-first region.
	if lastMemberID == "" {
		if userCountry != "" {
			homeRegion := geoip.Region(userCountry)
			if members, ok := byRegion[homeRegion]; ok && len(members) > 0 {
				return pickRandom(members)
			}
		}
		return pickRandom(byRegion[regions[0]])
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
			if members, ok := byRegion[homeRegion]; ok && len(members) > 0 {
				return pickRandom(members)
			}
		}
		return pickRandom(byRegion[regions[0]])
	}

	// Find the index of lastRegion in the sorted list and advance.
	for i, r := range regions {
		if r == lastRegion {
			next := (i + 1) % len(regions)
			candidates := byRegion[regions[next]]
			// If we wrapped back to the same region (only one region in
			// the pool), exclude lastMemberID so we still rotate.
			if regions[next] == lastRegion && len(candidates) > 1 {
				filtered := make([]*PoolMember, 0, len(candidates)-1)
				for _, c := range candidates {
					if c.ID != lastMemberID {
						filtered = append(filtered, c)
					}
				}
				return pickRandom(filtered)
			}
			return pickRandom(candidates)
		}
	}
	return pickRandom(eligible)
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
