# -*- coding: utf-8 -*-
"""Write UTF-8 release notes and optionally print body for gh."""
from pathlib import Path

BODY = """## koyori-ide v0.2.0

跨平台桌面发行版，提供 Linux / macOS / Windows 安装包与可执行文件。

### Windows
- `koyori-ide-0.2.0-windows-amd64.zip` — 便携压缩包（推荐）
- `koyori-ide-0.2.0-windows-amd64.exe` — 单文件 GUI（需 WebView2）

### Linux（amd64）
- `koyori-ide_0.2.0-1_amd64.deb` — Debian / Ubuntu
- `koyori-ide-0.2.0-1.x86_64.rpm` — Fedora / RHEL
- `koyori-ide-0.2.0-1-x86_64.pkg.tar.zst` — Arch Linux
- `koyori-ide_0.2.0-r1_x86_64.apk` — Alpine
- `koyori-ide-0.2.0-linux-amd64.AppImage` — 免安装便携包（内嵌运行库，最适合离线）
- `koyori-ide-0.2.0-linux-amd64-desktop.run` — 离线自解压安装脚本
- `koyori-ide-0.2.0-linux-amd64-offline.tar.gz` — 离线 tar 包 + install.sh
- `koyori-ide-0.2.0-linux-amd64` — 裸可执行文件

### macOS
- `koyori-ide-0.2.0-darwin-arm64-desktop.run` / `.tar.gz` — Apple Silicon 桌面离线安装包
- `koyori-ide-0.2.0-darwin-amd64-desktop.run` / `.tar.gz` — Intel 桌面离线安装包
- 另附对应架构的裸 Mach-O 二进制

### 说明
- **macOS 未签名、未公证**。首次打开请：右键应用 → 打开 → 确认。
- Linux 的 deb/rpm 等包依赖系统 GTK3 + WebKit2GTK；**离线优先用 AppImage 或 `.run`**。
- Windows 10/11 通常已自带 WebView2。
- 下载后请用 `SHA256SUMS` 校验完整性。

### 安装示例

```bash
# Linux AppImage
chmod +x koyori-ide-0.2.0-linux-amd64.AppImage
./koyori-ide-0.2.0-linux-amd64.AppImage

# Linux 离线 .run
chmod +x koyori-ide-0.2.0-linux-amd64-desktop.run
sudo ./koyori-ide-0.2.0-linux-amd64-desktop.run

# Debian/Ubuntu
sudo dpkg -i koyori-ide_0.2.0-1_amd64.deb

# macOS（Apple Silicon）
chmod +x koyori-ide-0.2.0-darwin-arm64-desktop.run
./koyori-ide-0.2.0-darwin-arm64-desktop.run
```

---

## English

Multi-platform desktop release with offline installers for Linux, macOS, and Windows.

- **Windows:** zip (recommended) or single `.exe` (WebView2 required).
- **Linux:** deb / rpm / Arch / apk / AppImage / offline `.run` / tarball. Prefer AppImage or `.run` for offline use.
- **macOS:** arm64 and amd64 desktop offline installers (unsigned; right-click → Open on first launch).
- Verify downloads with `SHA256SUMS`.
"""

root = Path(__file__).resolve().parents[2]
notes = root / "bin" / "release-v0.2.0" / "RELEASE_NOTES.md"
notes.parent.mkdir(parents=True, exist_ok=True)
notes.write_text(BODY, encoding="utf-8", newline="\n")

import json
payload = root / "bin" / "release-v0.2.0" / "edit-body.json"
payload.write_text(
    json.dumps({"body": BODY, "name": "koyori-ide v0.2.0"}, ensure_ascii=False, indent=2),
    encoding="utf-8",
    newline="\n",
)
print("wrote", notes)
print("sample:", BODY.splitlines()[2])
print("has 跨平台:", "跨平台" in BODY)
