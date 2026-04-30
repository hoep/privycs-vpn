package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
)

// ProtocolConfig holds a single protocol's configuration for a connection
type ProtocolConfig struct {
	Protocol      string `json:"protocol"`       // wireguard, openvpn, ipsec
	ConfigContent string `json:"config_content"` // raw config file content
	Filename      string `json:"filename"`       // original filename
	ServerAddress string `json:"server_address"` // extracted endpoint
	LocalAddress  string `json:"local_address"`  // VPN IP (if parseable)
	AddedAt       string `json:"added_at"`
}

// SavedConnection represents a VPN server/endpoint with one or more protocol configs
type SavedConnection struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	ActiveProtocol string            `json:"active_protocol"` // which protocol is selected
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

// GetProtocol returns the config for a specific protocol, or nil
func (c *SavedConnection) GetProtocol(protocol string) *ProtocolConfig {
	for _, p := range c.Protocols {
		if p.Protocol == protocol {
			return p
		}
	}
	return nil
}

// GetActiveConfig returns the currently selected protocol config
func (c *SavedConnection) GetActiveConfig() *ProtocolConfig {
	return c.GetProtocol(c.ActiveProtocol)
}

// AvailableProtocols returns the list of protocol names configured
func (c *SavedConnection) AvailableProtocols() []string {
	var names []string
	for _, p := range c.Protocols {
		names = append(names, p.Protocol)
	}
	return names
}

// HasProtocol checks if a protocol config exists
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

// AddOrUpdate adds a protocol config to a connection (creates connection if connID is empty)
func (r *ConnectionRegistry) AddOrUpdate(connID string, name string, pc *ProtocolConfig) (*SavedConnection, error) {
	var conn *SavedConnection

	if connID != "" {
		conn = r.Get(connID)
		if conn == nil {
			return nil, fmt.Errorf("connection not found: %s", connID)
		}
	}

	if conn == nil {
		// Create new connection
		conn = &SavedConnection{
			ID:             uuid.New().String(),
			Name:           name,
			ActiveProtocol: pc.Protocol,
			CreatedAt:      time.Now(),
		}
		r.Connections = append(r.Connections, conn)
	}

	// Replace existing protocol config or add new one
	replaced := false
	for i, existing := range conn.Protocols {
		if existing.Protocol == pc.Protocol {
			conn.Protocols[i] = pc
			replaced = true
			break
		}
	}
	if !replaced {
		conn.Protocols = append(conn.Protocols, pc)
	}

	// Update name if provided
	if name != "" {
		conn.Name = name
	}

	r.Save()
	return conn, nil
}

// SetActiveProtocol changes which protocol is used for a connection
func (r *ConnectionRegistry) SetActiveProtocol(connID string, protocol string) error {
	conn := r.Get(connID)
	if conn == nil {
		return fmt.Errorf("connection not found: %s", connID)
	}
	if !conn.HasProtocol(protocol) {
		return fmt.Errorf("protocol %s not configured for this connection", protocol)
	}
	conn.ActiveProtocol = protocol
	r.Save()
	return nil
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

// RemoveProtocol removes a single protocol from a connection
func (r *ConnectionRegistry) RemoveProtocol(connID string, protocol string) error {
	conn := r.Get(connID)
	if conn == nil {
		return fmt.Errorf("connection not found: %s", connID)
	}

	for i, p := range conn.Protocols {
		if p.Protocol == protocol {
			conn.Protocols = append(conn.Protocols[:i], conn.Protocols[i+1:]...)
			// If we removed the active protocol, switch to the first available
			if conn.ActiveProtocol == protocol && len(conn.Protocols) > 0 {
				conn.ActiveProtocol = conn.Protocols[0].Protocol
			}
			break
		}
	}

	// Delete the entire connection if no protocols remain
	if len(conn.Protocols) == 0 {
		return r.Delete(connID)
	}

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
	}
}
