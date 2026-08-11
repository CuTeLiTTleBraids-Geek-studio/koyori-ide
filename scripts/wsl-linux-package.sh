#!/usr/bin/env bash
# P9-G08 AC3 (Linux leg): build the Linux binary and package a real .deb with
# nfpm, then extract the package metadata and assert the Version equals VERSION.
# Requires nfpm on PATH and dpkg-deb. Usage: bash scripts/wsl-linux-package.sh [repo-root]
set -u

ROOT="${1:-$(pwd)}"
if [ ! -f "$ROOT/go.mod" ]; then echo "[wsl-linux-package] go.mod not found at $ROOT" >&2; exit 2; fi

export GOCACHE="${GOCACHE:-$HOME/.cache/go-build}"
export GOPATH="${GOPATH:-$HOME/go}"
if ! command -v go >/dev/null 2>&1 && [ -x /usr/local/go/bin/go ]; then export PATH="/usr/local/go/bin:$PATH"; fi
if ! command -v nfpm >/dev/null 2>&1 && [ -x "$HOME/go/bin/nfpm" ]; then export PATH="$HOME/go/bin:$PATH"; fi
for cmd in go nfpm dpkg-deb; do
  if ! command -v "$cmd" >/dev/null 2>&1; then echo "[wsl-linux-package] $cmd is not installed" >&2; exit 2; fi
done

cd "$ROOT"
VERSION="$(tr -d '[:space:]' < VERSION)"
echo "[wsl-linux-package] VERSION=$VERSION"

echo "[wsl-linux-package] RUN go build -tags production (linux)"
if ! go build -tags production -trimpath -buildvcs=false -o bin/koyori-ide .; then
  echo "[wsl-linux-package] FAIL linux build" >&2; exit 1
fi

TMP_YAML="$(mktemp)"
sed 's/arch: .*/arch: "amd64"/' build/linux/nfpm/nfpm.yaml > "$TMP_YAML"
DEB="$(mktemp --suffix=.deb)"
echo "[wsl-linux-package] RUN nfpm package (deb)"
if ! nfpm package --config "$TMP_YAML" --packager deb --target "$DEB"; then
  echo "[wsl-linux-package] FAIL nfpm package" >&2; rm -f "$TMP_YAML" "$DEB"; exit 1
fi
rm -f "$TMP_YAML"

PACKED_VERSION="$(dpkg-deb -f "$DEB" Version)"
SHA="$(sha256sum "$DEB" | awk '{print $1}')"
echo "[wsl-linux-package] deb Version=$PACKED_VERSION sha256=$SHA bytes=$(stat -c%s "$DEB")"
# Debian encodes version-revision (nfpm release: "1") as VERSION-1; the
# authoritative upstream version must be the VERSION prefix.
case "$PACKED_VERSION" in
  "$VERSION"|"$VERSION-"*) : ;;
  *)
    echo "[wsl-linux-package] FAIL deb Version $PACKED_VERSION does not start with VERSION $VERSION" >&2
    rm -f "$DEB"; exit 1
    ;;
esac
echo "[wsl-linux-package] OK - real Linux .deb carries VERSION $VERSION (encoded as $PACKED_VERSION)"