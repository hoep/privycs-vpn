package main

import (
	"archive/zip"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hoep/privycs/desktop/geoip"
)

// PoolImportProgress is emitted via Wails Events as import progresses.
// The frontend renders a stepper using these events.
type PoolImportProgress struct {
	Stage    string `json:"stage"`              // "extracting" | "parsing" | "resolving" | "done"
	Current  int    `json:"current"`
	Total    int    `json:"total"`
	Imported int    `json:"imported"`
	Skipped  int    `json:"skipped"`
	Message  string `json:"message,omitempty"`
}

// PoolImportResult is what the import returns to its caller.
type PoolImportResult struct {
	Members []*PoolMember `json:"members"`
	Skipped []SkippedFile `json:"skipped"`
}

// SkippedFile records a config file that could not be imported and why.
// Surfaced in the import-completion toast as "488 imported, 12 skipped"
// with the reasons grouped.
type SkippedFile struct {
	Filename string `json:"filename"`
	Reason   string `json:"reason"`
}

// importEntry is one in-memory file entry, regardless of whether it
// came from a ZIP archive or a directly-selected file.
type importEntry struct {
	name    string
	content []byte
}

// PoolImporter assembles members from a set of file paths or a ZIP
// archive. The geo resolver is optional; without one members get
// Country="" and Region="Other", which Geo-Nearest treats as
// "unavailable, fall back to Random".
type PoolImporter struct {
	geo CountryResolverIF
}

// CountryResolverIF mirrors selfip.CountryResolver but local to main
// so we can inject a stub in tests without importing the selfip
// package (which would create a cycle once selfip uses pool types).
type CountryResolverIF interface {
	CountryCode(ip net.IP) (string, error)
}

// NewPoolImporter constructs an importer wired to the production
// MMDB. nil geo is acceptable - imports proceed without country
// metadata.
func NewPoolImporter(geo CountryResolverIF) *PoolImporter {
	return &PoolImporter{geo: geo}
}

// Import processes a list of paths (each may be a .zip, .conf, .ovpn,
// or .sswan) and returns members ready to be wrapped in a Pool. Calls
// onProgress (if non-nil) at each stage transition and per-file during
// the parsing stage so the frontend can render a stepper without
// polling.
func (pi *PoolImporter) Import(paths []string, onProgress func(PoolImportProgress)) (*PoolImportResult, error) {
	result := &PoolImportResult{}

	emit := func(p PoolImportProgress) {
		if onProgress != nil {
			onProgress(p)
		}
	}

	// Stage 1: collect all config files (ZIPs are unpacked in-memory,
	// directly-passed config files are read straight in).
	emit(PoolImportProgress{Stage: "extracting"})
	var entries []importEntry
	for _, p := range paths {
		ext := strings.ToLower(filepath.Ext(p))
		switch ext {
		case ".zip":
			extracted, err := extractZipEntries(p)
			if err != nil {
				return nil, fmt.Errorf("extract %s: %w", p, err)
			}
			entries = append(entries, extracted...)
		case ".conf", ".ovpn", ".sswan":
			data, err := os.ReadFile(p)
			if err != nil {
				result.Skipped = append(result.Skipped, SkippedFile{filepath.Base(p), err.Error()})
				continue
			}
			entries = append(entries, importEntry{name: filepath.Base(p), content: data})
		default:
			result.Skipped = append(result.Skipped, SkippedFile{filepath.Base(p), "unsupported extension"})
		}
	}

	// Stage 2: parse + resolve. We do DNS resolution serially (the
	// system resolver caches across calls and goroutine fanout would
	// risk hitting per-process socket limits at 600+ entries).
	total := len(entries)
	for i, e := range entries {
		emit(PoolImportProgress{
			Stage:    "parsing",
			Current:  i + 1,
			Total:    total,
			Imported: len(result.Members),
			Skipped:  len(result.Skipped),
		})

		protocol := detectProtocolFromFilename(e.name)
		if protocol == "" {
			result.Skipped = append(result.Skipped, SkippedFile{e.name, "unsupported extension"})
			continue
		}

		host := extractEndpointHost(protocol, string(e.content))
		if host == "" {
			result.Skipped = append(result.Skipped, SkippedFile{e.name, "no endpoint in config"})
			continue
		}

		country := ""
		if pi.geo != nil {
			ip := resolveHostToIP(host)
			if ip != nil {
				if c, err := pi.geo.CountryCode(ip); err == nil {
					country = c
				}
			}
		}

		nameWithoutExt := strings.TrimSuffix(e.name, filepath.Ext(e.name))
		member := &PoolMember{
			ID:   uuid.New().String(),
			Name: nameWithoutExt,
			Config: &ProtocolConfig{
				Protocol:      protocol,
				ConfigContent: string(e.content),
				Filename:      e.name,
				ServerAddress: host,
				AddedAt:       time.Now().Format(time.RFC3339),
			},
			Country: country,
			Region:  geoip.Region(country),
			Active:  true,
		}
		result.Members = append(result.Members, member)
	}

	emit(PoolImportProgress{
		Stage:    "done",
		Current:  total,
		Total:    total,
		Imported: len(result.Members),
		Skipped:  len(result.Skipped),
	})

	log.Printf("Pool import: %d imported, %d skipped from %d input paths",
		len(result.Members), len(result.Skipped), len(paths))
	return result, nil
}

// extractZipEntries pulls every .conf / .ovpn / .sswan file out of a
// ZIP into memory. Skips directories, README files, and oversized
// entries (>1 MB per file is treated as "not a config").
func extractZipEntries(path string) ([]importEntry, error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer r.Close()

	const maxConfigSize = 1024 * 1024 // 1 MB - generous for the largest expected .ovpn with inline certs

	var entries []importEntry
	for _, f := range r.File {
		if f.FileInfo().IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(f.Name))
		if ext != ".conf" && ext != ".ovpn" && ext != ".sswan" {
			continue
		}
		if f.UncompressedSize64 > maxConfigSize {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(rc, maxConfigSize))
		rc.Close()
		if err != nil {
			continue
		}
		entries = append(entries, importEntry{name: filepath.Base(f.Name), content: data})
	}
	return entries, nil
}

// detectProtocolFromFilename returns the protocol id from a file
// extension, or "" if unrecognised.
func detectProtocolFromFilename(filename string) string {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".conf":
		return "wireguard"
	case ".ovpn":
		return "openvpn"
	case ".sswan":
		return "ipsec"
	}
	return ""
}

// extractEndpointHost returns the server hostname or IP from a config.
// Lightweight: does not validate the rest of the file, just enough to
// drive the country lookup. Returns "" if no endpoint is detectable.
//
// The function is deliberately separate from the protocol-specific
// parsers in protocol_*.go - those run heavier validation paths that
// touch system files (e.g. WireGuard tunnel-name registration). For
// Pool import we want pure parse-only.
func extractEndpointHost(protocol, content string) string {
	switch protocol {
	case "wireguard":
		return extractWireGuardEndpoint(content)
	case "openvpn":
		return extractOpenVPNEndpoint(content)
	case "ipsec":
		return extractIPSecEndpoint(content)
	}
	return ""
}

func extractWireGuardEndpoint(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(strings.ToLower(line), "endpoint") {
			continue
		}
		eq := strings.Index(line, "=")
		if eq < 0 {
			continue
		}
		hp := strings.TrimSpace(line[eq+1:])
		host, _, err := net.SplitHostPort(hp)
		if err == nil && host != "" {
			return host
		}
	}
	return ""
}

func extractOpenVPNEndpoint(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "remote ") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			return parts[1]
		}
	}
	return ""
}

// extractIPSecEndpoint pulls the first "addr" or "remote.addr" string
// value out of a .sswan JSON document. Avoids importing encoding/json
// for what amounts to a single-field lookup - the file format is
// stable enough that substring search is fine.
func extractIPSecEndpoint(content string) string {
	keys := []string{`"addr"`, `"remote.addr"`, `"remote_addr"`}
	for _, key := range keys {
		i := strings.Index(content, key)
		if i < 0 {
			continue
		}
		rest := content[i+len(key):]
		colon := strings.Index(rest, ":")
		if colon < 0 {
			continue
		}
		rest = strings.TrimSpace(rest[colon+1:])
		if len(rest) == 0 || rest[0] != '"' {
			continue
		}
		end := strings.Index(rest[1:], `"`)
		if end <= 0 {
			continue
		}
		return rest[1 : 1+end]
	}
	return ""
}

// resolveHostToIP returns the first IPv4 (preferred) or IPv6 address
// of host. If host is already a literal IP, that is returned directly.
// Returns nil on any failure - callers treat that as "country unknown"
// rather than a hard import error, since a stale upstream DNS or
// transient network failure should not abort a 600-config import.
func resolveHostToIP(host string) net.IP {
	if ip := net.ParseIP(host); ip != nil {
		return ip
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return nil
	}
	// MMDB country databases are IPv4-leaning; prefer v4 for higher hit rate.
	for _, ip := range ips {
		if v4 := ip.To4(); v4 != nil {
			return v4
		}
	}
	if len(ips) > 0 {
		return ips[0]
	}
	return nil
}
