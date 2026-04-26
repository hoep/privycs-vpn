package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/hoep/privycs/desktop/geoip"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// ============================================================================
// POOL WAILS METHODS
//
// All public methods on App are auto-exposed to the Vue frontend via
// Wails' bindings generator. The Pool surface is split out from app.go
// to keep the main file from sprawling - same pattern as
// app_connections.go and app_settings.go.
// ============================================================================

// PoolListItem is the wire-shape sent to the frontend for the picker.
// We do NOT send the full member list (potentially 600 entries) every
// poll - the picker only needs ID, name, member-count, policy, and
// active-member display. PoolDetailView fetches the full member list
// via GetPoolDetail.
type PoolListItem struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	Policy            string `json:"policy"`
	MemberCount       int    `json:"member_count"`
	ActiveMemberID    string `json:"active_member_id,omitempty"`
	ActiveMemberName  string `json:"active_member_name,omitempty"`
	ActiveMemberCC    string `json:"active_member_cc,omitempty"`
	IsActive          bool   `json:"is_active"` // is this pool the currently-activated one
}

// ListPools returns all pools as light-weight items for the picker.
func (a *App) ListPools() []PoolListItem {
	a.mu.RLock()
	activePoolID := a.activePoolID
	a.mu.RUnlock()

	pools := a.pools.List()
	out := make([]PoolListItem, 0, len(pools))
	for _, p := range pools {
		item := PoolListItem{
			ID:             p.ID,
			Name:           p.Name,
			Policy:         string(p.Policy),
			MemberCount:    len(p.Members),
			ActiveMemberID: p.ActiveMemberID,
			IsActive:       p.ID == activePoolID,
		}
		if m := p.MemberByID(p.ActiveMemberID); m != nil {
			item.ActiveMemberName = m.Name
			item.ActiveMemberCC = m.Country
		}
		out = append(out, item)
	}
	return out
}

// PoolDetail is the wire-shape for PoolDetailView.
type PoolDetail struct {
	Pool     *Pool            `json:"pool"`
	Coverage []RegionCoverage `json:"coverage"`
}

// GetPoolDetail returns the full pool with members + region coverage.
func (a *App) GetPoolDetail(id string) (*PoolDetail, error) {
	p := a.pools.Get(id)
	if p == nil {
		return nil, fmt.Errorf("pool not found: %s", id)
	}
	return &PoolDetail{
		Pool:     p,
		Coverage: p.Coverage(),
	}, nil
}

// CreatePoolFromUploads is the production import path called from the
// Vue frontend. The browser sandbox does not expose absolute filesystem
// paths via HTML File API or drag-drop, so the frontend has already
// read each file via FileReader and ships the bytes through Wails.
// Each upload's filename drives the protocol detection; .zip uploads
// are unpacked in-memory.
func (a *App) CreatePoolFromUploads(name string, policy string, uploads []PoolUpload) (*Pool, error) {
	if len(uploads) == 0 {
		return nil, fmt.Errorf("no files provided")
	}
	if !validPolicy(PoolPolicy(policy)) {
		return nil, fmt.Errorf("invalid policy %q", policy)
	}

	result, err := a.poolImporter.ImportFromUploads(uploads, func(p PoolImportProgress) {
		if a.ctx != nil {
			wailsRuntime.EventsEmit(a.ctx, "pool:import_progress", p)
		}
	})
	if err != nil {
		return nil, err
	}
	if len(result.Members) == 0 {
		return nil, fmt.Errorf("no valid configs found (skipped: %d)", len(result.Skipped))
	}

	pool, err := a.pools.Create(name, PoolPolicy(policy), result.Members)
	if err != nil {
		return nil, err
	}

	a.autoRestrictRoundRobinToHomeRegion(pool)

	log.Printf("Pool created: %s (%d members, %d skipped)", name, len(result.Members), len(result.Skipped))
	if a.ctx != nil {
		wailsRuntime.EventsEmit(a.ctx, "pool:created", map[string]interface{}{
			"pool_id":     pool.ID,
			"name":        pool.Name,
			"members":     len(result.Members),
			"skipped":     len(result.Skipped),
			"skipped_log": result.Skipped,
		})
	}
	return pool, nil
}

// CreatePoolFromPaths reads a list of file paths (each may be a .zip,
// .conf, .ovpn, or .sswan) and creates a Pool from them. Path-based
// import is for testing and CLI use only - the browser sandbox cannot
// pass real filesystem paths so production goes through
// CreatePoolFromUploads.
func (a *App) CreatePoolFromPaths(name string, policy string, paths []string) (*Pool, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("no files provided")
	}
	if !validPolicy(PoolPolicy(policy)) {
		return nil, fmt.Errorf("invalid policy %q", policy)
	}

	result, err := a.poolImporter.Import(paths, func(p PoolImportProgress) {
		if a.ctx != nil {
			wailsRuntime.EventsEmit(a.ctx, "pool:import_progress", p)
		}
	})
	if err != nil {
		return nil, err
	}
	if len(result.Members) == 0 {
		return nil, fmt.Errorf("no valid configs found (skipped: %d)", len(result.Skipped))
	}

	pool, err := a.pools.Create(name, PoolPolicy(policy), result.Members)
	if err != nil {
		return nil, err
	}

	a.autoRestrictRoundRobinToHomeRegion(pool)

	log.Printf("Pool created: %s (%d members, %d skipped)", name, len(result.Members), len(result.Skipped))
	if a.ctx != nil {
		wailsRuntime.EventsEmit(a.ctx, "pool:created", map[string]interface{}{
			"pool_id":     pool.ID,
			"name":        pool.Name,
			"members":     len(result.Members),
			"skipped":     len(result.Skipped),
			"skipped_log": result.Skipped,
		})
	}
	return pool, nil
}

// UpdatePool persists changes from the Edit-Pool modal: name, policy,
// rotation params, restrict-regions, country-override.
func (a *App) UpdatePool(id string, patch UpdatePoolRequest) (*Pool, error) {
	p := a.pools.Get(id)
	if p == nil {
		return nil, fmt.Errorf("pool not found: %s", id)
	}

	if patch.Name != "" {
		p.Name = patch.Name
	}
	if patch.Policy != "" {
		if !validPolicy(PoolPolicy(patch.Policy)) {
			return nil, fmt.Errorf("invalid policy %q", patch.Policy)
		}
		p.Policy = PoolPolicy(patch.Policy)
	}
	if patch.Rotation != nil {
		p.Rotation = *patch.Rotation
	}
	if patch.RestrictRegions != nil {
		p.RestrictRegions = *patch.RestrictRegions
	}
	if patch.CountryOverride != nil {
		p.CountryOverride = *patch.CountryOverride
	}

	if err := a.pools.Update(p); err != nil {
		return nil, err
	}

	// If this pool is the active one, push the updated rotation
	// settings into the rotator. Non-RR policies are no-ops there.
	a.mu.RLock()
	isActive := p.ID == a.activePoolID
	a.mu.RUnlock()
	if isActive {
		a.poolRotator.SetActivePool(p)
	}

	return p, nil
}

// UpdatePoolRequest carries the partial-update fields from the
// frontend. Pointer fields disambiguate "not in patch" from
// "explicitly cleared" (e.g. setting CountryOverride back to "").
type UpdatePoolRequest struct {
	Name            string        `json:"name"`
	Policy          string        `json:"policy"`
	Rotation        *PoolRotation `json:"rotation,omitempty"`
	RestrictRegions *[]string     `json:"restrict_regions,omitempty"`
	CountryOverride *string       `json:"country_override,omitempty"`
}

// DeletePool removes a pool and clears any active-pool reference. If
// the deleted pool was the active connection, the caller should also
// disconnect first - the registry has no awareness of connect state.
func (a *App) DeletePool(id string) error {
	a.mu.Lock()
	if a.activePoolID == id {
		a.activePoolID = ""
		a.poolRotator.SetActivePool(nil)
	}
	a.mu.Unlock()
	return a.pools.Delete(id)
}

// DeletePoolMember removes a single member from a pool.
func (a *App) DeletePoolMember(poolID, memberID string) error {
	return a.pools.DeleteMember(poolID, memberID)
}

// RenamePoolMember updates a single member's display name.
func (a *App) RenamePoolMember(poolID, memberID, newName string) error {
	return a.pools.RenameMember(poolID, memberID, newName)
}

// ActivePoolID returns the currently-activated pool's ID, "" if none.
func (a *App) ActivePoolID() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.activePoolID
}

// ActivatePool marks a pool as the active "virtual connection".
// Subsequent Connect() calls will run the pool's policy to pick a
// member. Calling with empty id deactivates any active pool.
//
// We do NOT fire Connect or Disconnect from here - activation is just
// "select this in the picker". The user still drives connect via the
// main button.
//
// The selection is persisted to pools.json via SetActiveID so it
// survives app restart - users do not want to re-pick their pool
// every cold-start.
func (a *App) ActivatePool(id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if id == "" {
		a.activePoolID = ""
		a.poolRotator.SetActivePool(nil)
		_ = a.pools.SetActiveID("")
		return nil
	}

	p := a.pools.Get(id)
	if p == nil {
		return fmt.Errorf("pool not found: %s", id)
	}
	a.activePoolID = id

	// Activating a pool also clears the active single-connection
	// selection so the picker shows clean state. ConnectionRegistry
	// keeps the saved data.
	a.connections.SetActive("")

	a.poolRotator.SetActivePool(p)
	_ = a.pools.SetActiveID(id)
	return nil
}

// SwitchPoolMember manually picks a specific member as the active one
// and (if the VPN is currently up) reconnects to it. Used by the
// Connect-Screen's [⟳ Switch member] button.
func (a *App) SwitchPoolMember(memberID string) error {
	a.mu.Lock()
	poolID := a.activePoolID
	wasConnected := a.connected
	a.mu.Unlock()

	if poolID == "" {
		return fmt.Errorf("no active pool")
	}
	pool := a.pools.Get(poolID)
	if pool == nil {
		return fmt.Errorf("active pool gone: %s", poolID)
	}
	member := pool.MemberByID(memberID)
	if member == nil {
		return fmt.Errorf("member not in pool: %s", memberID)
	}

	if err := a.pools.SetActiveMember(poolID, memberID); err != nil {
		return err
	}

	if wasConnected {
		// Tear down current and reconnect with the new member's config.
		// We reuse the existing connect path via importPoolMemberAsActive.
		a.mu.Lock()
		_ = a.disconnectInternal()
		a.mu.Unlock()
		if err := a.connectToPoolMember(member); err != nil {
			return fmt.Errorf("reconnect to %s: %w", member.Name, err)
		}
	}
	return nil
}

// connectToPoolMember stages the picked member's config in the
// matching protocol handler and calls Connect("") to drive the
// existing connect path. We do NOT route through ConnectionRegistry -
// pool members are not "saved connections" the user picks individually
// in the picker, they are pool-internal entries.
//
// Tunnel-name uses a deterministic "pool-<8 chars of memberID>"
// pattern so a re-pick of the same member reuses the same interface
// name (avoids leftover wg0 / privycs-vpn-X interfaces piling up).
func (a *App) connectToPoolMember(m *PoolMember) error {
	if m == nil || m.Config == nil {
		return fmt.Errorf("invalid member or config")
	}

	a.mu.Lock()
	proto, ok := a.protocols[m.Config.Protocol]
	if !ok {
		a.mu.Unlock()
		return fmt.Errorf("protocol %s not registered", m.Config.Protocol)
	}

	tunnelName := "pool-" + shortID(m.ID)
	setTunnelName(proto, tunnelName)

	if err := proto.Configure([]byte(m.Config.ConfigContent)); err != nil {
		a.mu.Unlock()
		return fmt.Errorf("invalid pool-member config: %w", err)
	}

	a.activeProtocol = m.Config.Protocol
	a.settings.ActiveProtocol = m.Config.Protocol
	a.mu.Unlock()

	if _, err := a.Connect(""); err != nil {
		return err
	}
	return nil
}

// shortID truncates a UUID to its first 8 chars for use in interface
// names. UUIDs are random enough that 8 hex chars give ~4 billion
// values - collision-safe within a single user's pool.
func shortID(id string) string {
	if len(id) >= 8 {
		return id[:8]
	}
	return id
}

// autoRestrictRoundRobinToHomeRegion sets RestrictRegions to the
// user's home region on a freshly-created Round-Robin pool. Default
// behaviour matches what most users expect: rotate through MY pool's
// servers near where I am, not pinball through every continent.
//
// User-country comes from the SelfIP detector - same DoH-trace path
// the user suggested with curl ipinfo.io, but using cloudflare-trace
// / icanhazip / Mullvad as the well-known endpoints (no third-party
// SaaS analytics, single GET, no auth, no cookies). Cached for 1h
// and invalidated on network roam.
//
// Falls through silently if:
//   - policy is not Round-Robin (Geo-Nearest already handles regional
//     preference, Random has no region semantics)
//   - SelfIP detection fails (no internet, captive portal, all
//     fallback endpoints down)
//   - resolved country maps to "Other" (private IP, unmapped range)
//
// User can clear the auto-restriction in Coverage card → Restrict to
// region → uncheck their home region → Apply.
func (a *App) autoRestrictRoundRobinToHomeRegion(pool *Pool) {
	if pool == nil || pool.Policy != PolicyRoundRobin || a.selfIPDetector == nil {
		return
	}
	userCountry := a.SelfIPCountry()
	if userCountry == "" {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		userCountry = a.selfIPDetector.CountryFor(ctx)
		cancel()
	}
	if userCountry == "" {
		log.Printf("Pool: no user-country detected, skipping auto-restrict")
		return
	}
	homeRegion := geoip.Region(userCountry)
	if homeRegion == "" || homeRegion == "Other" {
		return
	}
	pool.RestrictRegions = []string{homeRegion}
	if err := a.pools.Update(pool); err != nil {
		log.Printf("Pool: auto-restrict to %s failed: %v", homeRegion, err)
		return
	}
	log.Printf("Pool: auto-restricted Round-Robin to home region %s (user country %s)",
		homeRegion, userCountry)
}

// mostRecentlyUsedConnection returns the connection with the newest
// LastConnected timestamp. Used at startup to auto-select the most
// recent single-connection when neither a pool nor a single is
// explicitly selected. Returns nil for an empty list.
//
// A connection that has never been connected yet (LastConnected is
// zero) loses to any connection that has, so brand-new imports do
// not jump above proven-good selections. If all connections have
// zero timestamps, the first one in the list wins.
func mostRecentlyUsedConnection(connections []*SavedConnection) *SavedConnection {
	if len(connections) == 0 {
		return nil
	}
	best := connections[0]
	for _, c := range connections[1:] {
		if c.LastConnected.After(best.LastConnected) {
			best = c
		}
	}
	return best
}

// PickAndConnectActivePool runs the active pool's policy and connects
// to the selected member. Called by Connect() when activePoolID is
// set, and by the rotator's onRotate callback.
func (a *App) PickAndConnectActivePool() error {
	a.mu.RLock()
	poolID := a.activePoolID
	a.mu.RUnlock()
	if poolID == "" {
		return fmt.Errorf("no active pool")
	}
	pool := a.pools.Get(poolID)
	if pool == nil {
		return fmt.Errorf("active pool gone: %s", poolID)
	}

	// Resolve the user's country - fast path: cached. Slow path: probe
	// via DoH with 5s budget. Geo-Nearest tolerates "" gracefully.
	userCountry := ""
	if cached := a.selfIPDetector.Cached(); cached != nil {
		userCountry = cached.Country
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		userCountry = a.selfIPDetector.CountryFor(ctx)
	}

	member := PickMember(pool, userCountry, pool.ActiveMemberID)
	if member == nil {
		return fmt.Errorf("pool %s: no eligible member to pick", pool.Name)
	}

	if err := a.pools.SetActiveMember(pool.ID, member.ID); err != nil {
		log.Printf("Pool: SetActiveMember failed: %v", err)
	}

	log.Printf("Pool %s policy=%s picked member %s (%s, %s)",
		pool.Name, pool.Policy, member.Name, member.Country, member.Region)
	return a.connectToPoolMember(member)
}

// PoolRotatorStatus exposes the rotator's view to the frontend's
// "next rotation in 4:32" indicator. Returned regardless of whether
// a pool is active so the frontend can poll unconditionally.
func (a *App) PoolRotatorStatus() RotatorStatus {
	if a.poolRotator == nil {
		return RotatorStatus{}
	}
	return a.poolRotator.Status()
}

// SelfIPCountry exposes the cached self-IP country to the frontend's
// "Country override: Auto (currently AT)" display. Returns "" if no
// detection has happened yet or all probes failed.
func (a *App) SelfIPCountry() string {
	if a.selfIPDetector == nil {
		return ""
	}
	cached := a.selfIPDetector.Cached()
	if cached == nil {
		return ""
	}
	return cached.Country
}
