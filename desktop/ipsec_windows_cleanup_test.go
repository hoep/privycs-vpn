package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"strings"
	"testing"
	"time"

	"software.sslmate.com/src/go-pkcs12"
)

func testCert(t *testing.T, cn string, parent *x509.Certificate, parentKey *ecdsa.PrivateKey, isCA bool) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  isCA,
		BasicConstraintsValid: true,
	}
	if parent == nil {
		parent, parentKey = tmpl, key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, &key.PublicKey, parentKey)
	if err != nil {
		t.Fatal(err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return c, key
}

func TestIPSecWindowsArtifactsFor(t *testing.T) {
	ca, caKey := testCert(t, "Privycs CA", nil, nil, true)
	leaf, leafKey := testCert(t, "user@example", ca, caKey, false)
	p12, err := pkcs12.Modern.Encode(leafKey, leaf, []*x509.Certificate{ca}, "secret")
	if err != nil {
		t.Fatal(err)
	}
	var prof sswanProfile
	prof.Remote.Addr = "vpn.example.com"
	prof.Local.P12 = base64.StdEncoding.EncodeToString(p12)
	prof.Local.P12Password = "secret"
	content, _ := json.Marshal(prof)

	caB64 := base64.StdEncoding.EncodeToString(ca.Raw)
	wrapped := ""
	for i := 0; i < len(caB64); i += 76 {
		end := i + 76
		if end > len(caB64) {
			end = len(caB64)
		}
		wrapped += caB64[i:end] + "\n"
	}
	script := "#>\n$caCertBase64 = @\"\n" + wrapped + "\"@\n$serverFQDN = \"GW.Example.com\"\n"

	art := ipsecWindowsArtifactsFor(&ProtocolConfig{Protocol: "ipsec", ConfigContent: string(content), WindowsRoutesScript: script}, "gw-ipsec-7")
	if art.Slot != "gw-ipsec-7" {
		t.Fatalf("slot = %q", art.Slot)
	}
	if art.FQDN != "gw.example.com" {
		t.Fatalf("fqdn = %q (script FQDN must win, lower-cased)", art.FQDN)
	}
	want := winThumbprint(ca.Raw)
	if len(art.CAThumbs) != 1 || art.CAThumbs[0] != want {
		t.Fatalf("thumbs = %v, want [%s] (script CA and P12 CA are the same cert → deduped)", art.CAThumbs, want)
	}
	if !winThumbRe.MatchString(want) {
		t.Fatalf("thumbprint %q not in Windows format", want)
	}

	// No script → FQDN from the .sswan, CA from the P12.
	art = ipsecWindowsArtifactsFor(&ProtocolConfig{Protocol: "ipsec", ConfigContent: string(content)}, "x")
	if art.FQDN != "vpn.example.com" || len(art.CAThumbs) != 1 || art.CAThumbs[0] != want {
		t.Fatalf("sswan-only artifacts = %+v", art)
	}

	// Garbage content must not panic and yields just the slot.
	art = ipsecWindowsArtifactsFor(&ProtocolConfig{Protocol: "ipsec", ConfigContent: "not json", ServerAddress: "1.2.3.4"}, "y")
	if art.FQDN != "1.2.3.4" || len(art.CAThumbs) != 0 {
		t.Fatalf("garbage artifacts = %+v", art)
	}
}

func TestIPSecWindowsCleanupArgsRespectsKeepSets(t *testing.T) {
	a, b := strings.Repeat("A", 40), strings.Repeat("B", 40)
	art := ipsecWinArtifacts{Slot: "gw-ipsec-1", FQDN: "gw.example.com", CAThumbs: []string{a, b}}

	// Sibling connection to the same gateway still exists: keep pin + CA a.
	keep := ipsecWinKeepSets{
		Slots:  map[string]bool{"gw-ipsec-2": true},
		FQDNs:  map[string]bool{"gw.example.com": true},
		Thumbs: map[string]bool{a: true},
	}
	args := ipsecWindowsCleanupArgs(art, keep)
	if args["slot"] != "gw-ipsec-1" || args["connection_name"] != "gw-ipsec-1" {
		t.Fatalf("slot args = %v", args)
	}
	if _, ok := args["hosts_fqdn"]; ok {
		t.Fatalf("shared hosts pin must be kept: %v", args)
	}
	if args["ca_thumbprints"] != b {
		t.Fatalf("ca_thumbprints = %q, want only %s", args["ca_thumbprints"], b)
	}

	// Last connection to that gateway: everything goes.
	args = ipsecWindowsCleanupArgs(art, ipsecWinKeepSets{Slots: map[string]bool{}, FQDNs: map[string]bool{}, Thumbs: map[string]bool{}})
	if args["hosts_fqdn"] != "gw.example.com" || args["ca_thumbprints"] != a+","+b {
		t.Fatalf("last-connection args = %v", args)
	}
}

func TestBuildWindowsIPSecCleanupPSValidation(t *testing.T) {
	for _, bad := range []string{"", "gw'; Remove-Item C:\\ -Recurse; '", "a b", "gw-ipsec-1\n"} {
		if ps := buildWindowsIPSecCleanupPS(bad, "", ""); ps != "" {
			t.Fatalf("slot %q must be rejected", bad)
		}
	}
	ps := buildWindowsIPSecCleanupPS("gw-ipsec-5", "evil.com'; calc; '", "nothex,"+strings.Repeat("C", 40))
	if strings.Contains(ps, "evil.com") || strings.Contains(ps, "$fqdn=") {
		t.Fatal("invalid hosts FQDN must be dropped")
	}
	if strings.Contains(ps, "nothex") || !strings.Contains(ps, strings.Repeat("C", 40)) {
		t.Fatal("thumbprints must be filtered to valid SHA-1 hex")
	}
	if !strings.Contains(ps, "$_.Subject -like '*Privycs*'") {
		t.Fatal("CA removal must keep the Privycs-subject guard")
	}
	if strings.Contains(buildWindowsIPSecCleanupPS("gw-ipsec-5", "", ""), "foreach ($t in") {
		t.Fatal("no thumbprints → no CA removal block")
	}
}

func TestBuildWindowsIPSecReconcilePSFiltersKeep(t *testing.T) {
	ps := buildWindowsIPSecReconcilePS([]string{"gw-ipsec-3", "bad'slot", "pool-abcd1234"})
	if !strings.Contains(ps, "$keep=@('gw-ipsec-3','pool-abcd1234')") {
		t.Fatalf("keep array wrong:\n%s", ps)
	}
	if !strings.Contains(ps, `gw-ipsec-\d+|privycs-ipsec-[0-9a-f]{8}`) {
		t.Fatal("reconcile must be limited to app-generated names")
	}
}
