#!/usr/bin/env bash
# Read the stable release version without normalizing malformed file contents.
set -euo pipefail
export LC_ALL=C

if [ "$#" -gt 1 ]; then
  echo "usage: $0 [VERSION-file]" >&2
  exit 2
fi

version_file="${1:-VERSION}"
if [ ! -f "$version_file" ]; then
  echo "VERSION file not found: $version_file" >&2
  exit 1
fi

version=""
line_ending_bytes=0
if IFS= read -r version < "$version_file"; then
  line_ending_bytes=1
  if [[ "$version" == *$'\r' ]]; then
    version="${version%$'\r'}"
    line_ending_bytes=2
  fi
fi

# Bash variables cannot retain NUL bytes. Comparing the original byte count
# with the one validated line also rejects NULs, extra lines, and trailing data.
actual_bytes="$(wc -c < "$version_file")"
actual_bytes="${actual_bytes//[[:space:]]/}"
expected_bytes="$(( ${#version} + line_ending_bytes ))"
if [[ ! "$actual_bytes" =~ ^[0-9]+$ ]] || [ "$actual_bytes" -ne "$expected_bytes" ]; then
  echo "VERSION must contain exactly one value with at most one LF or CRLF terminator" >&2
  exit 1
fi

if [[ ! "$version" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
  echo "VERSION must be a stable X.Y.Z value: ${version:-<empty>}" >&2
  exit 1
fi

printf '%s\n' "$version"
