package main

import (
	"testing"
)

func atVie() *PoolMember { return &PoolMember{ID: "at1", Name: "AT-vie-1", Country: "AT", Region: "Europe", Active: true} }
func atVie2() *PoolMember { return &PoolMember{ID: "at2", Name: "AT-vie-2", Country: "AT", Region: "Europe", Active: true} }
func deFra() *PoolMember { return &PoolMember{ID: "de1", Name: "DE-fra-1", Country: "DE", Region: "Europe", Active: true} }
func usNyc() *PoolMember { return &PoolMember{ID: "us1", Name: "US-nyc-1", Country: "US", Region: "North America", Active: true} }
func usLax() *PoolMember { return &PoolMember{ID: "us2", Name: "US-lax-1", Country: "US", Region: "North America", Active: true} }
func jpTok() *PoolMember { return &PoolMember{ID: "jp1", Name: "JP-tok-1", Country: "JP", Region: "Asia-Pacific", Active: true} }

func TestPickGeoNearest_CountryMatch(t *testing.T) {
	p := &Pool{Policy: PolicyGeoNearest, Members: []*PoolMember{atVie(), deFra(), usNyc()}}
	got := PickMember(p, "AT", "")
	if got == nil || got.Country != "AT" {
		t.Errorf("Geo-Nearest in AT picked %+v, want AT member", got)
	}
}

func TestPickGeoNearest_RegionFallback(t *testing.T) {
	p := &Pool{Policy: PolicyGeoNearest, Members: []*PoolMember{deFra(), usNyc()}}
	got := PickMember(p, "AT", "")
	if got == nil || got.Region != "Europe" {
		t.Errorf("Geo-Nearest fallback for AT picked %+v, want a Europe member", got)
	}
}

func TestPickGeoNearest_RandomFallback(t *testing.T) {
	p := &Pool{Policy: PolicyGeoNearest, Members: []*PoolMember{usNyc(), jpTok()}}
	got := PickMember(p, "AT", "")
	if got == nil {
		t.Errorf("Geo-Nearest with no Europe members returned nil; should fall back to any")
	}
}

func TestPickGeoNearest_CountryOverride(t *testing.T) {
	p := &Pool{Policy: PolicyGeoNearest, CountryOverride: "DE", Members: []*PoolMember{atVie(), deFra(), usNyc()}}
	// Even though detected country is AT, override DE should win.
	got := PickMember(p, "AT", "")
	if got == nil || got.Country != "DE" {
		t.Errorf("override DE produced %+v, want DE member", got)
	}
}

func TestPickGeoNearest_EmptyUserCountry(t *testing.T) {
	p := &Pool{Policy: PolicyGeoNearest, Members: []*PoolMember{atVie(), usNyc()}}
	got := PickMember(p, "", "")
	if got == nil {
		t.Error("empty user country should still pick (random fallback), got nil")
	}
}

func TestPickRandom_Eligible(t *testing.T) {
	p := &Pool{Policy: PolicyRandom, Members: []*PoolMember{atVie(), deFra(), usNyc()}}
	for i := 0; i < 5; i++ {
		got := PickMember(p, "AT", "")
		if got == nil {
			t.Error("Random picked nil")
		}
	}
}

func TestPickRandom_SkipsInactive(t *testing.T) {
	inactive := atVie()
	inactive.Active = false
	p := &Pool{Policy: PolicyRandom, Members: []*PoolMember{inactive, deFra()}}
	for i := 0; i < 10; i++ {
		got := PickMember(p, "", "")
		if got == nil || got.ID == "at1" {
			t.Errorf("Random picked inactive or nil: %+v", got)
		}
	}
}

func TestPickRoundRobin_AdvancesAcrossRegions(t *testing.T) {
	members := []*PoolMember{atVie(), usNyc(), jpTok()}
	p := &Pool{Policy: PolicyRoundRobin, Members: members}

	// Track which regions get visited across many ticks. With 3
	// regions and our advance-by-region rule, all three should appear.
	visited := make(map[string]bool)
	last := ""
	for i := 0; i < 30; i++ {
		got := PickMember(p, "", last)
		if got == nil {
			t.Fatal("Round-Robin picked nil")
		}
		visited[got.Region] = true
		last = got.ID
	}
	if len(visited) != 3 {
		t.Errorf("Round-Robin visited %d regions, want 3 (visited: %v)", len(visited), visited)
	}
}

func TestPickRoundRobin_SingleRegionAdvancesByMember(t *testing.T) {
	members := []*PoolMember{atVie(), atVie2(), deFra()}
	p := &Pool{Policy: PolicyRoundRobin, Members: members}

	// Both Europe members exist; round-robin within the only region.
	// After picking at1, the next call should give either at2 or de1
	// (different from at1).
	got := PickMember(p, "", "at1")
	if got == nil || got.ID == "at1" {
		t.Errorf("after at1 picked %+v, wanted a different member", got)
	}
}

func TestPickRoundRobin_NoLastMember(t *testing.T) {
	p := &Pool{Policy: PolicyRoundRobin, Members: []*PoolMember{atVie(), usNyc()}}
	got := PickMember(p, "", "")
	if got == nil {
		t.Error("first pick returned nil")
	}
}

func TestPickRoundRobin_StaleLastMemberID(t *testing.T) {
	p := &Pool{Policy: PolicyRoundRobin, Members: []*PoolMember{atVie(), usNyc()}}
	// "deleted" is not in the members list (e.g. user removed it).
	got := PickMember(p, "", "deleted")
	if got == nil {
		t.Error("stale lastMemberID should not break picking")
	}
}

func TestPickMember_NilPool(t *testing.T) {
	if got := PickMember(nil, "AT", ""); got != nil {
		t.Errorf("PickMember(nil) = %+v, want nil", got)
	}
}

func TestPickMember_EmptyPool(t *testing.T) {
	p := &Pool{Policy: PolicyRandom, Members: nil}
	if got := PickMember(p, "AT", ""); got != nil {
		t.Errorf("PickMember on empty pool = %+v, want nil", got)
	}
}

func TestPickMember_AllUnreachable(t *testing.T) {
	m := atVie()
	m.Unreachable = true
	p := &Pool{Policy: PolicyRandom, Members: []*PoolMember{m}}
	if got := PickMember(p, "", ""); got != nil {
		t.Errorf("all-unreachable pool should return nil, got %+v", got)
	}
}

func TestPickMember_RestrictRegions(t *testing.T) {
	p := &Pool{
		Policy:          PolicyGeoNearest,
		RestrictRegions: []string{"Europe"},
		Members:         []*PoolMember{atVie(), usNyc(), jpTok()},
	}
	for i := 0; i < 10; i++ {
		got := PickMember(p, "JP", "")
		if got == nil {
			t.Fatal("RestrictRegions=Europe with JP user picked nil")
		}
		if got.Region != "Europe" {
			t.Errorf("RestrictRegions excluded JP/US, but got %s", got.Region)
		}
	}
}

func TestPickMember_UnknownPolicyDegradesToRandom(t *testing.T) {
	p := &Pool{Policy: PoolPolicy("future-ml-policy"), Members: []*PoolMember{atVie(), usNyc()}}
	got := PickMember(p, "AT", "")
	if got == nil {
		t.Error("unknown policy should degrade to random, not nil")
	}
}
