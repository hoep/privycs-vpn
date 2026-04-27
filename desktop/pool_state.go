package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// PoolStateRegistry persists and serves the FAST-MOVING runtime state
// of pools and their members - the bits that change on every rotation
// (active member, pending member, slot) and on every health-check
// failure (unreachable flag, error message, timestamp).
//
// Why split out from PoolRegistry:
//
//  1. CORRECTNESS: pool_registry.go's Update() rewrites the whole
//     pools.json on every state mutation. With 600 members at ~200B
//     each plus base data, that's ~150KB on every Unreachable flip,
//     every PendingMemberID set, every ActiveSlot persist. A typical
//     rotation triggers 4-6 such writes. JSON-marshaling 150KB and
//     a tmp+rename dance per write is non-trivial Disk-I/O on Windows.
//     The state.json is typically <2KB regardless of pool size.
//
//  2. CONCURRENCY: PoolRegistry.Update mutates Pool/Member pointers
//     directly. EligibleMembers (pure-read by design) was found to
//     also mutate (TTL clear, all-unreachable reset) which collided
//     with concurrent markMemberUnreachable. The state registry
//     funnels every mutation through methods with proper locking
//     so the fast-path stays safe even under heavy rotation load.
//
//  3. CRASH-SAFETY: pools.json holds the irreplaceable definitions
//     (config files, keys, country data). state.json holds derived
//     state. If state.json ever gets corrupted, we delete it and
//     pools become "fresh" from a runtime POV; no user data is lost.
//     Conversely, frequent writes to pools.json increase the chance
//     of a power-loss-during-rename corrupting irreplaceable data.
//
// Concurrency model:
//   - All methods take r.mu.
//   - r.mu must NOT be held while calling out to other registries
//     or callbacks. All persist work happens inside the lock.
//   - Throttled write: a 500ms debounce after the last mutation
//     batches rapid sequential mutations into one disk hit. The
//     scheduler is a single goroutine spawned at registry construction.
//
// File format (simple, hand-written-friendly for emergency edits):
//
//	{
//	  "pools": {
//	    "<pool-id>": {
//	      "active_member_id": "...",
//	      "pending_member_id": "...",
//	      "active_slot": "A",
//	      "members": {
//	        "<member-id>": {
//	          "unreachable": true,
//	          "last_unreachable": "2026-04-27T12:34:56Z",
//	          "last_error": "..."
//	        }
//	      }
//	    }
//	  }
//	}
type PoolStateRegistry struct {
	mu       sync.Mutex
	state    poolStateFile
	filePath string

	// Throttled-write machinery. dirty=true on every mutation, the
	// flusher goroutine wakes every flushInterval and drains.
	dirty   bool
	stopCh  chan struct{}
	wakeCh  chan struct{}
}

type poolStateFile struct {
	Pools map[string]*poolStateEntry `json:"pools"`
}

type poolStateEntry struct {
	ActiveMemberID  string                          `json:"active_member_id,omitempty"`
	PendingMemberID string                          `json:"pending_member_id,omitempty"`
	ActiveSlot      string                          `json:"active_slot,omitempty"`
	Members         map[string]*memberStateEntry    `json:"members,omitempty"`
	// MemberCursors is a per-region "last picked member" tracker for
	// the Round-Robin policy. Lets the picker advance through the
	// members of a region in a deterministic round-robin pattern
	// rather than picking randomly within. Without cursors, "Round-
	// Robin" was actually "rotate to next region, pick random
	// member" which could re-pick the same exit IP within a few
	// rotations and weakens the privacy property.
	//
	// Key: region name ("Europe", "Asia-Pacific", etc.). Value: ID
	// of the member that was picked last time this region was
	// rotated to. Picker advances to the member at index
	// (lastIndex+1) % len(membersInRegion).
	MemberCursors map[string]string `json:"member_cursors,omitempty"`
}

type memberStateEntry struct {
	Unreachable     bool      `json:"unreachable,omitempty"`
	LastUnreachable time.Time `json:"last_unreachable,omitempty"`
	LastError       string    `json:"last_error,omitempty"`
}

// stateFlushDebounce is the window during which rapid-fire mutations
// get batched into a single disk write. Long enough to coalesce a
// rotation's 4-6 writes, short enough that crash-loss-window stays
// near 0.5s.
const stateFlushDebounce = 500 * time.Millisecond

// NewPoolStateRegistry constructs a state registry, loading state.json
// if present. Spawns the throttled-flush goroutine.
func NewPoolStateRegistry() *PoolStateRegistry {
	r := &PoolStateRegistry{
		filePath: filepath.Join(appDataDir(), "pool_state.json"),
		stopCh:   make(chan struct{}),
		wakeCh:   make(chan struct{}, 1),
	}
	r.state.Pools = make(map[string]*poolStateEntry)
	r.load()
	go r.flusher()
	return r
}

// Stop terminates the flusher and forces a final synchronous save.
// Idempotent.
func (r *PoolStateRegistry) Stop() {
	r.mu.Lock()
	select {
	case <-r.stopCh:
		r.mu.Unlock()
		return
	default:
		close(r.stopCh)
	}
	dirty := r.dirty
	r.dirty = false
	r.mu.Unlock()
	if dirty {
		r.saveSafe()
	}
}

// PoolState returns the cached state for a pool, creating an empty
// entry if none exists. Caller must NOT mutate the returned map's
// contents - use the registry's Set/Mark methods instead.
func (r *PoolStateRegistry) PoolState(poolID string) *poolStateEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.poolStateLocked(poolID)
}

// MemberState returns the runtime state for a single member. Always
// returns a non-nil pointer; the empty entry means "no state recorded
// yet" which by convention is "reachable, never failed".
func (r *PoolStateRegistry) MemberState(poolID, memberID string) memberStateEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.poolStateLocked(poolID)
	if p.Members == nil {
		return memberStateEntry{}
	}
	if m, ok := p.Members[memberID]; ok && m != nil {
		return *m
	}
	return memberStateEntry{}
}

// MarkMemberUnreachable flips the flag and persists. Idempotent on
// repeated calls (timestamp updates each time, so the TTL clock
// effectively resets - intentional: a member that keeps failing should
// not become eligible just because the first failure aged out).
func (r *PoolStateRegistry) MarkMemberUnreachable(poolID, memberID, reason string) {
	r.mu.Lock()
	p := r.poolStateLocked(poolID)
	if p.Members == nil {
		p.Members = make(map[string]*memberStateEntry)
	}
	m, ok := p.Members[memberID]
	if !ok || m == nil {
		m = &memberStateEntry{}
		p.Members[memberID] = m
	}
	m.Unreachable = true
	m.LastUnreachable = time.Now()
	m.LastError = reason
	r.markDirtyLocked()
	r.mu.Unlock()
}

// ClearMemberUnreachable resets the flag for a single member. Used
// by the manual "Reset all unreachable" button in the Pool detail
// view, and by the lazy TTL sweep when the timestamp ages out.
func (r *PoolStateRegistry) ClearMemberUnreachable(poolID, memberID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.poolStateLocked(poolID)
	if p.Members == nil {
		return
	}
	if m, ok := p.Members[memberID]; ok && m != nil && (m.Unreachable || m.LastError != "") {
		m.Unreachable = false
		m.LastError = ""
		// Keep LastUnreachable timestamp - useful for "last seen
		// failing at X" diagnostic display even when not currently
		// flagged.
		r.markDirtyLocked()
	}
}

// ClearAllMembersUnreachable clears every member's unreachable flag
// for the pool. Returns the count cleared so the UI can show
// "Reset 12 unreachable members".
func (r *PoolStateRegistry) ClearAllMembersUnreachable(poolID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.poolStateLocked(poolID)
	if p.Members == nil {
		return 0
	}
	cleared := 0
	for _, m := range p.Members {
		if m == nil {
			continue
		}
		if m.Unreachable {
			m.Unreachable = false
			m.LastError = ""
			cleared++
		}
	}
	if cleared > 0 {
		r.markDirtyLocked()
	}
	return cleared
}

// SweepStaleUnreachable clears any member's unreachable flag whose
// timestamp is older than ttl. Returns count of flags cleared.
// Called from EligibleMembers' read path so stale flags rehabilitate
// without a separate sweeper goroutine.
func (r *PoolStateRegistry) SweepStaleUnreachable(poolID string, ttl time.Duration) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.poolStateLocked(poolID)
	if p.Members == nil {
		return 0
	}
	cleared := 0
	now := time.Now()
	for _, m := range p.Members {
		if m == nil || !m.Unreachable {
			continue
		}
		if !m.LastUnreachable.IsZero() && now.Sub(m.LastUnreachable) > ttl {
			m.Unreachable = false
			m.LastError = ""
			cleared++
		}
	}
	if cleared > 0 {
		r.markDirtyLocked()
	}
	return cleared
}

// SetActiveMember persists which member is currently the rotation's
// active pick.
func (r *PoolStateRegistry) SetActiveMember(poolID, memberID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.poolStateLocked(poolID)
	if p.ActiveMemberID == memberID {
		return
	}
	p.ActiveMemberID = memberID
	r.markDirtyLocked()
}

// SetPendingMember persists the pre-warmed next member.
func (r *PoolStateRegistry) SetPendingMember(poolID, memberID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.poolStateLocked(poolID)
	if p.PendingMemberID == memberID {
		return
	}
	p.PendingMemberID = memberID
	r.markDirtyLocked()
}

// SetActiveSlot persists which slot (A/B) is currently up.
func (r *PoolStateRegistry) SetActiveSlot(poolID, slot string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.poolStateLocked(poolID)
	if p.ActiveSlot == slot {
		return
	}
	p.ActiveSlot = slot
	r.markDirtyLocked()
}

// SetRegionCursor persists "last picked member" for a region.
// Round-Robin uses this to advance through the region members.
func (r *PoolStateRegistry) SetRegionCursor(poolID, region, memberID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.poolStateLocked(poolID)
	if p.MemberCursors == nil {
		p.MemberCursors = make(map[string]string)
	}
	if p.MemberCursors[region] == memberID {
		return
	}
	p.MemberCursors[region] = memberID
	r.markDirtyLocked()
}

// RegionCursor reads the last-picked member ID for a region. Empty
// means "never rotated to this region" -> picker starts at index 0.
func (r *PoolStateRegistry) RegionCursor(poolID, region string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.poolStateLocked(poolID)
	if p.MemberCursors == nil {
		return ""
	}
	return p.MemberCursors[region]
}

// DeletePool removes all state for a pool. Called when the pool is
// deleted from the definition registry.
func (r *PoolStateRegistry) DeletePool(poolID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.state.Pools[poolID]; ok {
		delete(r.state.Pools, poolID)
		r.markDirtyLocked()
	}
}

// DeleteMember removes one member's state from a pool. Also clears
// ActiveMemberID / PendingMemberID if they pointed at the removed
// member, so the rotator does not retain a dangling reference.
//
// Idempotent and safe to call even if the member never had any state
// recorded - the active/pending pointer cleanup still runs.
func (r *PoolStateRegistry) DeleteMember(poolID, memberID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.state.Pools[poolID]
	if !ok {
		return
	}
	dirty := false
	if p.Members != nil {
		if _, ok := p.Members[memberID]; ok {
			delete(p.Members, memberID)
			dirty = true
		}
	}
	// Active/pending cleanup runs regardless of whether the member
	// had a status entry - they can be set without status (e.g.
	// SetActiveMember called before any failure).
	if p.ActiveMemberID == memberID {
		p.ActiveMemberID = ""
		dirty = true
	}
	if p.PendingMemberID == memberID {
		p.PendingMemberID = ""
		dirty = true
	}
	if dirty {
		r.markDirtyLocked()
	}
}

// MigrateFromLegacy ingests state from a legacy PoolMember slice (the
// pre-state-separation format where Unreachable / LastError /
// LastUnreachable lived directly on PoolMember). Call once at app
// startup BEFORE writing pools.json - the migration moves the data
// into state.json, and the next pools.json save (with json:"-" tags)
// silently drops the legacy fields. Idempotent: if state.json already
// has entries for a member, they take precedence.
func (r *PoolStateRegistry) MigrateFromLegacy(poolID string, members []*PoolMember) {
	if len(members) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.poolStateLocked(poolID)
	if p.Members == nil {
		p.Members = make(map[string]*memberStateEntry)
	}
	migrated := 0
	for _, m := range members {
		if m == nil {
			continue
		}
		if !m.legacyUnreachable && m.legacyLastError == "" {
			continue
		}
		// Only migrate if not already present in state.
		if existing, ok := p.Members[m.ID]; ok && existing != nil {
			continue
		}
		p.Members[m.ID] = &memberStateEntry{
			Unreachable:     m.legacyUnreachable,
			LastUnreachable: m.legacyLastUnreachable,
			LastError:       m.legacyLastError,
		}
		migrated++
	}
	if migrated > 0 {
		log.Printf("PoolState: migrated %d legacy member-state entries for pool %s",
			migrated, poolID)
		r.markDirtyLocked()
	}
}

// poolStateLocked returns the entry for a pool, creating an empty
// one if needed. Caller must hold r.mu.
func (r *PoolStateRegistry) poolStateLocked(poolID string) *poolStateEntry {
	if r.state.Pools == nil {
		r.state.Pools = make(map[string]*poolStateEntry)
	}
	p, ok := r.state.Pools[poolID]
	if !ok || p == nil {
		p = &poolStateEntry{}
		r.state.Pools[poolID] = p
	}
	return p
}

// markDirtyLocked schedules a flush. Caller must hold r.mu.
func (r *PoolStateRegistry) markDirtyLocked() {
	r.dirty = true
	// Non-blocking wake of the flusher.
	select {
	case r.wakeCh <- struct{}{}:
	default:
	}
}

// flusher is the single goroutine that drains dirty state to disk.
// On wake, it sleeps for the debounce window so rapid sequential
// mutations coalesce into one write. Also persists periodically (every
// 5s) as a safety net in case the wake-channel was missed.
func (r *PoolStateRegistry) flusher() {
	periodic := time.NewTicker(5 * time.Second)
	defer periodic.Stop()
	for {
		select {
		case <-r.stopCh:
			return
		case <-r.wakeCh:
			// Debounce: wait for the rapid-fire window to close.
			select {
			case <-time.After(stateFlushDebounce):
			case <-r.stopCh:
				return
			}
			r.saveSafe()
		case <-periodic.C:
			r.saveSafe()
		}
	}
}

// saveSafe drains the dirty flag and writes state.json. Logs failure
// loudly because lost runtime state on next restart shows up as
// "rotation forgot which member was active" or "unreachable flags
// gone" - both diagnosable from this log.
func (r *PoolStateRegistry) saveSafe() {
	r.mu.Lock()
	if !r.dirty {
		r.mu.Unlock()
		return
	}
	data, err := json.MarshalIndent(r.state, "", "  ")
	r.dirty = false
	r.mu.Unlock()
	if err != nil {
		log.Printf("PoolState: marshal failed: %v", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(r.filePath), 0o755); err != nil {
		log.Printf("PoolState: mkdir failed: %v", err)
		return
	}
	tmp := r.filePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		log.Printf("PoolState: write tmp failed: %v", err)
		return
	}
	if err := os.Rename(tmp, r.filePath); err != nil {
		log.Printf("PoolState: rename failed: %v", err)
		_ = os.Remove(tmp)
	}
}

// load reads state.json. Missing-file is normal (first run, fresh
// install). Parse failure is logged loud but non-fatal: we drop the
// state and continue with empty - members will be re-flagged on the
// next failed connect, no user data is lost.
func (r *PoolStateRegistry) load() {
	data, err := os.ReadFile(r.filePath)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("PoolState: load %s: %v", r.filePath, err)
		}
		return
	}
	var parsed poolStateFile
	if err := json.Unmarshal(data, &parsed); err != nil {
		log.Printf("PoolState: parse %s: %v - resetting state", r.filePath, err)
		return
	}
	if parsed.Pools == nil {
		parsed.Pools = make(map[string]*poolStateEntry)
	}
	r.state = parsed
}

// SnapshotForPersist serializes a snapshot for callers that need to
// persist beside their own data (used by the encrypted backup feature).
// Returns the bytes a fresh state.json would contain RIGHT NOW.
func (r *PoolStateRegistry) SnapshotForPersist() ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	data, err := json.MarshalIndent(r.state, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("snapshot: %w", err)
	}
	return data, nil
}

// replaceFromSnapshot atomically swaps the registry's state with the
// given snapshot and triggers a save. Used by ImportBackup to restore
// runtime state from a backup.
func (r *PoolStateRegistry) replaceFromSnapshot(snapshot poolStateFile) {
	r.mu.Lock()
	if snapshot.Pools == nil {
		snapshot.Pools = make(map[string]*poolStateEntry)
	}
	r.state = snapshot
	r.markDirtyLocked()
	r.mu.Unlock()
}
