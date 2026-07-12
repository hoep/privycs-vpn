package main

import (
	"fmt"
	"strings"
	"testing"
)

// buildLongConfig produces a split-tunnel-shaped config: one AllowedIPs line
// carrying n prefixes, exactly like the gateway hands us.
func buildLongConfig(n int) (cfg string, prefixes []string) {
	for i := range n {
		prefixes = append(prefixes, fmt.Sprintf("10.%d.%d.0/24", i/256, i%256))
	}
	cfg = "[Interface]\nPrivateKey = k\nAddress = 10.0.0.2/32\n\n" +
		"[Peer]\nPublicKey = p\nEndpoint = 1.2.3.4:51820\n" +
		"AllowedIPs = " + strings.Join(prefixes, ", ") + "\n"
	return cfg, prefixes
}

// collectAllowedIPs gathers every prefix across all AllowedIPs lines — which is
// exactly what `wg setconf` does when a [Peer] repeats the key.
func collectAllowedIPs(cfg string) []string {
	var got []string
	for _, line := range strings.Split(cfg, "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "AllowedIPs") {
			continue
		}
		for _, p := range strings.Split(value, ",") {
			if p = strings.TrimSpace(p); p != "" {
				got = append(got, p)
			}
		}
	}
	return got
}

func TestSplitLongAllowedIPs_PreservesEveryPrefixInOrder(t *testing.T) {
	cfg, want := buildLongConfig(300)

	out, split := splitLongAllowedIPs(cfg)
	if split != 1 {
		t.Fatalf("split count = %d, want 1", split)
	}

	got := collectAllowedIPs(out)
	if len(got) != len(want) {
		t.Fatalf("prefix count = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("prefix %d = %q, want %q", i, got[i], want[i])
		}
	}

	for _, line := range strings.Split(out, "\n") {
		if len(line) > allowedIPsSplitThreshold {
			t.Errorf("line still oversized (%d chars): %.60q…", len(line), line)
		}
	}
}

func TestSplitLongAllowedIPs_LeavesShortConfigsAlone(t *testing.T) {
	cfg := "[Peer]\nPublicKey = p\nAllowedIPs = 0.0.0.0/0, ::/0\n"

	out, split := splitLongAllowedIPs(cfg)
	if split != 0 {
		t.Errorf("split count = %d, want 0 — a full-tunnel config must pass through untouched", split)
	}
	if out != cfg {
		t.Errorf("config was rewritten:\n%q", out)
	}
}
