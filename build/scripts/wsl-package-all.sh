#!/usr/bin/env bash
# Build Linux desktop packages (deb/rpm/arch/apk/tar.gz/AppImage/.run) in WSL,
# plus cross-compile pure-Go server binaries for macOS offline .run installers.
#
# Usage (inside WSL):
#   bash build/scripts/wsl-package-all.sh
# From Windows:
#   wsl -d Ubuntu -- bash /mnt/e/koyori-ide/Koyori IDE-main/build/scripts/wsl-package-all.sh

set -euo pipefail

export PATH="/usr/local/go/bin:/usr/local/lib/nodejs/bin:${HOME}/go/bin:${PATH}"

RED='\033[0;31m';GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'
info()  { echo -e "${BLUE}[INFO]${NC} $*"; }
ok()    { echo -e "${GREEN}[OK]${NC} $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC} $*"; }
fail()  { echo -e "${RED}[ERROR]${NC} $*"; exit 1; }

# Resolve source tree (Windows mount or native path)
if [ -n "${KOYORI_IDE_SRC:-}" ]; then
  SRC="$KOYORI_IDE_SRC"
elif [ -d /mnt/e/koyori-ide/Koyori IDE-main ]; then
  SRC=/mnt/e/koyori-ide/Koyori IDE-main
elif [ -f "$(dirname "$0")/../../main.go" ]; then
  SRC="$(cd "$(dirname "$0")/../.." && pwd)"
else
  fail "Cannot find project root. Set KOYORI_IDE_SRC."
fi

# App version lives under info.version in build/config.yml (top-level version: is Taskfile schema).
VERSION="0.1.0"
if [ -f "$SRC/build/config.yml" ]; then
  VERSION=$(
    awk '
      /^info:/ { in_info=1; next }
      in_info && /^[^[:space:]#]/ { in_info=0 }
      in_info && /^[[:space:]]+version:[[:space:]]*/ {
        v=$0; sub(/^[[:space:]]+version:[[:space:]]*/, "", v);
        gsub(/["\x27]/, "", v); print v; exit
      }
    ' "$SRC/build/config.yml"
  )
  [ -n "$VERSION" ] || VERSION="0.1.0"
fi
APP_NAME="koyori-ide"
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64) ARCH="amd64" ;;
  aarch64) ARCH="arm64" ;;
esac

# Build on Linux filesystem for speed, copy artifacts back
WORK="${HOME}/koyori-ide-pkg-build"
OUT_WIN="$SRC/bin"
mkdir -p "$OUT_WIN" "$WORK"

info "Source:  $SRC"
info "Work:    $WORK"
info "Version: $VERSION"
info "Arch:    $ARCH"

info "Syncing source to $WORK (excluding node_modules/bin/dist)..."
rsync -a --delete \
  --exclude '.git/' \
  --exclude 'node_modules/' \
  --exclude 'frontend/node_modules/' \
  --exclude 'frontend/dist/' \
  --exclude 'bin/' \
  --exclude '.task/' \
  --exclude 'docs/' \
  --exclude 'DESIGN-*.md' \
  "$SRC/" "$WORK/"

cd "$WORK"

# --------------------------------------------------------------------------
# deps
# --------------------------------------------------------------------------
command -v go >/dev/null || fail "Go not installed. Run wsl-install-toolchain.sh first."
command -v node >/dev/null || fail "Node not installed. Run wsl-install-toolchain.sh first."
command -v gcc >/dev/null || fail "gcc missing."
command -v nfpm >/dev/null || go install github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.44.1

if ! pkg-config --exists webkit2gtk-4.1 && ! pkg-config --exists webkitgtk-6.0; then
  fail "WebKitGTK dev packages missing."
fi

WEBKIT_TAG=""
if pkg-config --exists webkitgtk-6.0 2>/dev/null; then
  ok "WebKit: webkitgtk-6.0"
else
  ok "WebKit: webkit2gtk-4.1 (gtk3 tag)"
  WEBKIT_TAG="gtk3"
fi

# --------------------------------------------------------------------------
# frontend
# --------------------------------------------------------------------------
info "Building frontend..."
cd "$WORK"
node scripts/generate-bindings.mjs
cd "$WORK/frontend"
if [ ! -d node_modules ]; then
  npm install
else
  npm install --prefer-offline || npm install
fi
npx vite build --mode production
cd "$WORK"
ok "Frontend built"

# --------------------------------------------------------------------------
# Linux desktop binary
# --------------------------------------------------------------------------
info "Building Linux desktop binary (CGO + WebKit)..."
export CGO_ENABLED=1
export GOOS=linux
export GOARCH="$ARCH"
mkdir -p "$WORK/bin"
TAGS="production"
if [ -n "$WEBKIT_TAG" ]; then
  TAGS="production,${WEBKIT_TAG}"
fi
go build -tags "$TAGS" -trimpath -buildvcs=false -ldflags="-w -s" -o "$WORK/bin/${APP_NAME}" .
ok "Binary: $(ls -lh "$WORK/bin/${APP_NAME}" | awk '{print $5}')"
file "$WORK/bin/${APP_NAME}" || true

# --------------------------------------------------------------------------
# desktop file + icon
# --------------------------------------------------------------------------
mkdir -p "$WORK/build/linux"
cat > "$WORK/build/linux/${APP_NAME}.desktop" <<EOF
[Desktop Entry]
Type=Application
Name=koyori-ide
GenericName=Code Editor
Comment=AI-Powered Coding Desktop App
Exec=${APP_NAME}
Icon=${APP_NAME}
Categories=Development;IDE;
Terminal=false
StartupWMClass=${APP_NAME}
MimeType=text/plain;
EOF

# --------------------------------------------------------------------------
# nfpm config (all formats)
# --------------------------------------------------------------------------
NFPM_DIR="$WORK/build/linux/nfpm"
mkdir -p "$NFPM_DIR/scripts"

# Adjust runtime deps based on WebKit stack used at build time
if [ -n "$WEBKIT_TAG" ]; then
  DEB_DEPS='  - libgtk-3-0
  - libwebkit2gtk-4.1-0'
  RPM_DEPS='      - gtk3
      - webkit2gtk4.1'
  ARCH_DEPS='      - gtk3
      - webkit2gtk-4.1'
  APK_DEPS='      - gtk+3.0
      - webkit2gtk-4.1'
else
  DEB_DEPS='  - libgtk-4-1
  - libwebkitgtk-6.0-4'
  RPM_DEPS='      - gtk4
      - webkitgtk6.0'
  ARCH_DEPS='      - gtk4
      - webkitgtk-6.0'
  APK_DEPS='      - gtk4.0
      - webkitgtk-6.0'
fi

cat > "$NFPM_DIR/${APP_NAME}.yaml" <<EOF
name: "${APP_NAME}"
arch: "${ARCH}"
platform: "linux"
version: "${VERSION}"
section: "devel"
priority: "optional"
maintainer: "koyori-ide contributors <dianasoylu423@gmail.com>"
description: |
  Offline-first desktop AI IDE for Go and TypeScript/JavaScript.
  Single-binary distribution with sandboxed AI agents.
vendor: "Koyori IDE Contributors"
homepage: "https://github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide"
license: "MIT"
release: "1"

contents:
  - src: "./bin/${APP_NAME}"
    dst: "/usr/local/bin/${APP_NAME}"
    file_info:
      mode: 0755
  - src: "./build/appicon.png"
    dst: "/usr/share/icons/hicolor/128x128/apps/${APP_NAME}.png"
  - src: "./build/linux/${APP_NAME}.desktop"
    dst: "/usr/share/applications/${APP_NAME}.desktop"

depends:
${DEB_DEPS}

overrides:
  rpm:
    depends:
${RPM_DEPS}
  archlinux:
    depends:
${ARCH_DEPS}
  apk:
    depends:
${APK_DEPS}

scripts:
  postinstall: "./build/linux/nfpm/scripts/postinstall.sh"
  postremove: "./build/linux/nfpm/scripts/postremove.sh"
EOF

cat > "$NFPM_DIR/scripts/postinstall.sh" <<'SCRIPT'
#!/bin/sh
command -v update-desktop-database >/dev/null 2>&1 && update-desktop-database -q /usr/share/applications 2>/dev/null || true
command -v gtk-update-icon-cache >/dev/null 2>&1 && gtk-update-icon-cache -q /usr/share/icons/hicolor 2>/dev/null || true
exit 0
SCRIPT
cat > "$NFPM_DIR/scripts/postremove.sh" <<'SCRIPT'
#!/bin/sh
command -v update-desktop-database >/dev/null 2>&1 && update-desktop-database -q /usr/share/applications 2>/dev/null || true
command -v gtk-update-icon-cache >/dev/null 2>&1 && gtk-update-icon-cache -q /usr/share/icons/hicolor 2>/dev/null || true
exit 0
SCRIPT
chmod +x "$NFPM_DIR/scripts/"*.sh

# --------------------------------------------------------------------------
# Package formats
# --------------------------------------------------------------------------
cd "$WORK"
for fmt in deb rpm archlinux apk; do
  info "Packaging $fmt..."
  if nfpm pkg --config "$NFPM_DIR/${APP_NAME}.yaml" --packager "$fmt" --target "$WORK/bin/" 2>&1; then
    ok "$fmt package created"
  else
    warn "$fmt package failed (non-fatal)"
  fi
done

# tar.gz offline portable
info "Creating portable tar.gz..."
STAGE="$WORK/bin/stage-linux"
rm -rf "$STAGE"
mkdir -p "$STAGE"
cp "$WORK/bin/${APP_NAME}" "$STAGE/"
cp "$WORK/build/linux/${APP_NAME}.desktop" "$STAGE/"
cp "$WORK/build/appicon.png" "$STAGE/${APP_NAME}.png"
cat > "$STAGE/install.sh" <<'INSTALL'
#!/bin/bash
set -e
APP_NAME="koyori-ide"
INSTALL_DIR="/opt/${APP_NAME}"
BIN_PATH="/usr/local/bin/${APP_NAME}"
DESKTOP_FILE="/usr/share/applications/${APP_NAME}.desktop"
ICON_FILE="/usr/share/icons/hicolor/128x128/apps/${APP_NAME}.png"
if [ "$(id -u)" -ne 0 ]; then
  echo "Need root — re-running with sudo..."
  exec sudo bash "$0" "$@"
fi
echo "Installing ${APP_NAME} to ${INSTALL_DIR}..."
mkdir -p "${INSTALL_DIR}"
cp "${APP_NAME}" "${INSTALL_DIR}/${APP_NAME}"
chmod +x "${INSTALL_DIR}/${APP_NAME}"
ln -sf "${INSTALL_DIR}/${APP_NAME}" "${BIN_PATH}"
if [ -f "${APP_NAME}.png" ]; then
  mkdir -p "$(dirname "${ICON_FILE}")"
  cp "${APP_NAME}.png" "${ICON_FILE}"
fi
if [ -f "${APP_NAME}.desktop" ]; then
  sed "s|^Exec=.*|Exec=${BIN_PATH}|" "${APP_NAME}.desktop" > "${DESKTOP_FILE}"
  chmod 644 "${DESKTOP_FILE}"
fi
command -v update-desktop-database >/dev/null 2>&1 && update-desktop-database -q /usr/share/applications 2>/dev/null || true
echo "Done. Run: ${BIN_PATH}"
echo "Uninstall: sudo rm -rf ${INSTALL_DIR} ${BIN_PATH} ${DESKTOP_FILE} ${ICON_FILE}"
INSTALL
chmod +x "$STAGE/install.sh" "$STAGE/${APP_NAME}"
TARBALL="$WORK/bin/${APP_NAME}-${VERSION}-linux-${ARCH}-offline.tar.gz"
tar -czf "$TARBALL" -C "$STAGE" .
ok "tar.gz: $TARBALL"

# self-extracting .run offline installer
info "Creating .run offline installer..."
RUN_OUT="$WORK/bin/${APP_NAME}-${VERSION}-linux-${ARCH}-desktop.run"
{
  cat <<HEADER
#!/bin/bash
# koyori-ide ${VERSION} offline desktop installer for linux-${ARCH}
# Usage: chmod +x \$0 && sudo ./\$0
set -e
ARCHIVE_LINE=\$(grep -an '^__ARCHIVE_BELOW__\$' "\$0" | tail -1 | cut -d: -f1)
TMPDIR="/tmp/koyori-ide-install-\$(date +%s)-\$\$"
mkdir -p "\$TMPDIR"
echo "Extracting koyori-ide ${VERSION} offline installer..."
tail -n +\$((ARCHIVE_LINE + 1)) "\$0" | tar xzf - -C "\$TMPDIR"
cd "\$TMPDIR"
bash install.sh
RC=\$?
rm -rf "\$TMPDIR"
exit \$RC
__ARCHIVE_BELOW__
HEADER
  cat "$TARBALL"
} > "$RUN_OUT"
chmod +x "$RUN_OUT"
ok ".run: $RUN_OUT"

# AppImage (best-effort; needs FUSE or extract mode)
create_appimage() {
  info "Creating AppImage (best-effort)..."
  local ADIR="$WORK/build/linux/appimage"
  mkdir -p "$ADIR"
  cd "$ADIR"
  local APPDIR="${APP_NAME}.AppDir"
  rm -rf "$APPDIR"
  mkdir -p "$APPDIR/usr/bin"
  cp "$WORK/bin/${APP_NAME}" "$APPDIR/usr/bin/"
  cp "$WORK/build/appicon.png" "$APPDIR/${APP_NAME}.png"
  cp "$WORK/build/linux/${APP_NAME}.desktop" "$APPDIR/"
  # AppRun
  cat > "$APPDIR/AppRun" <<'APPRUN'
#!/bin/bash
HERE="$(dirname "$(readlink -f "$0")")"
exec "$HERE/usr/bin/koyori-ide" "$@"
APPRUN
  chmod +x "$APPDIR/AppRun" "$APPDIR/usr/bin/${APP_NAME}"

  local LD="linuxdeploy-x86_64.AppImage"
  if [ "$ARCH" = "arm64" ]; then LD="linuxdeploy-aarch64.AppImage"; fi
  if [ ! -f "$LD" ]; then
    wget -q -4 -N "https://github.com/linuxdeploy/linuxdeploy/releases/download/continuous/$LD" || {
      warn "linuxdeploy download failed"
      return 0
    }
    chmod +x "$LD"
  fi
  # AppImage often needs FUSE; try extract-and-run
  export APPIMAGE_EXTRACT_AND_RUN=1
  if ./"$LD" --appdir "$APPDIR" --output appimage 2>&1; then
    local OUTNAME="${APP_NAME}-${VERSION}-linux-${ARCH}.AppImage"
    mv ${APP_NAME}*.AppImage "$WORK/bin/$OUTNAME" 2>/dev/null || true
    ok "AppImage: $OUTNAME"
  else
    # fallback: pack AppDir as tar.gz portable
    tar -czf "$WORK/bin/${APP_NAME}-${VERSION}-linux-${ARCH}-AppDir.tar.gz" -C "$ADIR" "$APPDIR"
    warn "AppImage tool failed; wrote AppDir tar.gz instead"
  fi
  cd "$WORK"
}
create_appimage || warn "AppImage step skipped"

# --------------------------------------------------------------------------
# macOS: pure-Go server mode (desktop GUI needs macOS SDK / real Mac / Docker wails-cross)
# --------------------------------------------------------------------------
info "Cross-building macOS server binaries (offline HTTP mode, CGO=0)..."
export CGO_ENABLED=0
for darch in amd64 arm64; do
  out="$WORK/bin/${APP_NAME}-server-darwin-${darch}"
  if GOOS=darwin GOARCH="$darch" go build -tags "server,production" -trimpath -buildvcs=false -ldflags="-w -s" -o "$out" . 2>&1; then
    ok "darwin/${darch} server: $(ls -lh "$out" | awk '{print $5}')"
  else
    warn "darwin/${darch} server build failed"
  fi
done

# also linux server for completeness
for darch in amd64 arm64; do
  # only build native arch for linux server with CGO=0 as extra portable
  if [ "$darch" != "$ARCH" ] && [ "$darch" = "arm64" ]; then
    # try cross pure-go
    :
  fi
  out="$WORK/bin/${APP_NAME}-server-linux-${darch}"
  if GOOS=linux GOARCH="$darch" CGO_ENABLED=0 go build -tags "server,production" -trimpath -buildvcs=false -ldflags="-w -s" -o "$out" . 2>&1; then
    ok "linux/${darch} server: $(ls -lh "$out" | awk '{print $5}')"
  else
    warn "linux/${darch} server build failed"
  fi
done

# macOS offline .run installers (server mode)
make_macos_run() {
  local darch="$1"
  local bin="$WORK/bin/${APP_NAME}-server-darwin-${darch}"
  [ -f "$bin" ] || return 0
  local stage="$WORK/bin/stage-darwin-${darch}"
  rm -rf "$stage"
  mkdir -p "$stage"
  cp "$bin" "$stage/koyori-ide-server"
  cp "$WORK/build/appicon.png" "$stage/koyori-ide.png" 2>/dev/null || true
  cat > "$stage/install.sh" <<'MACINST'
#!/bin/bash
set -e
APP_NAME="koyori-ide"
APP_BUNDLE="/Applications/${APP_NAME}.app"
if [ "$(uname)" != "Darwin" ]; then
  echo "This installer is for macOS only"; exit 1
fi
echo "Installing koyori-ide server mode to ${APP_BUNDLE}..."
[ -d "${APP_BUNDLE}" ] && rm -rf "${APP_BUNDLE}"
MACOS_DIR="${APP_BUNDLE}/Contents/MacOS"
RESOURCES_DIR="${APP_BUNDLE}/Contents/Resources"
mkdir -p "${MACOS_DIR}" "${RESOURCES_DIR}"
cp koyori-ide-server "${MACOS_DIR}/${APP_NAME}-server"
chmod +x "${MACOS_DIR}/${APP_NAME}-server"
cat > "${MACOS_DIR}/${APP_NAME}" <<'LAUNCHER'
#!/bin/bash
DIR="$(dirname "$0")"
WAILS_SERVER_HOST=127.0.0.1 WAILS_SERVER_PORT=34115 "${DIR}/koyori-ide-server" &
SERVER_PID=$!
sleep 2
open "http://localhost:34115" 2>/dev/null || open "http://127.0.0.1:34115"
wait $SERVER_PID
LAUNCHER
chmod +x "${MACOS_DIR}/${APP_NAME}"
cat > "${APP_BUNDLE}/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleExecutable</key>
    <string>koyori-ide</string>
    <key>CFBundleIdentifier</key>
    <string>com.koyori.app</string>
    <key>CFBundleName</key>
    <string>Koyori IDE</string>
    <key>CFBundleDisplayName</key>
    <string>Koyori IDE</string>
    <key>CFBundleVersion</key>
    <string>1</string>
    <key>CFBundleShortVersionString</key>
    <string>0.1.0</string>
    <key>CFBundlePackageType</key>
    <string>APPL</string>
    <key>LSMinimumSystemVersion</key>
    <string>12.0</string>
    <key>NSHighResolutionCapable</key>
    <true/>
</dict>
</plist>
PLIST
[ -f koyori-ide.png ] && cp koyori-ide.png "${RESOURCES_DIR}/" || true
codesign --force --deep --sign - "${APP_BUNDLE}" 2>/dev/null || true
xattr -rd com.apple.quarantine "${APP_BUNDLE}" 2>/dev/null || true
echo "Done. Launch: open ${APP_BUNDLE}"
echo "Note: this is server-mode (browser UI). Native desktop GUI requires building on macOS."
MACINST
  chmod +x "$stage/install.sh"
  local tg="$WORK/bin/${APP_NAME}-${VERSION}-darwin-${darch}-offline.tar.gz"
  tar -czf "$tg" -C "$stage" .
  local run="$WORK/bin/${APP_NAME}-${VERSION}-darwin-${darch}.run"
  {
    cat <<HEADER
#!/bin/bash
# koyori-ide ${VERSION} offline installer for darwin-${darch} (server mode)
set -e
ARCHIVE_LINE=\$(grep -an '^__ARCHIVE_BELOW__\$' "\$0" | tail -1 | cut -d: -f1)
TMPDIR="/tmp/koyori-ide-install-\$(date +%s)-\$\$"
mkdir -p "\$TMPDIR"
echo "Extracting koyori-ide ${VERSION} (darwin-${darch})..."
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
  ok "macOS .run: $(basename "$run")"
}
make_macos_run amd64
make_macos_run arm64

# linux server .run as well
make_linux_server_run() {
  local darch="$1"
  local bin="$WORK/bin/${APP_NAME}-server-linux-${darch}"
  [ -f "$bin" ] || return 0
  local stage="$WORK/bin/stage-server-linux-${darch}"
  rm -rf "$stage"
  mkdir -p "$stage"
  cp "$bin" "$stage/koyori-ide-server"
  cp "$WORK/build/appicon.png" "$stage/koyori-ide.png" 2>/dev/null || true
  cat > "$stage/install.sh" <<'LINST'
#!/bin/bash
set -e
APP_NAME="koyori-ide"
INSTALL_DIR="/opt/${APP_NAME}"
BIN_PATH="/usr/local/bin/${APP_NAME}"
if [ "$(id -u)" -ne 0 ]; then exec sudo bash "$0" "$@"; fi
mkdir -p "${INSTALL_DIR}"
cp koyori-ide-server "${INSTALL_DIR}/${APP_NAME}"
chmod +x "${INSTALL_DIR}/${APP_NAME}"
ln -sf "${INSTALL_DIR}/${APP_NAME}" "${BIN_PATH}"
echo "Installed server mode to ${BIN_PATH}"
echo "Run: WAILS_SERVER_HOST=127.0.0.1 WAILS_SERVER_PORT=34115 ${BIN_PATH}  then open http://localhost:34115"
LINST
  chmod +x "$stage/install.sh"
  local tg="$WORK/bin/${APP_NAME}-${VERSION}-linux-${darch}-server-offline.tar.gz"
  tar -czf "$tg" -C "$stage" .
  local run="$WORK/bin/${APP_NAME}-${VERSION}-linux-${darch}-server.run"
  {
    cat <<HEADER
#!/bin/bash
set -e
ARCHIVE_LINE=\$(grep -an '^__ARCHIVE_BELOW__\$' "\$0" | tail -1 | cut -d: -f1)
TMPDIR="/tmp/koyori-ide-install-\$(date +%s)-\$\$"
mkdir -p "\$TMPDIR"
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
  ok "Linux server .run: $(basename "$run")"
}
make_linux_server_run amd64
make_linux_server_run arm64

# --------------------------------------------------------------------------
# SHA256SUMS + copy back to Windows tree
# --------------------------------------------------------------------------
info "Writing SHA256SUMS..."
cd "$WORK/bin"
# remove stages
rm -rf stage-* 2>/dev/null || true
sha256sum \
  "${APP_NAME}" \
  *.deb *.rpm *.apk *.pkg.tar.zst *.tar.gz *.run *.AppImage 2>/dev/null \
  | grep -v ' SHA256SUMS' > SHA256SUMS || true
# also try without globs failing
find . -maxdepth 1 -type f ! -name 'SHA256SUMS' ! -name 'stage-*' -printf '%P\n' 2>/dev/null | while read -r f; do
  sha256sum "$f"
done | sort > SHA256SUMS

info "Copying artifacts to $OUT_WIN ..."
mkdir -p "$OUT_WIN"
# copy packages only
find "$WORK/bin" -maxdepth 1 -type f \( \
  -name '*.deb' -o -name '*.rpm' -o -name '*.apk' -o -name '*.pkg.tar.zst' \
  -o -name '*.tar.gz' -o -name '*.run' -o -name '*.AppImage' \
  -o -name 'SHA256SUMS' -o -name "${APP_NAME}" \
  -o -name "${APP_NAME}-server-*" \
\) -exec cp -f {} "$OUT_WIN/" \;

echo ""
ok "============================================"
ok "  Packaging complete"
ok "============================================"
info "Artifacts in: $OUT_WIN"
ls -lh "$OUT_WIN" | sed -n '1,80p'
echo ""
info "Linux desktop install examples:"
info "  deb:      sudo dpkg -i ${APP_NAME}_${VERSION}_${ARCH}.deb"
info "  rpm:      sudo rpm -i ${APP_NAME}-${VERSION}-1.${ARCH}.rpm"
info "  arch:     sudo pacman -U ${APP_NAME}-${VERSION}-1-${ARCH}.pkg.tar.zst"
info "  apk:      sudo apk add --allow-untrusted ${APP_NAME}-${VERSION}-r1.apk  (or nfpm name)"
info "  offline:  chmod +x ${APP_NAME}-${VERSION}-linux-${ARCH}-desktop.run && sudo ./${APP_NAME}-${VERSION}-linux-${ARCH}-desktop.run"
info "  portable: tar xzf ${APP_NAME}-${VERSION}-linux-${ARCH}-offline.tar.gz && sudo ./install.sh"
echo ""
warn "macOS native desktop GUI (.app + WebKit) cannot be fully built on WSL without"
warn "Docker wails-cross + macOS SDK. Produced: server-mode offline .run for Intel/Apple Silicon."
warn "For true macOS desktop DMG: run build/scripts/build-macos.sh on a Mac."
