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
