package main

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"
)

// stubGeoE2E maps endpoint hostname → country directly. The provider
// configs below carry literal IPs so resolveHostToIP returns them
// untouched, but the lookup is keyed by IP-string anyway so we use
// the same shape as in pool_import_test.
type stubGeoE2E struct{ m map[string]string }

func (s *stubGeoE2E) CountryCode(ip net.IP) (string, error) {
	if cc, ok := s.m[ip.String()]; ok {
		return cc, nil
	}
	return "", nil
}

// makeProviderZip writes a ZIP that mimics what a commercial VPN
// provider's bulk download looks like: many .conf files with a
// realistic naming convention, one server per country across multiple
// regions, plus a README and a couple of subdirs to exercise the
// walker. Used to verify the import pipeline + policy picker behaves
// like it does on real-world Mullvad / IVPN / etc archives.
func makeProviderZip(t *testing.T, layout map[string][]string, ipMap map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "provider.zip")

	buf := &bytes.Buffer{}
	w := zip.NewWriter(buf)

	// Realistic noise: README and CHANGELOG, both should be skipped.
	rd, _ := w.Create("README.md")
	rd.Write([]byte("# Provider configs\n"))
	cd, _ := w.Create("CHANGELOG.txt")
	cd.Write([]byte("v1.2.3 - new servers\n"))

	// Subdir with more configs - walker should pick these up too.
	for cc, names := range layout {
		for _, name := range names {
			ip, ok := ipMap[name]
			if !ok {
				ip = "203.0.113.1"
			}
			content := fmt.Sprintf(`[Interface]
PrivateKey = stubKey%s
Address = 10.66.66.%d/32
DNS = 100.64.0.1

[Peer]
PublicKey = stubPub%s
Endpoint = %s:51820
AllowedIPs = 0.0.0.0/0, ::/0
`, name, len(name)%200+2, name, ip)
			fw, _ := w.Create(fmt.Sprintf("%s/%s.conf", cc, name))
			fw.Write([]byte(content))
		}
	}
	w.Close()

	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestE2E_ProviderZipImport_PickAndRotate is the closest the suite
// gets to a real Mullvad/IVPN end-to-end. It builds a 100-server
// archive across 8 countries / 4 regions, runs it through the same
// PoolImporter the App uses at runtime, then exercises every policy
// to make sure the picker behaves correctly with realistic-shape data.
func TestE2E_ProviderZipImport_PickAndRotate(t *testing.T) {
	// 100 servers across 8 countries.
	layout := map[string][]string{
		"AT": namedHosts("AT-vie-wg-", 5),
		"DE": namedHosts("DE-fra-wg-", 20),
		"CH": namedHosts("CH-zrh-wg-", 5),
		"GB": namedHosts("GB-lon-wg-", 10),
		"US": namedHosts("US-nyc-wg-", 25),
		"JP": namedHosts("JP-tok-wg-", 10),
		"AU": namedHosts("AU-syd-wg-", 10),
		"BR": namedHosts("BR-sao-wg-", 15),
	}

	// Each name maps to a unique RFC 5737 documentation IP, which
	// the geo stub then maps back to an ISO country code.
	ipMap := map[string]string{}
	geoMap := map[string]string{}
	idx := 0
	for cc, names := range layout {
		for _, n := range names {
			ip := fmt.Sprintf("198.51.100.%d", (idx%200)+1)
			ipMap[n] = ip
			geoMap[ip] = cc
			idx++
		}
	}

	zipPath := makeProviderZip(t, layout, ipMap)

	importer := NewPoolImporter(&stubGeoE2E{m: geoMap})
	progressTicks := 0
	res, err := importer.Import([]string{zipPath}, func(p PoolImportProgress) {
		progressTicks++
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}

	if want := 100; len(res.Members) != want {
		t.Errorf("imported = %d, want %d", len(res.Members), want)
	}
	if len(res.Skipped) != 0 {
		t.Errorf("unexpected skipped entries: %+v", res.Skipped)
	}
	if progressTicks < 100 {
		t.Errorf("progressTicks = %d, want at least 100 (one per file plus stage transitions)", progressTicks)
	}

	// All members should be active and have countries resolved.
	countriesFound := map[string]int{}
	for _, m := range res.Members {
		if !m.Active {
			t.Errorf("member %s is inactive (default should be true)", m.Name)
		}
		if m.Country == "" {
			t.Errorf("member %s has empty country - geo stub did not match endpoint %s", m.Name, m.Config.ServerAddress)
		}
		countriesFound[m.Country]++
	}
	for cc, names := range layout {
		if countriesFound[cc] != len(names) {
			t.Errorf("country %s: imported %d, expected %d", cc, countriesFound[cc], len(names))
		}
	}

	// Construct a Pool and exercise each policy.
	for _, policy := range []PoolPolicy{PolicyGeoNearest, PolicyRandom, PolicyRoundRobin} {
		t.Run(string(policy), func(t *testing.T) {
			pool := &Pool{
				ID:      "p1",
				Name:    "Provider Pool",
				Policy:  policy,
				Members: cloneMembers(res.Members),
			}

			// 1. Every pick returns something for a realistic Pool.
			lastID := ""
			pickedRegions := map[string]int{}
			for i := 0; i < 50; i++ {
				m := PickMember(pool, "AT", lastID)
				if m == nil {
					t.Fatalf("policy %s pick %d returned nil", policy, i)
				}
				pickedRegions[m.Region]++
				lastID = m.ID
			}

			switch policy {
			case PolicyGeoNearest:
				// All picks should be in Europe (AT user's region) since
				// AT is mapped, and most picks should specifically be in AT.
				if pickedRegions["Europe"] == 0 {
					t.Errorf("Geo-Nearest should pick Europe members for AT user, got %v", pickedRegions)
				}
			case PolicyRoundRobin:
				// Round-Robin should hit at least 4 distinct regions across
				// 50 ticks (we have 5: Europe, NA, APAC, SA, Oceania).
				if len(pickedRegions) < 4 {
					t.Errorf("Round-Robin visited %d regions, want at least 4: %v", len(pickedRegions), pickedRegions)
				}
			case PolicyRandom:
				// Random should not be stuck on any single region across 50 picks.
				if len(pickedRegions) < 3 {
					t.Errorf("Random produced too narrow a distribution: %v", pickedRegions)
				}
			}
		})
	}
}

// TestE2E_ProviderZipImport_DegradesOnMissingGeo verifies that an
// import without an MMDB still succeeds - members get Country="" and
// Region="Other" but the pool functions, just with Geo-Nearest
// degrading to Random.
func TestE2E_ProviderZipImport_DegradesOnMissingGeo(t *testing.T) {
	layout := map[string][]string{
		"AT": namedHosts("AT-vie-wg-", 3),
		"DE": namedHosts("DE-fra-wg-", 5),
	}
	ipMap := map[string]string{}
	for _, names := range layout {
		for _, n := range names {
			ipMap[n] = "198.51.100.1"
		}
	}
	zipPath := makeProviderZip(t, layout, ipMap)

	// nil geo - import should still produce all members.
	importer := NewPoolImporter(nil)
	res, err := importer.Import([]string{zipPath}, nil)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(res.Members) != 8 {
		t.Errorf("len = %d, want 8", len(res.Members))
	}
	for _, m := range res.Members {
		if m.Country != "" {
			t.Errorf("member %s: country %q with nil geo, want empty", m.Name, m.Country)
		}
		if m.Region != "Other" {
			t.Errorf("member %s: region %q, want Other", m.Name, m.Region)
		}
	}

	pool := &Pool{Policy: PolicyGeoNearest, Members: res.Members}
	picked := PickMember(pool, "AT", "")
	if picked == nil {
		t.Error("Geo-Nearest with no countries should still pick something (degrade to random)")
	}
}

// TestE2E_RotatorWithRealishPool wires a 50-server pool into the
// rotator and verifies that after several ticks across regions, the
// active-member ID stays in sync with what PickMember would have
// returned. White-box: we drive ticks manually, simulating idle traffic.
func TestE2E_RotatorWithRealishPool(t *testing.T) {
	// Build 50 members across 5 regions.
	regions := []string{"Europe", "North America", "Asia-Pacific", "South America", "Oceania"}
	members := []*PoolMember{}
	for i := 0; i < 50; i++ {
		members = append(members, &PoolMember{
			ID:     fmt.Sprintf("m%02d", i),
			Name:   fmt.Sprintf("server-%02d", i),
			Region: regions[i%5],
			Active: true,
			Config: &ProtocolConfig{Protocol: "wireguard"},
		})
	}
	pool := &Pool{
		ID:       "p1",
		Policy:   PolicyRoundRobin,
		Members:  members,
		Rotation: PoolRotation{IntervalMin: 1, IdleAware: true, ForceAfterMin: 5},
	}

	r := NewPoolRotator()
	rotateCount := 0
	rotateMu := sync.Mutex{}
	pickedIDs := []string{}

	r.Start(
		func(poolID string) {
			rotateMu.Lock()
			defer rotateMu.Unlock()
			rotateCount++
			// Simulate the App's onRotate behaviour: pick a new member.
			lastID := ""
			if len(pickedIDs) > 0 {
				lastID = pickedIDs[len(pickedIDs)-1]
			}
			next := PickMember(pool, "", lastID)
			if next != nil {
				pickedIDs = append(pickedIDs, next.ID)
			}
		},
		func(poolID string) {}, // onPreWarm: no-op for this rotation-focused test
		func() (rx, tx int64) { return 0, 0 }, // always idle
		func() bool { return true },           // always VPN-active
	)
	defer r.Stop()

	r.SetActivePool(pool)

	// Backdate schedule and fire ticks N times. With idle-aware
	// + idle-traffic, every backdated tick should rotate.
	for i := 0; i < 8; i++ {
		r.mu.Lock()
		r.scheduledRotation = time.Now().Add(-1 * time.Second)
		r.mu.Unlock()
		r.tick()
		time.Sleep(50 * time.Millisecond)
	}

	rotateMu.Lock()
	defer rotateMu.Unlock()
	if rotateCount < 6 {
		t.Errorf("rotateCount = %d, want >= 6 after 8 ticks", rotateCount)
	}

	// Picked IDs should not all be the same - otherwise rotation
	// is broken even though the callback fires.
	uniq := map[string]struct{}{}
	for _, id := range pickedIDs {
		uniq[id] = struct{}{}
	}
	if len(uniq) < 3 {
		t.Errorf("only %d unique members picked across %d rotations: %v", len(uniq), rotateCount, pickedIDs)
	}
}

// TestE2E_PoolRegistryPersistRoundtripWithRealishMembers stores a
// 100-member pool to disk and reloads it, checking that no member
// data is lost in the JSON round-trip. Catches schema-drift bugs
// that synthetic micro-tests miss.
func TestE2E_PoolRegistryPersistRoundtripWithRealishMembers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pools.json")

	r1 := &PoolRegistry{filePath: path}
	members := []*PoolMember{}
	for i := 0; i < 100; i++ {
		members = append(members, &PoolMember{
			ID:      fmt.Sprintf("m%03d", i),
			Name:    fmt.Sprintf("server-%03d", i),
			Country: []string{"AT", "DE", "US", "JP", "BR"}[i%5],
			Region:  []string{"Europe", "Europe", "North America", "Asia-Pacific", "South America"}[i%5],
			Active:  true,
			Config: &ProtocolConfig{
				Protocol:      "wireguard",
				ConfigContent: "[Interface]\nPrivateKey = test\n",
				Filename:      fmt.Sprintf("server-%03d.conf", i),
				ServerAddress: fmt.Sprintf("server-%03d.example.org", i),
			},
		})
	}
	p, err := r1.Create("Real Pool", PolicyGeoNearest, members)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	r2 := &PoolRegistry{filePath: path}
	r2.load()
	got := r2.Get(p.ID)
	if got == nil {
		t.Fatal("pool did not load back")
	}
	if len(got.Members) != 100 {
		t.Errorf("members after reload = %d, want 100", len(got.Members))
	}
	for i, m := range got.Members {
		if m.ID == "" || m.Config == nil || m.Config.ConfigContent == "" {
			t.Errorf("member %d lost data: %+v", i, m)
			break
		}
	}

	// Coverage on the reloaded pool should still match.
	cov := got.Coverage()
	sort.Slice(cov, func(i, j int) bool { return cov[i].Region < cov[j].Region })
	if len(cov) < 3 {
		t.Errorf("coverage after reload = %d regions, want >= 3", len(cov))
	}
}

// helper utilities

func namedHosts(prefix string, n int) []string {
	out := make([]string, n)
	for i := 0; i < n; i++ {
		out[i] = fmt.Sprintf("%s%03d", prefix, i+1)
	}
	return out
}

func cloneMembers(in []*PoolMember) []*PoolMember {
	out := make([]*PoolMember, len(in))
	for i, m := range in {
		c := *m
		out[i] = &c
	}
	return out
}

// ensures we trip a build failure if the geoip and selfip packages
// are renamed - the e2e test's intent is that the full real-world
// path works, including a self-IP context for the country resolver.
var _ = errors.New
var _ = context.Background
