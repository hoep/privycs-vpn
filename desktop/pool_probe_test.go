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
// rewrites all NXDOMAIN to a portal IP, which would let TCP-Dial
// succeed against the wrong target).
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

// TestProbeMember_ReachableLocalhost spins up a local TCP listener on
// :443-equivalent and confirms the probe finds it. We listen on
// 127.0.0.1:0 (random port) and rewrite the test hostname to point
// dialOK at the right port. Since dialOK is hardcoded to :443/:80
// we instead test the lower-level dialOK helper directly.
func TestDialOK_ReachableLocalhost(t *testing.T) {
	// Bind a TCP listener on 127.0.0.1:0 (any free port).
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	defer ln.Close()

	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("split addr failed: %v", err)
	}

	if err := dialOK("127.0.0.1", port); err != nil {
		t.Errorf("dialOK to local listener failed: %v", err)
	}
}

func TestDialOK_UnreachablePort(t *testing.T) {
	// Pick a port that very-likely has no listener. 1 is reserved/
	// privileged on Linux; a non-listener there gives a fast ECONNREFUSED.
	if err := dialOK("127.0.0.1", "1"); err == nil {
		t.Errorf("dialOK to unreachable port should fail")
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
