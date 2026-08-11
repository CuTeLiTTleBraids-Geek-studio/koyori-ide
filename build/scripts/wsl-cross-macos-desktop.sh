#!/usr/bin/env bash
# Cross-compile koyori-ide macOS desktop GUI (arm64 + amd64) via Docker wails-cross,
# then assemble offline .app bundles + .tar.gz + .run installers.
set -euo pipefail

export PATH="/usr/local/go/bin:/usr/local/lib/nodejs/bin:${HOME}/go/bin:/usr/bin:/bin:/usr/sbin:/sbin:${PATH}"

RED='\033[0;31m';GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'
info()  { echo -e "${BLUE}[INFO]${NC} $*"; }
ok()    { echo -e "${GREEN}[OK]${NC} $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC} $*"; }
fail()  { echo -e "${RED}[ERROR]${NC} $*"; exit 1; }

# Default to the checkout containing this script. A caller that deliberately
# keeps sources elsewhere may override it with KOYORI_IDE_SOURCE_DIR.
SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
DEFAULT_SOURCE_DIR=$(cd -- "$SCRIPT_DIR/../.." && pwd -P)
SOURCE_DIR_INPUT="${KOYORI_IDE_SOURCE_DIR:-$DEFAULT_SOURCE_DIR}"
if ! SRC=$(cd -- "$SOURCE_DIR_INPUT" 2>/dev/null && pwd -P); then
  fail "source directory does not exist: $SOURCE_DIR_INPUT"
fi
if [ ! -f "$SRC/go.mod" ] || [ ! -f "$SRC/build/docker/Dockerfile.cross" ]; then
  fail "source directory is not a Koyori IDE checkout: $SRC"
fi
APP=koyori-ide
VER=0.1.0
if [ -f "$SRC/build/config.yml" ]; then
  V=$(awk '
    /^info:/ { in_info=1; next }
    in_info && /^[^[:space:]#]/ { in_info=0 }
    in_info && /^[[:space:]]+version:[[:space:]]*/ {
      v=$0; sub(/^[[:space:]]+version:[[:space:]]*/, "", v);
      sub(/#.*$/, "", v);
      gsub(/["\x27[:space:]]/, "", v); print v; exit
    }
  ' "$SRC/build/config.yml")
  [ -n "$V" ] && VER="$V"
fi

# Work on Linux FS for speed; mount project into docker from WORK
WORK="${HOME}/koyori-ide-macos-cross"
OUT="$SRC/bin"
mkdir -p "$OUT" "$WORK"

info "Version: $VER"
info "Work:    $WORK"
info "Out:     $OUT"

# Ensure Docker is up
if ! command -v docker >/dev/null 2>&1; then
  fail "docker not found. Run build/scripts/wsl-install-docker.sh first."
fi
if ! docker info >/dev/null 2>&1; then
  info "Starting Docker..."
  sudo service docker start || true
  for i in $(seq 1 20); do
    docker info >/dev/null 2>&1 && break
    sleep 1
  done
fi
docker info >/dev/null 2>&1 || fail "Docker daemon not running"

# Sync source (exclude heavy junk)
info "Syncing source..."
rsync -a --delete \
  --exclude '.git/' \
  --exclude 'node_modules/' \
  --exclude 'frontend/node_modules/' \
  --exclude 'frontend/dist/' \
  --exclude 'bin/' \
  --exclude '.task/' \
  --exclude 'docs/' \
  --exclude 'DESIGN-*.md' \
  --exclude 'build/linux/appimage/' \
  "$SRC/" "$WORK/"

# Verify the pinned bindings entry point before handing the clean source tree to
# the container. Dockerfile.cross still rebuilds bindings and dist itself.
info "Verifying bindings..."
export PATH="/usr/local/lib/nodejs/bin:/usr/local/go/bin:${HOME}/go/bin:${PATH}"
cd "$WORK"
node scripts/generate-bindings.mjs
node scripts/check-bindings.mjs
ok "Bindings verified"

# Always evaluate the current Dockerfile; Docker layer caching keeps this fast
# while preventing an old image from retaining a stale build entry point.
info "Building wails-cross Docker image..."
docker build -t wails-cross -f build/docker/Dockerfile.cross build/docker/
ok "wails-cross image built"

# Cross-compile both arches
build_arch() {
  local arch="$1"
  info "Cross-compiling darwin/${arch} desktop (CGO + macOS SDK via Zig)..."
  docker run --rm \
    -v "$WORK:/app" \
    -e APP_NAME="$APP" \
    -e EXTRA_TAGS="" \
    wails-cross darwin "$arch"
  local bin="$WORK/bin/${APP}-darwin-${arch}"
  [ -f "$bin" ] || fail "Missing output: $bin"
  ok "Built $bin ($(du -h "$bin" | awk '{print $1}'))"
  file "$bin" || true
}

build_arch arm64
build_arch amd64

# Docker often writes bin/ as root — fix ownership before packaging
sudo chown -R "$(id -u):$(id -g)" "$WORK/bin" 2>/dev/null || true

# Assemble .app bundle (unsigned offline portable; codesign on real Mac recommended)
make_app_bundle() {
  local arch="$1"
  local bin="$WORK/bin/${APP}-darwin-${arch}"
  local stage="$WORK/bin/stage-app-${arch}"
  local appdir="$stage/${APP}.app"

  rm -rf "$stage"
  mkdir -p "$appdir/Contents/MacOS" "$appdir/Contents/Resources"

  cp "$bin" "$appdir/Contents/MacOS/${APP}"
  chmod +x "$appdir/Contents/MacOS/${APP}"

  if [ -f "$WORK/build/darwin/icons.icns" ]; then
    cp "$WORK/build/darwin/icons.icns" "$appdir/Contents/Resources/"
  fi
  if [ -f "$WORK/build/darwin/Assets.car" ]; then
    cp "$WORK/build/darwin/Assets.car" "$appdir/Contents/Resources/"
  fi
  if [ -f "$WORK/build/appicon.png" ]; then
    cp "$WORK/build/appicon.png" "$appdir/Contents/Resources/appicon.png"
  fi

  if [ -f "$WORK/build/darwin/Info.plist" ]; then
    cp "$WORK/build/darwin/Info.plist" "$appdir/Contents/Info.plist"
  else
    cat > "$appdir/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleExecutable</key>
  <string>${APP}</string>
  <key>CFBundleIdentifier</key>
  <string>com.koyori.app</string>
  <key>CFBundleName</key>
  <string>Koyori IDE</string>
  <key>CFBundleDisplayName</key>
  <string>Koyori IDE</string>
  <key>CFBundleVersion</key>
  <string>${VER}</string>
  <key>CFBundleShortVersionString</key>
  <string>${VER}</string>
  <key>CFBundlePackageType</key>
  <string>APPL</string>
  <key>LSMinimumSystemVersion</key>
  <string>12.0</string>
  <key>NSHighResolutionCapable</key>
  <true/>
  <key>NSAppTransportSecurity</key>
  <dict>
    <key>NSAllowsArbitraryLoads</key>
    <true/>
  </dict>
</dict>
</plist>
PLIST
  fi

  # Offline install script for macOS
  cat > "$stage/install.sh" <<'INST'
#!/bin/bash
set -e
APP_NAME="koyori-ide"
SRC_APP="$(cd "$(dirname "$0")" && pwd)/${APP_NAME}.app"
DEST="/Applications/${APP_NAME}.app"
if [ "$(uname)" != "Darwin" ]; then
  echo "This package is for macOS only"; exit 1
fi
echo "Installing ${APP_NAME} desktop to ${DEST}..."
rm -rf "${DEST}"
cp -R "${SRC_APP}" "${DEST}"
# Clear quarantine so first launch works without Gatekeeper block (user can still right-click open)
xattr -rd com.apple.quarantine "${DEST}" 2>/dev/null || true
# Ad-hoc codesign if available
if command -v codesign >/dev/null 2>&1; then
  codesign --force --deep --sign - "${DEST}" 2>/dev/null || true
fi
echo "Done. Launch: open ${DEST}"
echo "If macOS blocks it: right-click app → Open → confirm"
INST
  chmod +x "$stage/install.sh"

  # tar.gz portable
  local tg="$WORK/bin/${APP}-${VER}-darwin-${arch}-desktop.tar.gz"
  tar -czf "$tg" -C "$stage" .
  ok "tar.gz: $(basename "$tg") ($(du -h "$tg" | awk '{print $1}'))"

  # self-extracting .run
  local run="$WORK/bin/${APP}-${VER}-darwin-${arch}-desktop.run"
  {
    cat <<HEADER
#!/bin/bash
# koyori-ide ${VER} macOS desktop offline installer (${arch})
# Usage on Mac: chmod +x \$0 && ./\$0
set -e
ARCHIVE_LINE=\$(grep -an '^__ARCHIVE_BELOW__\$' "\$0" | tail -1 | cut -d: -f1)
TMPDIR="/tmp/koyori-ide-install-\$(date +%s)-\$\$"
mkdir -p "\$TMPDIR"
echo "Extracting koyori-ide ${VER} desktop (${arch})..."
tail -n +\$((ARCHIVE_LINE + 1)) "\$0" | tar xzf - -C "\$TMPDIR"
cd "\$TMPDIR"
bash install.sh
RC=\$?
rm -rf "\$TMPDIR"
exit \$RC
__ARCHIVE_BELOW__
HEADER
    cat "$tg"
  } > "$run"
  chmod +x "$run"
  ok ".run: $(basename "$run") ($(du -h "$run" | awk '{print $1}'))"

  # also keep raw binary
  cp -f "$bin" "$WORK/bin/${APP}-${VER}-darwin-${arch}"
}

make_app_bundle arm64
make_app_bundle amd64

# Copy artifacts to Windows bin
info "Copying to $OUT ..."
for arch in arm64 amd64; do
  for f in \
    "${APP}-darwin-${arch}" \
    "${APP}-${VER}-darwin-${arch}" \
    "${APP}-${VER}-darwin-${arch}-desktop.tar.gz" \
    "${APP}-${VER}-darwin-${arch}-desktop.run"
  do
    [ -f "$WORK/bin/$f" ] && cp -f "$WORK/bin/$f" "$OUT/"
  done
done

# Update SHA256SUMS (append / regenerate desktop macos lines)
cd "$OUT"
{
  echo "# koyori-ide macOS desktop cross-build ${VER}"
  for f in \
    "${APP}-${VER}-darwin-arm64-desktop.run" \
    "${APP}-${VER}-darwin-arm64-desktop.tar.gz" \
    "${APP}-${VER}-darwin-amd64-desktop.run" \
    "${APP}-${VER}-darwin-amd64-desktop.tar.gz" \
    "${APP}-darwin-arm64" \
    "${APP}-darwin-amd64"
  do
    [ -f "$f" ] && sha256sum "$f"
  done
} > SHA256SUMS-macos-desktop.txt
cat SHA256SUMS-macos-desktop.txt

echo ""
ok "============================================"
ok "  macOS desktop cross-compile complete"
ok "============================================"
ls -lh "$OUT"/${APP}*darwin*desktop* "$OUT"/${APP}-darwin-* 2>/dev/null || true
echo ""
info "On a Mac:"
info "  chmod +x ${APP}-${VER}-darwin-arm64-desktop.run   # Apple Silicon"
info "  ./$(echo ${APP}-${VER}-darwin-arm64-desktop.run)"
info "  # or Intel: ${APP}-${VER}-darwin-amd64-desktop.run"
info "First open: right-click app → Open if Gatekeeper warns."
warn "Unsigned cross-build: not notarized. Prefer codesign on a Mac for distribution."
