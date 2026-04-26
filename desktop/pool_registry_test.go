package main

import (
	"os"
	"path/filepath"
	"testing"
)

// withTempPoolRegistry redirects pools.json to a tmp dir so tests don't
// stomp the real user's pools.
func withTempPoolRegistry(t *testing.T) *PoolRegistry {
	t.Helper()
	dir := t.TempDir()
	r := &PoolRegistry{filePath: filepath.Join(dir, "pools.json")}
	return r
}

func TestPoolRegistry_CreateAndGet(t *testing.T) {
	r := withTempPoolRegistry(t)
	members := []*PoolMember{
		{ID: "m1", Name: "AT-vie-001", Country: "AT", Region: "Europe", Active: true},
		{ID: "m2", Name: "DE-fra-001", Country: "DE", Region: "Europe", Active: true},
	}
	p, err := r.Create("Test Pool", PolicyGeoNearest, members)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if p.ID == "" {
		t.Error("Create returned pool with empty ID")
	}
	if got := r.Get(p.ID); got == nil {
		t.Errorf("Get(%s) = nil, want pool", p.ID)
	}
	if l := r.List(); len(l) != 1 {
		t.Errorf("List len = %d, want 1", len(l))
	}
}

func TestPoolRegistry_DefaultRotation(t *testing.T) {
	r := withTempPoolRegistry(t)
	p, _ := r.Create("X", PolicyRoundRobin, nil)
	if p.Rotation.IntervalMin != 30 {
		t.Errorf("default IntervalMin = %d, want 30", p.Rotation.IntervalMin)
	}
	// Strict-by-default: IdleAware off so user-chosen Round-Robin
	// rotation actually fires on schedule. Users who want traffic-aware
	// deferral can opt in via the Edit-Pool modal.
	if p.Rotation.IdleAware {
		t.Error("default IdleAware should be false (strict rotation)")
	}
	if p.Rotation.ForceAfterMin != 30 {
		t.Errorf("default ForceAfterMin = %d, want 30", p.Rotation.ForceAfterMin)
	}
}

func TestPoolRegistry_RejectsBadInput(t *testing.T) {
	r := withTempPoolRegistry(t)
	if _, err := r.Create("", PolicyGeoNearest, nil); err == nil {
		t.Error("Create with empty name should error")
	}
	if _, err := r.Create("x", PoolPolicy("nonsense"), nil); err == nil {
		t.Error("Create with bad policy should error")
	}
}

func TestPoolRegistry_DeleteMember(t *testing.T) {
	r := withTempPoolRegistry(t)
	members := []*PoolMember{
		{ID: "m1", Name: "a", Active: true},
		{ID: "m2", Name: "b", Active: true},
		{ID: "m3", Name: "c", Active: true},
	}
	p, _ := r.Create("X", PolicyRandom, members)
	if err := r.DeleteMember(p.ID, "m2"); err != nil {
		t.Fatalf("DeleteMember: %v", err)
	}
	got := r.Get(p.ID)
	if len(got.Members) != 2 {
		t.Errorf("after delete, members len = %d, want 2", len(got.Members))
	}
	if got.MemberByID("m2") != nil {
		t.Error("m2 should be gone")
	}
}

func TestPoolRegistry_DeleteActiveMemberClearsPointer(t *testing.T) {
	r := withTempPoolRegistry(t)
	p, _ := r.Create("X", PolicyRandom, []*PoolMember{
		{ID: "m1", Name: "a", Active: true},
	})
	r.SetActiveMember(p.ID, "m1")
	if err := r.DeleteMember(p.ID, "m1"); err != nil {
		t.Fatalf("DeleteMember: %v", err)
	}
	got := r.Get(p.ID)
	if got.ActiveMemberID != "" {
		t.Errorf("ActiveMemberID = %q, want empty after delete", got.ActiveMemberID)
	}
}

func TestPoolRegistry_PersistRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pools.json")

	r1 := &PoolRegistry{filePath: path}
	p, err := r1.Create("Disk Test", PolicyGeoNearest, []*PoolMember{
		{ID: "m1", Name: "AT", Country: "AT", Region: "Europe", Active: true},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("pools.json should exist: %v", err)
	}

	r2 := &PoolRegistry{filePath: path}
	r2.load()
	got := r2.Get(p.ID)
	if got == nil {
		t.Fatalf("pool not loaded back from disk")
	}
	if got.Name != "Disk Test" {
		t.Errorf("name = %q, want %q", got.Name, "Disk Test")
	}
	if len(got.Members) != 1 || got.Members[0].Country != "AT" {
		t.Errorf("members did not survive round-trip: %+v", got.Members)
	}
}

func TestPool_Coverage(t *testing.T) {
	p := &Pool{Members: []*PoolMember{
		{Country: "AT", Region: "Europe", Active: true},
		{Country: "DE", Region: "Europe", Active: true},
		{Country: "DE", Region: "Europe", Active: true},
		{Country: "US", Region: "North America", Active: true},
		{Country: "JP", Region: "Asia-Pacific", Active: true},
		{Country: "JP", Region: "Asia-Pacific", Active: false}, // inactive: skipped
	}}
	cov := p.Coverage()
	if len(cov) != 3 {
		t.Fatalf("Coverage len = %d, want 3", len(cov))
	}
	if cov[0].Region != "Europe" || cov[0].Servers != 3 || cov[0].Countries != 2 {
		t.Errorf("Europe coverage wrong: %+v", cov[0])
	}
}

// TestPool_NextSlot covers the alternating slot logic that drives
// per-rotation tunnel-name flipping. The slot sequence on a fresh
// pool must be A -> B -> A -> B -> ..., never the same slot twice.
//
// This is the contract the rotator depends on: each connect uses
// the OPPOSITE slot of whatever was last persisted so the previous
// .conf file and OS service entry stay untouched and free for
// re-installation. Same slot twice in a row would race the .conf
// write against the still-running service.
func TestPool_NextSlot(t *testing.T) {
	p := &Pool{}

	// First call (empty ActiveSlot) starts at A.
	if got := p.NextSlot(); got != "A" {
		t.Errorf("NextSlot on fresh pool = %q, want A", got)
	}

	// Now simulate a connect: the App would persist ActiveSlot = "A".
	p.ActiveSlot = "A"
	if got := p.NextSlot(); got != "B" {
		t.Errorf("NextSlot after A = %q, want B", got)
	}

	// Next rotation: ActiveSlot = "B", expect A back.
	p.ActiveSlot = "B"
	if got := p.NextSlot(); got != "A" {
		t.Errorf("NextSlot after B = %q, want A", got)
	}

	// Long sequence sanity: 10 rotations alternate cleanly.
	p.ActiveSlot = ""
	for i := 0; i < 10; i++ {
		want := "A"
		if i%2 == 1 {
			want = "B"
		}
		got := p.NextSlot()
		if got != want {
			t.Errorf("rotation %d: NextSlot = %q, want %q", i, got, want)
		}
		p.ActiveSlot = got
	}
}

// TestPool_NextSlot_UnknownValue defends against persisted values that
// are neither A nor B (corrupted JSON, future schema). NextSlot must
// fail closed to a known slot rather than crash or loop.
func TestPool_NextSlot_UnknownValue(t *testing.T) {
	p := &Pool{ActiveSlot: "Z"}
	got := p.NextSlot()
	if got != "A" && got != "B" {
		t.Errorf("NextSlot on unknown ActiveSlot = %q, want A or B", got)
	}
}

func TestPool_EligibleMembers_RestrictRegions(t *testing.T) {
	p := &Pool{
		RestrictRegions: []string{"Europe"},
		Members: []*PoolMember{
			{ID: "1", Region: "Europe", Active: true},
			{ID: "2", Region: "North America", Active: true},
			{ID: "3", Region: "Europe", Active: true, Unreachable: true},
		},
	}
	got := p.EligibleMembers()
	if len(got) != 1 || got[0].ID != "1" {
		t.Errorf("EligibleMembers = %+v, want only ID=1", got)
	}
}
