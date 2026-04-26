package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	Message  string `json:"message,omitempty"`  // current hostname being resolved (during "resolving" stage)
}

// dnsLookupConcurrency caps how many DNS lookups can run in parallel.
// 20 chosen empirically: high enough that a 600-entry Mullvad ZIP
// finishes in ~30s typical / 60s with cache misses, low enough that
// we don't drown the system resolver or hit per-process socket
// limits. Net library uses one goroutine per Lookup so 20 is a safe
// ceiling on typical desktops (1024+ socket fd budget).
const dnsLookupConcurrency = 20

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

// PoolUpload is one file the frontend has already loaded into memory.
// Used by ImportFromUploads which is the production path - desktop
// browsers (Wails WebView) do not expose absolute filesystem paths
// to JS, so we cannot use os.ReadFile from the backend. Instead the
// frontend FileReader reads each file and ships the bytes via Wails.
type PoolUpload struct {
	Filename string `json:"filename"`
	Content  []byte `json:"content"`
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
// ZIP file at the given path. Convenience for tests and CLI use; the
// production import path uses extractZipEntriesFromReader so the
// frontend can ship ZIP bytes through Wails without filesystem access.
func extractZipEntries(path string) ([]importEntry, error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return extractZipEntriesCommon(&r.Reader)
}

// extractZipEntriesFromBytes reads ZIP bytes from memory. Used by
// ImportFromUploads when the frontend has loaded a .zip via
// FileReader.readAsArrayBuffer.
func extractZipEntriesFromBytes(data []byte) ([]importEntry, error) {
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	return extractZipEntriesCommon(r)
}

// extractZipEntriesCommon walks a *zip.Reader. Skips directories,
// README files, and oversized entries (>1 MB per file is treated as
// "not a config").
func extractZipEntriesCommon(r *zip.Reader) ([]importEntry, error) {
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

// ImportFromUploads is the production import path: the frontend has
// already loaded each file via FileReader (text or arraybuffer) and
// shipped the bytes here. We do not touch the filesystem - everything
// happens in memory.
//
// Pipeline:
//  1. Extract: unpack ZIPs in-memory, surface direct config files
//  2. Parse: detect protocol + extract endpoint hostname for each
//     entry. Cheap, runs in <1ms per entry.
//  3. Resolve: bulk-resolve hostnames to IPs via a worker pool
//     (sequential DNS makes a 600-entry import 5+ minutes). Each
//     completion bumps the progress counter.
//  4. Country lookup: synchronous MMDB hit per resolved IP.
//
// Stage 3 is where the wall-clock time goes; this is what the user
// sees as "import is running" in the progress toast.
func (pi *PoolImporter) ImportFromUploads(uploads []PoolUpload, onProgress func(PoolImportProgress)) (*PoolImportResult, error) {
	result := &PoolImportResult{}

	emit := func(p PoolImportProgress) {
		if onProgress != nil {
			onProgress(p)
		}
	}

	// Stage 1: unpack uploads into a flat list of entries.
	emit(PoolImportProgress{Stage: "extracting"})
	var entries []importEntry
	for _, up := range uploads {
		ext := strings.ToLower(filepath.Ext(up.Filename))
		switch ext {
		case ".zip":
			extracted, err := extractZipEntriesFromBytes(up.Content)
			if err != nil {
				result.Skipped = append(result.Skipped, SkippedFile{up.Filename, "zip parse: " + err.Error()})
				continue
			}
			entries = append(entries, extracted...)
		case ".conf", ".ovpn", ".sswan":
			entries = append(entries, importEntry{name: filepath.Base(up.Filename), content: up.Content})
		default:
			result.Skipped = append(result.Skipped, SkippedFile{up.Filename, "unsupported extension"})
		}
	}

	// Stage 2: parse - for each entry detect protocol and extract
	// endpoint host. Stage results carry into Stage 3's worker pool.
	type parsed struct {
		entry    importEntry
		protocol string
		host     string
	}
	parsedList := make([]parsed, 0, len(entries))
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
		parsedList = append(parsedList, parsed{entry: e, protocol: protocol, host: host})
	}

	// Stage 3: resolve hostnames in parallel. Members are appended in
	// their original parse order so the final pool keeps a stable
	// sort - even though the resolutions complete out of order.
	resolveTotal := len(parsedList)
	emit(PoolImportProgress{
		Stage:    "resolving",
		Current:  0,
		Total:    resolveTotal,
		Imported: len(result.Members),
		Skipped:  len(result.Skipped),
	})

	type resolved struct {
		index   int
		country string
	}

	// Bounded worker pool: dnsLookupConcurrency goroutines pull jobs
	// off `jobs`, push results onto `results`. The producer publishes
	// jobs in order; the consumer reads results out-of-order and
	// updates a per-index slot. We do not abort on errors - a failed
	// lookup yields country="" and the member still imports (Geo-
	// Nearest will skip them or treat as Other-region).
	type job struct {
		index int
		host  string
	}
	jobs := make(chan job, resolveTotal)
	results := make(chan resolved, resolveTotal)

	var wg sync.WaitGroup
	for w := 0; w < dnsLookupConcurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				cc := ""
				if pi.geo != nil {
					if ip := resolveHostToIP(j.host); ip != nil {
						if c, err := pi.geo.CountryCode(ip); err == nil {
							cc = c
						}
					}
				}
				results <- resolved{index: j.index, country: cc}
			}
		}()
	}
	for i, p := range parsedList {
		jobs <- job{index: i, host: p.host}
	}
	close(jobs)

	// Closer goroutine - signals end of results channel after workers drain.
	go func() {
		wg.Wait()
		close(results)
	}()

	countries := make([]string, resolveTotal)
	completed := 0
	for r := range results {
		countries[r.index] = r.country
		completed++
		// Emit on every completion. With 600 entries that's 600
		// events - negligible compared to the IPC overhead the
		// frontend already absorbs for status polls. The hostname
		// of the most-recently-completed entry is the "Message"
		// field so the user sees the toast text change even when
		// the counter pauses on a slow DNS server.
		if completed%5 == 0 || completed == resolveTotal {
			emit(PoolImportProgress{
				Stage:    "resolving",
				Current:  completed,
				Total:    resolveTotal,
				Imported: completed,
				Skipped:  len(result.Skipped),
				Message:  parsedList[r.index].host,
			})
		}
	}

	// Assemble members in original order using the resolved countries.
	for i, p := range parsedList {
		nameWithoutExt := strings.TrimSuffix(p.entry.name, filepath.Ext(p.entry.name))
		country := countries[i]
		member := &PoolMember{
			ID:   uuid.New().String(),
			Name: nameWithoutExt,
			Config: &ProtocolConfig{
				Protocol:      p.protocol,
				ConfigContent: string(p.entry.content),
				Filename:      p.entry.name,
				ServerAddress: p.host,
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
		Current:  resolveTotal,
		Total:    resolveTotal,
		Imported: len(result.Members),
		Skipped:  len(result.Skipped),
	})

	log.Printf("Pool import (uploads): %d imported, %d skipped from %d uploads",
		len(result.Members), len(result.Skipped), len(uploads))
	return result, nil
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
