#!/usr/bin/env bash
# P9-G07 AC3 Linux leg: run the CI go-test package set on a real Linux
# environment (WSL2 Ubuntu). This is reproducible I-level evidence; it is NOT
# a CI run (R). Usage: bash scripts/wsl-linux-gate.sh [repo-root]
set -u

ROOT="${1:-$(pwd)}"
if [ ! -f "$ROOT/go.mod" ]; then
  echo "[wsl-linux-gate] go.mod not found at $ROOT" >&2
  exit 2
fi
if ! command -v go >/dev/null 2>&1 && [ -x /usr/local/go/bin/go ]; then
  export PATH="/usr/local/go/bin:$PATH"
fi
if ! command -v go >/dev/null 2>&1; then
  echo "[wsl-linux-gate] go is not installed in this Linux environment" >&2
  exit 2
fi

export GOCACHE="${GOCACHE:-$HOME/.cache/go-build}"
export GOPATH="${GOPATH:-$HOME/go}"
export CGO_ENABLED="${CGO_ENABLED:-1}"

cd "$ROOT"
echo "[wsl-linux-gate] $(uname -sr) $(go version)"
echo "[wsl-linux-gate] go env GOOS=$(go env GOOS) GOARCH=$(go env GOARCH) CGO_ENABLED=$(go env CGO_ENABLED)"

run() {
  local name="$1"; shift
  echo "[wsl-linux-gate] RUN $name"
  if ! "$@"; then
    echo "[wsl-linux-gate] FAIL $name (exit $?)" >&2
    exit 1
  fi
  echo "[wsl-linux-gate] PASS $name"
}

run "go build ." go build -buildvcs=false .
run "go vet CI set" go vet ./services/... . ./internal/repo/...
run "go test -race CI set" go test -race ./services/... . ./internal/repo/... -count=1
run "go test -race -tags e2e internal/e2e" go test -race -tags e2e ./internal/e2e/... -count=1
echo "[wsl-linux-gate] RUN go test ./... -count=1 (with [no tests to run] check)"
if ! output="$(go test ./... -count=1 2>&1)"; then
  echo "[wsl-linux-gate] FAIL go test ./... -count=1" >&2
  printf '%s\n' "$output" | tail -20 >&2
  exit 1
fi
# P9-G07 AC4: an empty test binary must not be counted as a pass.
if printf '%s\n' "$output" | grep -q '\[no tests to run\]'; then
  echo "[wsl-linux-gate] FAIL go test ./... reported [no tests to run]" >&2
  exit 1
fi
echo "[wsl-linux-gate] PASS go test ./... -count=1 (no [no tests to run])"

unformatted="$(gofmt -l .)"
if [ -n "$unformatted" ]; then
  echo "[wsl-linux-gate] FAIL gofmt -l . is not empty:" >&2
  echo "$unformatted" >&2
  exit 1
fi
echo "[wsl-linux-gate] PASS gofmt -l . (empty)"
echo "[wsl-linux-gate] OK - all Linux gates passed"