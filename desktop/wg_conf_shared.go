package main

import (
	"bufio"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
)

// Shared WireGuard / AmneziaWG conf parsing and UAPI helpers.
//
// Used by the in-process tunnel implementations on macOS
// (wg_macos.go, wg_macos_awg.go) and Windows (wg_windows_awg.go).
// Linux's helper-side runs wg-quick / awg-quick via the OS package
// manager and doesn't go through these structs.
//
// The grammar matches wg-quick's .conf format: [Interface] +
// [Peer] sections, key=value lines, optional comments, whitespace-
// tolerant. AmneziaWG-specific [Interface] keys (jc, jmin, jmax,
// s1-s4, h1-h4, i1-i5, j1-j3) are captured into wgConfigParsed.AwgKeys
// in addition to (not instead of) any vanilla-WG keys.

// wgPeer holds the parsed [Peer] block from a .conf file.
type wgPeer struct {
	PublicKey           string // base64, 32-byte
	PresharedKey        string // base64, 32-byte (optional)
	Endpoint            string // host:port — hostname is resolved to IP at apply time
	AllowedIPs          []string
	PersistentKeepalive int
}

// wgConfigParsed holds the parsed [Interface] block + all [Peer] blocks.
type wgConfigParsed struct {
	PrivateKey string // base64, 32-byte
	Addresses  []string
	DNS        []string
	MTU        int
	ListenPort int
	Peers      []wgPeer
	// AwgKeys captures AmneziaWG-specific [Interface] keys verbatim.
	// Empty for vanilla WireGuard. Vanilla wg's UAPI silently drops
	// these keys; the AWG fork's UAPI consumes them as device-scope
	// obfuscation state.
	AwgKeys map[string]string
}

// awgInterfaceKeyRe matches AWG obfuscation keys (lower-cased) at
// [Interface] scope.
var awgInterfaceKeyRe = regexp.MustCompile(`^(jc|jmin|jmax|s[1-4]|h[1-4]|i[1-5]|j[1-3])$`)

// parseWGConf reads a wg-quick / awg-quick style .conf and returns
// the parsed structure. Tolerates whitespace, comments (# or ;), and
// standard wg camelCase keys. Multiple comma-separated values per
// line are split. AllowedIPs aggregate across multiple lines within
// a [Peer] block.
func parseWGConf(text string) (*wgConfigParsed, error) {
	cfg := &wgConfigParsed{}
	var currentPeer *wgPeer
	section := ""

	scanner := bufio.NewScanner(strings.NewReader(text))
	for scanner.Scan() {
		raw := scanner.Text()
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(strings.Trim(line, "[]"))
			if section == "peer" {
				cfg.Peers = append(cfg.Peers, wgPeer{})
				currentPeer = &cfg.Peers[len(cfg.Peers)-1]
			}
			continue
		}
		eq := strings.Index(line, "=")
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		keyLower := strings.ToLower(key)

		switch section {
		case "interface":
			switch keyLower {
			case "privatekey":
				cfg.PrivateKey = val
			case "address":
				for _, p := range splitCSV(val) {
					cfg.Addresses = append(cfg.Addresses, p)
				}
			case "dns":
				for _, p := range splitCSV(val) {
					cfg.DNS = append(cfg.DNS, p)
				}
			case "mtu":
				if n, err := strconv.Atoi(val); err == nil {
					cfg.MTU = n
				}
			case "listenport":
				if n, err := strconv.Atoi(val); err == nil {
					cfg.ListenPort = n
				}
			default:
				if awgInterfaceKeyRe.MatchString(keyLower) {
					if cfg.AwgKeys == nil {
						cfg.AwgKeys = make(map[string]string)
					}
					cfg.AwgKeys[keyLower] = val
				}
			}
		case "peer":
			if currentPeer == nil {
				continue
			}
			switch keyLower {
			case "publickey":
				currentPeer.PublicKey = val
			case "presharedkey":
				currentPeer.PresharedKey = val
			case "endpoint":
				currentPeer.Endpoint = val
			case "allowedips":
				for _, p := range splitCSV(val) {
					currentPeer.AllowedIPs = append(currentPeer.AllowedIPs, p)
				}
			case "persistentkeepalive":
				if n, err := strconv.Atoi(val); err == nil {
					currentPeer.PersistentKeepalive = n
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan conf: %w", err)
	}
	if cfg.PrivateKey == "" {
		return nil, fmt.Errorf("missing PrivateKey in [Interface]")
	}
	if len(cfg.Peers) == 0 {
		return nil, fmt.Errorf("no [Peer] blocks")
	}
	if cfg.MTU == 0 {
		cfg.MTU = 1420
	}
	return cfg, nil
}

// splitCSV splits "a, b , c" into ["a", "b", "c"].
func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		t := strings.TrimSpace(p)
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

// b64ToHex decodes a 32-byte WireGuard key encoded as base64 (the
// .conf format) and re-encodes it as hex (the UAPI format). Returns
// "" if val is empty.
func b64ToHex(val string) (string, error) {
	if val == "" {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(val)
	if err != nil {
		return "", fmt.Errorf("base64 decode: %w", err)
	}
	if len(raw) != 32 {
		return "", fmt.Errorf("expected 32-byte key, got %d", len(raw))
	}
	return hex.EncodeToString(raw), nil
}

// resolveEndpoint takes "hostname:port" or "ip:port" (v6 in brackets)
// and returns "ip:port" suitable for the UAPI endpoint= directive.
// The wireguard-go and amneziawg-go devices do not resolve hostnames
// themselves — passing a host name results in the peer being
// permanently unrouted. Prefers IPv4 when available.
func resolveEndpoint(ep string) (string, error) {
	host, port, err := net.SplitHostPort(ep)
	if err != nil {
		return "", fmt.Errorf("split endpoint %q: %w", ep, err)
	}
	if ip := net.ParseIP(host); ip != nil {
		return ep, nil
	}
	addrs, err := net.LookupIP(host)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", host, err)
	}
	if len(addrs) == 0 {
		return "", fmt.Errorf("no addresses for %s", host)
	}
	for _, a := range addrs {
		if v4 := a.To4(); v4 != nil {
			return net.JoinHostPort(v4.String(), port), nil
		}
	}
	return net.JoinHostPort(addrs[0].String(), port), nil
}

// awgKeyOrder returns the canonical emission order for AWG keys in
// the UAPI string. IpcSet is order-tolerant but a fixed order makes
// dumps deterministic across runs.
func awgKeyOrder() []string {
	return []string{
		"jc", "jmin", "jmax",
		"s1", "s2", "s3", "s4",
		"h1", "h2", "h3", "h4",
		"i1", "i2", "i3", "i4", "i5",
		"j1", "j2", "j3",
	}
}

// uapiHasRecentHandshake parses an IpcGet output for a non-zero
// last_handshake_time_sec. WireGuard sends a handshake at most every
// 120s when traffic is flowing; presence of any non-zero handshake
// time means the peer responded at least once and the tunnel is
// alive. Identical format on vanilla wg and AWG UAPI.
func uapiHasRecentHandshake(uapi string) bool {
	for _, line := range strings.Split(uapi, "\n") {
		if strings.HasPrefix(line, "last_handshake_time_sec=") {
			val := strings.TrimPrefix(line, "last_handshake_time_sec=")
			if n, _ := strconv.ParseInt(strings.TrimSpace(val), 10, 64); n > 0 {
				return true
			}
		}
	}
	return false
}
