package main

import (
	"strings"
	"testing"
)

// TestComputeLinuxRemoteTS pins the bypass -> remote_ts carve-out: with no
// bypass it's the full IPv4 tunnel; with bypass nets the result is the IPv4
// complement that excludes them; v6 + malformed entries are ignored.
func TestComputeLinuxRemoteTS(t *testing.T) {
	t.Run("no bypass -> full tunnel", func(t *testing.T) {
		if got := computeLinuxRemoteTS(nil); got != "0.0.0.0/0" {
			t.Fatalf("got %q, want 0.0.0.0/0", got)
		}
		if got := computeLinuxRemoteTS([]string{}); got != "0.0.0.0/0" {
			t.Fatalf("empty slice: got %q, want 0.0.0.0/0", got)
		}
	})

	t.Run("v6-only bypass ignored -> full tunnel (v4-only remote_ts)", func(t *testing.T) {
		if got := computeLinuxRemoteTS([]string{"fd00::/8", "2001:db8::/32"}); got != "0.0.0.0/0" {
			t.Fatalf("got %q, want 0.0.0.0/0", got)
		}
	})

	t.Run("malformed ignored -> full tunnel", func(t *testing.T) {
		if got := computeLinuxRemoteTS([]string{"not-a-cidr", "999.999/8"}); got != "0.0.0.0/0" {
			t.Fatalf("got %q, want 0.0.0.0/0", got)
		}
	})

	t.Run("v4 bypass carved out", func(t *testing.T) {
		got := computeLinuxRemoteTS([]string{"10.0.0.0/8"})
		if got == "0.0.0.0/0" {
			t.Fatalf("bypass was not carved out: %q", got)
		}
		if strings.Contains(got, "10.0.0.0/8") {
			t.Errorf("remote_ts must not contain the bypass net 10.0.0.0/8: %q", got)
		}
		// Complement must be multiple CIDRs and cover the low + high halves.
		if !strings.Contains(got, "/") || !strings.Contains(got, ",") {
			t.Errorf("expected a multi-CIDR complement, got %q", got)
		}
		if !strings.Contains(got, "128.0.0.0/1") {
			t.Errorf("complement of 10/8 should include the upper half 128.0.0.0/1: %q", got)
		}
	})

	t.Run("multiple v4 bypass + v6 mixed", func(t *testing.T) {
		got := computeLinuxRemoteTS([]string{"10.0.0.0/8", "192.168.0.0/16", "fd00::/8"})
		if strings.Contains(got, "10.0.0.0/8") || strings.Contains(got, "192.168.0.0/16") {
			t.Errorf("remote_ts must exclude both bypass nets: %q", got)
		}
		if strings.Contains(got, "::") {
			t.Errorf("remote_ts must stay IPv4-only (no v6): %q", got)
		}
	})

	t.Run("bypass covering all of v4 -> fall back to full", func(t *testing.T) {
		if got := computeLinuxRemoteTS([]string{"0.0.0.0/0"}); got != "0.0.0.0/0" {
			t.Fatalf("full-space bypass should fall back to 0.0.0.0/0, got %q", got)
		}
	})
}
