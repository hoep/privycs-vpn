package main

import (
	"context"
	"fmt"
	"log"
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
	for _, pool := range pools {
		changed := false
		for _, m := range pool.Members {
			if m.Country != "" || m.Config == nil || m.Config.ServerAddress == "" {
				continue
			}
			ip := resolveHostToIP(stripPortIfPresent(m.Config.ServerAddress))
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
		}
		if changed {
			if err := a.pools.Update(pool); err == nil {
				log.Printf("Pool %s: backfilled country data for previously-empty members", pool.Name)
			}
		}
	}
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

	member := PickMember(pool, userCountry, pool.ActiveMemberID)
	if member == nil {
		return fmt.Errorf("pool %s: no eligible member to pick", pool.Name)
	}

	pool.ActiveMemberID = member.ID
	if err := a.pools.Update(pool); err != nil {
		log.Printf("Pool: persist active-member failed: %v", err)
	}

	log.Printf("Pool %s policy=%s picked member %s (%s, %s)",
		pool.Name, pool.Policy, member.Name, member.Country, member.Region)

	// CRITICAL: tear down the existing tunnel before the new one's
	// config is written. Two reasons:
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
	if err := a.connectToPoolMember(member); err != nil {
		return err
	}

	// Notify the frontend that the active member changed so the
	// "Currently: ..." line on the Connect screen and the active-
	// member-display in the picker refresh without waiting for the
	// next 5-second pollRotator tick. The frontend listens on
	// "pool:rotated" and re-runs poolStore.refresh().
	if a.ctx != nil {
		wailsRuntime.EventsEmit(a.ctx, "pool:rotated", map[string]interface{}{
			"pool_id":           pool.ID,
			"active_member_id":  member.ID,
			"active_member_name": member.Name,
			"country":           member.Country,
		})
	}
	return nil
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
