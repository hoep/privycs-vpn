package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/google/uuid"
)

// ProtocolConfig holds a single protocol's configuration for a connection
type ProtocolConfig struct {
	// ID — stable per-config UUID. Drives the "multi-config-per-
	// protocol-per-connection" model: a SavedConnection may hold
	// any number of ProtocolConfigs (including multiples of the
	// same protocol type, e.g. two WireGuard endpoints UDP+TCP),
	// and SavedConnection.ActiveConfigID names the one the
	// connect path uses. Empty for legacy persisted data; the
	// load-time heal assigns a fresh UUID.
	ID            string `json:"id,omitempty"`
	Protocol      string `json:"protocol"`         // wireguard, amneziawg, openvpn, ipsec
	ConfigContent string `json:"config_content"`   // raw config file content
	Filename      string `json:"filename"`         // original filename
	// Nickname — user-editable label, shown in the pill row when
	// a connection has multiple configs of the same protocol
	// type ("Home WG UDP" vs "Home WG TCP"). Empty → fall back
	// to filename / protocol label.
	Nickname      string `json:"nickname,omitempty"`
	ServerAddress string `json:"server_address"`   // extracted endpoint
	LocalAddress  string `json:"local_address"`    // VPN IP (if parseable)
	AddedAt       string `json:"added_at"`
}

// SavedConnection represents a VPN server/endpoint with one or more protocol configs
type SavedConnection struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	ActiveProtocol string            `json:"active_protocol"` // legacy field — derived from ActiveConfigID
	// ActiveConfigID — the ID of the currently-active ProtocolConfig.
	// Replaces the old "ActiveProtocol enum" semantics which assumed
	// at most one config per protocol type. Empty on legacy data;
	// load() reconciles against ActiveProtocol.
	ActiveConfigID string            `json:"active_config_id,omitempty"`
	Protocols      []*ProtocolConfig `json:"protocols"`       // all available configs
	CreatedAt      time.Time         `json:"created_at"`
	LastConnected  time.Time         `json:"last_connected,omitempty"`
	IsFavorite     bool              `json:"is_favorite"`
	// Per-connection DNS override. Comma- or whitespace-separated
	// IPv4/IPv6. When non-empty, takes priority over the global
	// Settings.DNSOverride. Empty falls back to global. Use case:
	// "Home connection uses 192.168.1.1 (Pi-hole), Work uses
	// corporate DNS, Public uses Cloudflare" without flipping
	// global Settings on every switch. Mirrors Android's
	// VpnConnection.dnsOverride.
	DnsOverride    string            `json:"dns_override,omitempty"`
}

// GetProtocol returns the FIRST config for a specific protocol, or
// nil. Multi-config-aware callers should use GetConfigByID or
// OrderedConfigs instead — this method is kept for back-compat
// with code that doesn't yet think in config-id terms.
func (c *SavedConnection) GetProtocol(protocol string) *ProtocolConfig {
	for _, p := range c.Protocols {
		if p.Protocol == protocol {
			return p
		}
	}
	return nil
}

// GetConfigByID returns the ProtocolConfig with the given id, or nil.
func (c *SavedConnection) GetConfigByID(id string) *ProtocolConfig {
	for _, p := range c.Protocols {
		if p.ID == id {
			return p
		}
	}
	return nil
}

// GetActiveConfig returns the currently selected protocol config.
// Prefers ActiveConfigID; falls back to the first config matching
// the legacy ActiveProtocol; finally to the first config period.
func (c *SavedConnection) GetActiveConfig() *ProtocolConfig {
	if c.ActiveConfigID != "" {
		if p := c.GetConfigByID(c.ActiveConfigID); p != nil {
			return p
		}
	}
	if p := c.GetProtocol(c.ActiveProtocol); p != nil {
		return p
	}
	if len(c.Protocols) > 0 {
		return c.Protocols[0]
	}
	return nil
}

// OrderedConfigs returns the connection's protocol configs sorted
// in failover-preference order: protocol ordinal first (amneziawg
// → wireguard → openvpn → ipsec), then insertion order (AddedAt).
// Used by failover logic and the per-config pill row in the UI.
func (c *SavedConnection) OrderedConfigs() []*ProtocolConfig {
	out := make([]*ProtocolConfig, len(c.Protocols))
	copy(out, c.Protocols)
	sort.SliceStable(out, func(i, j int) bool {
		oi := protocolOrdinal(out[i].Protocol)
		oj := protocolOrdinal(out[j].Protocol)
		if oi != oj {
			return oi < oj
		}
		return out[i].AddedAt < out[j].AddedAt
	})
	return out
}

// protocolOrdinal returns the failover-preference rank for a
// protocol id. AmneziaWG first (DPI-evasion wins on restrictive
// networks), then vanilla WireGuard, then OpenVPN, then IPSec.
// Mirrors the Android enum order.
func protocolOrdinal(p string) int {
	switch p {
	case "amneziawg":
		return 0
	case "wireguard":
		return 1
	case "openvpn":
		return 2
	case "ipsec":
		return 3
	}
	return 99
}

// AvailableProtocols returns the distinct list of protocol names
// configured. Sorted by failover preference. Used for protocol-
// type-summary UIs (e.g. "this connection supports AWG, WG, OVPN").
func (c *SavedConnection) AvailableProtocols() []string {
	seen := map[string]bool{}
	var names []string
	for _, p := range c.OrderedConfigs() {
		if !seen[p.Protocol] {
			seen[p.Protocol] = true
			names = append(names, p.Protocol)
		}
	}
	return names
}

// HasProtocol checks if at least one config of the given protocol exists
func (c *SavedConnection) HasProtocol(protocol string) bool {
	return c.GetProtocol(protocol) != nil
}

// ConnectionRegistry manages multiple saved VPN connections
type ConnectionRegistry struct {
	Connections []*SavedConnection `json:"connections"`
	ActiveID    string             `json:"active_id"`
	filePath    string
}

// NewConnectionRegistry creates a new registry, loading from disk if available
func NewConnectionRegistry() *ConnectionRegistry {
	r := &ConnectionRegistry{
		filePath: filepath.Join(appDataDir(), "connections.json"),
	}
	r.load()
	return r
}

// List returns all saved connections
func (r *ConnectionRegistry) List() []*SavedConnection {
	return r.Connections
}

// Get returns a connection by ID
func (r *ConnectionRegistry) Get(id string) *SavedConnection {
	for _, c := range r.Connections {
		if c.ID == id {
			return c
		}
	}
	return nil
}

// Active returns the currently active connection
func (r *ConnectionRegistry) Active() *SavedConnection {
	return r.Get(r.ActiveID)
}

// AddOrUpdate adds a protocol config to a connection (creates connection if connID is empty).
// Multi-config-per-protocol: the same connection may hold multiple
// configs of the same protocol type. Update-in-place only when the
// caller's pc has a non-empty ID matching an existing entry;
// otherwise append a new entry (different ID → different config).
func (r *ConnectionRegistry) AddOrUpdate(connID string, name string, pc *ProtocolConfig) (*SavedConnection, error) {
	var conn *SavedConnection

	if connID != "" {
		conn = r.Get(connID)
		if conn == nil {
			return nil, fmt.Errorf("connection not found: %s", connID)
		}
	}

	// Ensure every persisted ProtocolConfig has a stable ID.
	if pc.ID == "" {
		pc.ID = uuid.New().String()
	}

	if conn == nil {
		// Create new connection
		conn = &SavedConnection{
			ID:             uuid.New().String(),
			Name:           name,
			ActiveProtocol: pc.Protocol,
			ActiveConfigID: pc.ID,
			CreatedAt:      time.Now(),
		}
		r.Connections = append(r.Connections, conn)
	}

	// Update-in-place only when the caller's ID matches an existing
	// config (e.g. re-import of the same logical endpoint). Otherwise
	// append — the caller asked for a NEW config slot.
	replaced := false
	for i, existing := range conn.Protocols {
		if existing.ID == pc.ID {
			conn.Protocols[i] = pc
			replaced = true
			break
		}
	}
	if !replaced {
		conn.Protocols = append(conn.Protocols, pc)
	}

	if conn.Name == "" && name != "" {
		conn.Name = name
	}

	// If the connection has no active config yet (legacy data or
	// brand-new connection), pin it to the one we just added.
	if conn.ActiveConfigID == "" {
		conn.ActiveConfigID = pc.ID
		conn.ActiveProtocol = pc.Protocol
	}

	r.Save()
	return conn, nil
}

// SetActiveConfig pins a specific ProtocolConfig (by id) as active
// on the given connection. Returns an error if the config isn't on
// the connection. Also updates the legacy ActiveProtocol field for
// back-compat with code paths that still read it.
func (r *ConnectionRegistry) SetActiveConfig(connID string, configID string) error {
	conn := r.Get(connID)
	if conn == nil {
		return fmt.Errorf("connection not found: %s", connID)
	}
	cfg := conn.GetConfigByID(configID)
	if cfg == nil {
		return fmt.Errorf("config %s not on connection %s", configID, connID)
	}
	conn.ActiveConfigID = configID
	conn.ActiveProtocol = cfg.Protocol
	r.Save()
	return nil
}

// SetActiveProtocol — back-compat alias. Switches to the first
// config of the requested protocol type. With multi-config support,
// callers that care about WHICH config (when there are several of
// the same protocol type) should use SetActiveConfig instead.
func (r *ConnectionRegistry) SetActiveProtocol(connID string, protocol string) error {
	conn := r.Get(connID)
	if conn == nil {
		return fmt.Errorf("connection not found: %s", connID)
	}
	for _, p := range conn.OrderedConfigs() {
		if p.Protocol == protocol {
			return r.SetActiveConfig(connID, p.ID)
		}
	}
	return fmt.Errorf("protocol %s not configured for this connection", protocol)
}

// SetActive marks a connection as active
func (r *ConnectionRegistry) SetActive(id string) {
	r.ActiveID = id
	r.Save()
}

// Delete removes a connection by ID
func (r *ConnectionRegistry) Delete(id string) error {
	for i, c := range r.Connections {
		if c.ID == id {
			r.Connections = append(r.Connections[:i], r.Connections[i+1:]...)
			if r.ActiveID == id {
				r.ActiveID = ""
			}
			r.Save()
			return nil
		}
	}
	return nil
}

// RemoveProtocol removes the FIRST config of the given protocol
// type from a connection. Back-compat for callers that don't know
// about per-config ids. New code should use RemoveConfig.
func (r *ConnectionRegistry) RemoveProtocol(connID string, protocol string) error {
	conn := r.Get(connID)
	if conn == nil {
		return fmt.Errorf("connection not found: %s", connID)
	}
	for _, p := range conn.Protocols {
		if p.Protocol == protocol {
			return r.RemoveConfig(connID, p.ID)
		}
	}
	return nil
}

// RemoveConfig removes a specific ProtocolConfig (by id). When the
// removed config was the active one and others remain, repick;
// when no configs remain, delete the whole connection.
func (r *ConnectionRegistry) RemoveConfig(connID string, configID string) error {
	conn := r.Get(connID)
	if conn == nil {
		return fmt.Errorf("connection not found: %s", connID)
	}
	for i, p := range conn.Protocols {
		if p.ID == configID {
			conn.Protocols = append(conn.Protocols[:i], conn.Protocols[i+1:]...)
			if conn.ActiveConfigID == configID && len(conn.Protocols) > 0 {
				replacement := conn.OrderedConfigs()[0]
				conn.ActiveConfigID = replacement.ID
				conn.ActiveProtocol = replacement.Protocol
			}
			break
		}
	}
	if len(conn.Protocols) == 0 {
		return r.Delete(connID)
	}
	r.Save()
	return nil
}

// RenameConfig sets the user-editable nickname on a specific config.
func (r *ConnectionRegistry) RenameConfig(connID string, configID string, nickname string) error {
	conn := r.Get(connID)
	if conn == nil {
		return fmt.Errorf("connection not found: %s", connID)
	}
	cfg := conn.GetConfigByID(configID)
	if cfg == nil {
		return fmt.Errorf("config %s not on connection %s", configID, connID)
	}
	cfg.Nickname = nickname
	r.Save()
	return nil
}

// Save persists the registry to disk
func (r *ConnectionRegistry) Save() {
	os.MkdirAll(filepath.Dir(r.filePath), 0700)
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		log.Printf("Failed to marshal connections: %v", err)
		return
	}
	if err := os.WriteFile(r.filePath, data, 0600); err != nil {
		log.Printf("Failed to save connections: %v", err)
	}
}

// load reads the registry from disk
func (r *ConnectionRegistry) load() {
	data, err := os.ReadFile(r.filePath)
	if err != nil {
		return
	}
	if err := json.Unmarshal(data, r); err != nil {
		log.Printf("Warning: connections.json corrupted, starting with empty registry: %v", err)
		return
	}

	healed := false
	for _, conn := range r.Connections {
		// Phase 1: assign a stable UUID to every ProtocolConfig
		// that doesn't have one. The ID field arrived with the
		// multi-config-per-protocol refactor; legacy persisted
		// data has ID="" because the field didn't exist.
		for _, pc := range conn.Protocols {
			if pc.ID == "" {
				pc.ID = uuid.New().String()
				healed = true
			}
		}

		// Phase 2: AmneziaWG-as-4th-protocol heal. v0.9.15.x split
		// AWG out of WIREGUARD into its own protocol slot.
		// Pre-existing data has AWG .conf content stored under
		// protocol=wireguard; reclassify so the dispatch picks
		// the right handler.
		awgReclassified := false
		for _, pc := range conn.Protocols {
			if pc.Protocol == "wireguard" && DetectVariant(pc.ConfigContent) == VariantAmnezia {
				pc.Protocol = "amneziawg"
				healed = true
				awgReclassified = true
			}
		}
		if awgReclassified && conn.ActiveProtocol == "wireguard" {
			hasVanillaWG := false
			for _, pc := range conn.Protocols {
				if pc.Protocol == "wireguard" {
					hasVanillaWG = true
					break
				}
			}
			if !hasVanillaWG {
				conn.ActiveProtocol = "amneziawg"
			}
		}

		// Phase 3: reconcile ActiveConfigID against the legacy
		// ActiveProtocol enum. With multi-config support an
		// "active slot" is identified by config-id, not protocol
		// type. If the field is empty (legacy data) or points at
		// a config that no longer exists, repoint at the first
		// config matching ActiveProtocol, falling back to the
		// first config in the connection.
		validActive := false
		if conn.ActiveConfigID != "" {
			for _, pc := range conn.Protocols {
				if pc.ID == conn.ActiveConfigID {
					validActive = true
					break
				}
			}
		}
		if !validActive && len(conn.Protocols) > 0 {
			var pick *ProtocolConfig
			for _, pc := range conn.Protocols {
				if pc.Protocol == conn.ActiveProtocol {
					pick = pc
					break
				}
			}
			if pick == nil {
				pick = conn.Protocols[0]
			}
			conn.ActiveConfigID = pick.ID
			healed = true
		}
	}
	if healed {
		if d, err := json.MarshalIndent(r, "", "  "); err == nil {
			_ = os.WriteFile(r.filePath, d, 0600)
		}
	}
}
