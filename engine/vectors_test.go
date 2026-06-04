package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Golden vectors are protocol/platform-neutral JSON: a profile set, an event
// script, and an expected final state. The SAME files are the cross-platform
// contract — P2/P3 replay them in a JVM test (Android AAR) and an XCTest (iOS
// xcframework); identical final state across all three proves the one shared Go
// engine behaves identically everywhere.

type vecStep struct {
	Ev    string `json:"ev"`
	OK    bool   `json:"ok"`
	RTT   int32  `json:"rtt"`
	Loss  int32  `json:"loss"`
	Timer string `json:"timer"`
	Rx    int64  `json:"rx"`
	Tx    int64  `json:"tx"`
}

type vector struct {
	Name         string    `json:"name"`
	Profiles     []string  `json:"profiles"` // "proto:id"
	Steps        []vecStep `json:"steps"`
	ExpectFSM    string    `json:"expectFSM"`
	ExpectActive string    `json:"expectActive"`
}

func protoOf(name string) Protocol {
	switch name {
	case "wireguard":
		return ProtoWireGuard
	case "amnezia":
		return ProtoAmnezia
	case "openvpn":
		return ProtoOpenVPN
	case "ipsec":
		return ProtoIPsec
	}
	return ProtoWireGuard
}

func timerOf(name string) TimerKind {
	switch name {
	case "Connect":
		return TimerConnect
	case "DegradeDwell":
		return TimerDegradeDwell
	case "Backoff":
		return TimerBackoff
	case "RecoveryStep":
		return TimerRecoveryStep
	}
	return TimerNone
}

func buildStore(profiles []string) ProfileSnapshot {
	snap := ProfileSnapshot{}
	for _, p := range profiles {
		proto, id := "", ""
		for i := 0; i < len(p); i++ {
			if p[i] == ':' {
				proto, id = p[:i], p[i+1:]
				break
			}
		}
		snap.Profiles = append(snap.Profiles, Profile{
			ID:        ProfileID(id),
			Protocol:  protoOf(proto),
			Endpoints: []Endpoint{{Host: id, Port: 443}},
		})
	}
	return snap
}

func stepEvent(st vecStep) Event {
	switch st.Ev {
	case "UserConnect":
		return Event{Kind: EvUserConnect}
	case "UserDisconnect":
		return Event{Kind: EvUserDisconnect}
	case "HandshakeOK":
		return Event{Kind: EvHandshakeOK}
	case "HandshakeFail":
		return Event{Kind: EvHandshakeFail}
	case "Probe":
		return Event{Kind: EvProbeResult, Probe: ProbeResult{Kind: ProbePath, OK: st.OK, RTTms: st.RTT, LossPpm: st.Loss}}
	case "Stats":
		return Event{Kind: EvTunnelStats, RxBytes: st.Rx, TxBytes: st.Tx}
	case "Timer":
		return Event{Kind: EvTimer, Timer: timerOf(st.Timer)}
	case "Suspend":
		return Event{Kind: EvSuspend}
	case "Resume":
		return Event{Kind: EvResume}
	case "CaptiveDetected":
		return Event{Kind: EvCaptiveDetected}
	case "CaptiveCleared":
		return Event{Kind: EvCaptiveCleared}
	case "PathChanged":
		return Event{Kind: EvPathChanged, Net: NetworkContext{Iface: IfaceCellular, HasV4: true}}
	}
	return Event{}
}

func TestVectors(t *testing.T) {
	files, err := filepath.Glob("testvectors/*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("no test vectors found: %v", err)
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		var v vector
		if err := json.Unmarshal(data, &v); err != nil {
			t.Fatalf("%s: bad json: %v", f, err)
		}
		t.Run(v.Name, func(t *testing.T) {
			store := buildStore(v.Profiles)
			s := State{FSM: StateIdle}
			for i, st := range v.Steps {
				ec := EvalContext{
					Now:   time.Unix(int64(i), 0),
					Cfg:   DefaultPolicy(),
					Net:   NetworkContext{HasV4: true, Class: ClassOpen},
					Store: store,
					Rand:  func() uint32 { return 1 },
				}
				s, _ = Reduce(s, stepEvent(st), ec)
			}
			if s.FSM.String() != v.ExpectFSM {
				t.Fatalf("%s: want FSM %s, got %s", v.Name, v.ExpectFSM, s.FSM)
			}
			if v.ExpectActive != "" && string(s.Active) != v.ExpectActive {
				t.Fatalf("%s: want active %s, got %s", v.Name, v.ExpectActive, s.Active)
			}
		})
	}
}
