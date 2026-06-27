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
	// GeoLite2-Country MMDB (IPv4 + IPv6) from P3TERX/GeoLite.mmdb, a
	// weekly-rebuilt mirror on the stable `download` branch (~9 MB,
	// under the binary-size budget). This replaced sapics/ip-location-db's
	// `geolite2-country-mmdb/geolite2-country.mmdb`, which started 404ing
	// in mid-2026 when that project dropped its MMDB outputs and kept only
	// CSV. The reader (geoip/geoip.go) decodes both the official nested
	// `country.iso_code` schema this file uses and the flat `country_code`
	// schema, so the source is swappable without touching the lookup code.
	sourceURL = "https://github.com/P3TERX/GeoLite.mmdb/raw/download/GeoLite2-Country.mmdb"
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

	// Retry with exponential backoff. github.com/raw frequently
	// returns transient 502 / 503 / 504 during CI peak hours; one
	// attempt is too brittle and blocks the entire release pipeline.
	// 3 tries with 2s / 4s / 8s waits absorb most flakes without
	// holding up successful fetches.
	const maxAttempts = 3
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		written, err := tryFetch(dbPath)
		if err == nil {
			log.Printf("fetchgeoip: saved %s (%d bytes) [%s]", dbPath, written, runtime.GOOS)
			return
		}
		lastErr = err
		log.Printf("fetchgeoip: attempt %d/%d failed: %v", attempt, maxAttempts, err)
		if attempt < maxAttempts {
			wait := time.Duration(1<<attempt) * time.Second
			time.Sleep(wait)
		}
	}
	log.Fatalf("fetchgeoip: %d attempts failed, last error: %v", maxAttempts, lastErr)
}

// tryFetch performs a single download. Returns (bytes-written, nil)
// on success or (0, err) on any failure. Caller decides whether to
// retry.
func tryFetch(dbPath string) (int64, error) {
	tmp, err := os.CreateTemp(filepath.Dir(dbPath), "Country.mmdb.*.tmp")
	if err != nil {
		return 0, fmt.Errorf("tempfile: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Get(sourceURL)
	if err != nil {
		tmp.Close()
		return 0, fmt.Errorf("GET %s: %w", sourceURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		tmp.Close()
		return 0, fmt.Errorf("%s -> HTTP %d", sourceURL, resp.StatusCode)
	}

	written, err := io.Copy(tmp, resp.Body)
	tmp.Close()
	if err != nil {
		return 0, fmt.Errorf("download: %w", err)
	}
	if written < 1024*1024 {
		return 0, fmt.Errorf("downloaded file is only %d bytes - source likely returned an error page", written)
	}

	if err := os.Rename(tmpPath, dbPath); err != nil {
		return 0, fmt.Errorf("rename %s -> %s: %w", tmpPath, dbPath, err)
	}
	return written, nil
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
