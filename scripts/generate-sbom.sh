#!/usr/bin/env bash
# Mandatory SPDX SBOM generation for one final release artifact.
# Requires: https://github.com/anchore/syft (or the pinned Docker image below).
# The manifest digest is deliberately pinned so a tag retarget cannot change
# the generator used for a release SBOM.
set -euo pipefail
if [[ "$#" -ne 2 ]]; then
  echo "usage: $0 <output.spdx.json> <final-artifact>" >&2
  exit 2
fi

OUT="$1"
SUBJECT="$2"
if [[ ! -f "$SUBJECT" ]]; then
  echo "SBOM subject is not a regular file: $SUBJECT" >&2
  exit 1
fi
if [[ "$OUT" == "$SUBJECT" ]]; then
  echo "SBOM output must differ from the final artifact" >&2
  exit 1
fi

mkdir -p "$(dirname "$OUT")"
TMP="${OUT}.tmp.$$"
trap 'rm -f "$TMP"' EXIT
SYFT_IMAGE="anchore/syft:v1.29.0@sha256:e86b0ba0b1d2fe8a2e9f96ed9b22033df9781f43b9a7eb27c57e6c89234946bc"

run_docker_syft() {
  if ! command -v docker >/dev/null 2>&1; then
    echo "Docker is required for pinned SBOM generation" >&2
    exit 1
  fi
  local root subject_abs subject_in_container
  root="$(pwd -P)"
  subject_abs="$(cd "$(dirname "$SUBJECT")" && pwd -P)/$(basename "$SUBJECT")"
  case "$subject_abs" in
    "$root"/*) ;;
    *) echo "Docker SBOM subject must be inside the repository root: $SUBJECT" >&2; exit 1 ;;
  esac
  subject_in_container="/src/${subject_abs#"$root"/}"
  docker run --rm -v "$root:/src:ro" "$SYFT_IMAGE" \
    "file:$subject_in_container" -o spdx-json >"$TMP"
  echo "Wrote $OUT for $SUBJECT via pinned docker syft"
}

case "${SYFT_MODE:-auto}" in
  docker)
    run_docker_syft
    ;;
  auto)
    if [[ -n "${SYFT_BIN:-}" ]]; then
      "$SYFT_BIN" "file:$SUBJECT" -o spdx-json >"$TMP"
      echo "Wrote $OUT for $SUBJECT via $SYFT_BIN"
    elif command -v syft >/dev/null 2>&1; then
      syft "file:$SUBJECT" -o spdx-json >"$TMP"
      echo "Wrote $OUT for $SUBJECT via local syft"
    elif command -v docker >/dev/null 2>&1; then
      run_docker_syft
    else
      echo "syft not found. Install: https://github.com/anchore/syft#installation" >&2
      exit 1
    fi
    ;;
  *)
    echo "unsupported SYFT_MODE: ${SYFT_MODE}" >&2
    exit 2
    ;;
esac
if [[ ! -s "$TMP" ]]; then
  echo "SBOM generator produced an empty file" >&2
  exit 1
fi
mv "$TMP" "$OUT"
trap - EXIT
