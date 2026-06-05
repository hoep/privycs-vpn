package engine

import "testing"

func TestSelect(t *testing.T) {
	all := []Protocol{ProtoWireGuard, ProtoAmnezia, ProtoOpenVPN, ProtoIPsec}
	cases := []struct {
		name    string
		in      SelectInput
		want    Protocol
		found   bool
		wantKey string
	}{
		{
			name:    "open network → WireGuard (fastest)",
			in:      SelectInput{Available: all, Country: "DE"},
			want:    ProtoWireGuard, found: true, wantKey: "reason.country_open",
		},
		{
			name:    "restrictive country → AmneziaWG (evasion)",
			in:      SelectInput{Available: all, Country: "CN"},
			want:    ProtoAmnezia, found: true, wantKey: "reason.country_restrictive_awg",
		},
		{
			name:    "restrictive, no AWG configured → OpenVPN (TCP), warn",
			in:      SelectInput{Available: []Protocol{ProtoWireGuard, ProtoOpenVPN, ProtoIPsec}, Country: "IR"},
			want:    ProtoOpenVPN, found: true, wantKey: "reason.country_restrictive_no_awg",
		},
		{
			name:    "open, no WG configured → AmneziaWG (next fastest)",
			in:      SelectInput{Available: []Protocol{ProtoAmnezia, ProtoOpenVPN}, Country: "US"},
			want:    ProtoAmnezia, found: true, wantKey: "reason.country_open",
		},
		{
			name:    "failover: WireGuard excluded → AmneziaWG",
			in:      SelectInput{Available: all, Country: "DE", Exclude: []Protocol{ProtoWireGuard}},
			want:    ProtoAmnezia, found: true, wantKey: "reason.country_open",
		},
		{
			name:    "restrictive failover: AWG excluded → OpenVPN, warn (AWG no longer usable)",
			in:      SelectInput{Available: all, Country: "RU", Exclude: []Protocol{ProtoAmnezia}},
			want:    ProtoOpenVPN, found: true, wantKey: "reason.country_restrictive_no_awg",
		},
		{
			name:  "all excluded → not found",
			in:    SelectInput{Available: []Protocol{ProtoWireGuard}, Country: "DE", Exclude: []Protocol{ProtoWireGuard}},
			found: false,
		},
		{
			name:  "nothing available → not found",
			in:    SelectInput{Available: nil, Country: "DE"},
			found: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Select(c.in)
			if got.Found != c.found {
				t.Fatalf("Found=%v want %v", got.Found, c.found)
			}
			if !c.found {
				return
			}
			if got.Protocol != c.want {
				t.Errorf("Protocol=%v want %v", got.Protocol, c.want)
			}
			if got.ReasonKey != c.wantKey {
				t.Errorf("ReasonKey=%q want %q", got.ReasonKey, c.wantKey)
			}
		})
	}
}

// Determinism: same input → same output.
func TestSelectDeterministic(t *testing.T) {
	in := SelectInput{Available: []Protocol{ProtoWireGuard, ProtoAmnezia, ProtoOpenVPN, ProtoIPsec}, Country: "CN"}
	first := Select(in)
	for i := 0; i < 100; i++ {
		g := Select(in)
		if g.Protocol != first.Protocol || g.Found != first.Found || g.ReasonKey != first.ReasonKey {
			t.Fatal("Select is not deterministic")
		}
	}
}

func TestSelectRoamingCellular(t *testing.T) {
	all := []Protocol{ProtoWireGuard, ProtoAmnezia, ProtoOpenVPN, ProtoIPsec}
	// Open network on cellular → IPSec bumped to 2nd (MOBIKE roaming).
	order := SelectOrder(SelectInput{Available: all, Country: "DE", Net: NetworkContext{Iface: IfaceCellular}})
	if len(order) < 2 || order[0] != ProtoWireGuard || order[1] != ProtoIPsec {
		t.Errorf("cellular open: want [WG, IPSec, ...], got %v", order)
	}
	// Wi-Fi → standard speed order (IPSec last).
	order = SelectOrder(SelectInput{Available: all, Country: "DE", Net: NetworkContext{Iface: IfaceWifi}})
	if order[0] != ProtoWireGuard || order[1] != ProtoAmnezia {
		t.Errorf("wifi open: want [WG, AWG, ...], got %v", order)
	}
	// Restrictive country stays evasion-first even on cellular.
	order = SelectOrder(SelectInput{Available: all, Country: "CN", Net: NetworkContext{Iface: IfaceCellular}})
	if order[0] != ProtoAmnezia {
		t.Errorf("restrictive cellular: want AWG first, got %v", order)
	}
}

func TestSelectAdaptiveStats(t *testing.T) {
	all := []Protocol{ProtoWireGuard, ProtoAmnezia, ProtoOpenVPN}
	now := int64(1_000_000)
	// WireGuard failed 1 minute ago on this network → demoted below AmneziaWG.
	stats := map[Protocol]ProtoStat{
		ProtoWireGuard: {LastFailSec: now - 60, SuccessEWMA: 100},
		ProtoAmnezia:   {SuccessEWMA: 900},
	}
	order := SelectOrder(SelectInput{Available: all, Country: "DE", Stats: stats, NowSec: now})
	if order[0] != ProtoAmnezia {
		t.Errorf("recent WG failure: want AmneziaWG first, got %v", order)
	}
	// Old failure (outside cooldown) → WireGuard back to context-first.
	stats[ProtoWireGuard] = ProtoStat{LastFailSec: now - failCooldownSec - 1, SuccessEWMA: 100}
	order = SelectOrder(SelectInput{Available: all, Country: "DE", Stats: stats, NowSec: now})
	if order[0] != ProtoWireGuard {
		t.Errorf("stale WG failure: want WireGuard first again, got %v", order)
	}
}
