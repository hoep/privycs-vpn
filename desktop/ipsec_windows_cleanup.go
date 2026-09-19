package main

import (
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"software.sslmate.com/src/go-pkcs12"
)

// Windows IPSec OS-artifact cleanup.
//
// A gateway-downloaded IPSec config on Windows is installed by the gateway's
// setup script (run by the SYSTEM helper, see rewriteWindowsSetupScriptAllUser),
// which leaves these machine-wide artifacts behind:
//
//   - the all-user RAS VPN entry, named after the app's SLOT (gw-ipsec-<id>),
//     not the connection's display name — its routes + phonebook section go
//     with Remove-VpnConnection;
//   - NRPT rules as registry keys DnsPolicyConfig\Privycs-<slot> (catch-all)
//     or Privycs-<slot>-<i> (one per DNS suffix) — deterministic, because the
//     client rewrites the script's $VPNName to the slot;
//   - a hosts-file IPv4 pin for the server FQDN (marker comment + "<ip> <fqdn>"),
//     shared by every connection to the same gateway;
//   - the gateway CA in LocalMachine\Root and the client leaf in LocalMachine\My.
//
// Shared artifacts (hosts pin, CA) are only removed when NO remaining
// connection or pool member still needs them — deleting one of two
// connections to the same self-signed gateway must not break the other (the
// connect path re-imports only the P12 into My, never the CA into Root).
//
// The machine-wide RasMan values the script sets (StrongCRLCheck=0,
// NegotiateDH2048_AES256=2) are deliberately left alone: they affect every
// IKEv2 VPN on the machine and the prior value is unknown.

// ipsecWinArtifacts is what one IPSec ProtocolConfig may have installed.
type ipsecWinArtifacts struct {
	Slot     string   // RAS entry name = tunnelNameForSlot(cfg.ID, conn.Name)
	FQDN     string   // server address the hosts pin is keyed on
	CAThumbs []string // SHA-1 thumbprints (Windows format, upper-case hex)
}

// ipsecWinKeepSets are the artifacts still referenced after a delete.
type ipsecWinKeepSets struct {
	Slots  map[string]bool
	FQDNs  map[string]bool
	Thumbs map[string]bool
}

var (
	scriptCACertRe = regexp.MustCompile(`(?s)\$caCertBase64\s*=\s*@"\s*(.*?)\s*"@`)
	scriptFQDNRe   = regexp.MustCompile(`\$serverFQDN\s*=\s*"([^"]+)"`)

	// Validation for everything that crosses into the helper's PowerShell.
	// The helper runs as SYSTEM, so it re-validates; these are shared.
	winSlotRe  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)
	winFQDNRe  = regexp.MustCompile(`^[A-Za-z0-9.:-]{1,253}$`)
	winThumbRe = regexp.MustCompile(`^[0-9A-F]{40}$`)
)

// winThumbprint returns the Windows-style SHA-1 thumbprint of a DER cert.
func winThumbprint(der []byte) string {
	sum := sha1.Sum(der)
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// ipsecWindowsArtifactsFor derives the artifacts one IPSec config may own.
// Best-effort: unparsable content just yields fewer entries.
func ipsecWindowsArtifactsFor(cfg *ProtocolConfig, slot string) ipsecWinArtifacts {
	art := ipsecWinArtifacts{Slot: slot}
	if cfg == nil {
		return art
	}
	thumbs := map[string]bool{}

	// The CA the setup script imported into Root, and the FQDN it pinned.
	if s := cfg.WindowsRoutesScript; s != "" {
		if m := scriptCACertRe.FindStringSubmatch(s); m != nil {
			b64 := strings.Join(strings.Fields(m[1]), "")
			if der, err := base64.StdEncoding.DecodeString(b64); err == nil && len(der) > 0 {
				thumbs[winThumbprint(der)] = true
			}
		}
		if m := scriptFQDNRe.FindStringSubmatch(s); m != nil {
			art.FQDN = m[1]
		}
	}

	// CA certs bundled in the .sswan P12 (may also land in Root/CA stores).
	var profile sswanProfile
	if err := json.Unmarshal([]byte(cfg.ConfigContent), &profile); err == nil {
		if art.FQDN == "" {
			art.FQDN = profile.Remote.Addr
		}
		if profile.Local.P12 != "" {
			pass := profile.Local.P12Password
			if pass == "" {
				pass = "privycs"
			}
			if p12, err := base64Decode(profile.Local.P12); err == nil {
				if _, _, cas, err := pkcs12.DecodeChain(p12, pass); err == nil {
					for _, c := range cas {
						thumbs[winThumbprint(c.Raw)] = true
					}
				}
			}
		}
	}
	if art.FQDN == "" {
		art.FQDN = cfg.ServerAddress
	}
	art.FQDN = strings.ToLower(strings.TrimSpace(art.FQDN))

	for t := range thumbs {
		art.CAThumbs = append(art.CAThumbs, t)
	}
	sort.Strings(art.CAThumbs)
	return art
}

// ipsecWindowsKeepSets collects the artifacts every remaining IPSec config
// (saved connections AND pool members) still references. Caller holds a.mu.
func (a *App) ipsecWindowsKeepSets() ipsecWinKeepSets {
	keep := ipsecWinKeepSets{Slots: map[string]bool{}, FQDNs: map[string]bool{}, Thumbs: map[string]bool{}}
	add := func(art ipsecWinArtifacts) {
		keep.Slots[art.Slot] = true
		if art.FQDN != "" {
			keep.FQDNs[art.FQDN] = true
		}
		for _, t := range art.CAThumbs {
			keep.Thumbs[t] = true
		}
	}
	if a.connections != nil {
		for _, conn := range a.connections.List() {
			for _, cfg := range conn.Protocols {
				if cfg != nil && cfg.Protocol == "ipsec" {
					add(ipsecWindowsArtifactsFor(cfg, tunnelNameForSlot(cfg.ID, conn.Name)))
				}
			}
		}
	}
	if a.pools != nil {
		for _, p := range a.pools.List() {
			for _, m := range p.Members {
				if m != nil && m.Config != nil && m.Config.Protocol == "ipsec" {
					add(ipsecWindowsArtifactsFor(m.Config, "pool-"+shortID(m.ID)))
				}
			}
		}
	}
	return keep
}

// ipsecWindowsCleanupArgs builds the ipsec_cleanup helper args for one removed
// config: the shared artifacts still in use are filtered out HERE, so the
// helper only ever receives what it may delete. connection_name carries the
// slot too, so an older helper (which only reads connection_name) at least
// removes the right RAS entry.
func ipsecWindowsCleanupArgs(art ipsecWinArtifacts, keep ipsecWinKeepSets) map[string]string {
	args := map[string]string{"connection_name": art.Slot, "slot": art.Slot}
	if art.FQDN != "" && !keep.FQDNs[art.FQDN] {
		args["hosts_fqdn"] = art.FQDN
	}
	var drop []string
	for _, t := range art.CAThumbs {
		if !keep.Thumbs[t] {
			drop = append(drop, t)
		}
	}
	if len(drop) > 0 {
		args["ca_thumbprints"] = strings.Join(drop, ",")
	}
	return args
}

// psQuote renders a PowerShell single-quoted string literal.
func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// splitValidated splits a comma list and keeps only entries matching re.
func splitValidated(list string, re *regexp.Regexp) []string {
	var out []string
	for _, v := range strings.Split(list, ",") {
		if v = strings.TrimSpace(v); v != "" && re.MatchString(v) {
			out = append(out, v)
		}
	}
	return out
}

func psArray(items []string) string {
	q := make([]string, len(items))
	for i, s := range items {
		q[i] = psQuote(s)
	}
	return "@(" + strings.Join(q, ",") + ")"
}

const winNRPTBase = `HKLM:\SYSTEM\CurrentControlSet\Services\Dnscache\Parameters\DnsPolicyConfig`

// hostsPinMarker is the comment line the gateway setup script writes above
// its "<ipv4> <fqdn>" pin (privycs repo, ipsec_windows_ps_service.go).
const hostsPinMarker = "# Privycs VPN - force IPv4 transport (prevents IPv6 routing loop)"

// buildWindowsIPSecCleanupPS renders the helper's per-config cleanup script.
// Every input is validated again here; an invalid slot aborts (returns "").
func buildWindowsIPSecCleanupPS(slot, hostsFQDN, caThumbs string) string {
	if !winSlotRe.MatchString(slot) {
		return ""
	}
	var b strings.Builder
	b.WriteString("$ErrorActionPreference='SilentlyContinue'\n$done=@()\n")
	fmt.Fprintf(&b, "$slot=%s\n", psQuote(slot))

	// 1. RAS entry (routes + phonebook section go with it).
	b.WriteString("if (Get-VpnConnection -Name $slot -AllUserConnection) { Remove-VpnConnection -Name $slot -AllUserConnection -Force; $done+='vpn' }\n")

	// 2. NRPT keys Privycs-<slot> / Privycs-<slot>-<i>. The anchored regex
	//    keeps gw-ipsec-5 from matching gw-ipsec-51.
	fmt.Fprintf(&b, "$nrptBase=%s\n", psQuote(winNRPTBase))
	b.WriteString("$re='^Privycs-' + [regex]::Escape($slot) + '(-\\d+)?$'\n")
	b.WriteString("Get-ChildItem $nrptBase | Where-Object { $_.PSChildName -match $re } | ForEach-Object { Remove-Item -Path $_.PSPath -Recurse -Force; $done+=('nrpt:'+$_.PSChildName) }\n")
	b.WriteString("if ($done -like 'nrpt:*') { Clear-DnsClientCache }\n")

	// 3. hosts pin — only the marker + pin pair the gateway script wrote
	//    (plus stale duplicate markers right above it); user entries stay.
	if hostsFQDN != "" && winFQDNRe.MatchString(hostsFQDN) {
		fmt.Fprintf(&b, "$fqdn=%s\n$marker=%s\n", psQuote(hostsFQDN), psQuote(hostsPinMarker))
		b.WriteString(`$hostsFile="$env:SystemRoot\System32\drivers\etc\hosts"
$lines=@(Get-Content -Path $hostsFile)
$pinRe='^\s*[0-9.]+\s+' + [regex]::Escape($fqdn) + '\s*$'
$out=New-Object System.Collections.Generic.List[string]
$removed=0
for ($i=0; $i -lt $lines.Count; $i++) {
  if ($lines[$i].Trim() -eq $marker -and ($i+1) -lt $lines.Count -and $lines[$i+1] -match $pinRe) {
    while ($out.Count -gt 0 -and $out[$out.Count-1].Trim() -eq $marker) { $out.RemoveAt($out.Count-1) }
    $i++; $removed++; continue
  }
  $out.Add($lines[$i])
}
if ($removed -gt 0) { Set-Content -Path $hostsFile -Value $out -Force; ipconfig /flushdns | Out-Null; $done+=('hosts:'+$removed) }
`)
	}

	// 4. Certificates. The leaf the connect path tagged for this slot, then
	//    CAs no remaining config uses — by exact thumbprint AND a Privycs
	//    subject, so a public root (ISRG Root X1) can never be removed.
	b.WriteString("Get-ChildItem Cert:\\LocalMachine\\My | Where-Object { $_.FriendlyName -eq ('Privycs IPSec - ' + $slot) } | ForEach-Object { Remove-Item -Path $_.PSPath -Force; $done+='leaf' }\n")
	if thumbs := splitValidated(caThumbs, winThumbRe); len(thumbs) > 0 {
		fmt.Fprintf(&b, "foreach ($t in %s) {\n", psArray(thumbs))
		b.WriteString(`  foreach ($store in 'Root','CA') {
    Get-ChildItem "Cert:\LocalMachine\$store" | Where-Object { $_.Thumbprint -eq $t -and $_.Subject -like '*Privycs*' } | ForEach-Object {
      $caSubject=$_.Subject
      Get-ChildItem Cert:\LocalMachine\My | Where-Object { $_.Issuer -eq $caSubject } | ForEach-Object { Remove-Item -Path $_.PSPath -Force; $done+='leaf-by-ca' }
      Remove-Item -Path $_.PSPath -Force; $done+=('ca:'+$store)
    }
  }
}
`)
	}
	b.WriteString("Write-Output ('removed: ' + ($done -join ' '))\n")
	return b.String()
}

// ipsecReconcileSlotPattern — only names the APP generates. RAS entries or
// NRPT keys the user created by double-clicking the gateway .cmd themselves
// carry the peer display name and are never touched.
const ipsecReconcileSlotPattern = `(gw-ipsec-\d+|privycs-ipsec-[0-9a-f]{8})`

// buildWindowsIPSecReconcilePS renders the orphan sweep: app-named RAS
// entries + NRPT keys whose slot no saved connection references any more.
// hosts pins and CAs are NOT swept — they can't be attributed to the app
// safely (a manually-run .cmd writes the same marker / CA).
func buildWindowsIPSecReconcilePS(keepSlots []string) string {
	var valid []string
	for _, s := range keepSlots {
		if winSlotRe.MatchString(s) {
			valid = append(valid, s)
		}
	}
	var b strings.Builder
	b.WriteString("$ErrorActionPreference='SilentlyContinue'\n$done=@()\n")
	fmt.Fprintf(&b, "$keep=%s\n", psArray(valid))
	fmt.Fprintf(&b, "$nrptBase=%s\n", psQuote(winNRPTBase))
	fmt.Fprintf(&b, "$slotRe='^%s$'\n$nrptRe='^Privycs-%s(-\\d+)?$'\n", ipsecReconcileSlotPattern, ipsecReconcileSlotPattern)
	b.WriteString(`Get-VpnConnection -AllUserConnection | Where-Object { $_.Name -match $slotRe -and $keep -notcontains $_.Name } | ForEach-Object {
  Remove-VpnConnection -Name $_.Name -AllUserConnection -Force; $done+=('vpn:'+$_.Name)
}
Get-ChildItem $nrptBase | ForEach-Object {
  if ($_.PSChildName -match $nrptRe -and $keep -notcontains $Matches[1]) { Remove-Item -Path $_.PSPath -Recurse -Force; $done+=('nrpt:'+$_.PSChildName) }
}
if ($done -like 'nrpt:*') { Clear-DnsClientCache }
Write-Output ('removed: ' + ($done -join ' '))
`)
	return b.String()
}

// reconcileWindowsIPSecOrphans sweeps RAS entries + NRPT keys left behind by
// earlier builds (whose delete path passed the display name instead of the
// slot, so nothing was removed). Windows only; skipped when the connection
// registry failed to load — an empty keep-set would otherwise wipe live ones.
func (a *App) reconcileWindowsIPSecOrphans() {
	if runtime.GOOS != "windows" {
		return
	}
	a.mu.Lock()
	if a.connections == nil || !a.connections.LoadedCleanly() {
		a.mu.Unlock()
		log.Printf("IPSec reconcile: skipped — connection registry not loaded cleanly")
		return
	}
	keep := a.ipsecWindowsKeepSets()
	a.mu.Unlock()

	slots := make([]string, 0, len(keep.Slots))
	for s := range keep.Slots {
		slots = append(slots, s)
	}
	sort.Strings(slots)

	client := NewHelperClient()
	if !client.IsHelperReachable() {
		return
	}
	resp, err := client.SendCommand("ipsec_reconcile_windows", map[string]string{"keep_slots": strings.Join(slots, ",")})
	if err != nil {
		log.Printf("IPSec reconcile: %v", err)
		return
	}
	log.Printf("IPSec reconcile: %s", resp.Output)
}

// cleanupIPSecArtifacts runs the helper cleanup for configs that were just
// removed. Call WITHOUT a.mu held (helper IPC); it takes the lock itself to
// compute the keep-sets from the post-delete state.
func (a *App) cleanupIPSecArtifacts(removed []ipsecWinArtifacts) {
	if len(removed) == 0 {
		return
	}
	client := NewHelperClient()
	if !client.IsHelperReachable() {
		log.Printf("IPSec cleanup on delete: helper unreachable, skipped")
		return
	}
	if runtime.GOOS != "windows" {
		// Linux swanctl: single-connection-per-host files, one call suffices.
		if resp, err := client.SendCommand("ipsec_cleanup", map[string]string{"connection_name": removed[0].Slot}); err == nil {
			log.Printf("IPSec cleanup on delete: %s", resp.Output)
		}
		return
	}
	a.mu.Lock()
	keep := a.ipsecWindowsKeepSets()
	a.mu.Unlock()
	for _, art := range removed {
		resp, err := client.SendCommand("ipsec_cleanup", ipsecWindowsCleanupArgs(art, keep))
		if err != nil {
			log.Printf("IPSec cleanup on delete (%s): %v", art.Slot, err)
			continue
		}
		log.Printf("IPSec cleanup on delete (%s): %s", art.Slot, resp.Output)
	}
}
