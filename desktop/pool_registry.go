package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

// PoolPolicy is the strategy a Pool uses to pick a member at connect-time.
type PoolPolicy string

const (
	PolicyGeoNearest PoolPolicy = "geo-nearest"
	PolicyRandom     PoolPolicy = "random"
	PolicyRoundRobin PoolPolicy = "round-robin-region"
)

// PoolRotation captures Round-Robin-only timing parameters. Ignored by
// Geo-Nearest and Random policies but kept on every Pool so users can
// flip policies without losing their rotation preferences.
type PoolRotation struct {
	IntervalMin       int  `json:"interval_min"`        // 5 / 10 / 30 / custom
	IdleAware         bool `json:"idle_aware"`          // skip rotation if traffic
	ForceAfterMin     int  `json:"force_after_min"`     // hard cap on idle-block
}

// DefaultRotation returns the rotation defaults written for new Pools.
// 30 min interval, NOT idle-aware, 30 min force-cap. Strict-by-default:
// the user has explicitly chosen Round-Robin so rotation should fire
// reliably; if they want idle-aware deferral they enable it via the
// Edit-Pool modal. Earlier defaults (idle_aware=true, force_after=60)
// led to cases where traffic-driven sessions blocked rotation for an
// hour - that defeats the privacy intent of Round-Robin.
func DefaultRotation() PoolRotation {
	return PoolRotation{IntervalMin: 30, IdleAware: false, ForceAfterMin: 30}
}

// PoolMember is one config inside a Pool. Each member is an independent
// VPN endpoint with its own config, country, and health state. Members
// may be from different protocols within the same Pool (importing a
// mixed ZIP of .conf and .ovpn files is supported).
//
// Runtime state (Unreachable / LastError / LastUnreachable) was moved
// out of this struct in v0.9.11.39 and lives in pool_state.json now.
// The Unreachable / LastError / LastUnreachable fields here are
// deprecated and exist ONLY for one-time migration from old
// pools.json files. They are NOT persisted (json:"-") and NOT read
// at runtime (Unreachable() / LastError() / LastUnreachable() helpers
// route to the state registry).
type PoolMember struct {
	ID      string          `json:"id"`
	Name    string          `json:"name"`         // derived from filename
	Config  *ProtocolConfig `json:"config"`       // reused from connection_registry
	Country string          `json:"country"`      // ISO 3166-1 alpha-2, "" if unknown
	Region  string          `json:"region"`       // derived from country via geoip.Region
	Active  bool            `json:"active"`       // future Pro-tier cap; default true

	// Legacy runtime fields - kept ONLY so an existing pools.json
	// from before the v0.9.11.39 state-split can be read. The custom
	// UnmarshalJSON copies them into legacy* fields, then registry
	// migration moves them to pool_state.json. After the first save
	// these never appear on disk again because of json:"-".
	legacyUnreachable     bool
	legacyLastError       string
	legacyLastUnreachable time.Time
}

// UnmarshalJSON exists so we can read the old field names from a
// legacy pools.json without persisting them again. The extra struct
// gives us the wire format with the legacy keys; we copy into the
// real struct (which omits them) plus the legacy* fields used by
// MigrateFromLegacy.
func (m *PoolMember) UnmarshalJSON(data []byte) error {
	type wireFormat struct {
		ID              string          `json:"id"`
		Name            string          `json:"name"`
		Config          *ProtocolConfig `json:"config"`
		Country         string          `json:"country"`
		Region          string          `json:"region"`
		Active          bool            `json:"active"`
		Unreachable     bool            `json:"unreachable"`
		LastError       string          `json:"last_error"`
		LastUnreachable time.Time       `json:"last_unreachable"`
	}
	var w wireFormat
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	m.ID = w.ID
	m.Name = w.Name
	m.Config = w.Config
	m.Country = w.Country
	m.Region = w.Region
	m.Active = w.Active
	m.legacyUnreachable = w.Unreachable
	m.legacyLastError = w.LastError
	m.legacyLastUnreachable = w.LastUnreachable
	return nil
}

// UnreachableTTL is how long after a connect/probe failure we keep a
// member out of rotation. 30 min strikes a balance: long enough that
// an immediately-following retry skips it (no thrash), short enough
// that a real provider outage clears within the same user session.
const UnreachableTTL = 30 * time.Minute

// Pool is a virtual connection that wraps multiple PoolMembers and
// picks one of them at connect-time using its policy.
//
// Runtime state (ActiveMemberID, PendingMemberID, ActiveSlot) was
// moved into pool_state.json in v0.9.11.39. The fields below are
// kept on the struct so existing call-sites compile without churn,
// but they are NOT serialized into pools.json (json:"-"). Code that
// reads them goes through PoolRegistry helpers (ActiveMemberID(),
// PendingMemberID(), ActiveSlot()) which delegate to PoolStateRegistry.
//
// Legacy fields (legacyActiveMemberID etc.) preserve old pools.json
// values for one-time migration into state.json.
type Pool struct {
	ID              string           `json:"id"`
	Name            string           `json:"name"`
	CreatedAt       time.Time        `json:"created_at"`
	Policy          PoolPolicy       `json:"policy"`
	Rotation        PoolRotation     `json:"rotation"`
	Members         []*PoolMember    `json:"members"`
	CountryOverride string           `json:"country_override,omitempty"`
	RestrictRegions []string         `json:"restrict_regions,omitempty"`
	SplitTunnel     PoolSplitTunnel  `json:"split_tunnel,omitempty"`
	// Per-pool DNS override. Comma- or whitespace-separated IPs.
	// When non-empty, overrides Settings.DNSOverride for the
	// duration of this pool's tunnel. Empty falls back to global.
	DnsOverride     string           `json:"dns_override,omitempty"`

	// memberByID is a parallel index for O(1) MemberByID lookups.
	// Built on load + Create + DeleteMember + RenameMember. NOT
	// persisted (rebuilt deterministically from Members). Hot path:
	// connectToPoolMember + ListPools call MemberByID multiple times
	// per pool per second.
	memberByID map[string]*PoolMember

	// Legacy runtime fields - read from old pools.json, migrated to
	// state.json on first load post-upgrade, then never persisted
	// again. Do not read these directly at runtime - use the
	// registry helpers.
	legacyActiveMemberID  string
	legacyPendingMemberID string
	legacyActiveSlot      string
}

// UnmarshalJSON reads pools.json including the legacy runtime fields
// so they can be migrated to state.json. After migration, save
// behavior (default Marshal) produces a clean pools.json without
// runtime fields.
func (p *Pool) UnmarshalJSON(data []byte) error {
	type wireFormat struct {
		ID              string           `json:"id"`
		Name            string           `json:"name"`
		CreatedAt       time.Time        `json:"created_at"`
		Policy          PoolPolicy       `json:"policy"`
		Rotation        PoolRotation     `json:"rotation"`
		Members         []*PoolMember    `json:"members"`
		CountryOverride string           `json:"country_override"`
		RestrictRegions []string         `json:"restrict_regions"`
		SplitTunnel     PoolSplitTunnel  `json:"split_tunnel"`
		ActiveMemberID  string           `json:"active_member_id"`
		PendingMemberID string           `json:"pending_member_id"`
		ActiveSlot      string           `json:"active_slot"`
	}
	var w wireFormat
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	p.ID = w.ID
	p.Name = w.Name
	p.CreatedAt = w.CreatedAt
	p.Policy = w.Policy
	p.Rotation = w.Rotation
	p.Members = w.Members
	p.CountryOverride = w.CountryOverride
	p.RestrictRegions = w.RestrictRegions
	p.SplitTunnel = w.SplitTunnel
	p.legacyActiveMemberID = w.ActiveMemberID
	p.legacyPendingMemberID = w.PendingMemberID
	p.legacyActiveSlot = w.ActiveSlot
	p.rebuildMemberIndex()
	return nil
}

// PoolSplitTunnel is the per-pool client-side split-tunnel config.
// Mirrors Android's data.models.PoolSplitTunnel; same on-disk JSON
// shape so backups round-trip across platforms.
type PoolSplitTunnel struct {
	BypassCidrs             []string `json:"bypass_cidrs,omitempty"`
	ExcludePrivateNetworks  bool     `json:"exclude_private_networks,omitempty"`
}

// IsActive reports whether this config affects the connect path.
// Empty bypass list + toggle off = no-op.
func (s PoolSplitTunnel) IsActive() bool {
	return len(s.BypassCidrs) > 0 || s.ExcludePrivateNetworks
}

// rebuildMemberIndex rebuilds memberByID from Members. Called on
// load and after any add/remove operation. Cheap (~600 entries).
func (p *Pool) rebuildMemberIndex() {
	if p.memberByID == nil {
		p.memberByID = make(map[string]*PoolMember, len(p.Members))
	} else {
		// Reuse map to avoid GC churn.
		for k := range p.memberByID {
			delete(p.memberByID, k)
		}
	}
	for _, m := range p.Members {
		if m != nil && m.ID != "" {
			p.memberByID[m.ID] = m
		}
	}
}

// NextSlot returns the slot to use for the NEXT connect for this
// pool. Reads runtime state from the registry (not stored on Pool
// since v0.9.11.39). Caller must pass the registry; nil registry
// means "no state, start with A".
//
// NOTE: this method exists for callers that already have a *Pool +
// *PoolRegistry pair. Most call sites can use registry.ActiveSlot()
// directly with their own logic.
func (p *Pool) NextSlot(r *PoolRegistry) string {
	if r == nil {
		return "A"
	}
	if r.ActiveSlot(p.ID) == "A" {
		return "B"
	}
	return "A"
}

// MemberByID returns the member with the given ID, or nil. O(1) via
// the memberByID index (rebuilt on load / add / remove).
func (p *Pool) MemberByID(id string) *PoolMember {
	if id == "" {
		return nil
	}
	if p.memberByID == nil {
		// Defensive: index was lost (e.g. zero-value Pool) - rebuild.
		p.rebuildMemberIndex()
	}
	return p.memberByID[id]
}

// ActiveMembers returns members whose Active flag is true. Today this
// is all of them; when the Pro-tier cap lands, the registry's
// EnforceActiveCap() will flip surplus Active flags to false.
func (p *Pool) ActiveMembers() []*PoolMember {
	out := make([]*PoolMember, 0, len(p.Members))
	for _, m := range p.Members {
		if m.Active {
			out = append(out, m)
		}
	}
	return out
}

// EligibleMembers returns active members that are not flagged
// unreachable AND match the RestrictRegions filter (if any).
//
// PURE READ: this method does NOT mutate state. The TTL-clear
// semantics that used to live here have moved to the state registry's
// SweepStaleUnreachable, which the caller invokes once before the
// pick. The all-unreachable reset is also a state-registry operation
// (ClearAllMembersUnreachable).
//
// Why the split: earlier versions mutated *PoolMember pointers from
// here, racing with concurrent markMemberUnreachable from the
// rotator. With state moved out and reads-only, EligibleMembers can
// be called from any goroutine without lock-discipline concerns.
//
// The state registry parameter is required - callers always have it
// because PoolRegistry holds the reference. Passing nil yields the
// "treat all members as reachable" behavior, which is used by tests
// that don't set up a state registry.
func (p *Pool) EligibleMembers(state *PoolStateRegistry) []*PoolMember {
	out := make([]*PoolMember, 0, len(p.Members))

	// Lazy TTL sweep: clear stale flags before filtering. State
	// registry handles its own locking; this returns count cleared
	// (used only for diagnostic logs, not control flow).
	if state != nil {
		state.SweepStaleUnreachable(p.ID, UnreachableTTL)
	}

	for _, m := range p.Members {
		if m == nil || !m.Active {
			continue
		}
		if state != nil && state.MemberState(p.ID, m.ID).Unreachable {
			continue
		}
		if len(p.RestrictRegions) > 0 && !containsString(p.RestrictRegions, m.Region) {
			continue
		}
		out = append(out, m)
	}

	if len(out) > 0 {
		return out
	}

	// All-unreachable reset path: clear every flag and return all
	// active region-matching members. The reset is a real state
	// mutation and persists.
	if state != nil {
		state.ClearAllMembersUnreachable(p.ID)
	}
	for _, m := range p.Members {
		if m == nil || !m.Active {
			continue
		}
		if len(p.RestrictRegions) > 0 && !containsString(p.RestrictRegions, m.Region) {
			continue
		}
		out = append(out, m)
	}
	return out
}

// Coverage groups eligible members by region for the Pool-Detail-View
// breakdown. Returns regions sorted by member count desc, then alpha.
func (p *Pool) Coverage() []RegionCoverage {
	byRegion := make(map[string]*RegionCoverage)
	countries := make(map[string]map[string]struct{})

	for _, m := range p.Members {
		if !m.Active {
			continue
		}
		r := m.Region
		if r == "" {
			r = "Other"
		}
		if _, ok := byRegion[r]; !ok {
			byRegion[r] = &RegionCoverage{Region: r}
			countries[r] = make(map[string]struct{})
		}
		byRegion[r].Servers++
		if m.Country != "" {
			countries[r][m.Country] = struct{}{}
		}
	}
	for r, c := range countries {
		byRegion[r].Countries = len(c)
	}

	out := make([]RegionCoverage, 0, len(byRegion))
	for _, v := range byRegion {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Servers != out[j].Servers {
			return out[i].Servers > out[j].Servers
		}
		return out[i].Region < out[j].Region
	})
	return out
}

// RegionCoverage is one row of the Pool-Detail-View coverage table.
type RegionCoverage struct {
	Region    string `json:"region"`
	Servers   int    `json:"servers"`
	Countries int    `json:"countries"`
}

// PoolRegistry manages all saved Pools, parallel to ConnectionRegistry.
// Pool and SavedConnection live in separate files so each can evolve
// independently and the UI just merges them in the connection picker.
//
// ActiveID persists which pool is currently the "selected" one across
// restarts - parallel to ConnectionRegistry.ActiveID for singles. The
// App reads this at NewApp() time to restore the user's last selection
// rather than booting into an empty Welcome state.
//
// Runtime state lives in PoolStateRegistry (pool_state.json). Methods
// on PoolRegistry that mutate runtime fields (MarkUnreachable,
// SetActiveMember, etc.) delegate to the state registry while keeping
// the lock-discipline simple: PoolRegistry locks for definition-data,
// PoolStateRegistry locks for runtime-data, and the two registries
// never lock-bridge each other.
type PoolRegistry struct {
	Pools    []*Pool `json:"pools"`
	ActiveID string  `json:"active_id,omitempty"`

	// poolByID indexes the Pools slice for O(1) Get. Rebuilt on
	// load / Create / Delete. NOT persisted.
	poolByID map[string]*Pool

	// state is the runtime-state companion. Pointer so it can be
	// shared with anyone who needs to read state directly without
	// going through registry methods (rare - prefer the helpers).
	state *PoolStateRegistry

	filePath string
	mu       sync.Mutex
}

// SetActiveID persists the currently-selected pool ID. Empty string
// clears the selection. Used by App.ActivatePool / ActivateConnection
// to keep the on-disk state in sync with App.activePoolID.
func (r *PoolRegistry) SetActiveID(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if id != "" && r.findLocked(id) == nil {
		return fmt.Errorf("pool: not found %s", id)
	}
	r.ActiveID = id
	return r.saveLocked()
}

// NewPoolRegistry creates a registry, loading from disk if available.
// state must be a constructed PoolStateRegistry; the registry uses it
// for all runtime-state reads/writes. Migration runs synchronously on
// first load: any legacy runtime fields embedded in pools.json are
// copied into the state registry, and the next pools.json save drops
// them.
func NewPoolRegistry(state *PoolStateRegistry) *PoolRegistry {
	r := &PoolRegistry{
		state:    state,
		filePath: filepath.Join(appDataDir(), "pools.json"),
		poolByID: make(map[string]*Pool),
	}
	r.load()
	r.migrateLegacyState()
	return r
}

// migrateLegacyState copies any legacy runtime fields from old
// pools.json into the state registry. Runs once at construction;
// subsequent saves of pools.json drop the legacy fields entirely.
//
// Idempotent: state registry's MigrateFromLegacy refuses to overwrite
// entries that already exist, so calling this twice (or running with
// a state.json that's already populated) doesn't reset anything.
func (r *PoolRegistry) migrateLegacyState() {
	if r.state == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	migratedAny := false
	for _, p := range r.Pools {
		if p.legacyActiveMemberID != "" {
			r.state.SetActiveMember(p.ID, p.legacyActiveMemberID)
			migratedAny = true
		}
		if p.legacyPendingMemberID != "" {
			r.state.SetPendingMember(p.ID, p.legacyPendingMemberID)
			migratedAny = true
		}
		if p.legacyActiveSlot != "" {
			r.state.SetActiveSlot(p.ID, p.legacyActiveSlot)
			migratedAny = true
		}
		// Member-level legacy state.
		hasMemberState := false
		for _, m := range p.Members {
			if m != nil && (m.legacyUnreachable || m.legacyLastError != "") {
				hasMemberState = true
				break
			}
		}
		if hasMemberState {
			r.state.MigrateFromLegacy(p.ID, p.Members)
			migratedAny = true
		}
		// Clear legacy fields so the next save excludes them.
		p.legacyActiveMemberID = ""
		p.legacyPendingMemberID = ""
		p.legacyActiveSlot = ""
		for _, m := range p.Members {
			if m != nil {
				m.legacyUnreachable = false
				m.legacyLastError = ""
				m.legacyLastUnreachable = time.Time{}
			}
		}
	}
	if migratedAny {
		// Persist the cleaned-up pools.json once so subsequent loads
		// have nothing legacy to migrate. saveLocked is fine here
		// because we already hold r.mu.
		if err := r.saveLocked(); err != nil {
			log.Printf("PoolRegistry: post-migration save failed: %v", err)
		}
	}
}

// List returns all pools (snapshot copy of slice header; pool pointers
// are shared - callers must not mutate Pool definition fields without
// coordinating, and runtime fields must be touched only via the state
// registry).
func (r *PoolRegistry) List() []*Pool {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*Pool, len(r.Pools))
	copy(out, r.Pools)
	return out
}

// Get returns a pool by ID, or nil. O(1).
func (r *PoolRegistry) Get(id string) *Pool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.findLocked(id)
}

func (r *PoolRegistry) findLocked(id string) *Pool {
	if r.poolByID == nil {
		return nil
	}
	return r.poolByID[id]
}

// rebuildPoolIndex re-indexes the Pools slice. Caller must hold r.mu.
func (r *PoolRegistry) rebuildPoolIndex() {
	if r.poolByID == nil {
		r.poolByID = make(map[string]*Pool, len(r.Pools))
	} else {
		for k := range r.poolByID {
			delete(r.poolByID, k)
		}
	}
	for _, p := range r.Pools {
		if p != nil && p.ID != "" {
			r.poolByID[p.ID] = p
		}
	}
}

// ============================================================================
// RUNTIME STATE HELPERS - delegate to PoolStateRegistry
// ============================================================================
// These keep callsites compact: app_pool.go can read pool.ActiveMember
// or call registry.MarkMemberUnreachable without juggling two registry
// references. All locking is owned by PoolStateRegistry (independent
// of PoolRegistry.mu).

// ActiveMemberID returns the runtime "currently active" member ID for
// a pool, or "" if none. Reads from state registry.
func (r *PoolRegistry) ActiveMemberID(poolID string) string {
	if r == nil || r.state == nil {
		return ""
	}
	return r.state.PoolState(poolID).ActiveMemberID
}

// PendingMemberID returns the pre-warmed next member ID, or "".
func (r *PoolRegistry) PendingMemberID(poolID string) string {
	if r == nil || r.state == nil {
		return ""
	}
	return r.state.PoolState(poolID).PendingMemberID
}

// ActiveSlot returns "A" or "B" or "" if no slot has been used yet.
func (r *PoolRegistry) ActiveSlot(poolID string) string {
	if r == nil || r.state == nil {
		return ""
	}
	return r.state.PoolState(poolID).ActiveSlot
}

// SetActiveMember persists the runtime active member.
func (r *PoolRegistry) SetActiveMember(poolID, memberID string) {
	if r != nil && r.state != nil {
		r.state.SetActiveMember(poolID, memberID)
	}
}

// SetPendingMember persists the pre-warmed next member.
func (r *PoolRegistry) SetPendingMember(poolID, memberID string) {
	if r != nil && r.state != nil {
		r.state.SetPendingMember(poolID, memberID)
	}
}

// SetActiveSlot persists which slot (A/B) is currently up.
func (r *PoolRegistry) SetActiveSlot(poolID, slot string) {
	if r != nil && r.state != nil {
		r.state.SetActiveSlot(poolID, slot)
	}
}

// MarkMemberUnreachable persists the unreachable flag + reason
// + timestamp for a single member.
func (r *PoolRegistry) MarkMemberUnreachable(poolID, memberID, reason string) {
	if r != nil && r.state != nil {
		r.state.MarkMemberUnreachable(poolID, memberID, reason)
	}
}

// ClearMemberUnreachable clears the flag for a single member.
func (r *PoolRegistry) ClearMemberUnreachable(poolID, memberID string) {
	if r != nil && r.state != nil {
		r.state.ClearMemberUnreachable(poolID, memberID)
	}
}

// ClearAllMembersUnreachable clears every member's flag in the pool.
// Returns count cleared.
func (r *PoolRegistry) ClearAllMembersUnreachable(poolID string) int {
	if r == nil || r.state == nil {
		return 0
	}
	return r.state.ClearAllMembersUnreachable(poolID)
}

// MemberStatus returns the runtime status block for a single member.
// Returns zero value if no failures have ever been recorded.
func (r *PoolRegistry) MemberStatus(poolID, memberID string) memberStateEntry {
	if r == nil || r.state == nil {
		return memberStateEntry{}
	}
	return r.state.MemberState(poolID, memberID)
}

// IsMemberUnreachable is a convenience for filter loops.
func (r *PoolRegistry) IsMemberUnreachable(poolID, memberID string) bool {
	return r.MemberStatus(poolID, memberID).Unreachable
}

// Create persists a new Pool. Caller is responsible for assembling
// Members via the import pipeline before calling.
func (r *PoolRegistry) Create(name string, policy PoolPolicy, members []*PoolMember) (*Pool, error) {
	if name == "" {
		return nil, fmt.Errorf("pool: name must not be empty")
	}
	if !validPolicy(policy) {
		return nil, fmt.Errorf("pool: invalid policy %q", policy)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	p := &Pool{
		ID:        uuid.New().String(),
		Name:      name,
		CreatedAt: time.Now(),
		Policy:    policy,
		Rotation:  DefaultRotation(),
		Members:   members,
	}
	p.rebuildMemberIndex()
	r.Pools = append(r.Pools, p)
	if r.poolByID == nil {
		r.poolByID = make(map[string]*Pool)
	}
	r.poolByID[p.ID] = p
	if err := r.saveLocked(); err != nil {
		// Roll back the in-memory append so the persisted state and
		// the in-memory state stay in sync.
		r.Pools = r.Pools[:len(r.Pools)-1]
		delete(r.poolByID, p.ID)
		return nil, err
	}
	return p, nil
}

// Update persists definition changes (name, policy, rotation,
// restrict-regions, country-override). Caller mutates the Pool
// pointer they got from Get/List directly, then calls Update to
// flush.
//
// IMPORTANT: this method is for DEFINITION-LEVEL changes only.
// Runtime fields (active member, pending member, slot, unreachable)
// must NOT be persisted via this path - they live in state.json and
// have their own helpers (SetActiveMember, MarkMemberUnreachable,
// etc.). Calling Update for a runtime change just wastes a 100KB+
// pools.json rewrite for no effect on the runtime state file.
//
// API contract: the supplied pointer must be the SAME pointer that
// Get/List returned. We validate by ID lookup, not pointer identity,
// so a detached pointer with a matching ID would silently no-op.
// The earlier behavior was the same; flagging it here for any
// future audit.
func (r *PoolRegistry) Update(p *Pool) error {
	if p == nil {
		return fmt.Errorf("pool: nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.findLocked(p.ID) == nil {
		return fmt.Errorf("pool: not found %s", p.ID)
	}
	return r.saveLocked()
}

// Delete removes a Pool by ID. The caller must have already
// disconnected if this Pool was the active connection - the registry
// has no awareness of connect state.
//
// Also clears ActiveID if it pointed at the deleted pool. Without
// this, pools.json carries a stale ActiveID across restart and the
// startup restore branch silently fails (Get returns nil for a
// dangling ID, neither pool nor MRU-single is auto-selected, user
// lands on the empty Welcome screen).
func (r *PoolRegistry) Delete(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, p := range r.Pools {
		if p.ID == id {
			r.Pools = append(r.Pools[:i], r.Pools[i+1:]...)
			delete(r.poolByID, id)
			if r.ActiveID == id {
				r.ActiveID = ""
			}
			// State for this pool is no longer needed - drop it so
			// pool_state.json doesn't accumulate orphan entries.
			if r.state != nil {
				r.state.DeletePool(id)
			}
			return r.saveLocked()
		}
	}
	return fmt.Errorf("pool: not found %s", id)
}

// DeleteMember removes a single member from a pool.
func (r *PoolRegistry) DeleteMember(poolID, memberID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.findLocked(poolID)
	if p == nil {
		return fmt.Errorf("pool: not found %s", poolID)
	}
	for i, m := range p.Members {
		if m.ID == memberID {
			p.Members = append(p.Members[:i], p.Members[i+1:]...)
			delete(p.memberByID, memberID)
			// Drop runtime state too (state-registry handles
			// active/pending bookkeeping if this was the active one).
			if r.state != nil {
				r.state.DeleteMember(poolID, memberID)
			}
			return r.saveLocked()
		}
	}
	return fmt.Errorf("pool: member not found %s", memberID)
}

// RenameMember updates a single member's display name.
func (r *PoolRegistry) RenameMember(poolID, memberID, newName string) error {
	if newName == "" {
		return fmt.Errorf("pool: member name must not be empty")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.findLocked(poolID)
	if p == nil {
		return fmt.Errorf("pool: not found %s", poolID)
	}
	m := p.MemberByID(memberID)
	if m == nil {
		return fmt.Errorf("pool: member not found %s", memberID)
	}
	m.Name = newName
	return r.saveLocked()
}

// SetActiveMemberValidated is a thin validation wrapper around the
// state-registry's SetActiveMember. Returns an error if the pool or
// member does not exist; otherwise updates state.json. Used by the
// UI's manual member-switch path; rotation hot-path goes directly
// through SetActiveMember (no validation needed because callers
// already have the *PoolMember in hand).
func (r *PoolRegistry) SetActiveMemberValidated(poolID, memberID string) error {
	r.mu.Lock()
	p := r.findLocked(poolID)
	if p == nil {
		r.mu.Unlock()
		return fmt.Errorf("pool: not found %s", poolID)
	}
	if memberID != "" && p.MemberByID(memberID) == nil {
		r.mu.Unlock()
		return fmt.Errorf("pool: member not found %s", memberID)
	}
	r.mu.Unlock()
	r.SetActiveMember(poolID, memberID)
	return nil
}

// load reads pools.json. Missing-file is not an error - it just means
// the user has no pools yet. Re-builds the byID index post-parse.
func (r *PoolRegistry) load() {
	data, err := os.ReadFile(r.filePath)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("pool: load %s: %v", r.filePath, err)
		}
		return
	}
	if err := json.Unmarshal(data, r); err != nil {
		log.Printf("pool: parse %s: %v - resetting", r.filePath, err)
		r.Pools = nil
	}
	r.rebuildPoolIndex()
}

// saveLocked persists the current state to disk. Caller must hold r.mu.
func (r *PoolRegistry) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(r.filePath), 0o755); err != nil {
		return fmt.Errorf("pool: mkdir: %w", err)
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("pool: marshal: %w", err)
	}
	tmp := r.filePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("pool: write: %w", err)
	}
	if err := os.Rename(tmp, r.filePath); err != nil {
		return fmt.Errorf("pool: rename: %w", err)
	}
	return nil
}

func validPolicy(p PoolPolicy) bool {
	switch p {
	case PolicyGeoNearest, PolicyRandom, PolicyRoundRobin:
		return true
	}
	return false
}

func containsString(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}
