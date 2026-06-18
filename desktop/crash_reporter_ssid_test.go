package main

import (
	"strings"
	"testing"
)

// TestRedactStringSSID locks in the SSID-stripping the privacy policy claims
// (audit finding 2026-06-18: the docstring + policy promised it but the code
// didn't do it). Covers the common key/value shapes SSIDs appear in.
func TestRedactStringSSID(t *testing.T) {
	cases := []struct {
		in       string
		leakWord string
	}{
		{`ssid=MySecretWifi`, "MySecretWifi"},
		{`"ssid":"HomeNet5G"`, "HomeNet5G"},
		{`SSID: CorpWiFi-Guest`, "CorpWiFi-Guest"},
		{`network ssid = CafeGuest and more`, "CafeGuest"},
	}
	for _, c := range cases {
		got := redactString(c.in)
		if strings.Contains(got, c.leakWord) {
			t.Errorf("redactString(%q) = %q — SSID %q NOT redacted", c.in, got, c.leakWord)
		}
		if !strings.Contains(got, "<redacted-ssid>") {
			t.Errorf("redactString(%q) = %q — missing <redacted-ssid> marker", c.in, got)
		}
	}
}
