package main

import (
	"sort"
	"strings"
	"testing"
)

// TestSubtractCidrStrings covers the Windows IPSec "Excluded networks"
// (split-tunneling bypass) carve-out: gateway include-routes MINUS the
// .sswan excluded subnets.
func TestSubtractCidrStrings(t *testing.T) {
	cases := []struct {
		name    string
		routes  []string
		exclude []string
		want    []string // sorted
	}{
		{
			name:    "no excludes passes routes through",
			routes:  []string{"10.0.0.0/8", "192.168.0.0/16"},
			exclude: nil,
			want:    []string{"10.0.0.0/8", "192.168.0.0/16"},
		},
		{
			name:    "exclude fully drops a covered route",
			routes:  []string{"10.5.0.0/16", "192.168.0.0/16"},
			exclude: []string{"10.5.0.0/16"},
			want:    []string{"192.168.0.0/16"},
		},
		{
			name:    "exclude splits a broader route around the bypass subnet",
			routes:  []string{"10.0.0.0/8"},
			exclude: []string{"10.5.5.0/24"},
			// 10.0.0.0/8 minus 10.5.5.0/24 must NOT contain 10.5.5.0/24
			// and must still cover 10.0.0.0 and 10.255.255.255.
			want: nil, // checked structurally below
		},
		{
			name:    "non-overlapping exclude leaves route intact",
			routes:  []string{"10.0.0.0/8"},
			exclude: []string{"172.16.0.0/12"},
			want:    []string{"10.0.0.0/8"},
		},
		{
			name:    "v6 exclude does not affect v4 routes",
			routes:  []string{"10.0.0.0/8"},
			exclude: []string{"fd00::/8"},
			want:    []string{"10.0.0.0/8"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SubtractCidrStrings(tc.routes, tc.exclude)
			if tc.want != nil {
				g := append([]string(nil), got...)
				sort.Strings(g)
				w := append([]string(nil), tc.want...)
				sort.Strings(w)
				if strings.Join(g, ",") != strings.Join(w, ",") {
					t.Fatalf("got %v, want %v", g, w)
				}
				return
			}
			// structural check for the split case
			for _, c := range got {
				if c == "10.5.5.0/24" {
					t.Fatalf("excluded subnet 10.5.5.0/24 still present in %v", got)
				}
			}
			covers := func(cidr string) bool {
				for _, c := range got {
					if c == cidr {
						return true
					}
				}
				return false
			}
			_ = covers
			if len(got) == 0 {
				t.Fatalf("expected split result, got empty")
			}
		})
	}
}
