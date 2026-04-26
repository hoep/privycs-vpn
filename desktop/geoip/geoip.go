// Package geoip wraps the MaxMind GeoLite2-Country MMDB database for
// resolving an IP address to an ISO 3166-1 alpha-2 country code. The
// database is shipped as a static asset in assets/geoip/Country.mmdb.
//
// The wrapper exists so the rest of the codebase does not import the
// MaxMind library directly - if we ever swap to a different IP-to-country
// data source (db-ip lite, ip-location-db, etc.) only this file changes.
package geoip

import (
	_ "embed"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"

	"github.com/oschwald/maxminddb-golang"
)

// DefaultDBFilename is the filename of the MMDB that operators can
// substitute via PRIVYCS_GEOIP_DB or place beside the executable.
const DefaultDBFilename = "Country.mmdb"

// embeddedDB is baked into the binary at build time via go:embed. The
// repo commits a tiny placeholder file at geoip/Country.mmdb so the
// build always succeeds; CI's preBuildHook overwrites it with the
// real GeoLite2-Country MMDB before linking. If the placeholder is
// what gets embedded (because someone built without running
// scripts/fetch-geoip.sh), geoip2.FromBytes will return an error and
// Default() falls through to the disk-lookup chain.
//
//go:embed Country.mmdb
var embeddedDB []byte

// Reader is a thin wrapper around the MMDB reader that exposes only
// the operations Pool selection needs. Goroutine-safe (the underlying
// maxminddb reader is documented as concurrent-read-safe).
//
// We use maxminddb-golang directly instead of geoip2-golang because
// the latter validates the database type identifier against the
// hardcoded suffix "Country", rejecting any MMDB whose type string
// does not match (e.g. sapics/ip-location-db's combined v4+v6 file
// uses the type "country ipvAll" which is functionally identical
// but textually different). The lower-level maxminddb library has
// no such check and reads any country-shaped database.
type Reader struct {
	r *maxminddb.Reader
}

// countryRecord is the subset of fields we extract per IP lookup.
// Two schemas in the wild:
//   - MaxMind GeoLite2 / GeoIP2 official: nested country.iso_code
//   - sapics/ip-location-db combined and similar redistributions:
//     flat country_code at the root
// We declare both and pick whichever the MMDB populated. The
// ToISOCode method centralises the precedence so callers always get
// a single string.
type countryRecord struct {
	// MaxMind official format
	Country struct {
		ISOCode string `maxminddb:"iso_code"`
	} `maxminddb:"country"`
	// sapics / db-ip flat format
	FlatCountryCode string `maxminddb:"country_code"`
}

// ToISOCode returns the most specific country code the record
// carries. Prefers the flat field because that's what our shipped
// MMDB uses; falls back to the nested form so a privately-shipped
// MaxMind-format MMDB (set via PRIVYCS_GEOIP_DB) still works.
func (r countryRecord) ToISOCode() string {
	if r.FlatCountryCode != "" {
		return r.FlatCountryCode
	}
	return r.Country.ISOCode
}

var (
	defaultReader     *Reader
	defaultReaderOnce sync.Once
	defaultReaderErr  error
)

// Default returns a process-singleton Reader loaded from the
// platform-specific asset directory. Subsequent calls return the cached
// instance even if the first call failed - the error is sticky so we do
// not retry IO on every Pool resolution.
//
// On failure (DB missing, corrupt, unsupported format) callers should
// fall back to country="UN" (unknown) and surface a one-time warning to
// the user that Geo-Nearest will degrade to Random.
func Default() (*Reader, error) {
	defaultReaderOnce.Do(func() {
		// Disk override wins - operators with custom MMDB or
		// dev-mode users with a fresh fetch can point at it via
		// PRIVYCS_GEOIP_DB.
		if env := os.Getenv("PRIVYCS_GEOIP_DB"); env != "" {
			defaultReader, defaultReaderErr = Open(env)
			return
		}

		// Embedded happy path - the build had a real MMDB linked in.
		// >256 byte threshold rejects the placeholder file committed
		// for go:embed compatibility (the placeholder is ~74 bytes).
		if len(embeddedDB) > 256 {
			r, err := maxminddb.FromBytes(embeddedDB)
			if err == nil {
				defaultReader = &Reader{r: r}
				log.Printf("geoip: loaded embedded MMDB (%d bytes, type=%q)",
					len(embeddedDB), r.Metadata.DatabaseType)
				return
			}
			// Loud failure - embed parse errors are otherwise silent
			// and surface only as "all members country=unknown" in
			// pool imports, which is hard to diagnose without seeing
			// THIS line in the log.
			log.Printf("geoip: embedded MMDB present but parse failed: %v - falling back to disk", err)
		}

		// Disk fallback for dev environments where the binary is run
		// from the repo with assets/ alongside.
		path, err := defaultDBPath()
		if err != nil {
			defaultReaderErr = err
			return
		}
		defaultReader, defaultReaderErr = Open(path)
	})
	return defaultReader, defaultReaderErr
}

// Open loads an MMDB file from disk. Used both by Default() and by
// tests that ship their own fixture DB.
func Open(path string) (*Reader, error) {
	r, err := maxminddb.Open(path)
	if err != nil {
		return nil, fmt.Errorf("geoip: open %s: %w", path, err)
	}
	return &Reader{r: r}, nil
}

// Close releases the mmap backing the reader. Safe to call multiple
// times; subsequent calls are no-ops.
func (r *Reader) Close() error {
	if r == nil || r.r == nil {
		return nil
	}
	err := r.r.Close()
	r.r = nil
	return err
}

// CountryCode returns the ISO 3166-1 alpha-2 country code for ip,
// uppercased. Returns ("", nil) when the IP is in the database but
// has no country attribution (rare, mostly anonymous proxies), AND
// when the IP is a private/reserved range that the MMDB does not
// catalogue (10.x, 192.168.x, 100.64.0.0/10, IPv6 ULAs etc.) - the
// caller should treat empty as "country unknown" without alarm.
func (r *Reader) CountryCode(ip net.IP) (string, error) {
	if r == nil || r.r == nil {
		return "", fmt.Errorf("geoip: reader not initialised")
	}
	if ip == nil {
		return "", fmt.Errorf("geoip: nil IP")
	}
	var rec countryRecord
	if err := r.r.Lookup(ip, &rec); err != nil {
		return "", fmt.Errorf("geoip: lookup %s: %w", ip, err)
	}
	return rec.ToISOCode(), nil
}

// defaultDBPath resolves the bundled MMDB location. Order:
//  1. PRIVYCS_GEOIP_DB env var (operator override, useful for CI)
//  2. <executable-dir>/assets/geoip/Country.mmdb (production layout)
//  3. <cwd>/assets/geoip/Country.mmdb (dev layout)
func defaultDBPath() (string, error) {
	if env := os.Getenv("PRIVYCS_GEOIP_DB"); env != "" {
		if _, err := os.Stat(env); err == nil {
			return env, nil
		}
	}
	exe, err := os.Executable()
	if err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "assets", "geoip", DefaultDBFilename)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	if cwd, err := os.Getwd(); err == nil {
		candidate := filepath.Join(cwd, "assets", "geoip", DefaultDBFilename)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("geoip: %s not found in any standard location", DefaultDBFilename)
}

// Region returns the broad region label for a country code. Used by
// the Round-Robin-Region policy and the Pool-Detail-View coverage
// breakdown. Source taxonomy: UN M.49 with VPN-relevant simplification.
func Region(country string) string {
	if r, ok := countryToRegion[country]; ok {
		return r
	}
	return "Other"
}

// countryToRegion is a static ISO 3166-1 alpha-2 → region map. Kept
// inline rather than loaded from disk because it is small (~250 entries)
// and read-only. Six buckets matches what users intuitively expect from
// "Round-Robin Region" without sub-region complexity.
var countryToRegion = buildCountryRegionMap()

func buildCountryRegionMap() map[string]string {
	m := make(map[string]string, 256)

	europe := []string{"AT", "BE", "BG", "CH", "CY", "CZ", "DE", "DK", "EE", "ES", "FI", "FR", "GB", "GR", "HR", "HU", "IE", "IS", "IT", "LI", "LT", "LU", "LV", "MD", "MT", "NL", "NO", "PL", "PT", "RO", "SE", "SI", "SK", "UA", "RS", "BA", "AL", "MK", "ME", "XK", "BY", "RU"}
	for _, c := range europe {
		m[c] = "Europe"
	}

	northAmerica := []string{"US", "CA", "MX"}
	for _, c := range northAmerica {
		m[c] = "North America"
	}

	asiaPacific := []string{"JP", "KR", "CN", "TW", "HK", "SG", "MY", "TH", "VN", "PH", "ID", "IN", "PK", "BD", "LK", "NP", "MM", "KH", "LA", "MN", "KZ", "UZ", "TJ", "KG", "TM", "AZ", "AM", "GE"}
	for _, c := range asiaPacific {
		m[c] = "Asia-Pacific"
	}

	southAmerica := []string{"BR", "AR", "CL", "CO", "PE", "VE", "EC", "BO", "PY", "UY", "GY", "SR", "GF"}
	for _, c := range southAmerica {
		m[c] = "South America"
	}

	africa := []string{"ZA", "NG", "EG", "KE", "MA", "DZ", "TN", "GH", "ET", "TZ", "UG", "AO", "MZ", "MG", "CM", "CI", "SN", "ZW", "RW", "BW", "NA", "ZM", "MU", "MW", "BI", "BJ", "BF", "ML", "NE", "TD", "GN", "GA", "GW", "GQ", "CG", "CD", "CF", "DJ", "ER", "GM", "LR", "LS", "LY", "SD", "SS", "SO", "SL", "SC", "ST", "SZ", "TG", "EH"}
	for _, c := range africa {
		m[c] = "Africa"
	}

	oceania := []string{"AU", "NZ", "FJ", "PG", "SB", "VU", "NC", "PF", "WS", "TO", "KI", "TV", "NR", "MH", "FM", "PW"}
	for _, c := range oceania {
		m[c] = "Oceania"
	}

	middleEast := []string{"AE", "SA", "IL", "TR", "IR", "IQ", "JO", "LB", "PS", "QA", "KW", "OM", "YE", "BH", "SY", "AF"}
	for _, c := range middleEast {
		m[c] = "Middle East"
	}

	return m
}
