#!/usr/bin/env bash
# Install Go / Node / nfpm / wails3 inside WSL for offline packaging builds.
set -euo pipefail

export DEBIAN_FRONTEND=noninteractive
LOG="${HOME}/guga-toolchain.log"
exec > >(tee "$LOG") 2>&1
DOWNLOAD_DIR="$(mktemp -d)"
trap 'rm -rf "$DOWNLOAD_DIR"' EXIT

echo "=== install Go 1.25 ==="
if [ ! -x /usr/local/go/bin/go ]; then
  readonly GO_VERSION="1.25.0"
  readonly GO_ARCHIVE="go${GO_VERSION}.linux-amd64.tar.gz"
  readonly GO_SHA256="2852af0cb20a13139b3448992e69b868e50ed0f8a1e5940ee1de9e19a123b613"
  curl -fsSL "https://go.dev/dl/${GO_ARCHIVE}" -o "${DOWNLOAD_DIR}/${GO_ARCHIVE}"
  echo "${GO_SHA256}  ${DOWNLOAD_DIR}/${GO_ARCHIVE}" | sha256sum -c -
  sudo rm -rf /usr/local/go
  sudo tar -C /usr/local -xzf "${DOWNLOAD_DIR}/${GO_ARCHIVE}"
fi
export PATH="/usr/local/go/bin:${HOME}/go/bin:${PATH}"
go version

echo "=== install Node 20 ==="
if [ ! -x /usr/local/lib/nodejs/bin/node ]; then
  readonly NODE_VERSION="20.19.2"
  readonly NODE_ARCHIVE="node-v${NODE_VERSION}-linux-x64.tar.xz"
  readonly NODE_SHA256="cbe59620b21732313774df4428586f7222a84af29e556f848abf624ba41caf90"
  curl -fsSL "https://nodejs.org/dist/v${NODE_VERSION}/${NODE_ARCHIVE}" -o "${DOWNLOAD_DIR}/${NODE_ARCHIVE}"
  echo "${NODE_SHA256}  ${DOWNLOAD_DIR}/${NODE_ARCHIVE}" | sha256sum -c -
  sudo mkdir -p /usr/local/lib/nodejs
  sudo tar -xJf "${DOWNLOAD_DIR}/${NODE_ARCHIVE}" -C /usr/local/lib/nodejs --strip-components=1
fi
export PATH="/usr/local/lib/nodejs/bin:${PATH}"
node -v
npm -v

echo "=== install nfpm v2.44.1 ==="
go install github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.44.1
nfpm --version

echo "=== install wails3 ==="
go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-beta.8 || true
command -v wails3 && wails3 version || echo "wails3 optional"

echo "TOOLCHAIN_OK"
