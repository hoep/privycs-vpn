package main

import (
	"context"
	"strings"
)

// VPNProtocol is the interface that all VPN protocol implementations must satisfy.
// Ported from cmd/privycs-connect/protocol.go
type VPNProtocol interface {
	// Name returns the protocol identifier (wireguard, openvpn, ipsec)
	Name() string

	// Up brings up the VPN tunnel using the stored configuration
	Up(ctx context.Context) error

	// Down tears down the VPN tunnel
	Down(ctx context.Context) error

	// Status returns the current connection status
	Status() ProtocolStatus

	// IsAvailable checks if the protocol can be used on this system
	IsAvailable() bool

	// Configure applies a protocol-specific configuration
	Configure(cfg []byte) error
}

// ProtocolStatus represents the current state of a VPN protocol connection
type ProtocolStatus struct {
	Connected     bool   `json:"connected"`
	Protocol      string `json:"protocol"`
	// Variant differentiates WireGuard-class implementations: empty
	// or "wireguard" means vanilla WG (wg-quick on Linux/Win,
	// in-process wireguard-go on macOS). "amneziawg" means the
	// DPI-evasion fork (awg-quick on Linux, in-process amneziawg-go
	// on macOS, sidecar on Windows). Other protocols ignore it.
	// Filled in by the protocol handler at Up() time after parsing
	// the conf for AWG-specific fields (Jc, Jmin, Jmax, S1-4, H1-4).
	Variant       string  `json:"variant,omitempty"`
	ServerAddress string  `json:"server_address,omitempty"`
	LocalAddress  string  `json:"local_address,omitempty"`
	BytesRx       int64   `json:"bytes_rx"`
	BytesTx       int64   `json:"bytes_tx"`
	ConnectedAt   string  `json:"connected_at,omitempty"`
	LastHandshake string  `json:"last_handshake,omitempty"`
	LatencyMs     float64 `json:"latency_ms,omitempty"`
	Error         string  `json:"error,omitempty"`
}

// detectProtocol auto-detects VPN protocol from config file content or filename
func detectProtocol(content string, filename string) string {
	lower := strings.ToLower(filename)

	// By filename extension. .conf is shared between vanilla WG
	// and AmneziaWG — same grammar, AWG adds [Interface]-scope
	// keys (Jc/Jmin/Jmax/S1-4/H1-4/I1-5). Detect by content to
	// choose the right protocol slot.
	if strings.HasSuffix(lower, ".conf") {
		if DetectVariant(content) == VariantAmnezia {
			return "amneziawg"
		}
		return "wireguard"
	}
	if strings.HasSuffix(lower, ".ovpn") {
		return "openvpn"
	}
	if strings.HasSuffix(lower, ".sswan") || strings.HasSuffix(lower, ".mobileconfig") || strings.HasSuffix(lower, ".p12") {
		return "ipsec"
	}

	// By content detection — same AWG-aware branching for files
	// whose name doesn't carry an extension.
	if strings.Contains(content, "[Interface]") && strings.Contains(content, "PrivateKey") {
		if DetectVariant(content) == VariantAmnezia {
			return "amneziawg"
		}
		return "wireguard"
	}
	if strings.Contains(content, "remote ") || strings.Contains(content, "<ca>") || strings.Contains(content, "client") {
		return "openvpn"
	}

	return ""
}
