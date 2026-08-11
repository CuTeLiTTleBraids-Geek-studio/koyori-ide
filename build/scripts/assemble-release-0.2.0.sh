#!/usr/bin/env bash
# Assemble v0.2.0 release artifacts from existing/cross-built binaries.
set -euo pipefail
export PATH="/usr/local/go/bin:/usr/local/lib/nodejs/bin:${HOME}/go/bin:/usr/bin:/bin:${PATH}"

SRC=/mnt/e/koyori-ide/Koyori IDE-main
OUT="$SRC/bin/release-v0.2.0"
VER=0.2.0
APP=koyori-ide
LINUX_WORK="${HOME}/koyori-ide-pkg-build"
MAC_WORK="${HOME}/koyori-ide-macos-cross"

mkdir -p "$OUT"
rm -rf "$OUT"/*
info(){ echo "[INFO] $*"; }
ok(){ echo "[OK] $*"; }

# ---- Linux desktop packages from WSL build tree ----
if [ ! -x "$LINUX_WORK/bin/koyori-ide" ]; then
  # fall back to Windows-mounted bin if present
  if [ -x "$SRC/bin/koyori-ide" ]; then
    mkdir -p "$LINUX_WORK/bin"
    cp -f "$SRC/bin/koyori-ide" "$LINUX_WORK/bin/koyori-ide"
    # need appicon + desktop for nfpm - from work or src
    mkdir -p "$LINUX_WORK/build/linux/nfpm/scripts" "$LINUX_WORK/build/linux"
    cp -f "$SRC/build/appicon.png" "$LINUX_WORK/build/appicon.png" 2>/dev/null || true
  else
    echo "Missing Linux desktop binary"; exit 1
  fi
fi

# ensure desktop file + nfpm assets
mkdir -p "$LINUX_WORK/build/linux/nfpm/scripts"
[ -f "$LINUX_WORK/build/appicon.png" ] || cp -f "$SRC/build/appicon.png" "$LINUX_WORK/build/appicon.png"
cat > "$LINUX_WORK/build/linux/${APP}.desktop" <<EOF
[Desktop Entry]
Type=Application
Name=koyori-ide
GenericName=Code Editor
Comment=AI-Powered Coding Desktop App
Exec=${APP}
Icon=${APP}
Categories=Development;IDE;
Terminal=false
StartupWMClass=${APP}
MimeType=text/plain;
EOF

cat > "$LINUX_WORK/build/linux/nfpm/${APP}.yaml" <<EOF
name: "${APP}"
arch: "amd64"
platform: "linux"
version: "${VER}"
section: "devel"
priority: "optional"
maintainer: "koyori-ide contributors <dianasoylu423@gmail.com>"
description: |
  Offline-first desktop AI IDE for Go and TypeScript/JavaScript.
vendor: "Koyori IDE Contributors"
homepage: "https://github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide"
license: "MIT"
release: "1"
contents:
  - src: "./bin/${APP}"
    dst: "/usr/local/bin/${APP}"
    file_info:
      mode: 0755
  - src: "./build/appicon.png"
    dst: "/usr/share/icons/hicolor/128x128/apps/${APP}.png"
  - src: "./build/linux/${APP}.desktop"
    dst: "/usr/share/applications/${APP}.desktop"
depends:
  - libgtk-3-0
  - libwebkit2gtk-4.1-0
overrides:
  rpm:
    depends:
      - gtk3
      - webkit2gtk4.1
  archlinux:
    depends:
      - gtk3
      - webkit2gtk-4.1
  apk:
    depends:
      - gtk+3.0
      - webkit2gtk-4.1
scripts:
  postinstall: "./build/linux/nfpm/scripts/postinstall.sh"
  postremove: "./build/linux/nfpm/scripts/postremove.sh"
EOF
cat > "$LINUX_WORK/build/linux/nfpm/scripts/postinstall.sh" <<'S'
#!/bin/sh
command -v update-desktop-database >/dev/null 2>&1 && update-desktop-database -q /usr/share/applications 2>/dev/null || true
exit 0
S
cat > "$LINUX_WORK/build/linux/nfpm/scripts/postremove.sh" <<'S'
#!/bin/sh
command -v update-desktop-database >/dev/null 2>&1 && update-desktop-database -q /usr/share/applications 2>/dev/null || true
exit 0
S
chmod +x "$LINUX_WORK/build/linux/nfpm/scripts/"*.sh

cd "$LINUX_WORK"
info "nfpm packages..."
for fmt in deb rpm archlinux apk; do
  nfpm pkg --config "build/linux/nfpm/${APP}.yaml" --packager "$fmt" --target "$LINUX_WORK/bin/" || true
done

# portable + .run
STAGE="$LINUX_WORK/bin/stage-linux-rel"
rm -rf "$STAGE" && mkdir -p "$STAGE"
cp "$LINUX_WORK/bin/${APP}" "$STAGE/"
cp "build/linux/${APP}.desktop" "$STAGE/"
cp "build/appicon.png" "$STAGE/${APP}.png"
cat > "$STAGE/install.sh" <<'INSTALL'
#!/bin/bash
set -e
APP_NAME="koyori-ide"
INSTALL_DIR="/opt/${APP_NAME}"
BIN_PATH="/usr/local/bin/${APP_NAME}"
DESKTOP_FILE="/usr/share/applications/${APP_NAME}.desktop"
ICON_FILE="/usr/share/icons/hicolor/128x128/apps/${APP_NAME}.png"
if [ "$(id -u)" -ne 0 ]; then exec sudo bash "$0" "$@"; fi
mkdir -p "${INSTALL_DIR}"
cp "${APP_NAME}" "${INSTALL_DIR}/${APP_NAME}"
chmod +x "${INSTALL_DIR}/${APP_NAME}"
ln -sf "${INSTALL_DIR}/${APP_NAME}" "${BIN_PATH}"
[ -f "${APP_NAME}.png" ] && mkdir -p "$(dirname "${ICON_FILE}")" && cp "${APP_NAME}.png" "${ICON_FILE}"
[ -f "${APP_NAME}.desktop" ] && sed "s|^Exec=.*|Exec=${BIN_PATH}|" "${APP_NAME}.desktop" > "${DESKTOP_FILE}" && chmod 644 "${DESKTOP_FILE}"
echo "Installed: ${BIN_PATH}"
INSTALL
chmod +x "$STAGE/install.sh" "$STAGE/${APP}"
TARBALL="$OUT/${APP}-${VER}-linux-amd64-offline.tar.gz"
tar -czf "$TARBALL" -C "$STAGE" .
RUN_OUT="$OUT/${APP}-${VER}-linux-amd64-desktop.run"
{
  cat <<HEADER
#!/bin/bash
# koyori-ide ${VER} offline desktop installer for linux-amd64
set -e
ARCHIVE_LINE=\$(grep -an '^__ARCHIVE_BELOW__\$' "\$0" | tail -1 | cut -d: -f1)
TMPDIR="/tmp/koyori-ide-install-\$(date +%s)-\$\$"
mkdir -p "\$TMPDIR"
echo "Extracting koyori-ide ${VER}..."
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

# copy nfpm outputs with 0.2.0 names
for f in "$LINUX_WORK/bin"/*.deb "$LINUX_WORK/bin"/*.rpm "$LINUX_WORK/bin"/*.apk "$LINUX_WORK/bin"/*.pkg.tar.zst; do
  [ -f "$f" ] || continue
  base=$(basename "$f")
  case "$base" in
    *0.2.0*) cp -f "$f" "$OUT/" ;;
    *) cp -f "$f" "$OUT/" ;;
  esac
done

# AppImage: rename/copy existing if present
if [ -f "$SRC/bin/koyori-ide-0.1.0-linux-amd64.AppImage" ]; then
  cp -f "$SRC/bin/koyori-ide-0.1.0-linux-amd64.AppImage" "$OUT/${APP}-${VER}-linux-amd64.AppImage"
elif [ -f "$LINUX_WORK/bin/koyori-ide-0.1.0-linux-amd64.AppImage" ]; then
  cp -f "$LINUX_WORK/bin/koyori-ide-0.1.0-linux-amd64.AppImage" "$OUT/${APP}-${VER}-linux-amd64.AppImage"
elif [ -f "$SRC/bin/${APP}-${VER}-linux-amd64.AppImage" ]; then
  cp -f "$SRC/bin/${APP}-${VER}-linux-amd64.AppImage" "$OUT/"
fi
# also bare linux binary
cp -f "$LINUX_WORK/bin/${APP}" "$OUT/${APP}-${VER}-linux-amd64"
ok "Linux packages ready"

# ---- macOS desktop from cross-build ----
package_mac() {
  local arch="$1"
  local bin=""
  for cand in \
    "$MAC_WORK/bin/${APP}-darwin-${arch}" \
    "$SRC/bin/${APP}-darwin-${arch}" \
    "$SRC/bin/${APP}-0.1.0-darwin-${arch}"
  do
    if [ -f "$cand" ]; then bin="$cand"; break; fi
  done
  [ -n "$bin" ] || { echo "skip mac $arch — no binary"; return 0; }

  local stage="$OUT/stage-mac-${arch}"
  local appdir="$stage/${APP}.app"
  rm -rf "$stage"
  mkdir -p "$appdir/Contents/MacOS" "$appdir/Contents/Resources"
  cp "$bin" "$appdir/Contents/MacOS/${APP}"
  chmod +x "$appdir/Contents/MacOS/${APP}"
  [ -f "$SRC/build/darwin/icons.icns" ] && cp "$SRC/build/darwin/icons.icns" "$appdir/Contents/Resources/" || true
  [ -f "$SRC/build/appicon.png" ] && cp "$SRC/build/appicon.png" "$appdir/Contents/Resources/" || true
  cat > "$appdir/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleExecutable</key><string>${APP}</string>
  <key>CFBundleIdentifier</key><string>com.koyori.app</string>
  <key>CFBundleName</key><string>Koyori IDE</string>
  <key>CFBundleDisplayName</key><string>Koyori IDE</string>
  <key>CFBundleVersion</key><string>${VER}</string>
  <key>CFBundleShortVersionString</key><string>${VER}</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>LSMinimumSystemVersion</key><string>12.0</string>
  <key>NSHighResolutionCapable</key><true/>
</dict>
</plist>
PLIST
  cat > "$stage/install.sh" <<'INST'
#!/bin/bash
set -e
APP_NAME="koyori-ide"
SRC_APP="$(cd "$(dirname "$0")" && pwd)/${APP_NAME}.app"
DEST="/Applications/${APP_NAME}.app"
[ "$(uname)" = "Darwin" ] || { echo "macOS only"; exit 1; }
echo "Installing ${APP_NAME} to ${DEST}..."
rm -rf "${DEST}"
cp -R "${SRC_APP}" "${DEST}"
xattr -rd com.apple.quarantine "${DEST}" 2>/dev/null || true
command -v codesign >/dev/null 2>&1 && codesign --force --deep --sign - "${DEST}" 2>/dev/null || true
echo "Done: open ${DEST}"
INST
  chmod +x "$stage/install.sh"
  local tg="$OUT/${APP}-${VER}-darwin-${arch}-desktop.tar.gz"
  tar -czf "$tg" -C "$stage" .
  local run="$OUT/${APP}-${VER}-darwin-${arch}-desktop.run"
  {
    cat <<HEADER
#!/bin/bash
# koyori-ide ${VER} macOS desktop offline installer (${arch})
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
  cp -f "$bin" "$OUT/${APP}-${VER}-darwin-${arch}"
  rm -rf "$stage"
  ok "macOS $arch packaged"
}
package_mac arm64
package_mac amd64

# Windows zip if present
if [ -f "$SRC/bin/koyori-ide.exe" ]; then
  # create portable zip structure
  WSTAGE="$OUT/stage-win"
  rm -rf "$WSTAGE" && mkdir -p "$WSTAGE"
  cp -f "$SRC/bin/koyori-ide.exe" "$WSTAGE/${APP}.exe"
  # prefer freshly built release exe if named
  if [ -f "$SRC/bin/koyori-ide-0.2.0-windows-amd64.exe" ]; then
    cp -f "$SRC/bin/koyori-ide-0.2.0-windows-amd64.exe" "$WSTAGE/${APP}.exe"
  fi
  (cd "$WSTAGE" && zip -9 -r "$OUT/${APP}-${VER}-windows-amd64.zip" "${APP}.exe")
  cp -f "$WSTAGE/${APP}.exe" "$OUT/${APP}-${VER}-windows-amd64.exe"
  rm -rf "$WSTAGE"
  ok "Windows packaged"
else
  echo "WARN: koyori-ide.exe missing"
fi

# SHA256
cd "$OUT"
rm -rf stage-* 2>/dev/null || true
find . -maxdepth 1 -type f ! -name 'SHA256SUMS' -printf '%P\n' | sort | while read -r f; do
  sha256sum "$f"
done > SHA256SUMS

echo ""
echo "=== release-v0.2.0 contents ==="
ls -lh "$OUT"
echo DONE
