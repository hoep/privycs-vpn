// fetchgeoip downloads the GeoLite2 Country MMDB used by the Pool
// feature into desktop/geoip/Country.mmdb. Cross-platform (no bash
// requirement), invoked from CI before `wails build` and runnable
// locally via `go run scripts/fetchgeoip/main.go` from the desktop/
// directory.
//
// Source: sapics/ip-location-db (CC-BY-4.0 / MIT permissive mix,
// redistributable). See assets/geoip/README.md for licensing notes.
package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

const (
	// Combined IPv4+IPv6 country database from sapics/ip-location-db.
	// The sibling file `geolite2-country-ipv4.mmdb` (which we used in
	// earlier releases) only carries IPv4 ranges - IPv6-only endpoints
	// would silently miss the country lookup. The combined `.mmdb`
	// file is ~6 MB total (still under the binary-size budget) and
	// covers both stacks.
	sourceURL = "https://github.com/sapics/ip-location-db/raw/main/geolite2-country-mmdb/geolite2-country.mmdb"
	maxAge    = 7 * 24 * time.Hour // skip download if existing file is newer than this
)

func main() {
	dbPath := destinationPath()

	if !needsDownload(dbPath) {
		log.Printf("fetchgeoip: %s is fresh, skipping", dbPath)
		return
	}

	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		log.Fatalf("fetchgeoip: mkdir: %v", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(dbPath), "Country.mmdb.*.tmp")
	if err != nil {
		log.Fatalf("fetchgeoip: tempfile: %v", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // best-effort cleanup if rename fails

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Get(sourceURL)
	if err != nil {
		tmp.Close()
		log.Fatalf("fetchgeoip: GET %s: %v", sourceURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		tmp.Close()
		log.Fatalf("fetchgeoip: %s -> HTTP %d", sourceURL, resp.StatusCode)
	}

	written, err := io.Copy(tmp, resp.Body)
	tmp.Close()
	if err != nil {
		log.Fatalf("fetchgeoip: download: %v", err)
	}
	if written < 1024*1024 {
		log.Fatalf("fetchgeoip: downloaded file is only %d bytes - source likely returned an error page", written)
	}

	if err := os.Rename(tmpPath, dbPath); err != nil {
		log.Fatalf("fetchgeoip: rename %s -> %s: %v", tmpPath, dbPath, err)
	}

	log.Printf("fetchgeoip: saved %s (%d bytes) [%s]", dbPath, written, runtime.GOOS)
}

// destinationPath resolves where to write the MMDB. Order:
//  1. PRIVYCS_GEOIP_DB env var (operator override / test fixtures)
//  2. <this-source's-dir>/../../geoip/Country.mmdb (production layout
//     when invoked via `go run` from desktop/)
//  3. ./geoip/Country.mmdb (CWD-relative fallback, for direct calls)
func destinationPath() string {
	if env := os.Getenv("PRIVYCS_GEOIP_DB"); env != "" {
		return env
	}

	// runtime.Caller(0) returns this source file's path. Walk up two
	// directories (out of scripts/fetchgeoip, into desktop/) and into
	// geoip/. This works regardless of where `go run` was invoked from.
	if _, thisFile, _, ok := runtime.Caller(0); ok {
		anchor := filepath.Dir(filepath.Dir(filepath.Dir(thisFile))) // scripts/fetchgeoip → scripts → desktop
		candidate := filepath.Join(anchor, "geoip", "Country.mmdb")
		if abs, err := filepath.Abs(candidate); err == nil {
			return abs
		}
	}

	// Last-ditch: CWD-relative.
	return filepath.Join("geoip", "Country.mmdb")
}

// needsDownload returns true if the existing file is missing, smaller
// than the placeholder threshold (we recognise the placeholder as
// "must download"), or older than maxAge.
func needsDownload(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return true
	}
	// The committed placeholder is ~74 bytes; real MMDB is ~6 MB. Anything
	// below 256 bytes means we have the placeholder.
	if info.Size() < 256 {
		return true
	}
	if time.Since(info.ModTime()) > maxAge {
		return true
	}
	return false
}

// printUsage when invoked with -h / --help. Manual contract for
// developers running the script directly.
func init() {
	for _, a := range os.Args[1:] {
		if a == "-h" || a == "--help" {
			fmt.Println(`fetchgeoip - download GeoLite2 Country MMDB for the Pool feature

Usage:
  go run scripts/fetchgeoip/main.go

Environment:
  PRIVYCS_GEOIP_DB   override destination path (operator / CI use)

Skips download when an existing file is recent (<7 days) and larger
than the placeholder threshold. Source: sapics/ip-location-db
(CC-BY-4.0 / MIT).`)
			os.Exit(0)
		}
	}
}
