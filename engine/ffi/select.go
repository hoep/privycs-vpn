package ffi

import (
	"encoding/json"
	"strings"

	eng "github.com/hoep/privycs-vpn/engine"
)

type selectResultDTO struct {
	Protocol   string   `json:"protocol"`
	Found      bool     `json:"found"`
	Reason     string   `json:"reason"`
	ReasonArgs []string `json:"reasonArgs"`
}

// SelectProtocol is the pure active-mode single-pick selector (gomobile-bound,
// static). availableCSV/excludeCSV are comma-separated protocol tokens; iface is
// "wifi"/"cellular"/"ethernet". Stats-less (context + roaming only). Returns
// JSON {"protocol":"amneziawg","found":true,"reason":"...","reasonArgs":["CN"]}.
func SelectProtocol(availableCSV, country, iface string, metered bool, excludeCSV string) string {
	r := eng.Select(eng.SelectInput{
		Available: protosFromCSV(availableCSV),
		Country:   country,
		Net:       eng.NetworkContext{Iface: eng.IfaceFromString(iface), Metered: metered},
		Exclude:   protosFromCSV(excludeCSV),
	})
	out := selectResultDTO{Found: r.Found, Reason: r.ReasonKey, ReasonArgs: r.ReasonArgs}
	if r.Found {
		out.Protocol = tokenFromProto(r.Protocol)
	}
	b, err := json.Marshal(out)
	if err != nil {
		return `{"found":false}`
	}
	return string(b)
}

// SelectOrder returns the usable protocols ranked best-first (context + roaming
// + adaptive stats) as a comma-separated token list — platforms use it to drive
// connect + failover in active mode. statsJSON maps protocol tokens to learned
// stats, e.g. {"wireguard":{"successEwma":820,"lastFailSec":1717000000}}; pass
// "{}" when no stats yet. nowSec is the current unix time (for the recent-fail
// window). Static call.
func SelectOrder(availableCSV, country, iface string, metered bool, excludeCSV, statsJSON string, nowSec int64) string {
	order := eng.SelectOrder(eng.SelectInput{
		Available: protosFromCSV(availableCSV),
		Country:   country,
		Net:       eng.NetworkContext{Iface: eng.IfaceFromString(iface), Metered: metered},
		Exclude:   protosFromCSV(excludeCSV),
		Stats:     parseStats(statsJSON),
		NowSec:    nowSec,
	})
	toks := make([]string, 0, len(order))
	for _, p := range order {
		toks = append(toks, tokenFromProto(p))
	}
	return strings.Join(toks, ",")
}

type protoStatDTO struct {
	SuccessEWMA int32 `json:"successEwma"`
	LastFailSec int64 `json:"lastFailSec"`
}

func parseStats(js string) map[eng.Protocol]eng.ProtoStat {
	js = strings.TrimSpace(js)
	if js == "" || js == "{}" {
		return nil
	}
	var raw map[string]protoStatDTO
	if err := json.Unmarshal([]byte(js), &raw); err != nil {
		return nil
	}
	out := make(map[eng.Protocol]eng.ProtoStat, len(raw))
	for tok, s := range raw {
		if p, ok := eng.ParseProtocol(tok); ok {
			out[p] = eng.ProtoStat{SuccessEWMA: s.SuccessEWMA, LastFailSec: s.LastFailSec}
		}
	}
	return out
}

func protosFromCSV(csv string) []eng.Protocol {
	csv = strings.TrimSpace(csv)
	if csv == "" {
		return nil
	}
	var out []eng.Protocol
	for _, t := range strings.Split(csv, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, protoFromString(t))
		}
	}
	return out
}

func tokenFromProto(p eng.Protocol) string {
	switch p {
	case eng.ProtoWireGuard:
		return "wireguard"
	case eng.ProtoAmnezia:
		return "amneziawg"
	case eng.ProtoOpenVPN:
		return "openvpn"
	case eng.ProtoIPsec:
		return "ipsec"
	}
	return ""
}
