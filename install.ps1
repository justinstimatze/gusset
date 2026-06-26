# gusset installer for Windows (PowerShell).
#
#   irm https://raw.githubusercontent.com/justinstimatze/gusset/main/install.ps1 | iex
#
# Downloads the latest release .zip for your architecture from GitHub Releases,
# verifies its SHA-256 against the release's checksums.txt (and, if the GitHub
# CLI is installed, its SLSA build-provenance attestation), installs gusset.exe
# to %LOCALAPPDATA%\Programs\gusset, and adds that folder to your user PATH.
#
# Pin a version with $env:GUSSET_VERSION = 'v1.2.3' before running. Override the
# install dir with $env:GUSSET_BINDIR. This script never touches your Firefox
# profile or config — run `gusset doctor` afterwards to see what it finds.

$ErrorActionPreference = 'Stop'
$Repo = 'justinstimatze/gusset'

function Fail($msg) { Write-Error "install.ps1: $msg"; exit 1 }

$bindir = if ($env:GUSSET_BINDIR) { $env:GUSSET_BINDIR } else { Join-Path $env:LOCALAPPDATA 'Programs\gusset' }

# Architecture: gusset ships windows amd64 and arm64.
$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
  'AMD64' { 'amd64' }
  'ARM64' { 'arm64' }
  default { Fail "unsupported arch '$($env:PROCESSOR_ARCHITECTURE)' — gusset ships amd64 and arm64. Build from source: https://github.com/$Repo" }
}

# Resolve the tag: explicit override, else the latest published release.
$tag = $env:GUSSET_VERSION
if (-not $tag) {
  try {
    $rel = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases/latest" -Headers @{ 'User-Agent' = 'gusset-install' }
    $tag = $rel.tag_name
  } catch {
    Fail "no published release found yet. Once one is tagged this script will work; until then build from source: git clone https://github.com/$Repo; cd gusset; go build -o gusset.exe ./cmd/gusset"
  }
}
$ver = $tag.TrimStart('v') # archive names use the version without the leading 'v'

$archive = "gusset_${ver}_windows_${arch}.zip"
$base    = "https://github.com/$Repo/releases/download/$tag"
$tmp     = Join-Path ([System.IO.Path]::GetTempPath()) ("gusset-install-" + [System.Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $tmp -Force | Out-Null

try {
  Write-Host "-> downloading gusset $tag (windows/$arch)"
  $zip = Join-Path $tmp $archive
  Invoke-WebRequest -Uri "$base/$archive"       -OutFile $zip -UseBasicParsing
  Invoke-WebRequest -Uri "$base/checksums.txt"  -OutFile (Join-Path $tmp 'checksums.txt') -UseBasicParsing

  # Verify SHA-256 against the release checksums.txt (lines: "<hash>  <name>").
  $want = (Get-Content (Join-Path $tmp 'checksums.txt') | Where-Object { $_ -match "\s$([regex]::Escape($archive))$" } |
           Select-Object -First 1) -split '\s+' | Select-Object -First 1
  $got  = (Get-FileHash $zip -Algorithm SHA256).Hash.ToLower()
  if ($want) {
    if ($got -ne $want.ToLower()) { Fail "checksum mismatch for $archive (expected $want, got $got) — refusing to install" }
    Write-Host "OK sha256 verified"
  } else {
    Write-Warning "could not find $archive in checksums.txt — continuing without checksum verification"
  }

  # Stronger than the checksum: verify SLSA build provenance, if gh is installed.
  if (Get-Command gh -ErrorAction SilentlyContinue) {
    & gh attestation verify $zip --repo $Repo *> $null
    if ($LASTEXITCODE -eq 0) { Write-Host "OK build provenance verified (SLSA attestation)" }
    else { Write-Warning "provenance check skipped or failed (gh attestation verify) — checksum still passed" }
  }

  Expand-Archive -Path $zip -DestinationPath $tmp -Force
  $exe = Join-Path $tmp 'gusset.exe'
  if (-not (Test-Path $exe)) { Fail "archive did not contain gusset.exe" }

  New-Item -ItemType Directory -Path $bindir -Force | Out-Null
  Copy-Item $exe (Join-Path $bindir 'gusset.exe') -Force
  Write-Host "OK installed gusset $tag to $bindir\gusset.exe"

  # Add to the user PATH if absent (user scope — never the machine-wide PATH).
  $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
  if (($userPath -split ';') -notcontains $bindir) {
    [Environment]::SetEnvironmentVariable('Path', "$userPath;$bindir", 'User')
    Write-Host "OK added $bindir to your user PATH (open a new terminal to pick it up)"
  }
} finally {
  Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}

Write-Host ""
Write-Host "next steps (in a new terminal):"
Write-Host "  gusset doctor          # confirm it can find your Firefox profile (read-only)"
Write-Host "  gusset gen-passphrase  # make a passphrase to share across your devices"
Write-Host "  gusset init            # create the config (prints a command to pair other devices)"
Write-Host "See https://github.com/$Repo/blob/main/TESTING.md for the full quickstart."
