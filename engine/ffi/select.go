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

// SelectProtocol is the pure active-mode protocol selector exposed to the mobile
// platforms (gomobile-bound). It is a STATIC call — no session needed.
//
// availableCSV / excludeCSV are comma-separated protocol tokens
// ("wireguard,amneziawg,openvpn,ipsec"); iface is "wifi"/"cellular"/"ethernet".
// Returns JSON: {"protocol":"amneziawg","found":true,"reason":"...","reasonArgs":["CN"]}.
// found=false (protocol="") when no usable protocol remains.
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

// ProtocolOrder returns the engine's country-aware protocol preference as a
// comma-separated token list (most-preferred first) — platforms use it to drive
// failover ordering in active mode. Static call.
func ProtocolOrder(country string) string {
	order := eng.ProtocolOrder(country)
	toks := make([]string, 0, len(order))
	for _, p := range order {
		toks = append(toks, tokenFromProto(p))
	}
	return strings.Join(toks, ",")
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
