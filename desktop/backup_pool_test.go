package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestBackupRoundtrip_IncludesPools verifies that an ExportBackup ->
// ImportBackup cycle preserves the pool registry, including all
// members, the active-member pointer, and policy settings.
func TestBackupRoundtrip_IncludesPools(t *testing.T) {
	dir := t.TempDir()

	// Source app: build with one pool that has 3 members.
	src := minimalAppForBackup(t, filepath.Join(dir, "src"))
	pool, err := src.pools.Create("Test Pool", PolicyRoundRobin, []*PoolMember{
		{ID: "m1", Name: "AT-vie-001", Country: "AT", Region: "Europe", Active: true,
			Config: &ProtocolConfig{Protocol: "wireguard", ConfigContent: "[Interface]", ServerAddress: "vie.example"}},
		{ID: "m2", Name: "DE-fra-001", Country: "DE", Region: "Europe", Active: true,
			Config: &ProtocolConfig{Protocol: "wireguard", ConfigContent: "[Interface]", ServerAddress: "fra.example"}},
		{ID: "m3", Name: "US-nyc-001", Country: "US", Region: "North America", Active: true,
			Config: &ProtocolConfig{Protocol: "wireguard", ConfigContent: "[Interface]", ServerAddress: "nyc.example"}},
	})
	if err != nil {
		t.Fatalf("Create source pool: %v", err)
	}
	src.pools.SetActiveMember(pool.ID, "m2")
	// Force flush the state registry so the snapshot baked into the
	// backup includes the active-member assignment we just made.
	if src.poolStates != nil {
		src.poolStates.saveSafe()
	}

	// Tweak the pool's policy params to a non-default so we can verify
	// the restore preserves them.
	pool.Rotation = PoolRotation{IntervalMin: 15, IdleAware: false, ForceAfterMin: 45}
	pool.RestrictRegions = []string{"Europe"}
	pool.CountryOverride = "AT"
	src.pools.Update(pool)

	backupPath := filepath.Join(dir, "test.backup")
	if err := src.ExportBackup(backupPath, "secretPass123"); err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}

	// Destination app: fresh instance with empty pool registry.
	dst := minimalAppForBackup(t, filepath.Join(dir, "dst"))
	if got := len(dst.pools.List()); got != 0 {
		t.Fatalf("dst should start empty, has %d pools", got)
	}

	if err := dst.ImportBackup(backupPath, "secretPass123"); err != nil {
		t.Fatalf("ImportBackup: %v", err)
	}

	// Verify the pool came back identical.
	got := dst.pools.Get(pool.ID)
	if got == nil {
		t.Fatalf("pool %s did not restore", pool.ID)
	}
	if got.Name != "Test Pool" {
		t.Errorf("Name = %q, want %q", got.Name, "Test Pool")
	}
	if got.Policy != PolicyRoundRobin {
		t.Errorf("Policy = %s, want %s", got.Policy, PolicyRoundRobin)
	}
	if len(got.Members) != 3 {
		t.Errorf("Members = %d, want 3", len(got.Members))
	}
	if id := dst.pools.ActiveMemberID(got.ID); id != "m2" {
		t.Errorf("ActiveMemberID = %q, want m2", id)
	}
	if got.Rotation.IntervalMin != 15 {
		t.Errorf("Rotation.IntervalMin = %d, want 15", got.Rotation.IntervalMin)
	}
	if got.Rotation.IdleAware {
		t.Errorf("Rotation.IdleAware = true, want false")
	}
	if got.CountryOverride != "AT" {
		t.Errorf("CountryOverride = %q, want AT", got.CountryOverride)
	}
	if len(got.RestrictRegions) != 1 || got.RestrictRegions[0] != "Europe" {
		t.Errorf("RestrictRegions = %v, want [Europe]", got.RestrictRegions)
	}

	// Member configs survive too.
	m2 := got.MemberByID("m2")
	if m2 == nil || m2.Config == nil || m2.Config.ServerAddress != "fra.example" {
		t.Errorf("m2 config did not survive: %+v", m2)
	}

	// And the on-disk pools.json is at the destination's path,
	// not the source's path - filePath is preserved from current process.
	if dst.pools.filePath != filepath.Join(dir, "dst", "pools.json") {
		t.Errorf("destination filePath wrong: %q", dst.pools.filePath)
	}
}

// TestBackup_V1Compat verifies that a v1 backup (pre-Pool feature)
// still loads cleanly on a v2-aware client. Pools-field absence
// must not error; the destination's existing pools (none in this
// scenario) stay untouched.
func TestBackup_V1Compat(t *testing.T) {
	dir := t.TempDir()

	src := minimalAppForBackup(t, filepath.Join(dir, "src"))
	src.connections.AddOrUpdate("", "Office", &ProtocolConfig{
		Protocol:      "wireguard",
		ConfigContent: "[Interface]\nPrivateKey = test\n",
		Filename:      "office.conf",
	})

	// Hand-craft a v1-shaped envelope by exporting then patching the
	// version field down. (Easier than building the whole envelope
	// from scratch.)
	backupPath := filepath.Join(dir, "v1.backup")
	if err := src.ExportBackup(backupPath, "p1"); err != nil {
		t.Fatalf("ExportBackup: %v", err)
	}

	// Read, decrypt, mutate version, re-encrypt as v1 envelope.
	// To keep the test isolated, we just rewrite the inner schema's
	// version=1 by hand: the easiest way is to manually construct
	// the plaintext ourselves.
	v1Plain := backupPlaintext{
		Version:     1,
		AppVersion:  AppVersion,
		Connections: src.connections,
		Settings:    src.settings,
		// Pools intentionally absent
	}
	v1Bytes, _ := jsonMarshal(v1Plain)
	env, err := encryptBackup(v1Bytes, "p1")
	if err != nil {
		t.Fatalf("encrypt v1: %v", err)
	}
	envBytes, _ := jsonMarshal(env)
	if err := os.WriteFile(backupPath, envBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	dst := minimalAppForBackup(t, filepath.Join(dir, "dst"))
	if err := dst.ImportBackup(backupPath, "p1"); err != nil {
		t.Fatalf("ImportBackup of v1 backup failed: %v", err)
	}
	if len(dst.connections.List()) != 1 {
		t.Errorf("v1 connections should have restored")
	}
	// Pools still empty - v1 didn't carry any.
	if len(dst.pools.List()) != 0 {
		t.Errorf("dst.pools should still be empty after v1 import, got %d", len(dst.pools.List()))
	}
}

// TestBackup_FutureVersionRejected ensures we don't try to interpret
// a future-format envelope with current logic.
func TestBackup_FutureVersionRejected(t *testing.T) {
	dir := t.TempDir()

	plain := backupPlaintext{
		Version:    999,
		AppVersion: "future",
	}
	bytes, _ := jsonMarshal(plain)
	env, err := encryptBackup(bytes, "p1")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	// Override the envelope's version field so it claims to be a
	// future backup format (the envelope-level check is what
	// ImportBackup enforces).
	env.Version = 999
	envBytes, _ := jsonMarshal(env)

	path := filepath.Join(dir, "future.backup")
	os.WriteFile(path, envBytes, 0o600)

	app := minimalAppForBackup(t, filepath.Join(dir, "app"))
	err = app.ImportBackup(path, "p1")
	if err == nil {
		t.Error("future-version backup should have errored, did not")
	}
}

// minimalAppForBackup constructs an App with just the registries and
// settings hooked up - skips protocol handlers, network monitor, etc.
// All the parts ExportBackup / ImportBackup actually touch.
func minimalAppForBackup(t *testing.T, dir string) *App {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	connections := &ConnectionRegistry{filePath: filepath.Join(dir, "connections.json")}
	poolStates := &PoolStateRegistry{
		filePath: filepath.Join(dir, "pool_state.json"),
		stopCh:   make(chan struct{}),
		wakeCh:   make(chan struct{}, 1),
	}
	poolStates.state.Pools = make(map[string]*poolStateEntry)
	pools := &PoolRegistry{
		filePath: filepath.Join(dir, "pools.json"),
		poolByID: make(map[string]*Pool),
		state:    poolStates,
	}
	settings := &AppSettings{}

	return &App{
		connections: connections,
		pools:       pools,
		poolStates:  poolStates,
		settings:    settings,
	}
}

// jsonMarshal wraps encoding/json so the test reads naturally; matches
// the JSON serialisation choices made in backup.go.
func jsonMarshal(v any) ([]byte, error) {
	return json.Marshal(v)
}
