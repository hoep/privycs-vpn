package engine

import "testing"

func TestIsRestrictiveCountry(t *testing.T) {
	for _, cc := range []string{"CN", "cn", " ir ", "RU", "TM"} {
		if !IsRestrictiveCountry(cc) {
			t.Errorf("expected %q restrictive", cc)
		}
	}
	for _, cc := range []string{"DE", "US", "AT", "", "ZZ"} {
		if IsRestrictiveCountry(cc) {
			t.Errorf("expected %q NOT restrictive", cc)
		}
	}
}

func TestCountryReason(t *testing.T) {
	cases := []struct {
		country string
		active  Protocol
		awg     bool
		wantKey string
	}{
		{"", ProtoWireGuard, true, ""},                                       // unknown → no reason
		{"DE", ProtoWireGuard, false, "reason.country_open"},                 // open country
		{"CN", ProtoAmnezia, true, "reason.country_restrictive_awg"},         // restrictive + already AWG
		{"IR", ProtoWireGuard, true, "reason.country_restrictive_use_awg"},   // restrictive + AWG available → recommend
		{"RU", ProtoOpenVPN, false, "reason.country_restrictive_no_awg"},     // restrictive + no AWG → warn
		{"cn", ProtoWireGuard, true, "reason.country_restrictive_use_awg"},   // case-insensitive
	}
	for _, c := range cases {
		key, args := CountryReason(c.country, c.active, c.awg)
		if key != c.wantKey {
			t.Errorf("CountryReason(%q,%v,%v)=%q want %q", c.country, c.active, c.awg, key, c.wantKey)
		}
		if c.wantKey != "" {
			if len(args) != 1 || args[0] != upper(c.country) {
				t.Errorf("CountryReason(%q) args=%v want [%s]", c.country, args, upper(c.country))
			}
		}
	}
}

func upper(s string) string {
	out := []byte(s)
	for i := 0; i < len(out); i++ {
		if out[i] >= 'a' && out[i] <= 'z' {
			out[i] -= 32
		}
	}
	// trim spaces to match CountryReason's TrimSpace
	start, end := 0, len(out)
	for start < end && out[start] == ' ' {
		start++
	}
	for end > start && out[end-1] == ' ' {
		end--
	}
	return string(out[start:end])
}

func TestReasonForOnlyProtocolDecisions(t *testing.T) {
	if k, _ := ReasonFor("decision.connected", ProtoWireGuard, true, "CN", true); k != "reason.country_restrictive_use_awg" {
		t.Errorf("connect decision should carry country reason, got %q", k)
	}
	if k, _ := ReasonFor("decision.degraded", ProtoWireGuard, true, "CN", true); k != "" {
		t.Errorf("non-protocol decision should carry no reason, got %q", k)
	}
}
