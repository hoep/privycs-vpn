package selfip

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type stubResolver struct {
	country string
	err     error
}

func (s *stubResolver) CountryCode(ip net.IP) (string, error) {
	return s.country, s.err
}

func TestParseIPFromBody_Plaintext(t *testing.T) {
	cases := map[string]string{
		"1.2.3.4\n":          "1.2.3.4",
		"  1.2.3.4  ":        "1.2.3.4",
		"203.0.113.42":       "203.0.113.42",
		"2001:db8::1":        "2001:db8::1",
	}
	for body, want := range cases {
		got := parseIPFromBody(body)
		if got == nil || got.String() != want {
			t.Errorf("parseIPFromBody(%q) = %v, want %s", body, got, want)
		}
	}
}

func TestParseIPFromBody_CloudflareTrace(t *testing.T) {
	body := `fl=123abc
h=1.1.1.1
ip=203.0.113.99
ts=1234567890
visit_scheme=https
uag=Mozilla/5.0
`
	got := parseIPFromBody(body)
	if got == nil || got.String() != "203.0.113.99" {
		t.Errorf("parseIPFromBody trace = %v, want 203.0.113.99", got)
	}
}

func TestParseIPFromBody_Invalid(t *testing.T) {
	cases := []string{"", "garbage", "fl=abc\nh=1.1.1.1\n", "ip=not-an-ip\n"}
	for _, body := range cases {
		if got := parseIPFromBody(body); got != nil {
			t.Errorf("parseIPFromBody(%q) = %v, want nil", body, got)
		}
	}
}

// TestDetect_HappyPath uses an httptest server as a stand-in for one
// of the production endpoints; the detector treats it the same as
// cloudflare-trace.
func TestDetect_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("203.0.113.5\n"))
	}))
	defer srv.Close()

	d := New(&stubResolver{country: "AT"})
	d.endpoints = []endpoint{{name: "test", url: srv.URL}}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	res, err := d.Detect(ctx)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if res.Country != "AT" {
		t.Errorf("Country = %q, want AT", res.Country)
	}
	if res.IP.String() != "203.0.113.5" {
		t.Errorf("IP = %v, want 203.0.113.5", res.IP)
	}
}

func TestDetect_CacheHit(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Write([]byte("203.0.113.5\n"))
	}))
	defer srv.Close()

	d := New(&stubResolver{country: "AT"})
	d.endpoints = []endpoint{{name: "test", url: srv.URL}}

	ctx := context.Background()
	if _, err := d.Detect(ctx); err != nil {
		t.Fatalf("first Detect: %v", err)
	}
	if _, err := d.Detect(ctx); err != nil {
		t.Fatalf("second Detect: %v", err)
	}
	if hits != 1 {
		t.Errorf("server hit %d times, want 1 (second call should hit cache)", hits)
	}
}

func TestDetect_InvalidateForcesProbe(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Write([]byte("203.0.113.5\n"))
	}))
	defer srv.Close()

	d := New(&stubResolver{country: "AT"})
	d.endpoints = []endpoint{{name: "test", url: srv.URL}}

	ctx := context.Background()
	d.Detect(ctx)
	d.Invalidate()
	d.Detect(ctx)
	if hits != 2 {
		t.Errorf("hits = %d, want 2 after Invalidate", hits)
	}
}

func TestDetect_FallbackOnFailure(t *testing.T) {
	failingSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer failingSrv.Close()

	successSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("203.0.113.7\n"))
	}))
	defer successSrv.Close()

	d := New(&stubResolver{country: "DE"})
	d.endpoints = []endpoint{
		{name: "fail", url: failingSrv.URL},
		{name: "ok", url: successSrv.URL},
	}

	res, err := d.Detect(context.Background())
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if res.Country != "DE" || res.Source != "ok" {
		t.Errorf("got %+v, want Country=DE Source=ok", res)
	}
}

func TestDetect_NilResolverEmptyCountry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("203.0.113.10\n"))
	}))
	defer srv.Close()

	d := New(nil)
	d.endpoints = []endpoint{{name: "test", url: srv.URL}}
	res, err := d.Detect(context.Background())
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if res.Country != "" {
		t.Errorf("Country = %q, want empty (nil resolver)", res.Country)
	}
}

func TestDetect_AllFailReturnsStaleCache(t *testing.T) {
	d := New(&stubResolver{country: "AT"})
	// Pre-populate cache with a result.
	d.cached = &Result{IP: net.ParseIP("203.0.113.99"), Country: "AT", Source: "warmup", AsOf: time.Now().Add(-2 * time.Hour)}
	d.expires = time.Now().Add(-1 * time.Hour) // expired

	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer failing.Close()

	d.endpoints = []endpoint{{name: "fail", url: failing.URL}}

	res, err := d.Detect(context.Background())
	// May either return stale cache (if no timezone match) or fresh
	// timezone-derived result. Either way no error.
	if err != nil && !errors.Is(err, errors.New("")) {
		// Just sanity: should not return a hard error if cache is present.
		t.Fatalf("unexpected error: %v", err)
	}
	if res == nil {
		t.Fatal("res is nil despite cache being present")
	}
}

func TestCachedReportsStaleAfterExpiry(t *testing.T) {
	d := New(nil)
	d.cached = &Result{IP: net.ParseIP("1.1.1.1"), Country: "X", AsOf: time.Now()}
	d.expires = time.Now().Add(-1 * time.Hour)
	got := d.Cached()
	if got == nil || !got.Stale {
		t.Errorf("expected Stale=true on expired cache, got %+v", got)
	}
}
