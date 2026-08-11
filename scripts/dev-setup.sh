#!/usr/bin/env bash
# prompt-11 11-N: one-shot contributor setup (Unix)
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
echo "==> go mod download"
go mod download
echo "==> frontend npm ci"
cd frontend
npm ci
echo "==> pinned Wails bindings"
node ../scripts/generate-bindings.mjs
echo "==> vitest"
npx vitest run
cd "$ROOT"
echo "==> go test services"
go test ./services/ -count=1 -timeout 120s
if ! command -v gopls >/dev/null 2>&1; then
  echo "==> install gopls"
  go install golang.org/x/tools/gopls@latest
fi
if ! command -v dlv >/dev/null 2>&1; then
  echo "==> install dlv"
  go install github.com/go-delve/delve/cmd/dlv@latest
fi
echo "Done. See .github/CONTRIBUTING.md for wails3 dev."
