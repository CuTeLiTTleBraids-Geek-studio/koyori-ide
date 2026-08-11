#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
go_bin="${GO_BIN:-go}"

if ! command -v "$go_bin" >/dev/null 2>&1; then
  windows_go="/mnt/c/Program Files/Go/bin/go.exe"
  if [[ "$go_bin" == "go" && -x "$windows_go" ]]; then
    go_bin="$windows_go"
  else
    printf 'Go executable not found: %s\n' "$go_bin" >&2
    exit 127
  fi
fi

cd "$repo_root"
"$go_bin" test ./services -run '^$' -bench 'Benchmark(SearchWorkspace1KFiles|SymbolSearch100K)$' -benchmem -benchtime=500ms -count=3

cd "$repo_root/frontend"
npm test -- --run src/components/ai-assistant/MessageList.test.ts src/components/explorer/FileTree.test.ts
