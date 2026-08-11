#!/usr/bin/env bash
set -euo pipefail
export PATH="/usr/local/go/bin:/usr/local/lib/nodejs/bin:${HOME}/go/bin:${PATH}"
export APPIMAGE_EXTRACT_AND_RUN=1

WORK="${HOME}/koyori-ide-pkg-build"
OUT="/mnt/e/koyori-ide/Koyori IDE-main/bin"
APP=koyori-ide
VER=0.1.0
ARCH=amd64
ADIR="${WORK}/build/linux/appimage"
mkdir -p "$ADIR"
cd "$ADIR"

APPDIR="${APP}.AppDir"
rm -rf "$APPDIR"
mkdir -p "$APPDIR/usr/bin"
cp "$WORK/bin/${APP}" "$APPDIR/usr/bin/"
cp "$WORK/build/appicon.png" "$APPDIR/${APP}.png"
cat > "$APPDIR/${APP}.desktop" <<'D'
[Desktop Entry]
Type=Application
Name=koyori-ide
Exec=koyori-ide
Icon=koyori-ide
Categories=Development;IDE;
Terminal=false
D
cat > "$APPDIR/AppRun" <<'A'
#!/bin/bash
HERE="$(dirname "$(readlink -f "$0")")"
exec "$HERE/usr/bin/koyori-ide" "$@"
A
chmod +x "$APPDIR/AppRun" "$APPDIR/usr/bin/${APP}"

LD=linuxdeploy-x86_64.AppImage
if [ ! -f "$LD" ]; then
  wget -q -4 -N "https://github.com/linuxdeploy/linuxdeploy/releases/download/continuous/$LD"
  chmod +x "$LD"
fi
./"$LD" --appdir "$APPDIR" --output appimage
mv -f ${APP}*.AppImage "$WORK/bin/${APP}-${VER}-linux-${ARCH}.AppImage"
cp -f "$WORK/bin/${APP}-${VER}-linux-${ARCH}.AppImage" "$OUT/"
rm -f "$OUT/koyori-ide-3-1-x86_64.pkg.tar.zst" 2>/dev/null || true

cd "$OUT"
sha256sum \
  koyori-ide \
  koyori-ide_0.1.0-1_amd64.deb \
  koyori-ide-0.1.0-1.x86_64.rpm \
  koyori-ide-0.1.0-1-x86_64.pkg.tar.zst \
  koyori-ide_0.1.0-r1_x86_64.apk \
  koyori-ide-0.1.0-linux-amd64-offline.tar.gz \
  koyori-ide-0.1.0-linux-amd64-desktop.run \
  koyori-ide-0.1.0-linux-amd64.AppImage \
  koyori-ide-0.1.0-darwin-amd64.run \
  koyori-ide-0.1.0-darwin-arm64.run \
  koyori-ide-0.1.0-linux-amd64-server.run \
  koyori-ide-0.1.0-linux-arm64-server.run \
  2>/dev/null > SHA256SUMS || true

ls -lh "$OUT"/koyori-ide*0.1.0* "$OUT"/koyori-ide "$OUT"/SHA256SUMS 2>/dev/null | head -50
echo APPIMAGE_OK
