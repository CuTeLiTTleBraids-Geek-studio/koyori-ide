#!/usr/bin/env bash
# Collect reproducible release evidence without claiming artifact signatures.
set -euo pipefail

if [[ $# -lt 1 ]]; then
  echo "usage: $0 <artifact> [artifact ...]" >&2
  exit 2
fi

if [[ ! -f go.mod ]]; then
  echo "run this script from the repository root" >&2
  exit 1
fi
if ! command -v go >/dev/null 2>&1; then
  echo "Go is required to run the pinned govulncheck" >&2
  exit 1
fi

for artifact in "$@"; do
  if [[ ! -f "$artifact" ]]; then
    echo "artifact not found: $artifact" >&2
    exit 1
  fi
done

if command -v sha256sum >/dev/null 2>&1; then
  sha256sum "$@" > SHA256SUMS
elif command -v shasum >/dev/null 2>&1; then
  shasum -a 256 "$@" > SHA256SUMS
else
  echo "sha256sum or shasum is required" >&2
  exit 1
fi
echo "Wrote SHA256SUMS"

if command -v syft >/dev/null 2>&1 || command -v docker >/dev/null 2>&1; then
  for artifact in "$@"; do
    sbom="${artifact}.sbom.spdx.json"
    bash scripts/generate-sbom.sh "$sbom" "$artifact"
    if command -v sha256sum >/dev/null 2>&1; then
      sha256sum "$sbom" >> SHA256SUMS
    else
      shasum -a 256 "$sbom" >> SHA256SUMS
    fi
  done
else
  echo "SBOM: syft or Docker is required; release evidence is incomplete" >&2
  exit 1
fi

go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./services/... .

echo "Signing: not checked. Verify each platform signature separately and disclose unsigned artifacts."
