# Build a Windows GUI executable (no CMD console) and clean residual files.
# Usage (from repo root Koyori IDE-main):
#   pwsh -File scripts/build-windows-gui.ps1
[CmdletBinding()]
param(
  [string]$OutDir = "bin"
)

$ErrorActionPreference = "Stop"

$Root = Split-Path -Parent $PSScriptRoot
if (-not (Test-Path (Join-Path $Root "go.mod"))) {
  $Root = (Get-Location).Path
}
Set-Location $Root
Write-Host "==> Root: $Root"

function Remove-Residuals {
  Write-Host "==> Cleaning residual files..."
  $toRemove = @()
  $toRemove += Get-ChildItem -Path $Root -Filter "wails_windows_*.syso" -ErrorAction SilentlyContinue
  $toRemove += Get-ChildItem -Path $Root -Filter "*.syso" -ErrorAction SilentlyContinue
  $toRemove += Get-ChildItem -Path (Join-Path $Root $OutDir) -Filter "*.exe~" -ErrorAction SilentlyContinue
  $toRemove += Get-ChildItem -Path (Join-Path $Root $OutDir) -Filter "*.tmp" -ErrorAction SilentlyContinue
  $toRemove += Get-ChildItem -Path (Join-Path $Root $OutDir) -Filter "*.pdb" -ErrorAction SilentlyContinue
  foreach ($f in $toRemove) {
    if ($null -eq $f) { continue }
    Remove-Item -Force -ErrorAction SilentlyContinue $f.FullName
    Write-Host "    removed $($f.FullName)"
  }
}

function Test-PeIsGui([string]$ExePath) {
  $bytes = [System.IO.File]::ReadAllBytes((Resolve-Path $ExePath))
  $pe = [BitConverter]::ToInt32($bytes, 0x3C)
  $magic = [BitConverter]::ToUInt16($bytes, $pe + 0x18)
  $sub = [BitConverter]::ToUInt16($bytes, $pe + 0x18 + 0x44)
  # 2 = IMAGE_SUBSYSTEM_WINDOWS_GUI, 3 = IMAGE_SUBSYSTEM_WINDOWS_CUI
  return @{ Magic = $magic; Subsystem = $sub; IsGui = ($sub -eq 2) }
}

Remove-Residuals

Write-Host "==> Frontend production build..."
& node (Join-Path $Root "scripts\generate-bindings.mjs")
if ($LASTEXITCODE -ne 0) {
  throw "Wails binding generation failed with exit $LASTEXITCODE"
}
Push-Location (Join-Path $Root "frontend")
try {
  if (-not (Test-Path "node_modules")) {
    npm ci
    if ($LASTEXITCODE -ne 0) {
      throw "Frontend dependency installation failed with exit $LASTEXITCODE"
    }
  }
  npm run build
  if ($LASTEXITCODE -ne 0) {
    throw "Frontend production build failed with exit $LASTEXITCODE"
  }
} finally {
  Pop-Location
}

Write-Host "==> Generate syso (icon/manifest)..."
New-Item -ItemType Directory -Force -Path (Join-Path $Root $OutDir) | Out-Null
$sysoOut = Join-Path $Root "wails_windows_amd64.syso"
try {
  & wails3 generate syso `
    -arch amd64 `
    -icon (Join-Path $Root "build\windows\icon.ico") `
    -manifest (Join-Path $Root "build\windows\wails.exe.manifest") `
    -info (Join-Path $Root "build\windows\info.json") `
    -out $sysoOut
} catch {
  Write-Host "    syso skipped: $_"
}

$outExe = Join-Path $Root (Join-Path $OutDir "koyori-ide.exe")
Write-Host "==> go build (GUI subsystem, no console)..."
# Critical: -H=windowsgui must use = form so PowerShell does not strip it.
$ldflags = "-s -w -H=windowsgui"
$env:CGO_ENABLED = "0"
$env:GOOS = "windows"
& go build -tags production -trimpath -buildvcs=false -ldflags $ldflags -o $outExe .
if ($LASTEXITCODE -ne 0) {
  throw "go build failed with exit $LASTEXITCODE"
}

Remove-Residuals

if (-not (Test-Path $outExe)) {
  throw "Build failed: $outExe not found"
}

$pe = Test-PeIsGui $outExe
Write-Host ("==> PE Magic=0x{0:X} Subsystem={1} (2=GUI, 3=Console)" -f $pe.Magic, $pe.Subsystem)
if (-not $pe.IsGui) {
  throw "Executable is still CONSOLE subsystem — CMD window would appear. Aborting."
}

$hash = (Get-FileHash -Algorithm SHA256 $outExe).Hash
$item = Get-Item $outExe
Write-Host ""
Write-Host "OK: GUI app built (no CMD console)"
Write-Host "  Path:   $($item.FullName)"
Write-Host "  Size:   $([math]::Round($item.Length/1MB, 2)) MB"
Write-Host "  SHA256: $hash"
Write-Host ""
Write-Host "Double-click the exe to launch. Residuals cleaned."
