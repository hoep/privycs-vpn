package main

import (
	"archive/zip"
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type stubGeo struct {
	countries map[string]string // ip-string → country
}

func (s *stubGeo) CountryCode(ip net.IP) (string, error) {
	if cc, ok := s.countries[ip.String()]; ok {
		return cc, nil
	}
	return "", nil
}

func TestDetectProtocolFromFilename(t *testing.T) {
	cases := map[string]string{
		"server.conf":          "wireguard",
		"path/to/x.OVPN":       "openvpn",
		"profile.sswan":        "ipsec",
		"readme.txt":           "",
		"":                     "",
	}
	for name, want := range cases {
		if got := detectProtocolFromFilename(name); got != want {
			t.Errorf("detectProtocolFromFilename(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestExtractWireGuardEndpoint(t *testing.T) {
	wg := `[Interface]
PrivateKey = aaa
Address = 10.0.0.2/24
DNS = 1.1.1.1

[Peer]
PublicKey = bbb
Endpoint = de-fra-wg-002.example.org:51820
AllowedIPs = 0.0.0.0/0
`
	if got := extractWireGuardEndpoint(wg); got != "de-fra-wg-002.example.org" {
		t.Errorf("extractWireGuardEndpoint = %q, want de-fra-wg-002.example.org", got)
	}
}

func TestExtractWireGuardEndpoint_NoEndpoint(t *testing.T) {
	wg := `[Interface]
PrivateKey = aaa
Address = 10.0.0.2/24
`
	if got := extractWireGuardEndpoint(wg); got != "" {
		t.Errorf("expected empty for config without Endpoint, got %q", got)
	}
}

func TestExtractOpenVPNEndpoint(t *testing.T) {
	ovpn := `client
dev tun
proto udp
remote vpn-us-east-3.example.org 1194
verb 3
`
	if got := extractOpenVPNEndpoint(ovpn); got != "vpn-us-east-3.example.org" {
		t.Errorf("extractOpenVPNEndpoint = %q, want vpn-us-east-3.example.org", got)
	}
}

func TestExtractIPSecEndpoint(t *testing.T) {
	sswan := `{"uuid":"abc","name":"office","type":"ikev2-eap","remote":{"addr":"ipsec.example.org"},"local":{}}`
	if got := extractIPSecEndpoint(sswan); got != "ipsec.example.org" {
		t.Errorf("extractIPSecEndpoint = %q, want ipsec.example.org", got)
	}
}

// makeTestZip writes a zip with named entries and returns the path.
func makeTestZip(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "pool.zip")

	buf := &bytes.Buffer{}
	w := zip.NewWriter(buf)
	for name, content := range files {
		fw, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestImport_ZipMixedContent(t *testing.T) {
	zipPath := makeTestZip(t, map[string]string{
		"de-fra-001.conf": `[Peer]
Endpoint = 1.2.3.4:51820
`,
		"us-nyc-001.ovpn": `client
remote 5.6.7.8 1194
`,
		"office.sswan":  `{"remote":{"addr":"9.10.11.12"}}`,
		"README.txt":    "ignore me",
		"subdir/":       "",
		"empty.txt":     "",
		"jp-tok-001.conf": `[Peer]
Endpoint = 13.14.15.16:51820
`,
	})

	geo := &stubGeo{countries: map[string]string{
		"1.2.3.4":     "DE",
		"5.6.7.8":     "US",
		"9.10.11.12":  "GB",
		"13.14.15.16": "JP",
	}}
	importer := NewPoolImporter(geo)

	progressEvents := 0
	res, err := importer.Import([]string{zipPath}, func(p PoolImportProgress) {
		progressEvents++
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}

	if len(res.Members) != 4 {
		t.Errorf("members = %d, want 4 (1 wg + 1 ovpn + 1 sswan + 1 wg)", len(res.Members))
	}
	if progressEvents == 0 {
		t.Error("expected at least one progress event")
	}

	// All members should have countries resolved (because we passed literal IPs).
	for _, m := range res.Members {
		if m.Country == "" {
			t.Errorf("member %s has empty country", m.Name)
		}
		if m.Region == "" || m.Region == "Other" && m.Country != "" {
			t.Errorf("member %s region = %q for country %q", m.Name, m.Region, m.Country)
		}
	}
}

func TestImport_NoEndpointSkipped(t *testing.T) {
	zipPath := makeTestZip(t, map[string]string{
		"valid.conf": `[Peer]
Endpoint = 1.2.3.4:51820
`,
		"missing-endpoint.conf": `[Interface]
PrivateKey = aaa
`,
	})
	geo := &stubGeo{countries: map[string]string{"1.2.3.4": "DE"}}
	importer := NewPoolImporter(geo)
	res, err := importer.Import([]string{zipPath}, nil)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(res.Members) != 1 {
		t.Errorf("members = %d, want 1", len(res.Members))
	}
	if len(res.Skipped) != 1 {
		t.Errorf("skipped = %d, want 1", len(res.Skipped))
	}
	if len(res.Skipped) >= 1 && !strings.Contains(res.Skipped[0].Reason, "endpoint") {
		t.Errorf("skip reason = %q, want to mention endpoint", res.Skipped[0].Reason)
	}
}

func TestImport_DirectFilesNotZipped(t *testing.T) {
	dir := t.TempDir()
	conf := filepath.Join(dir, "single.conf")
	os.WriteFile(conf, []byte("[Peer]\nEndpoint = 1.2.3.4:51820\n"), 0o600)

	geo := &stubGeo{countries: map[string]string{"1.2.3.4": "DE"}}
	importer := NewPoolImporter(geo)
	res, err := importer.Import([]string{conf}, nil)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(res.Members) != 1 {
		t.Fatalf("members = %d, want 1", len(res.Members))
	}
	if res.Members[0].Country != "DE" {
		t.Errorf("country = %q, want DE", res.Members[0].Country)
	}
}

func TestImport_NilGeoStillImports(t *testing.T) {
	conf := filepath.Join(t.TempDir(), "x.conf")
	os.WriteFile(conf, []byte("[Peer]\nEndpoint = host.example.invalid:51820\n"), 0o600)

	importer := NewPoolImporter(nil)
	res, err := importer.Import([]string{conf}, nil)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(res.Members) != 1 {
		t.Fatalf("members = %d, want 1", len(res.Members))
	}
	if res.Members[0].Country != "" {
		t.Errorf("country = %q, want empty (no geo resolver)", res.Members[0].Country)
	}
}

func TestResolveHostToIP_Literal(t *testing.T) {
	if got := resolveHostToIP("8.8.8.8"); got == nil || got.String() != "8.8.8.8" {
		t.Errorf("resolveHostToIP(8.8.8.8) = %v", got)
	}
	if got := resolveHostToIP("2001:db8::1"); got == nil {
		t.Errorf("resolveHostToIP(IPv6 literal) returned nil")
	}
}

func TestStripPortIfPresent(t *testing.T) {
	cases := map[string]string{
		// Standard host:port forms.
		"1.2.3.4:1194":             "1.2.3.4",
		"vpn.example.com:443":      "vpn.example.com",
		"[2001:db8::1]:51820":      "2001:db8::1",
		// Bare host - no port to strip.
		"1.2.3.4":                  "1.2.3.4",
		"vpn.example.com":          "vpn.example.com",
		"2001:db8::1":              "2001:db8::1", // bare IPv6 must NOT be split as host:port
		"::1":                      "::1",
		// Edge cases.
		"":                         "",
		"  1.2.3.4:1194  ":         "1.2.3.4",
		"[2001:db8::1]":            "2001:db8::1", // bracket-wrapped without port
	}
	for in, want := range cases {
		if got := stripPortIfPresent(in); got != want {
			t.Errorf("stripPortIfPresent(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestExtractEndpointHost_AllFormats verifies the public extractor
// handles every shape we see in real-world configs - including the
// non-standard inline host:port form for OpenVPN that some providers
// emit, and IPSec .sswan with port in addr field.
func TestExtractEndpointHost_AllFormats(t *testing.T) {
	cases := []struct {
		name     string
		protocol string
		content  string
		want     string
	}{
		{
			name:     "WireGuard IPv4 with port",
			protocol: "wireguard",
			content:  "[Peer]\nEndpoint = 198.51.100.5:51820\n",
			want:     "198.51.100.5",
		},
		{
			name:     "WireGuard IPv6 with port",
			protocol: "wireguard",
			content:  "[Peer]\nEndpoint = [2001:db8::1]:51820\n",
			want:     "2001:db8::1",
		},
		{
			name:     "OpenVPN space-separated standard",
			protocol: "openvpn",
			content:  "remote 198.51.100.5 1194 udp\n",
			want:     "198.51.100.5",
		},
		{
			name:     "OpenVPN inline host:port (non-standard)",
			protocol: "openvpn",
			content:  "remote 198.51.100.5:1194\n",
			want:     "198.51.100.5",
		},
		{
			name:     "IPSec sswan with literal addr",
			protocol: "ipsec",
			content:  `{"remote":{"addr":"198.51.100.5"}}`,
			want:     "198.51.100.5",
		},
		{
			name:     "IPSec sswan with addr containing port",
			protocol: "ipsec",
			content:  `{"remote":{"addr":"198.51.100.5:500"}}`,
			want:     "198.51.100.5",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := extractEndpointHost(c.protocol, c.content); got != c.want {
				t.Errorf("extractEndpointHost(%s) = %q, want %q", c.protocol, got, c.want)
			}
		})
	}
}
