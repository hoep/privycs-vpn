package main

import (
	"strings"
	"testing"
)

func testIPSecCfg(bypass []string) *IPSecConfig {
	return &IPSecConfig{
		ConnectionName: "gw-ipsec-2",
		RemoteAddress:  "vpn.example.com",
		LocalID:        "client@example.com",
		RemoteID:       "vpn.example.com",
		BypassNetworks: bypass,
	}
}

// countTS returns how many prefixes appear across all remote_ts lines — i.e. the
// number of IKEv2 traffic selectors that end up on the wire.
func countTS(conf string) int {
	n := 0
	for _, line := range strings.Split(conf, "\n") {
		_, value, ok := strings.Cut(line, "remote_ts =")
		if !ok {
			continue
		}
		for _, p := range strings.Split(value, ",") {
			if strings.TrimSpace(p) != "" {
				n++
			}
		}
	}
	return n
}

func TestBuildLinuxSwanctlConf_TunnelChildAlwaysNegotiatesFullTS(t *testing.T) {
	// A realistic gateway split-tunnel: a handful of excluded LAN subnets. Their
	// complement is ~300 prefixes — which is exactly what must NOT reach IKE.
	bypass := []string{
		"10.10.10.0/24", "10.10.20.0/24", "10.10.30.0/24",
		"192.168.9.0/24", "192.168.15.0/24", "fd00::/8",
	}
	conf := buildLinuxSwanctlConf(testIPSecCfg(bypass), "ike", "esp")

	if !strings.Contains(conf, "remote_ts = "+linuxFullTS) {
		t.Errorf("tunnel child must negotiate the full dual-stack TS; got:\n%s", conf)
	}
	// 2 (full TS) + 6 (bypass) = 8. The old complement-in-remote_ts approach put
	// ~300 here, which inflated IKE_AUTH to 11760 bytes / 11 UDP fragments and
	// the peer stopped answering.
	if got := countTS(conf); got != 8 {
		t.Errorf("traffic selector count = %d, want 8 (2 tunnel + 6 bypass)", got)
	}
}

func TestBuildLinuxSwanctlConf_ExclusionsBecomePassPolicies(t *testing.T) {
	conf := buildLinuxSwanctlConf(testIPSecCfg([]string{"10.10.10.0/24", "192.168.9.0/24"}), "ike", "esp")

	if !strings.Contains(conf, "mode = pass") {
		t.Error("exclusions must be carved out with mode = pass")
	}
	if !strings.Contains(conf, "gw-ipsec-2"+bypassConnSuffix) {
		t.Error("bypass connection missing")
	}
	if !strings.Contains(conf, "remote_addrs = 127.0.0.1") {
		t.Error("bypass connection must never initiate IKE (remote_addrs = 127.0.0.1)")
	}
	for _, net := range []string{"10.10.10.0/24", "192.168.9.0/24"} {
		if !strings.Contains(conf, net) {
			t.Errorf("bypass net %s missing from conf", net)
		}
	}

	// The tunnel child must keep start_action = none — trap there re-creates the
	// v1.1.5.104 blackhole (policies installed at import, before any connect).
	tunnel, _, _ := strings.Cut(conf, bypassConnSuffix)
	if !strings.Contains(tunnel, "start_action = none") {
		t.Error("tunnel child must use start_action = none")
	}
}

func TestBuildLinuxSwanctlConf_NoBypassEmitsNoSecondConnection(t *testing.T) {
	conf := buildLinuxSwanctlConf(testIPSecCfg(nil), "ike", "esp")

	if strings.Contains(conf, bypassConnSuffix) {
		t.Errorf("no bypass nets -> no bypass connection; got:\n%s", conf)
	}
	if got := countTS(conf); got != 2 {
		t.Errorf("traffic selector count = %d, want 2 (full tunnel only)", got)
	}
}

func TestBuildLinuxSwanctlConf_MalformedBypassEntriesDropped(t *testing.T) {
	conf := buildLinuxSwanctlConf(testIPSecCfg([]string{"not-a-cidr", "999.999/8", "10.10.10.0/24"}), "ike", "esp")

	if strings.Contains(conf, "not-a-cidr") || strings.Contains(conf, "999.999") {
		t.Errorf("malformed bypass entries must be dropped; got:\n%s", conf)
	}
	if !strings.Contains(conf, "10.10.10.0/24") {
		t.Error("valid bypass entry was dropped along with the malformed ones")
	}
}
