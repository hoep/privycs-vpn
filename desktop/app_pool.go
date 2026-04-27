package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
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
	// Pre-warmed next member (set by the rotator's pre-warm step
	// 60 s before rotation, cleared after the rotation actually
	// fires). UI shows "Next: <name>" when these are set.
	PendingMemberID   string `json:"pending_member_id,omitempty"`
	PendingMemberName string `json:"pending_member_name,omitempty"`
	PendingMemberCC   string `json:"pending_member_cc,omitempty"`
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
		if p.PendingMemberID != "" {
			if pm := p.MemberByID(p.PendingMemberID); pm != nil {
				item.PendingMemberID = pm.ID
				item.PendingMemberName = pm.Name
				item.PendingMemberCC = pm.Country
			}
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

// ResetPoolUnreachable clears the Unreachable flag and LastError on
// every member of the pool, with one persisted write. Manual override
// for the user when they know the network came back ("just reconnected
// to home WiFi") and do not want to wait for the 30-min TTL.
//
// Returns the count of members whose flag was actually cleared so the
// frontend can show a meaningful confirmation ("Reset 12 unreachable
// members").
func (a *App) ResetPoolUnreachable(poolID string) (int, error) {
	if poolID == "" {
		return 0, fmt.Errorf("pool id required")
	}
	pool := a.pools.Get(poolID)
	if pool == nil {
		return 0, fmt.Errorf("pool not found: %s", poolID)
	}
	cleared := 0
	for _, m := range pool.Members {
		if m.Unreachable {
			m.Unreachable = false
			m.LastError = ""
			m.LastUnreachable = time.Time{}
			cleared++
		}
	}
	if cleared > 0 {
		if err := a.pools.Update(pool); err != nil {
			return cleared, fmt.Errorf("persist failed: %w", err)
		}
	}
	return cleared, nil
}

// ActivePoolID returns the currently-activated pool's ID, "" if none.
func (a *App) ActivePoolID() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.activePoolID
}

// emitLoading is a small helper used by the startup goroutines to
// surface progress on long-running background tasks (SelfIP DoH probe
// chain, MMDB backfill) as app:loading events. ConnectionView shows
// these in a transient toast - users get visible feedback during the
// few-seconds window where the screen would otherwise look frozen.
//
// stage values:
//   - "geo-detect"        SelfIP probe started
//   - "geo-detect-done"   SelfIP probe finished (extra carries CC or "")
//   - "backfill"          MMDB backfill in progress (current/total set)
//   - "backfill-done"     MMDB backfill finished
//
// Safe to call before a.ctx is set (silent no-op).
func (a *App) emitLoading(stage string, current, total int, extra string) {
	if a.ctx == nil {
		return
	}
	wailsRuntime.EventsEmit(a.ctx, "app:loading", map[string]any{
		"stage":   stage,
		"current": current,
		"total":   total,
		"extra":   extra,
	})
}

// BootstrapStateInfo is the synchronous startup snapshot the frontend
// reads to render the connection screen on its very first frame -
// before any of the heavier refresh() IPC calls have round-tripped.
//
// Carrying the active pool's display fields here means the pool card
// is visible the instant ConnectionView mounts: name, policy, member
// count, and the active member's "Currently:" line all populate from
// the same in-memory state that ListPools would return - just shaped
// for the single pool we actually need at boot.
type BootstrapStateInfo struct {
	ActivePoolID     string `json:"active_pool_id"`
	ActivePool       *PoolListItem `json:"active_pool,omitempty"`
	HasActiveSingle  bool   `json:"has_active_single"`
	ActiveSingleName string `json:"active_single_name,omitempty"`
}

// BootstrapState returns the minimum data the connection screen needs
// to render meaningfully on the first paint. Cheap (in-memory only,
// no DNS / no MMDB / no network) so it's safe to call synchronously
// during ConnectionView setup.
func (a *App) BootstrapState() *BootstrapStateInfo {
	a.mu.RLock()
	activePoolID := a.activePoolID
	a.mu.RUnlock()

	info := &BootstrapStateInfo{ActivePoolID: activePoolID}

	if activePoolID != "" && a.pools != nil {
		if p := a.pools.Get(activePoolID); p != nil {
			item := &PoolListItem{
				ID:             p.ID,
				Name:           p.Name,
				Policy:         string(p.Policy),
				MemberCount:    len(p.Members),
				ActiveMemberID: p.ActiveMemberID,
				IsActive:       true,
			}
			if m := p.MemberByID(p.ActiveMemberID); m != nil {
				item.ActiveMemberName = m.Name
				item.ActiveMemberCC = m.Country
			}
			info.ActivePool = item
		}
	}

	if c := a.connections.Active(); c != nil {
		info.HasActiveSingle = true
		info.ActiveSingleName = c.Name
	}

	return info
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

	if id == "" {
		a.activePoolID = ""
		a.poolRotator.SetActivePool(nil)
		_ = a.pools.SetActiveID("")
		a.mu.Unlock()
		return nil
	}

	p := a.pools.Get(id)
	if p == nil {
		a.mu.Unlock()
		return fmt.Errorf("pool not found: %s", id)
	}
	a.activePoolID = id

	// Activating a pool also clears the active single-connection
	// selection so the picker shows clean state. ConnectionRegistry
	// keeps the saved data.
	a.connections.SetActive("")

	a.poolRotator.SetActivePool(p)
	if err := a.pools.SetActiveID(id); err != nil {
		// Persist failure means the next restart will not see this
		// pool as the active one. Loud log so the symptom (Welcome
		// screen instead of restored pool) is diagnosable.
		log.Printf("Pool: SetActiveID(%s) failed: %v - active state will not survive restart", id, err)
	} else {
		log.Printf("Pool: persisted active pool %s", id)
	}
	needsAutoRestrict := len(p.RestrictRegions) == 0

	// Release the write lock BEFORE running auto-restrict. The
	// auto-restrict path may probe SelfIP via DoH which can take up
	// to 3s on a cold cache - holding a.mu through that window
	// blocks the status emitter and every Wails RLock call (ListPools,
	// ActivePoolID, PoolRotatorStatus, SelfIPCountry...) and the user
	// perceives the entire app as frozen during pool activation.
	a.mu.Unlock()

	if needsAutoRestrict {
		// Run in a goroutine so the user-facing ActivatePool call
		// returns immediately. The restriction takes effect on the
		// next PickAndConnect; in the unlikely race where Connect
		// fires before this finishes, PickAndConnect's just-in-time
		// guard sets it inline.
		go a.autoRestrictRoundRobinToHomeRegion(p)
	}
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
// Tunnel-name is STABLE per pool. Pool semantics guarantee only one
// member is active at a time, so reusing the same tunnel name across
// rotations keeps OS state minimal: one .conf file overwritten,
// one WireGuardTunnel$ service stop/restart cycled, one wintun
// adapter. Without this we accumulated one .conf + one service per
// member ever connected (up to 600 leftover entries on a Mullvad
// pool with full rotation history).
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

	// Alternating slot pattern: each connect uses the OPPOSITE slot
	// of whatever's currently active. Per the user's suggestion -
	// "du musst dir halt merken welcher conf welche ist". Reasons:
	//
	//   - The .conf file at the OS WireGuard config dir for slot A
	//     stays untouched while slot B's tunnel is up. No race
	//     between writing config and the service reading it.
	//   - The service entry for slot A stays cleanly stopped (or
	//     uninstalled) while slot B is up. Re-installing slot B's
	//     fresh service is conflict-free; reusing the same name
	//     after a Down sometimes hit "service already exists" race.
	//   - Net effect: rotation just becomes "down current slot, up
	//     opposite slot". Each step touches a different name on disk
	//     and in the service registry.
	suffix := shortID(a.activePoolID)
	if suffix == "" {
		suffix = "active"
	}
	slot := "A"
	if a.pools != nil {
		if pool := a.pools.Get(a.activePoolID); pool != nil {
			slot = pool.NextSlot()
		}
	}
	tunnelName := "privycs-pool-" + suffix + "-" + slot
	setTunnelName(proto, tunnelName)

	// Was this slot's .conf pre-written 60 s ago? If yes, adopt it
	// (read-only) instead of writing again with identical content.
	preWritten := false
	if a.pools != nil && m.Config.Protocol == "wireguard" {
		if pool := a.pools.Get(a.activePoolID); pool != nil && pool.PendingMemberID == m.ID {
			confPath := filepath.Join(appDataDir(), tunnelName+".conf")
			if _, err := os.Stat(confPath); err == nil {
				preWritten = true
			}
		}
	}

	if preWritten {
		if wg, ok := proto.(*WireGuardProtocol); ok {
			if err := wg.AdoptExistingConfig(); err != nil {
				// Fallback: pre-written file vanished or unreadable.
				if err := proto.Configure([]byte(m.Config.ConfigContent)); err != nil {
					a.mu.Unlock()
					return fmt.Errorf("invalid pool-member config: %w", err)
				}
			}
		} else {
			if err := proto.Configure([]byte(m.Config.ConfigContent)); err != nil {
				a.mu.Unlock()
				return fmt.Errorf("invalid pool-member config: %w", err)
			}
		}
	} else {
		if err := proto.Configure([]byte(m.Config.ConfigContent)); err != nil {
			a.mu.Unlock()
			return fmt.Errorf("invalid pool-member config: %w", err)
		}
	}

	a.activeProtocol = m.Config.Protocol
	a.settings.ActiveProtocol = m.Config.Protocol
	a.mu.Unlock()

	if _, err := a.Connect(""); err != nil {
		return err
	}

	// Persist the new slot AFTER successful connect. If Up failed
	// the pool's ActiveSlot stays at the previous value so a retry
	// targets the same slot (no orphan slot-flip from a half-failed
	// connect).
	if a.pools != nil {
		if pool := a.pools.Get(a.activePoolID); pool != nil {
			pool.ActiveSlot = slot
			if err := a.pools.Update(pool); err != nil {
				log.Printf("Pool: persist ActiveSlot=%s failed: %v", slot, err)
			}
		}
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
// user's home region on any pool that does not yet have an
// explicit restriction. Restrict-to-home is the default for all
// policies because:
//
//   - Round-Robin without restriction pinballs across continents
//     (HK → IL → US for an AT user) which is rarely what's wanted
//   - Random without restriction can pick any server globally,
//     same problem
//   - Geo-Nearest's tier-3 random fallback also benefits when
//     "any" actually means "any in my region"
//
// Power users who explicitly want global rotation/picking clear
// the home region in Coverage card → Restrict to region → uncheck
// → Apply.
//
// User-country comes from the SelfIP detector via the DoH-trace
// chain (cloudflare-trace, icanhazip, Mullvad) - same approach the
// user suggested with curl ipinfo.io but using well-known
// privacy-respecting endpoints, single GET, no analytics. Cached
// for 1h, invalidated on network roam.
//
// Falls through silently if:
//   - SelfIP detection fails (no internet, captive portal)
//   - resolved country maps to "Other" (private IP, unmapped range)
//
// (Despite the legacy name, this function applies to all policies.)
func (a *App) autoRestrictRoundRobinToHomeRegion(pool *Pool) {
	if pool == nil || a.selfIPDetector == nil {
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

// resolveServerCountry returns the ISO 3166-1 alpha-2 country code
// for the given server address (IP or hostname). Three-tier lookup:
//
//  1. If a pool is active, walk the active member to read the
//     pre-resolved country - cheapest, no network or MMDB call.
//  2. If the server address parses as a literal IP, run it
//     through the MMDB directly. Fast (microseconds).
//  3. Hostname: DNS resolve once then MMDB. Slower but rare since
//     most VPN configs carry literal IPs.
//
// Returns "" on any failure - the frontend renders without a flag
// in that case rather than blocking on a slow lookup.
func (a *App) resolveServerCountry(serverAddr string) string {
	if serverAddr == "" || a.selfIPDetector == nil {
		return ""
	}

	// Tier 1: active pool member already carries country.
	a.mu.RLock()
	activePoolID := a.activePoolID
	a.mu.RUnlock()
	if activePoolID != "" && a.pools != nil {
		if pool := a.pools.Get(activePoolID); pool != nil && pool.ActiveMemberID != "" {
			if m := pool.MemberByID(pool.ActiveMemberID); m != nil && m.Country != "" {
				return m.Country
			}
		}
	}

	// Tier 2 + 3: split host:port if present, then resolve via MMDB.
	host := stripPortIfPresent(serverAddr)
	geoR, _ := geoip.Default()
	if geoR == nil {
		return ""
	}
	ip := resolveHostToIP(host)
	if ip == nil {
		return ""
	}
	cc, _ := geoR.CountryCode(ip)
	return cc
}

// resolveServerCity extracts a best-effort city label.
//
// For active pools, ActivatePool clears the singles' active id, so
// connName is empty - we must read the city from the pool's active
// member name (which IS the Mullvad-style hostname like
// "it-mil-wg-001"). Tier 1 covers that.
//
// For singles, connName is the saved-connection name. That can be
// anything the user typed (or the file basename). Tier 2 tries the
// same hostname pattern parse for users who imported provider
// configs as singles. Returns "" if nothing matches.
func (a *App) resolveServerCity(connName string) string {
	// Tier 1: active pool member name.
	a.mu.RLock()
	activePoolID := a.activePoolID
	a.mu.RUnlock()
	if activePoolID != "" && a.pools != nil {
		if pool := a.pools.Get(activePoolID); pool != nil && pool.ActiveMemberID != "" {
			if m := pool.MemberByID(pool.ActiveMemberID); m != nil && m.Name != "" {
				if city := cityFromHostnamePattern(m.Name); city != "" {
					return city
				}
			}
		}
	}

	// Tier 2: single connection's name.
	return cityFromHostnamePattern(connName)
}

// cityFromHostnamePattern parses a "<country>-<city>-<protocol>-N"
// hostname like "de-fra-wg-002" and returns the full city name from
// the lookup table. Returns "" if the input is too short or the
// city code is unknown.
func cityFromHostnamePattern(name string) string {
	if name == "" {
		return ""
	}
	parts := strings.Split(name, "-")
	if len(parts) < 2 {
		return ""
	}
	if cityFull, ok := cityCodeToName[strings.ToLower(parts[1])]; ok {
		return cityFull
	}
	return ""
}

// cityCodeToName maps the 3-letter city codes commonly used in
// Mullvad/IVPN/Proton hostnames to full English names. Inline rather
// than a separate file because the list is small and read-mostly.
var cityCodeToName = map[string]string{
	"vie": "Vienna",
	"fra": "Frankfurt",
	"ber": "Berlin",
	"muc": "Munich",
	"dus": "Düsseldorf",
	"ham": "Hamburg",
	"zrh": "Zurich",
	"gva": "Geneva",
	"par": "Paris",
	"mrs": "Marseille",
	"lon": "London",
	"mnc": "Manchester",
	"glw": "Glasgow",
	"mad": "Madrid",
	"bcn": "Barcelona",
	"mil": "Milan",
	"rom": "Rome",
	"ams": "Amsterdam",
	"bru": "Brussels",
	"sto": "Stockholm",
	"got": "Gothenburg",
	"mma": "Malmö",
	"osl": "Oslo",
	"cph": "Copenhagen",
	"hel": "Helsinki",
	"prg": "Prague",
	"war": "Warsaw",
	"buh": "Bucharest",
	"sof": "Sofia",
	"bud": "Budapest",
	"ath": "Athens",
	"lis": "Lisbon",
	"dub": "Dublin",
	"tll": "Tallinn",
	"rix": "Riga",
	"vno": "Vilnius",
	"beg": "Belgrade",
	"zag": "Zagreb",
	"lju": "Ljubljana",
	"bts": "Bratislava",
	"kiv": "Kyiv",
	"nyc": "New York",
	"chi": "Chicago",
	"lax": "Los Angeles",
	"sea": "Seattle",
	"sjc": "San Jose",
	"mia": "Miami",
	"dal": "Dallas",
	"den": "Denver",
	"atl": "Atlanta",
	"phx": "Phoenix",
	"bos": "Boston",
	"iad": "Washington",
	"slc": "Salt Lake City",
	"yyz": "Toronto",
	"yvr": "Vancouver",
	"ymq": "Montreal",
	"mex": "Mexico City",
	"sao": "São Paulo",
	"gru": "São Paulo",
	"eze": "Buenos Aires",
	"scl": "Santiago",
	"bog": "Bogotá",
	"lim": "Lima",
	"tok": "Tokyo",
	"nrt": "Tokyo",
	"osa": "Osaka",
	"sel": "Seoul",
	"icn": "Seoul",
	"hkg": "Hong Kong",
	"tpe": "Taipei",
	"sin": "Singapore",
	"kul": "Kuala Lumpur",
	"bkk": "Bangkok",
	"jkt": "Jakarta",
	"mnl": "Manila",
	"hnd": "Hanoi",
	"sgn": "Ho Chi Minh City",
	"bom": "Mumbai",
	"del": "Delhi",
	"blr": "Bangalore",
	"mad_eu": "Madrid", // dedup safety
	"syd": "Sydney",
	"mel": "Melbourne",
	"per": "Perth",
	"akl": "Auckland",
	"jnb": "Johannesburg",
	"cpt": "Cape Town",
	"lag": "Lagos",
	"nai": "Nairobi",
	"cai": "Cairo",
	"dxb": "Dubai",
	"tlv": "Tel Aviv",
	"ist": "Istanbul",
}

// backfillPoolCountries iterates every saved pool and re-resolves
// countries for members whose Country field is empty. Pools imported
// before v0.9.11.9 (MMDB schema mismatch) ended up with all members
// at country="" - which made flag rendering, Geo-Nearest, and the
// pool-detail country lookup all fall through silently. This
// background pass repairs them in place so the user does not have
// to delete + reimport.
//
// Runs in a goroutine off app startup. Each member resolves with the
// already-loaded GeoIP reader; total wall time for 600 members is
// bounded by the DNS-resolution worker pool.
func (a *App) backfillPoolCountries() {
	if a.pools == nil {
		return
	}
	geoR, err := geoip.Default()
	if err != nil || geoR == nil {
		return
	}
	pools := a.pools.List()

	// Count how many members actually need backfill so the toast
	// progress is meaningful (no progress for already-correct pools).
	var pending int
	for _, pool := range pools {
		for _, m := range pool.Members {
			if m.Country == "" && m.Config != nil && m.Config.ServerAddress != "" {
				pending++
			}
		}
	}
	if pending == 0 {
		return
	}
	a.emitLoading("backfill", 0, pending, "")

	processed := 0
	// Throttle progress events: every 10 members or 250ms, whichever
	// fires first. Emitting on every member would saturate the
	// frontend with hundreds of events for a 600-member pool.
	lastEmit := time.Now()
	for _, pool := range pools {
		changed := false
		for _, m := range pool.Members {
			if m.Country != "" || m.Config == nil || m.Config.ServerAddress == "" {
				continue
			}
			ip := resolveHostToIP(stripPortIfPresent(m.Config.ServerAddress))
			processed++
			if ip == nil {
				continue
			}
			cc, _ := geoR.CountryCode(ip)
			if cc == "" {
				continue
			}
			m.Country = cc
			m.Region = geoip.Region(cc)
			changed = true
			if processed%10 == 0 || time.Since(lastEmit) > 250*time.Millisecond {
				a.emitLoading("backfill", processed, pending, "")
				lastEmit = time.Now()
			}
		}
		if changed {
			if err := a.pools.Update(pool); err == nil {
				log.Printf("Pool %s: backfilled country data for previously-empty members", pool.Name)
			}
		}
	}
	a.emitLoading("backfill-done", pending, pending, "")
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

	// Resolve the user's country from the SelfIP cache. We do NOT
	// probe synchronously here even on a cold cache - the DoH chain
	// can take 3-8s and Connect needs to be snappy. Startup pre-warms
	// the cache; ActivatePool's background goroutine refreshes it.
	// In the unlikely race where neither has populated yet, Geo-
	// Nearest tolerates "" gracefully (degrades to Random within the
	// pool's RestrictRegions filter, if any).
	userCountry := ""
	if cached := a.selfIPDetector.Cached(); cached != nil {
		userCountry = cached.Country
	}

	// Last-line backfill: if user has a known country and the pool
	// still has no restriction, apply the home region in memory. We
	// do NOT write pools.json here - the SetActiveMember call below
	// will save the pool with both the new RestrictRegions AND the
	// new ActiveMemberID in a single ~1MB write instead of two.
	if userCountry != "" && len(pool.RestrictRegions) == 0 {
		homeRegion := geoip.Region(userCountry)
		if homeRegion != "" && homeRegion != "Other" {
			pool.RestrictRegions = []string{homeRegion}
			log.Printf("Pool: just-in-time restrict to %s (will persist with active-member set)", homeRegion)
		}
	}

	// CRITICAL: tear down the existing tunnel ONCE before the retry
	// loop starts. Two reasons:
	//
	//   1. Connect short-circuits on "tunnel already running" so a
	//      Configure-then-Connect without disconnect does not actually
	//      change the routing - the rotation becomes metadata-only.
	//   2. The new member's config writes to the same .conf path
	//      (stable per-pool tunnel name in connectToPoolMember). If
	//      we overwrite while the OS WireGuard service is still
	//      reading it, we corrupt the file the running service
	//      depends on. Wait for proto.Down to complete (which happens
	//      synchronously inside disconnectInternal) before letting
	//      connectToPoolMember touch disk.
	//
	// If disconnect FAILS (helper unreachable, service stuck) we
	// must NOT proceed - the old tunnel is still up at the OS level
	// and overwriting .conf would corrupt its state. Bail with an
	// error so the user knows the rotation did not happen.
	a.mu.RLock()
	wasConnected := a.connected
	a.mu.RUnlock()
	if wasConnected {
		a.mu.Lock()
		err := a.disconnectInternal()
		a.mu.Unlock()
		if err != nil {
			return fmt.Errorf("pool rotate: disconnect failed, refusing to overwrite config: %w", err)
		}
	}

	// Retry loop: up to maxConnectAttempts members per rotation. Each
	// failure (Up error OR no-handshake-after-5s) marks the member
	// Unreachable+timestamp, picks the next, and tries again. Silent
	// per user choice in v0.9.11.33 - we do NOT toast intermediate
	// failures; only the final all-failed state surfaces (as a
	// disconnected app state, not a popup).
	//
	// First-iteration preference: if the rotator's pre-warm already
	// picked PendingMemberID, honour that (deterministic with the
	// "Next:" UI line). Subsequent iterations re-pick via the policy
	// excluding everything already attempted.
	const maxConnectAttempts = 3
	var attempted []string
	var lastErr error

	for attempt := 0; attempt < maxConnectAttempts; attempt++ {
		var member *PoolMember
		if attempt == 0 && pool.PendingMemberID != "" {
			candidate := pool.MemberByID(pool.PendingMemberID)
			if candidate != nil && !candidate.Unreachable {
				member = candidate
			}
		}
		if member == nil {
			member = PickMemberExcluding(pool, userCountry, pool.ActiveMemberID, attempted)
		}
		if member == nil {
			break
		}
		attempted = append(attempted, member.ID)

		pool.ActiveMemberID = member.ID
		pool.PendingMemberID = "" // Clear pre-warm hint - decision is now made.
		if err := a.pools.Update(pool); err != nil {
			log.Printf("Pool: persist active-member failed: %v", err)
		}

		log.Printf("Pool %s policy=%s attempt %d/%d: trying member %s (%s, %s)",
			pool.Name, pool.Policy, attempt+1, maxConnectAttempts,
			member.Name, member.Country, member.Region)

		if err := a.connectToPoolMember(member); err != nil {
			lastErr = err
			a.markMemberUnreachable(pool, member, err.Error())
			log.Printf("Pool %s: connect to %s failed (%v) - retrying with next member",
				pool.Name, member.Name, err)
			continue
		}

		// Layer-B-V2: peer-health verification by traffic-counter.
		// WireGuard handshakes are lazy - they only fire when the
		// first packet attempts to traverse the tunnel. The naïve
		// "poll latest-handshake after Up" check used in v0.9.11.33-35
		// returned zero on every healthy connect where the user had
		// not yet generated traffic, killing rotation with false-
		// positives (issue reported in v0.9.11.36).
		//
		// V2 fixes this by ACTIVELY triggering a packet through the
		// tunnel (a 1-byte UDP write to a target derived from the
		// member's AllowedIPs) and then polling `bytes_rx` from
		// `wg show`. A non-zero rx counter means the remote peer
		// completed the handshake and sent something back - the
		// strongest "peer is alive" signal we can read locally
		// without admin/raw-socket privileges.
		//
		// Only WireGuard. OpenVPN/IPSec have their own auth flows
		// that fail-fast on dead peers via Up().
		if member.Config != nil && member.Config.Protocol == "wireguard" {
			if !a.verifyWireGuardPeerHealth(member, peerHealthTimeout) {
				lastErr = fmt.Errorf("peer did not respond to triggered traffic within %s", peerHealthTimeout)
				a.markMemberUnreachable(pool, member, lastErr.Error())
				log.Printf("Pool %s: %s up but peer silent (no rx) - marking unreachable, retrying",
					pool.Name, member.Name)
				a.mu.Lock()
				_ = a.disconnectInternal()
				a.mu.Unlock()
				continue
			}
		}

		// Healthy connect. Fire the rotated event so the UI refreshes
		// the active-member line, and we are done.
		if a.ctx != nil {
			wailsRuntime.EventsEmit(a.ctx, "pool:rotated", map[string]interface{}{
				"pool_id":            pool.ID,
				"active_member_id":   member.ID,
				"active_member_name": member.Name,
				"country":            member.Country,
			})
		}
		return nil
	}

	if lastErr == nil {
		return fmt.Errorf("pool %s: no eligible member to pick", pool.Name)
	}
	return fmt.Errorf("pool %s: all %d connect attempts failed; last error: %w",
		pool.Name, len(attempted), lastErr)
}

// peerHealthTimeout is the budget for verifyWireGuardPeerHealth.
// 5 s is generous: a healthy WireGuard handshake completes in well
// under 1 s, so the typical-case verification finishes in 500-800 ms
// (one trigger + one poll). The full timeout only burns on a genuinely
// dead peer where we never see rx.
const peerHealthTimeout = 5 * time.Second

// verifyWireGuardPeerHealth triggers a small packet through the new
// tunnel and polls until rx bytes show up. Returns true on first
// non-zero rx reading, false on timeout. Read-only with respect to
// app state - safe to call without taking app.mu.
//
// Why "trigger then poll" instead of just polling latest-handshake:
// WireGuard handshakes are lazy; the kernel only fires them when a
// packet needs to traverse the tunnel. Without traffic, an idle
// tunnel sits forever showing latest-handshake=0 even though the
// peer is alive and responsive. By firing a 1-byte UDP write to a
// destination routed through the tunnel, we force the kernel to
// initiate the handshake. The peer's handshake-response packet
// counts as bytes_rx; if it never arrives, the peer is genuinely
// silent.
func (a *App) verifyWireGuardPeerHealth(member *PoolMember, timeout time.Duration) bool {
	proto, ok := a.protocols["wireguard"]
	if !ok {
		return true // protocol not loaded - cannot verify, accept connect
	}

	// Find a target IP routed through the tunnel. Parsed once from the
	// member's AllowedIPs. If no usable target exists (IPv6-only or
	// malformed), we skip the trigger and fall back to "trust Up" -
	// no false-positive risk because we never assert "dead".
	target := parseAllowedIPsTarget(member.Config.ConfigContent)
	if target != "" {
		triggerWGTraffic(target)
	} else {
		log.Printf("Pool: peer-health: no IPv4 target in AllowedIPs, skipping trigger - trusting Up()")
		return true
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		s := proto.Status()
		if s.BytesRx > 0 {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

// parseAllowedIPsTarget extracts a single routable IPv4 destination
// from a WireGuard config's AllowedIPs line. Strategy:
//   - 0.0.0.0/0 present -> use 1.1.1.1 (Cloudflare DNS, well-known
//     benign target; the actual response does not matter, only that
//     the kernel routes the packet through the wg interface)
//   - Else the first IPv4 CIDR yields network-address + 1 (the
//     typical gateway IP in private VPN networks; even if no host
//     answers, the kernel still routes the packet through wg and
//     fires the handshake)
//   - IPv6-only or unparseable -> "" (caller falls back to no-trigger)
//
// Returns "" rather than picking a non-routable address - silently
// triggering through eth0 instead of wg would defeat the purpose.
func parseAllowedIPsTarget(content string) string {
	for _, rawLine := range strings.Split(content, "\n") {
		line := strings.TrimSpace(rawLine)
		if !strings.HasPrefix(line, "AllowedIPs") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		cidrs := strings.Split(parts[1], ",")
		for _, c := range cidrs {
			c = strings.TrimSpace(c)
			if c == "" || strings.Contains(c, ":") {
				continue // skip IPv6
			}
			if c == "0.0.0.0/0" {
				return "1.1.1.1"
			}
			_, ipnet, err := net.ParseCIDR(c)
			if err != nil {
				continue
			}
			ip := ipnet.IP.To4()
			if ip == nil {
				continue
			}
			// network-address + 1: typical gateway. Make a copy so
			// we do not mutate the parsed CIDR's net.IP slice.
			out := make(net.IP, 4)
			copy(out, ip)
			out[3]++
			return out.String()
		}
		return "" // AllowedIPs line found but no IPv4 entry usable
	}
	return ""
}

// triggerWGTraffic sends a single 1-byte UDP packet to the given
// target on port 53. The packet is malformed for any DNS server but
// the response is irrelevant - the only purpose is to force the
// kernel to route through the wg interface, which triggers the
// WireGuard handshake. Fire-and-forget on a goroutine so a slow
// allocation never blocks the rotator.
func triggerWGTraffic(targetIP string) {
	if targetIP == "" {
		return
	}
	go func() {
		conn, err := net.DialTimeout("udp", net.JoinHostPort(targetIP, "53"), 500*time.Millisecond)
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = conn.Write([]byte{0})
	}()
}

// preWarmActivePool runs 60 s before the next scheduled rotation. It
// picks the next member using the same logic as the rotation itself,
// persists the pick as Pool.PendingMemberID, and emits "pool:rotated"
// so the frontend refreshes and renders "Next: <name>". The actual
// disconnect+connect still happens at the rotation tick - this just
// surfaces the upcoming server name early so the user sees what's
// coming and so the rotation tick does not also have to do the pick
// work in the critical path.
func (a *App) preWarmActivePool() {
	a.mu.RLock()
	poolID := a.activePoolID
	a.mu.RUnlock()
	if poolID == "" || a.pools == nil {
		return
	}
	pool := a.pools.Get(poolID)
	if pool == nil {
		return
	}

	userCountry := ""
	if a.selfIPDetector != nil {
		if cached := a.selfIPDetector.Cached(); cached != nil {
			userCountry = cached.Country
		}
	}

	// Pre-warm probe: pick → DNS+TCP probe → if fail mark unreachable
	// and pick another. Up to 3 picks per pre-warm cycle so a single
	// dead server cannot stall rotation indefinitely. Probe failures
	// here are HINTS - the rotation tick's connect+handshake is the
	// authoritative gate, but doing this work 60 s ahead means most
	// dead servers are filtered before they ever cause a user-visible
	// connect failure.
	const maxPreWarmAttempts = 3
	var attempted []string
	var member *PoolMember
	for i := 0; i < maxPreWarmAttempts; i++ {
		candidate := PickMemberExcluding(pool, userCountry, pool.ActiveMemberID, attempted)
		if candidate == nil {
			break
		}
		attempted = append(attempted, candidate.ID)
		if err := probeMember(candidate); err != nil {
			log.Printf("Pool %s pre-warm: probe %s failed (%v) - marking unreachable",
				pool.Name, candidate.Name, err)
			a.markMemberUnreachable(pool, candidate, err.Error())
			continue
		}
		member = candidate
		break
	}
	if member == nil {
		log.Printf("Pool %s pre-warm: no probe-passing member after %d tries", pool.Name, len(attempted))
		return
	}
	pool.PendingMemberID = member.ID
	if err := a.pools.Update(pool); err != nil {
		log.Printf("Pool: preWarm persist failed: %v", err)
		return
	}

	// Pre-write the .conf file for the next slot to disk RIGHT NOW
	// (60 s before rotation). The rotation tick then only has to do
	// the OS-service install + handshake; the file write that used
	// to live in the disconnect-then-write-then-up critical path is
	// gone. For protocols other than WireGuard the path layout is
	// different - we skip pre-write for OpenVPN/IPSec rather than
	// risk writing to the wrong place.
	if member.Config != nil && member.Config.Protocol == "wireguard" {
		nextSlot := pool.NextSlot()
		nextTunnel := "privycs-pool-" + shortID(pool.ID) + "-" + nextSlot
		if err := a.preWriteWGConfig(nextTunnel, member.Config.ConfigContent); err != nil {
			log.Printf("Pool: pre-write %s.conf failed: %v", nextTunnel, err)
		} else {
			log.Printf("Pool %s pre-warm: wrote %s.conf for next member %s",
				pool.Name, nextTunnel, member.Name)
		}
	} else {
		log.Printf("Pool %s pre-warm: next member will be %s (%s)",
			pool.Name, member.Name, member.Country)
	}

	if a.ctx != nil {
		wailsRuntime.EventsEmit(a.ctx, "pool:prewarm", map[string]interface{}{
			"pool_id":             pool.ID,
			"pending_member_id":   member.ID,
			"pending_member_name": member.Name,
			"country":             member.Country,
		})
	}
}

// preWriteWGConfig writes a WireGuard .conf to the same path
// w.Configure would write to (<appDataDir>/<tunnelName>.conf),
// without mutating the protocol handler's current state. Used by
// preWarmActivePool to stage the next slot's config 60 s ahead so
// the rotation tick does not have to do the file write in the
// critical path.
func (a *App) preWriteWGConfig(tunnelName, content string) error {
	confPath := filepath.Join(appDataDir(), tunnelName+".conf")
	if err := os.MkdirAll(filepath.Dir(confPath), 0o700); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	return os.WriteFile(confPath, []byte(content), 0o600)
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
