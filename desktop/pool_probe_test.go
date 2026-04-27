package main

import (
	"net"
	"strings"
	"testing"
)

// TestProbeMember_NilOrEmpty exercises the input-validation guards.
// These cases must error fast without touching the network.
func TestProbeMember_NilOrEmpty(t *testing.T) {
	if err := probeMember(nil); err == nil {
		t.Errorf("probeMember(nil) returned nil error")
	}
	m := &PoolMember{ID: "x"}
	if err := probeMember(m); err == nil {
		t.Errorf("probeMember(no config) returned nil error")
	}
	m.Config = &ProtocolConfig{ServerAddress: ""}
	if err := probeMember(m); err == nil {
		t.Errorf("probeMember(empty endpoint) returned nil error")
	}
}

// TestProbeMember_DNSFailure feeds an invalid hostname guaranteed to
// fail DNS lookup and confirms the probe surfaces a "dns" error.
// Skipped when the host resolves random labels (some captive WiFi
// rewrites all NXDOMAIN to a portal IP, which would still pass DNS
// resolution and make this test pointless).
func TestProbeMember_DNSFailure(t *testing.T) {
	bad := "this-host-does-not-exist-" + randHex(16) + ".invalid"
	if ips, err := net.LookupHost(bad); err == nil && len(ips) > 0 {
		t.Skipf("Local DNS resolved %q to %v - cannot test DNS-fail path here", bad, ips)
	}
	m := &PoolMember{
		ID:     "test",
		Name:   "test",
		Config: &ProtocolConfig{ServerAddress: bad + ":51820"},
	}
	err := probeMember(m)
	if err == nil {
		t.Fatal("expected probe error for nonexistent host, got nil")
	}
	if !strings.Contains(err.Error(), "dns") {
		t.Errorf("expected dns error, got: %v", err)
	}
}

// TestProbeMember_BareIPSucceeds confirms the bare-IP short-circuit:
// if the endpoint is a literal IPv4, no DNS lookup is needed and the
// probe must accept it. This is the typical case for VPN providers
// that ship configs with hardcoded IP endpoints (no hostname to
// resolve) - earlier TCP-Dial probe wrongly rejected these.
func TestProbeMember_BareIPSucceeds(t *testing.T) {
	m := &PoolMember{
		ID:     "test",
		Name:   "test",
		Config: &ProtocolConfig{ServerAddress: "203.0.113.1:51820"},
	}
	if err := probeMember(m); err != nil {
		t.Errorf("bare-IP probe should succeed without network calls, got: %v", err)
	}
}

// randHex returns 2*n hex characters from a fixed-but-acceptable
// source (math/rand is fine here - we are not after security, just
// uniqueness in test runs).
func randHex(n int) string {
	const hex = "0123456789abcdef"
	out := make([]byte, n)
	for i := range out {
		out[i] = hex[i%len(hex)]
	}
	return string(out)
}
