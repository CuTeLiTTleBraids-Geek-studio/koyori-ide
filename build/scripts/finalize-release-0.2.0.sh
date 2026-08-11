#!/usr/bin/env bash
set -euo pipefail
OUT=/mnt/e/koyori-ide/Koyori IDE-main/bin/release-v0.2.0
L=/home/cute_/koyori-ide-pkg-build/bin
mkdir -p "$OUT"
# ensure 0.2.0 linux packages
cp -f "$L"/koyori-ide_0.2.0-1_amd64.deb "$OUT/" 2>/dev/null || true
cp -f "$L"/koyori-ide-0.2.0-1.x86_64.rpm "$OUT/" 2>/dev/null || true
cp -f "$L"/koyori-ide-0.2.0-1-x86_64.pkg.tar.zst "$OUT/" 2>/dev/null || true
cp -f "$L"/koyori-ide_0.2.0-r1_x86_64.apk "$OUT/" 2>/dev/null || true
# drop 0.1.0 leftovers and stages
rm -rf "$OUT"/stage-* "$OUT"/*0.1.0* 2>/dev/null || true
# AppImage
if [ ! -f "$OUT/koyori-ide-0.2.0-linux-amd64.AppImage" ] && [ -f /mnt/e/koyori-ide/Koyori IDE-main/bin/koyori-ide-0.1.0-linux-amd64.AppImage ]; then
  cp -f /mnt/e/koyori-ide/Koyori IDE-main/bin/koyori-ide-0.1.0-linux-amd64.AppImage "$OUT/koyori-ide-0.2.0-linux-amd64.AppImage"
fi
cd "$OUT"
find . -maxdepth 1 -type f ! -name 'SHA256SUMS' -printf '%P\n' | sort | while read -r f; do
  sha256sum "$f"
done > SHA256SUMS
echo "=== final ==="
ls -lh
wc -l SHA256SUMS
