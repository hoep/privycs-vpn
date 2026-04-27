package main

import "testing"

// TestParseAllowedIPsTarget covers the three branches of the trigger-
// target picker: full-tunnel default, restricted CIDR with synthesized
// gateway, and unusable input falling back to "".
func TestParseAllowedIPsTarget(t *testing.T) {
	cases := []struct {
		name string
		conf string
		want string
	}{
		{
			name: "full tunnel uses well-known cloudflare DNS",
			conf: "[Interface]\nAddress = 10.0.0.2/24\n[Peer]\nAllowedIPs = 0.0.0.0/0, ::/0\n",
			want: "1.1.1.1",
		},
		{
			name: "restricted CIDR yields network+1 gateway",
			conf: "[Interface]\nAddress = 10.50.0.5/24\n[Peer]\nAllowedIPs = 10.50.0.0/24\n",
			want: "10.50.0.1",
		},
		{
			name: "first IPv4 CIDR wins, IPv6 entries skipped",
			conf: "[Peer]\nAllowedIPs = ::/0, 192.168.1.0/24, 10.0.0.0/8\n",
			want: "192.168.1.1",
		},
		{
			name: "IPv6 only AllowedIPs - no usable target",
			conf: "[Peer]\nAllowedIPs = ::/0, fd00::/8\n",
			want: "",
		},
		{
			name: "missing AllowedIPs line",
			conf: "[Interface]\nAddress = 10.0.0.2/24\n[Peer]\nPublicKey = abc\n",
			want: "",
		},
		{
			name: "leading whitespace tolerated",
			conf: "[Peer]\n   AllowedIPs   =   0.0.0.0/0\n",
			want: "1.1.1.1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseAllowedIPsTarget(tc.conf)
			if got != tc.want {
				t.Errorf("parseAllowedIPsTarget = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestParseAllowedIPsTarget_DoesNotMutateInput is the regression guard
// for an old class of bug where IP-arithmetic on the parsed net.IP
// modified the CIDR's underlying byte slice. The function should yield
// the same target on repeat calls with identical input.
func TestParseAllowedIPsTarget_DoesNotMutateInput(t *testing.T) {
	conf := "[Peer]\nAllowedIPs = 10.50.0.0/24\n"
	first := parseAllowedIPsTarget(conf)
	second := parseAllowedIPsTarget(conf)
	if first != second {
		t.Errorf("repeated parse mismatch: %q then %q", first, second)
	}
	if first != "10.50.0.1" {
		t.Errorf("expected 10.50.0.1, got %q", first)
	}
}
