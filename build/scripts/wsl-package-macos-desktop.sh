#!/usr/bin/env bash
# Package already-built koyori-ide-darwin-{arm64,amd64} into offline .app installers.
set -euo pipefail
export PATH="/usr/bin:/bin:/usr/sbin:/sbin:${PATH}"

WORK="${HOME}/koyori-ide-macos-cross"
OUT=/mnt/e/koyori-ide/Koyori IDE-main/bin
APP=koyori-ide
VER=0.1.0

# Fix root-owned files from docker
if [ -d "$WORK/bin" ]; then
  sudo chown -R "$(id -u):$(id -g)" "$WORK/bin" 2>/dev/null || true
fi

[ -f "$WORK/bin/${APP}-darwin-arm64" ] || { echo "missing arm64 binary"; exit 1; }
[ -f "$WORK/bin/${APP}-darwin-amd64" ] || { echo "missing amd64 binary"; exit 1; }

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
    cp "$WORK/build/darwin/icons.icns" "$appdir/Contents/Resources/" || true
  fi
  if [ -f "$WORK/build/darwin/Assets.car" ]; then
    cp "$WORK/build/darwin/Assets.car" "$appdir/Contents/Resources/" || true
  fi
  if [ -f "$WORK/build/appicon.png" ]; then
    cp "$WORK/build/appicon.png" "$appdir/Contents/Resources/appicon.png" || true
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
</dict>
</plist>
PLIST
  fi

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
xattr -rd com.apple.quarantine "${DEST}" 2>/dev/null || true
if command -v codesign >/dev/null 2>&1; then
  codesign --force --deep --sign - "${DEST}" 2>/dev/null || true
fi
echo "Done. Launch: open ${DEST}"
echo "If blocked: right-click app → Open → confirm"
INST
  chmod +x "$stage/install.sh"

  local tg="$WORK/bin/${APP}-${VER}-darwin-${arch}-desktop.tar.gz"
  tar -czf "$tg" -C "$stage" .
  echo "[OK] $tg ($(du -h "$tg" | awk '{print $1}'))"

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
  echo "[OK] $run ($(du -h "$run" | awk '{print $1}'))"

  cp -f "$bin" "$WORK/bin/${APP}-${VER}-darwin-${arch}"
}

make_app_bundle arm64
make_app_bundle amd64

mkdir -p "$OUT"
for arch in arm64 amd64; do
  for f in \
    "${APP}-darwin-${arch}" \
    "${APP}-${VER}-darwin-${arch}" \
    "${APP}-${VER}-darwin-${arch}-desktop.tar.gz" \
    "${APP}-${VER}-darwin-${arch}-desktop.run"
  do
    [ -f "$WORK/bin/$f" ] && cp -f "$WORK/bin/$f" "$OUT/" && echo "copied $f"
  done
done

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
} | tee SHA256SUMS-macos-desktop.txt

echo ""
echo "=== Final macOS desktop artifacts ==="
ls -lh "$OUT"/${APP}*darwin*desktop* "$OUT"/${APP}-darwin-* 2>/dev/null || true
file "$OUT/${APP}-darwin-arm64" "$OUT/${APP}-darwin-amd64" 2>/dev/null || true
echo DONE
