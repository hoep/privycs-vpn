package main

import "strings"

import "testing"

// A profile name is attacker-influenced: it arrives from a downloaded gateway
// config or a hand-imported .sswan. It is interpolated into PowerShell
// single-quoted literals, where the only escape is doubling the quote.
func TestEscapePowerShellStringClosesTheBreakout(t *testing.T) {
	// A name that would end the literal and append a second statement.
	hostile := `x'; Start-Process calc; '`

	got := escapePowerShellString(hostile)

	// Every quote doubled -> PowerShell reads one literal, never a statement break.
	if strings.Count(got, "'")%2 != 0 {
		t.Fatalf("odd number of quotes survives, literal can still be closed: %q", got)
	}
	if want := `x''; Start-Process calc; ''`; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestEscapePowerShellStringLeavesOrdinaryNamesAlone(t *testing.T) {
	for _, s := range []string{"Privycs Shielded", "gw-ipsec-2", "vpn.example.com"} {
		if got := escapePowerShellString(s); got != s {
			t.Errorf("%q was altered to %q", s, got)
		}
	}
}
