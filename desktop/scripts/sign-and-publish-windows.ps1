<#
  sign-and-publish-windows.ps1 — ONE-command Windows release signing.

  Downloads the unsigned Windows binaries from a GitHub release, signs them with
  the Certum (SimplySign cloud) code-signing cert via signtool (SHA-256 +
  RFC-3161 timestamp), refreshes the .sha256 sidecars, and re-uploads the signed
  files to the same release.

  PREREQUISITE (the one manual bit — cloud sessions can't be fully automated):
    - SimplySign Desktop installed AND logged in (OTP from the mobile app, or
      `oathtool --totp -b <your TOTP seed>`), with "register certificate in the
      Windows store" enabled, so the cert is in CurrentUser\My.
    - gh CLI installed + authenticated (`gh auth login`).

  USAGE:
    .\sign-and-publish-windows.ps1 -Tag v1.1.5.78
    .\sign-and-publish-windows.ps1 -Tag v1.1.5.78 -NoUpload      # sign only, don't re-upload
    .\sign-and-publish-windows.ps1 -Tag v1.1.5.78 -Thumbprint ABC123   # pin a cert
#>
param(
  [Parameter(Mandatory)][string]$Tag,
  [string]$Repo = "hoep/privycs-vpn",
  [string]$Thumbprint,
  [string]$TimestampUrl = "http://time.certum.pl",
  [switch]$NoUpload
)
$ErrorActionPreference = "Stop"

# --- locate signtool ---
function Find-SignTool {
  $cmd = Get-Command signtool.exe -ErrorAction SilentlyContinue
  if ($cmd) { return $cmd.Source }
  foreach ($r in @("${env:ProgramFiles(x86)}\Windows Kits\10\bin", "${env:ProgramFiles}\Windows Kits\10\bin")) {
    if (Test-Path $r) {
      $st = Get-ChildItem $r -Recurse -Filter signtool.exe -ErrorAction SilentlyContinue |
            Where-Object { $_.FullName -match '\\x64\\' } | Sort-Object FullName -Descending | Select-Object -First 1
      if ($st) { return $st.FullName }
    }
  }
  throw "signtool.exe not found - install the Windows SDK."
}
$signtool = Find-SignTool
Write-Host "signtool: $signtool"

# --- pick code-signing cert ---
if (-not $Thumbprint) {
  $certs = @(Get-ChildItem Cert:\CurrentUser\My -CodeSigningCert | Where-Object { $_.NotAfter -gt (Get-Date) })
  if ($certs.Count -eq 0) { throw "No valid code-signing cert in CurrentUser\My. Is SimplySign Desktop logged in + cert registered in the store?" }
  if ($certs.Count -gt 1) {
    Write-Host "Multiple code-signing certs - pass -Thumbprint:"; $certs | ForEach-Object { Write-Host "  $($_.Thumbprint)  $($_.Subject)" }
    throw "Ambiguous certificate."
  }
  $Thumbprint = $certs[0].Thumbprint
  Write-Host "Using cert: $($certs[0].Subject)  [$Thumbprint]"
}

# --- work in a temp dir ---
$work = Join-Path $env:TEMP "privycs-sign-$Tag"
Remove-Item $work -Recurse -Force -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Path $work | Out-Null
Push-Location $work
try {
  # 1. download unsigned binaries + checksums
  Write-Host "`n=== Downloading $Tag from $Repo ===" -ForegroundColor Cyan
  & gh release download $Tag --repo $Repo --pattern "privycs-vpn-windows-amd64*" --clobber
  if ($LASTEXITCODE -ne 0) { throw "gh release download failed (exit $LASTEXITCODE)" }

  $exes = @("privycs-vpn-windows-amd64.exe", "privycs-vpn-windows-amd64-setup.exe") | Where-Object { Test-Path $_ }
  if (-not $exes) { throw "No Windows .exe found in release $Tag." }

  # 2. sign + verify + refresh sha256
  $upload = @()
  foreach ($f in $exes) {
    Write-Host "`n=== Signing $f ===" -ForegroundColor Cyan
    & $signtool sign /sha1 $Thumbprint /fd sha256 /tr $TimestampUrl /td sha256 /v $f
    if ($LASTEXITCODE -ne 0) { throw "signtool sign failed for $f" }
    & $signtool verify /pa /v $f
    if ($LASTEXITCODE -ne 0) { throw "signtool verify failed for $f" }
    ((Get-FileHash $f -Algorithm SHA256).Hash.ToLower() + "  " + $f) | Out-File -Encoding ASCII "$f.sha256"
    $upload += $f; $upload += "$f.sha256"
  }

  # 3. re-upload signed files
  if ($NoUpload) {
    Write-Host "`n-NoUpload: signed files left in $work" -ForegroundColor Yellow
  } else {
    Write-Host "`n=== Uploading signed files to $Tag ===" -ForegroundColor Cyan
    & gh release upload $Tag --repo $Repo --clobber @upload
    if ($LASTEXITCODE -ne 0) { throw "gh release upload failed (exit $LASTEXITCODE)" }
    Write-Host "`nDone — signed + published: $($exes -join ', ')" -ForegroundColor Green
  }
} finally {
  Pop-Location
}
