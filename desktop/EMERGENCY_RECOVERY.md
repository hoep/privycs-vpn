# Privycs VPN — Emergency Network Recovery (Windows)

**TL;DR**: If the Privycs Kill Switch leaves you with no internet, run the
PowerShell script at the bottom of this file in an **Administrator**
PowerShell window.

This document is the documented fallback for the case where the
Privycs Kill Switch leaves the system with non-functional network
connectivity — either because the app crashed mid-engage, the
SinkholeController was unable to release rules, or a downgrade
left rules behind that the older version doesn't know about.

The new sinkhole system (Phase 2/3) is designed to never leave
stuck rules: snapshot-on-engage, atomic apply with rollback, idempotent
release, and crash-recovery on next startup. But the user has been
burned before — this document exists so there is **always** a manual
escape hatch.

---

## When to use this

Run these commands when:

1. The Privycs app crashed and now you have no internet.
2. You toggled Kill Switch off but network is still blocked.
3. You uninstalled Privycs and connectivity is still broken.
4. After a Privycs version downgrade, rules from the newer version
   persist that the older version cannot remove.
5. Any other "Privycs apparently broke my network" scenario.

The commands below are safe to run when there is nothing to clean
up — `Remove-NetFirewallRule` with `-ErrorAction SilentlyContinue`
ignores missing rules silently.

---

## How to open Administrator PowerShell

1. Press `Win + X`
2. Click **Terminal (Administrator)** or **Windows PowerShell (Administrator)**
3. Click **Yes** on the UAC prompt
4. Paste the script below and press Enter

---

## Step 1 — Inspect what's there (read-only diagnostic)

```powershell
# What Privycs firewall rules exist right now?
Get-NetFirewallRule | Where-Object {$_.DisplayName -match '^Privycs'} |
    Format-Table DisplayName, Action, Direction, Enabled

# Is the Privycs helper service still installed?
sc.exe query PrivycsVPNHelper

# Is there a leftover sinkhole snapshot file?
Test-Path "$env:PROGRAMDATA\PrivycsVPN\sinkhole-snapshot.json"

# Current default route - should NOT be a tunnel address (10.x, 100.64.x)
Get-NetRoute -DestinationPrefix "0.0.0.0/0" |
    Format-Table InterfaceAlias, NextHop, RouteMetric, ifMetric

# DNS per interface - should be your normal LAN/public DNS
Get-DnsClientServerAddress -AddressFamily IPv4 |
    Where-Object {$_.ServerAddresses.Count -gt 0} |
    Format-Table InterfaceAlias, ServerAddresses
```

If the diagnostic shows Privycs rules + no internet, proceed to Step 2.

---

## Step 2 — Remove all Privycs firewall rules

```powershell
# Remove rules from the LEGACY (pre-v0.9.10.15) kill switch:
Remove-NetFirewallRule -DisplayName 'PrivycsKS-*' -ErrorAction SilentlyContinue

# Remove rules from the NEW (v0.9.10.15+) sinkhole:
Remove-NetFirewallRule -DisplayName 'Privycs-Sinkhole-*' -ErrorAction SilentlyContinue

# Belt-and-suspenders: catch any other 'Privycs' prefixed rules
Get-NetFirewallRule | Where-Object {$_.DisplayName -match '^Privycs'} |
    Remove-NetFirewallRule -ErrorAction SilentlyContinue
```

After this, internet should work again. Test with:

```powershell
Test-NetConnection -ComputerName 8.8.8.8 -Port 53
```

If this returns `TcpTestSucceeded : True`, you are done.

---

## Step 3 — Remove leftover snapshot file (so the app doesn't try to recover stale state)

```powershell
Remove-Item "$env:PROGRAMDATA\PrivycsVPN\sinkhole-snapshot.json" -ErrorAction SilentlyContinue
```

---

## Step 4 — Stop and remove the Privycs helper service (only if Privycs is uninstalled)

If Privycs is still installed and you plan to keep using it: SKIP this step.

If Privycs is uninstalled but the helper service is still listed:

```powershell
Stop-Service -Name PrivycsVPNHelper -Force -ErrorAction SilentlyContinue
sc.exe delete PrivycsVPNHelper
```

---

## Step 5 — DNS reset (only if DNS is broken)

If you can ping `8.8.8.8` but cannot resolve names:

```powershell
# Show all adapters
Get-NetAdapter | Format-Table Name, Status, MacAddress

# For EACH adapter that should use DHCP-provided DNS, reset:
Set-DnsClientServerAddress -InterfaceAlias 'Ethernet'   -ResetServerAddresses
Set-DnsClientServerAddress -InterfaceAlias 'Ethernet 2' -ResetServerAddresses
Set-DnsClientServerAddress -InterfaceAlias 'WLAN'       -ResetServerAddresses

# Flush any cached resolutions
Clear-DnsClientCache
ipconfig /flushdns
```

Adjust the `-InterfaceAlias` names to match what `Get-NetAdapter`
shows on your machine.

---

## Step 6 — Last-resort: full Windows Firewall reset

ONLY use this if Steps 2-5 did not restore connectivity. This wipes
ALL custom firewall rules across the system, not just Privycs ones —
if you have hand-tuned firewall rules for other apps, this removes
them too.

```powershell
# WARNING: removes every custom firewall rule on this PC.
netsh advfirewall reset

# Restart the firewall service to make sure the reset takes effect
Restart-Service -Name MpsSvc -Force
```

---

## One-shot recovery script

Copy-paste this whole block into Admin PowerShell. It runs Steps 1-3
in sequence, prints a summary, and verifies connectivity. Safe to run
when nothing is wrong (the cleanup is idempotent).

```powershell
Write-Host "=== Privycs Emergency Recovery ===" -ForegroundColor Cyan

Write-Host "`n[1/4] Removing Privycs firewall rules..." -ForegroundColor Yellow
Remove-NetFirewallRule -DisplayName 'PrivycsKS-*'         -ErrorAction SilentlyContinue
Remove-NetFirewallRule -DisplayName 'Privycs-Sinkhole-*'  -ErrorAction SilentlyContinue
$leftover = Get-NetFirewallRule | Where-Object {$_.DisplayName -match '^Privycs'}
if ($leftover) {
    $leftover | Remove-NetFirewallRule -ErrorAction SilentlyContinue
    Write-Host "  Removed $($leftover.Count) additional Privycs rule(s)" -ForegroundColor Green
} else {
    Write-Host "  No leftover Privycs rules found" -ForegroundColor Green
}

Write-Host "`n[2/4] Removing leftover sinkhole snapshot..." -ForegroundColor Yellow
$snap = "$env:PROGRAMDATA\PrivycsVPN\sinkhole-snapshot.json"
if (Test-Path $snap) {
    Remove-Item $snap -Force
    Write-Host "  Removed $snap" -ForegroundColor Green
} else {
    Write-Host "  No snapshot file present" -ForegroundColor Green
}

Write-Host "`n[3/4] Flushing DNS cache..." -ForegroundColor Yellow
Clear-DnsClientCache
ipconfig /flushdns | Out-Null
Write-Host "  DNS cache cleared" -ForegroundColor Green

Write-Host "`n[4/4] Verifying connectivity..." -ForegroundColor Yellow
$test = Test-NetConnection -ComputerName 8.8.8.8 -Port 53 -WarningAction SilentlyContinue
if ($test.TcpTestSucceeded) {
    Write-Host "  TCP 8.8.8.8:53 reachable - network OK" -ForegroundColor Green
} else {
    Write-Host "  Cannot reach 8.8.8.8:53. Try Step 5 (DNS reset) and Step 6 (firewall reset)." -ForegroundColor Red
}

Write-Host "`n=== Recovery complete ===" -ForegroundColor Cyan
```

---

## What if the script doesn't help?

If the one-shot recovery script reports failure on the connectivity
check, the issue is no longer Privycs-related — Privycs has no other
mechanism to block traffic. Look for:

- Other VPN clients still running
- Antivirus / EDR blocking traffic
- Hardware/driver issues (try `Get-NetAdapter | Restart-NetAdapter`)
- Manual firewall rules added before Privycs was installed

Send the output of the **Step 1 diagnostic** plus the one-shot script
result to support so we can investigate.

---

## Linux fallback

```bash
# Remove the Privycs sinkhole chain (new system, v0.9.10.15+)
sudo iptables -D OUTPUT -j PRIVYCS_SINKHOLE 2>/dev/null
sudo iptables -F PRIVYCS_SINKHOLE 2>/dev/null
sudo iptables -X PRIVYCS_SINKHOLE 2>/dev/null

# Remove legacy privycs-ks comment-tagged rules (older system)
while sudo iptables -L OUTPUT --line-numbers -n | grep -q privycs-ks; do
  line=$(sudo iptables -L OUTPUT --line-numbers -n | grep privycs-ks | head -1 | awk '{print $1}')
  sudo iptables -D OUTPUT $line
done

# Snapshot cleanup
rm -f ~/.local/state/privycs-vpn/sinkhole-snapshot.json

# Verify
ping -c 2 8.8.8.8
```

## macOS fallback

```bash
# Flush new sinkhole anchor
sudo pfctl -a com.privycs/sinkhole -F all 2>/dev/null

# Flush legacy anchor
sudo pfctl -a privycs_ks -F all 2>/dev/null
sudo rm -f /etc/pf.anchors/privycs_ks

# Snapshot cleanup
rm -f ~/Library/Application\ Support/Privycs\ VPN/sinkhole-snapshot.json

# Verify
ping -c 2 8.8.8.8
```
