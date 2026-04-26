// Package selfip detects the user's public IP and country before any
// VPN tunnel is up. Used by Pool's Geo-Nearest policy to pick the
// geographically closest member, and by the Pool-Detail-View's
// "Country override: Auto (currently AT)" display.
//
// Privacy posture: at most ONE outbound HTTPS GET to a well-known
// self-IP endpoint per detection. Cached for 1 hour by default, and
// invalidated on network change events from NetworkMonitor.OnChange.
// On user-disabled auto-detect, the Detector is never called - the
// Pool reads the user's manually-set country override instead.
//
// Endpoint fallback chain (each with 2s timeout):
//  1. https://1.1.1.1/cdn-cgi/trace            (Cloudflare, plain text)
//  2. https://ipv4.icanhazip.com               (Cloudflare, plain text)
//  3. https://am.i.mullvad.net/ip              (Mullvad, plain text)
//  4. Timezone heuristic (offline, lossy fallback)
package selfip

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hoep/privycs/desktop/geoip"
)

// DefaultCacheTTL is how long a successful detection is reused before
// re-probing. Override via Detector.SetTTL for tests or special use.
const DefaultCacheTTL = 1 * time.Hour

// Result carries one self-IP-detection outcome.
type Result struct {
	IP      net.IP    `json:"ip"`
	Country string    `json:"country"`        // ISO 3166-1 alpha-2; "" if unknown
	Source  string    `json:"source"`         // human label of which endpoint won
	AsOf    time.Time `json:"as_of"`
	Stale   bool      `json:"stale"`          // returned from cache after expiry on network failure
}

// CountryResolver is the subset of geoip.Reader the detector uses.
// Pulled out as an interface so tests can inject a stub without
// shipping an MMDB.
type CountryResolver interface {
	CountryCode(ip net.IP) (string, error)
}

// Detector probes self-IP and resolves it to a country. Goroutine-safe.
type Detector struct {
	geo       CountryResolver
	httpClient *http.Client
	endpoints []endpoint

	mu      sync.Mutex
	cached  *Result
	expires time.Time
	ttl     time.Duration
}

type endpoint struct {
	name string
	url  string
}

// New constructs a Detector with the production endpoint chain.
func New(geo CountryResolver) *Detector {
	return &Detector{
		geo: geo,
		httpClient: &http.Client{
			// Per-attempt timeout is enforced via context inside Detect.
			// This client-level timeout is the upper bound to catch
			// pathological cases where context cancellation does not
			// propagate.
			Timeout: 5 * time.Second,
			Transport: &http.Transport{
				TLSHandshakeTimeout: 2 * time.Second,
				TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
				DisableKeepAlives:   true, // each detection is one-shot
			},
		},
		endpoints: []endpoint{
			{name: "cloudflare-trace", url: "https://1.1.1.1/cdn-cgi/trace"},
			{name: "icanhazip", url: "https://ipv4.icanhazip.com"},
			{name: "mullvad", url: "https://am.i.mullvad.net/ip"},
		},
		ttl: DefaultCacheTTL,
	}
}

// SetTTL overrides the cache TTL. Mainly for tests.
func (d *Detector) SetTTL(ttl time.Duration) {
	d.mu.Lock()
	d.ttl = ttl
	d.mu.Unlock()
}

// Cached returns the most recent result without firing a probe. Returns
// nil if no detection has succeeded yet. Stale=true if the cache is
// expired but still serves as the best-known fallback.
func (d *Detector) Cached() *Result {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cached == nil {
		return nil
	}
	out := *d.cached
	out.Stale = time.Now().After(d.expires)
	return &out
}

// Invalidate forces the next Detect call to re-probe even if the cache
// is fresh. Wired to NetworkMonitor.OnChange so a network roam
// (Ethernet → WiFi, WiFi → cellular, airport switch) triggers a
// fresh country lookup automatically.
func (d *Detector) Invalidate() {
	d.mu.Lock()
	d.expires = time.Time{}
	d.mu.Unlock()
}

// Detect returns the user's country, probing if cache is stale. The
// context bounds the entire fallback chain - if the caller passes a
// short ctx, we may end up returning a stale-cached result with
// Stale=true rather than a fresh one.
func (d *Detector) Detect(ctx context.Context) (*Result, error) {
	d.mu.Lock()
	if d.cached != nil && time.Now().Before(d.expires) {
		out := *d.cached
		d.mu.Unlock()
		return &out, nil
	}
	d.mu.Unlock()

	// Probe in order; first success wins.
	for _, ep := range d.endpoints {
		probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		ip, err := d.probeEndpoint(probeCtx, ep)
		cancel()
		if err != nil {
			log.Printf("selfip: %s failed: %v", ep.name, err)
			continue
		}

		country := ""
		if d.geo != nil {
			if c, err := d.geo.CountryCode(ip); err == nil {
				country = c
			} else {
				log.Printf("selfip: country lookup for %s failed: %v", ip, err)
			}
		}

		res := &Result{
			IP:      ip,
			Country: country,
			Source:  ep.name,
			AsOf:    time.Now(),
		}
		d.cacheStore(res)
		return res, nil
	}

	// Timezone fallback - offline, no network needed.
	if country, ok := timezoneCountry(); ok {
		res := &Result{
			Country: country,
			Source:  "timezone-fallback",
			AsOf:    time.Now(),
		}
		// Timezone result is cached too, but with shorter TTL so a
		// network-recovered probe will replace it sooner.
		d.cacheStore(res)
		return res, nil
	}

	// Total failure - return stale cache if we have one.
	d.mu.Lock()
	if d.cached != nil {
		out := *d.cached
		out.Stale = true
		d.mu.Unlock()
		return &out, nil
	}
	d.mu.Unlock()

	return nil, errors.New("selfip: all endpoints failed and no cache available")
}

func (d *Detector) cacheStore(r *Result) {
	d.mu.Lock()
	d.cached = r
	d.expires = time.Now().Add(d.ttl)
	d.mu.Unlock()
}

// probeEndpoint executes one GET and returns the parsed IP.
func (d *Detector) probeEndpoint(ctx context.Context, ep endpoint) (net.IP, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ep.url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Privycs-VPN/selfip")

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}

	// Cap body to prevent hostile-size response from a misconfigured
	// or compromised endpoint. 8 KB is overkill for what should be
	// <300 bytes (cloudflare trace) or <16 bytes (raw IP).
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if err != nil {
		return nil, err
	}

	ip := parseIPFromBody(string(body))
	if ip == nil {
		return nil, errors.New("response did not contain a valid IP address")
	}
	return ip, nil
}

// parseIPFromBody handles both the cloudflare-trace format
// (key=value\nkey=value\n with one line "ip=1.2.3.4") and the plain
// "1.2.3.4\n" format used by icanhazip and mullvad.
func parseIPFromBody(body string) net.IP {
	body = strings.TrimSpace(body)

	// Plain-text single IP form.
	if ip := net.ParseIP(body); ip != nil {
		return ip
	}

	// cloudflare-trace key=value form.
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "ip=") {
			candidate := strings.TrimSpace(strings.TrimPrefix(line, "ip="))
			if ip := net.ParseIP(candidate); ip != nil {
				return ip
			}
		}
	}
	return nil
}

// SubscribeNetworkChanges wires up the Detector to a NetworkMonitor
// so cache is invalidated on roam events. The change callback fires
// asynchronously - the next Detect call after the change will probe
// fresh. We do NOT eagerly probe on the change because the network
// may not be settled yet (DHCP renewal, captive portal redirect).
type NetworkMonitor interface {
	OnChange(fn func())
}

func (d *Detector) SubscribeNetworkChanges(nm NetworkMonitor) {
	if nm == nil {
		return
	}
	nm.OnChange(func() {
		d.Invalidate()
	})
}

// CountryFor returns just the ISO country, calling Detect with a
// reasonable budget. Convenience for callers that do not care about
// the underlying Result fields.
func (d *Detector) CountryFor(ctx context.Context) string {
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
	}
	res, err := d.Detect(ctx)
	if err != nil || res == nil {
		return ""
	}
	return res.Country
}

// timezoneCountry guesses the user's country from the system timezone
// via a static map of common Olson zones. Far from perfect (timezones
// span multiple countries: e.g. Europe/Berlin covers DE / DK / SE
// historical, America/New_York covers US / CA / partial), but better
// than nothing when all network endpoints fail.
func timezoneCountry() (string, bool) {
	loc := time.Now().Location()
	if loc == nil {
		return "", false
	}
	zone := loc.String()
	if zone == "" || zone == "UTC" || zone == "Local" {
		return "", false
	}
	if cc, ok := timezoneToCountry[zone]; ok {
		return cc, true
	}
	return "", false
}

var timezoneToCountry = map[string]string{
	"Europe/Vienna":      "AT",
	"Europe/Berlin":      "DE",
	"Europe/Zurich":      "CH",
	"Europe/Paris":       "FR",
	"Europe/London":      "GB",
	"Europe/Madrid":      "ES",
	"Europe/Rome":        "IT",
	"Europe/Amsterdam":   "NL",
	"Europe/Brussels":    "BE",
	"Europe/Stockholm":   "SE",
	"Europe/Oslo":        "NO",
	"Europe/Copenhagen":  "DK",
	"Europe/Helsinki":    "FI",
	"Europe/Warsaw":      "PL",
	"Europe/Prague":      "CZ",
	"Europe/Budapest":    "HU",
	"Europe/Athens":      "GR",
	"Europe/Bucharest":   "RO",
	"Europe/Sofia":       "BG",
	"Europe/Lisbon":      "PT",
	"Europe/Dublin":      "IE",
	"Europe/Tallinn":     "EE",
	"Europe/Riga":        "LV",
	"Europe/Vilnius":     "LT",
	"Europe/Belgrade":    "RS",
	"Europe/Zagreb":      "HR",
	"Europe/Ljubljana":   "SI",
	"Europe/Bratislava":  "SK",
	"Europe/Kiev":        "UA",
	"Europe/Kyiv":        "UA",
	"Europe/Moscow":      "RU",

	"America/New_York":     "US",
	"America/Los_Angeles":  "US",
	"America/Chicago":      "US",
	"America/Denver":       "US",
	"America/Phoenix":      "US",
	"America/Anchorage":    "US",
	"America/Toronto":      "CA",
	"America/Vancouver":    "CA",
	"America/Montreal":     "CA",
	"America/Mexico_City":  "MX",
	"America/Sao_Paulo":    "BR",
	"America/Buenos_Aires": "AR",
	"America/Santiago":     "CL",
	"America/Bogota":       "CO",
	"America/Lima":         "PE",

	"Asia/Tokyo":      "JP",
	"Asia/Seoul":      "KR",
	"Asia/Shanghai":   "CN",
	"Asia/Hong_Kong":  "HK",
	"Asia/Taipei":     "TW",
	"Asia/Singapore":  "SG",
	"Asia/Bangkok":    "TH",
	"Asia/Kuala_Lumpur": "MY",
	"Asia/Jakarta":    "ID",
	"Asia/Manila":     "PH",
	"Asia/Kolkata":    "IN",
	"Asia/Calcutta":   "IN",
	"Asia/Dubai":      "AE",
	"Asia/Tehran":     "IR",
	"Asia/Jerusalem":  "IL",
	"Asia/Istanbul":   "TR",

	"Australia/Sydney":   "AU",
	"Australia/Melbourne": "AU",
	"Australia/Brisbane": "AU",
	"Australia/Perth":    "AU",
	"Pacific/Auckland":   "NZ",

	"Africa/Johannesburg": "ZA",
	"Africa/Cairo":        "EG",
	"Africa/Lagos":        "NG",
	"Africa/Nairobi":      "KE",
}

// EnsureFromGeoIP returns a Detector wired to the process-singleton
// MMDB reader. If the MMDB is missing the detector still works, but
// returned Results carry Country="" - callers should treat that as
// "Geo-Nearest unavailable, fall back to Random".
func EnsureFromGeoIP() *Detector {
	r, err := geoip.Default()
	if err != nil {
		log.Printf("selfip: geoip unavailable (%v); detection will probe but country will be empty", err)
		return New(nil)
	}
	return New(r)
}
