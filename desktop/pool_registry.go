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
type PoolMember struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`         // derived from filename
	Config      *ProtocolConfig `json:"config"`       // reused from connection_registry
	Country     string          `json:"country"`      // ISO 3166-1 alpha-2, "" if unknown
	Region      string          `json:"region"`       // derived from country via geoip.Region
	Active      bool            `json:"active"`       // future Pro-tier cap; default true
	Unreachable bool            `json:"unreachable"`  // last connect attempt failed
	LastError   string          `json:"last_error,omitempty"`
}

// Pool is a virtual connection that wraps multiple PoolMembers and
// picks one of them at connect-time using its policy.
type Pool struct {
	ID              string       `json:"id"`
	Name            string       `json:"name"`
	CreatedAt       time.Time    `json:"created_at"`
	Policy          PoolPolicy   `json:"policy"`
	Rotation        PoolRotation `json:"rotation"`
	Members         []*PoolMember `json:"members"`
	CountryOverride string       `json:"country_override,omitempty"` // "" = auto-detect
	RestrictRegions []string     `json:"restrict_regions,omitempty"` // [] = no restriction
	ActiveMemberID  string       `json:"active_member_id,omitempty"` // most recently picked
	// ActiveSlot tracks which of two alternating tunnel-name slots
	// is currently up ("A" or "B"). Each rotation flips to the
	// opposite slot so the next member's config writes to a fresh
	// .conf file and installs to a fresh OS service entry, avoiding
	// the same-name-reuse race that caused tunnels to fail to come
	// back up on rotation. "" means "no slot yet, start with A".
	ActiveSlot string `json:"active_slot,omitempty"`
}

// NextSlot returns the slot to use for the NEXT connect (opposite of
// the currently-active slot). Empty current → start with "A".
func (p *Pool) NextSlot() string {
	if p.ActiveSlot == "A" {
		return "B"
	}
	return "A"
}

// MemberByID returns the member with the given ID, or nil.
func (p *Pool) MemberByID(id string) *PoolMember {
	for _, m := range p.Members {
		if m.ID == id {
			return m
		}
	}
	return nil
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
// unreachable AND match the RestrictRegions filter (if any). This is
// the set the policies pick from at connect-time.
func (p *Pool) EligibleMembers() []*PoolMember {
	out := make([]*PoolMember, 0, len(p.Members))
	for _, m := range p.Members {
		if !m.Active || m.Unreachable {
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
type PoolRegistry struct {
	Pools    []*Pool `json:"pools"`
	ActiveID string  `json:"active_id,omitempty"`
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
func NewPoolRegistry() *PoolRegistry {
	r := &PoolRegistry{
		filePath: filepath.Join(appDataDir(), "pools.json"),
	}
	r.load()
	return r
}

// List returns all pools (snapshot copy of slice header; pool pointers
// are shared - callers must not mutate without coordinating).
func (r *PoolRegistry) List() []*Pool {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*Pool, len(r.Pools))
	copy(out, r.Pools)
	return out
}

// Get returns a pool by ID, or nil.
func (r *PoolRegistry) Get(id string) *Pool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.findLocked(id)
}

func (r *PoolRegistry) findLocked(id string) *Pool {
	for _, p := range r.Pools {
		if p.ID == id {
			return p
		}
	}
	return nil
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
	r.Pools = append(r.Pools, p)
	if err := r.saveLocked(); err != nil {
		// Roll back the in-memory append so the persisted state and
		// the in-memory state stay in sync.
		r.Pools = r.Pools[:len(r.Pools)-1]
		return nil, err
	}
	return p, nil
}

// Update writes back changes to a Pool. Used by the Edit-Pool modal
// for name / policy / rotation / restrict-regions / country-override
// changes.
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
			if r.ActiveID == id {
				r.ActiveID = ""
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
			if p.ActiveMemberID == memberID {
				p.ActiveMemberID = ""
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

// SetActiveMember records the most recently picked member so the UI
// can show "Currently: Germany (de-fra-wg-002)" without re-running
// the policy on every poll.
func (r *PoolRegistry) SetActiveMember(poolID, memberID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.findLocked(poolID)
	if p == nil {
		return fmt.Errorf("pool: not found %s", poolID)
	}
	if memberID != "" && p.MemberByID(memberID) == nil {
		return fmt.Errorf("pool: member not found %s", memberID)
	}
	p.ActiveMemberID = memberID
	return r.saveLocked()
}

// load reads pools.json. Missing-file is not an error - it just means
// the user has no pools yet.
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
