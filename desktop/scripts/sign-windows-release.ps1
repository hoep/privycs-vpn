<#
  sign-windows-release.ps1 — sign the Privycs VPN Windows binaries with the
  Certum (SimplySign cloud) code-signing certificate via signtool.

  PREREQUISITE: SimplySign Desktop installed AND logged in (OTP from the mobile
  app, or `oathtool --totp -b <your TOTP seed>`), so the certificate is present
  in the Windows certificate store (CurrentUser\My). Check with:
      Get-ChildItem Cert:\CurrentUser\My -CodeSigningCert | Format-List Subject,Thumbprint

  USAGE (run from the folder holding the .exe files):
      .\sign-windows-release.ps1                       # signs the 2 default Privycs exes in CWD
      .\sign-windows-release.ps1 a.exe b.exe           # signs the given files
      .\sign-windows-release.ps1 -Thumbprint ABC123    # pin a specific cert (if several)

  FULL RELEASE FLOW (CI builds unsigned -> you sign -> replace on the release):
      gh release download v1.1.5.78 --pattern "privycs-vpn-windows-amd64*.exe"
      .\sign-windows-release.ps1
      gh release upload v1.1.5.78 privycs-vpn-windows-amd64.exe privycs-vpn-windows-amd64-setup.exe --clobber
#>
param(
  [string[]]$Files,
  [string]$Thumbprint,
  [string]$TimestampUrl = "http://time.certum.pl"
)
$ErrorActionPreference = "Stop"

# 1. Locate signtool.exe (from the Windows SDK)
function Find-SignTool {
  $cmd = Get-Command signtool.exe -ErrorAction SilentlyContinue
  if ($cmd) { return $cmd.Source }
  $roots = @("${env:ProgramFiles(x86)}\Windows Kits\10\bin", "${env:ProgramFiles}\Windows Kits\10\bin")
  foreach ($r in $roots) {
    if (Test-Path $r) {
      $st = Get-ChildItem $r -Recurse -Filter signtool.exe -ErrorAction SilentlyContinue |
            Where-Object { $_.FullName -match '\\x64\\' } |
            Sort-Object FullName -Descending | Select-Object -First 1
      if ($st) { return $st.FullName }
    }
  }
  throw "signtool.exe not found - install the Windows SDK (or add signtool to PATH)."
}
$signtool = Find-SignTool
Write-Host "signtool: $signtool"

# 2. Pick the code-signing certificate
if (-not $Thumbprint) {
  $certs = @(Get-ChildItem Cert:\CurrentUser\My -CodeSigningCert | Where-Object { $_.NotAfter -gt (Get-Date) })
  if ($certs.Count -eq 0) {
    throw "No valid code-signing cert in CurrentUser\My. Is SimplySign Desktop running + logged in?"
  }
  if ($certs.Count -gt 1) {
    Write-Host "Multiple code-signing certs found - pass -Thumbprint to choose:"
    $certs | ForEach-Object { Write-Host "  $($_.Thumbprint)  $($_.Subject)" }
    throw "Ambiguous certificate."
  }
  $Thumbprint = $certs[0].Thumbprint
  Write-Host "Using cert: $($certs[0].Subject)  [$Thumbprint]"
}

# 3. Default to the two Privycs Windows artifacts if no files given
if (-not $Files -or $Files.Count -eq 0) {
  $Files = @("privycs-vpn-windows-amd64.exe", "privycs-vpn-windows-amd64-setup.exe") |
           Where-Object { Test-Path $_ }
  if (-not $Files) { throw "No files given and no default Privycs exes found in $(Get-Location)." }
}

# 4. Sign + verify each, refresh any .sha256 sidecar
foreach ($f in $Files) {
  if (-not (Test-Path $f)) { Write-Warning "skip (not found): $f"; continue }
  Write-Host "`n=== Signing $f ===" -ForegroundColor Cyan
  & $signtool sign /sha1 $Thumbprint /fd sha256 /tr $TimestampUrl /td sha256 /v $f
  if ($LASTEXITCODE -ne 0) { throw "signtool sign failed for $f (exit $LASTEXITCODE)" }
  & $signtool verify /pa /v $f
  if ($LASTEXITCODE -ne 0) { throw "signtool verify failed for $f" }
  $sidecar = "$f.sha256"
  if (Test-Path $sidecar) {
    ((Get-FileHash $f -Algorithm SHA256).Hash.ToLower() + "  " + (Split-Path $f -Leaf)) |
      Out-File -Encoding ASCII $sidecar
    Write-Host "updated $sidecar"
  }
}
Write-Host "`nDone. Signed: $($Files -join ', ')" -ForegroundColor Green
