# prompt-11 11-N: one-shot contributor setup (Windows PowerShell)
# Target: gopls + frontend tests green in ~30 minutes.
$ErrorActionPreference = "Stop"
$Root = Split-Path -Parent $PSScriptRoot
if (-not (Test-Path (Join-Path $Root "go.mod"))) {
  # when scripts/ is under Koyori IDE-main
  $Root = $PSScriptRoot + "\.."
}
Set-Location $Root
Write-Host "==> go mod download"
go mod download
Write-Host "==> frontend npm ci"
Set-Location frontend
npm ci
Write-Host "==> pinned Wails bindings"
node ../scripts/generate-bindings.mjs
Write-Host "==> vitest"
node node_modules/vitest/vitest.mjs run
Set-Location $Root
Write-Host "==> go test services (short subset)"
go test ./services/ -count=1 -timeout 120s
Write-Host "==> optional: install gopls"
if (-not (Get-Command gopls -ErrorAction SilentlyContinue)) {
  go install golang.org/x/tools/gopls@latest
}
Write-Host "==> optional: install dlv for debug"
if (-not (Get-Command dlv -ErrorAction SilentlyContinue)) {
  go install github.com/go-delve/delve/cmd/dlv@latest
}
Write-Host "Done. See .github/CONTRIBUTING.md for wails3 dev."
