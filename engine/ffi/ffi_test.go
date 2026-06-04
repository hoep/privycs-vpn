package ffi

import (
	"encoding/json"
	"testing"
	"time"
)

// waitDecisions polls until the session has at least min decisions or times out.
func waitDecisions(t *testing.T, s *Session, min int) []decisionDTO {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var out []decisionDTO
		if err := json.Unmarshal([]byte(s.PollDecisions()), &out); err != nil {
			t.Fatalf("PollDecisions returned invalid JSON: %v", err)
		}
		if len(out) >= min {
			return out
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for >=%d decisions", min)
	return nil
}

func TestConnectProducesDecisions(t *testing.T) {
	s := NewSession(`["wireguard","openvpn"]`)
	defer s.Close()

	s.ObserveConnect("wireguard")
	out := waitDecisions(t, s, 1)

	// Every decision must carry a stable i18n key (never pre-translated text)
	// and a parseable timestamp.
	for _, d := range out {
		if d.Key == "" {
			t.Errorf("decision %s→%s has empty i18n key", d.From, d.To)
		}
		if _, err := time.Parse(time.RFC3339, d.At); err != nil {
			t.Errorf("decision has unparseable At %q: %v", d.At, err)
		}
	}

	// The connect path must reach Connected in shadow.
	last := out[len(out)-1]
	if last.To != "Connected" {
		t.Errorf("expected to reach Connected, last decision To=%q", last.To)
	}
}

func TestEmptyAndInvalidProfilesFallBack(t *testing.T) {
	for _, js := range []string{"", "not-json", "[]"} {
		s := NewSession(js)
		s.ObserveConnect("wireguard")
		_ = waitDecisions(t, s, 1) // must still produce decisions via default order
		s.Close()
	}
}

func TestPollEmptyIsJSONArray(t *testing.T) {
	s := NewSession(`["wireguard"]`)
	defer s.Close()
	if got := s.PollDecisions(); got != "[]" {
		t.Errorf("fresh session PollDecisions = %q, want []", got)
	}
}

func TestCloseIsIdempotentAndNilSafe(t *testing.T) {
	s := NewSession(`["wireguard"]`)
	s.Close()
	s.Close() // must not panic
	var nilS *Session
	nilS.ObserveConnect("wireguard")      // nil-safe
	nilS.ObserveDisconnect()   //
	nilS.ObserveHealth("dead") //
	if got := nilS.PollDecisions(); got != "[]" {
		t.Errorf("nil session PollDecisions = %q, want []", got)
	}
	nilS.Close()
}
