#!/usr/bin/env bash
set -euo pipefail
ROOT=/mnt/e/koyori-ide/Koyori IDE-main
python3 "$ROOT/build/scripts/write-release-notes-v0.2.0.py"
ls -la "$ROOT/bin/release-v0.2.0/edit-body.json"
# gh may be only on Windows; use curl with token from env
if [ -z "${GH_TOKEN:-}" ]; then
  echo "GH_TOKEN not set"
  exit 1
fi
ID=$(curl -fsSL -H "Authorization: Bearer $GH_TOKEN" -H "Accept: application/vnd.github+json" \
  "https://api.github.com/repos/CuTeLiTTleBraids-Geek-studio/Koyori IDE/releases/tags/v0.2.0" | python3 -c "import sys,json; print(json.load(sys.stdin)['id'])")
echo "release id=$ID"
curl -fsSL -X PATCH \
  -H "Authorization: Bearer $GH_TOKEN" \
  -H "Accept: application/vnd.github+json" \
  -H "Content-Type: application/json" \
  --data-binary @"$ROOT/bin/release-v0.2.0/edit-body.json" \
  "https://api.github.com/repos/CuTeLiTTleBraids-Geek-studio/Koyori IDE/releases/$ID" \
  -o "$ROOT/bin/release-v0.2.0/api-out.json"
python3 - <<'PY'
import json
from pathlib import Path
d=json.loads(Path("/mnt/e/koyori-ide/Koyori IDE-main/bin/release-v0.2.0/api-out.json").read_text(encoding="utf-8"))
body=d.get("body") or ""
print(body[:400])
print("OK" if "跨平台桌面" in body else "FAIL")
PY
