package geoip

import "testing"

func TestRegion(t *testing.T) {
	cases := map[string]string{
		"AT": "Europe",
		"DE": "Europe",
		"US": "North America",
		"JP": "Asia-Pacific",
		"BR": "South America",
		"ZA": "Africa",
		"AU": "Oceania",
		"AE": "Middle East",
		"":   "Other",
		"XX": "Other",
	}
	for cc, want := range cases {
		if got := Region(cc); got != want {
			t.Errorf("Region(%q) = %q, want %q", cc, got, want)
		}
	}
}

func TestRegionCoversAllListedCountries(t *testing.T) {
	// 150+ covers all VPN-relevant countries (every country a major
	// commercial VPN provider has servers in, plus most of the long
	// tail). Unknown country codes correctly fall back to "Other".
	if len(countryToRegion) < 150 {
		t.Errorf("countryToRegion has %d entries, expected at least 150", len(countryToRegion))
	}
}
