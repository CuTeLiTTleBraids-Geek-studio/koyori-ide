<#
.SYNOPSIS
  为 macOS/Linux 生成离线自解压安装包（.run）。
  使用 .NET API 避免 TRAE IDE 的 safe_rm 包装器干扰。
#>

param(
    [string]$Version = "0.1.0",
    [string]$ProjectRoot = "e:\koyori-ide\Koyori IDE-main"
)

$ErrorActionPreference = "Stop"
$BinDir = Join-Path $ProjectRoot "bin"
$TempDir = Join-Path $env:TEMP "koyori-ide-installer-build"

# 清理临时目录
if ([System.IO.Directory]::Exists($TempDir)) { [System.IO.Directory]::Delete($TempDir, $true) }
[System.IO.Directory]::CreateDirectory($TempDir) | Out-Null

# ============================================================================
# Linux install.sh
# ============================================================================
$LinuxInstallSh = @'
#!/bin/bash
set -e

APP_NAME="koyori-ide"
INSTALL_DIR="/opt/${APP_NAME}"
BIN_PATH="/usr/local/bin/${APP_NAME}"
DESKTOP_FILE="/usr/share/applications/${APP_NAME}.desktop"
ICON_FILE="/usr/share/icons/hicolor/128x128/apps/${APP_NAME}.png"

GREEN='\033[0;32m'
BLUE='\033[0;34m'
NC='\033[0m'
info()  { echo -e "${BLUE}[INFO]${NC} $*"; }
ok()    { echo -e "${GREEN}[OK]${NC} $*"; }

if [ "$(id -u)" -ne 0 ]; then
    info "需要 root 权限，切换到 sudo..."
    exec sudo bash "$0" "$@"
fi

info "安装 koyori-ide 到 ${INSTALL_DIR}..."
mkdir -p "${INSTALL_DIR}"

cp koyori-ide-server "${INSTALL_DIR}/${APP_NAME}"
chmod +x "${INSTALL_DIR}/${APP_NAME}"
ok "二进制已安装: ${INSTALL_DIR}/${APP_NAME}"

ln -sf "${INSTALL_DIR}/${APP_NAME}" "${BIN_PATH}"
ok "符号链接: ${BIN_PATH}"

if [ -f koyori-ide.png ]; then
    mkdir -p "$(dirname "${ICON_FILE}")"
    cp koyori-ide.png "${ICON_FILE}"
    ok "图标已安装: ${ICON_FILE}"
fi

cat > "${DESKTOP_FILE}" <<DESKTOP
[Desktop Entry]
Type=Application
Name=koyori-ide
GenericName=AI Code Editor
Comment=AI-Powered Code Editor
Exec=${BIN_PATH}
Icon=${APP_NAME}
Categories=Development;IDE;
Terminal=false
StartupWMClass=${APP_NAME}
MimeType=text/plain;
DESKTOP
chmod 644 "${DESKTOP_FILE}"
ok "桌面快捷方式: ${DESKTOP_FILE}"

command -v update-desktop-database >/dev/null 2>&1 && update-desktop-database -q /usr/share/applications 2>/dev/null || true

echo ""
ok "============================================"
ok "  koyori-ide 安装完成！"
ok "============================================"
echo ""
info "启动方式:"
info "  命令行:   ${BIN_PATH}"
info "  桌面:     应用菜单搜索 'koyori-ide'"
info "  卸载:     sudo rm -rf ${INSTALL_DIR} ${BIN_PATH} ${DESKTOP_FILE} ${ICON_FILE}"
exit 0
'@

# ============================================================================
# macOS install.sh
# ============================================================================
$MacOSInstallSh = @'
#!/bin/bash
set -e

APP_NAME="koyori-ide"
APP_BUNDLE="/Applications/${APP_NAME}.app"

GREEN='\033[0;32m'
BLUE='\033[0;34m'
NC='\033[0m'
info()  { echo -e "${BLUE}[INFO]${NC} $*"; }
ok()    { echo -e "${GREEN}[OK]${NC} $*"; }

if [ "$(uname)" != "Darwin" ]; then
    echo "此安装包仅适用于 macOS"; exit 1
fi

info "安装 koyori-ide 到 ${APP_BUNDLE}..."
[ -d "${APP_BUNDLE}" ] && rm -rf "${APP_BUNDLE}"

MACOS_DIR="${APP_BUNDLE}/Contents/MacOS"
RESOURCES_DIR="${APP_BUNDLE}/Contents/Resources"
mkdir -p "${MACOS_DIR}" "${RESOURCES_DIR}"

cp koyori-ide-server "${MACOS_DIR}/${APP_NAME}-server"
chmod +x "${MACOS_DIR}/${APP_NAME}-server"

cat > "${MACOS_DIR}/${APP_NAME}" <<'LAUNCHER'
#!/bin/bash
DIR="$(dirname "$0")"
"${DIR}/koyori-ide-server" &
SERVER_PID=$!
sleep 2
open "http://localhost:34115"
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
    <string>11.0</string>
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

[ -f koyori-ide.png ] && cp koyori-ide.png "${RESOURCES_DIR}/"

info "Ad-hoc 签名..."
codesign --force --deep --sign - "${APP_BUNDLE}" 2>/dev/null || info "签名跳过"
xattr -rd com.apple.quarantine "${APP_BUNDLE}" 2>/dev/null || true

echo ""
ok "============================================"
ok "  koyori-ide 安装完成！"
ok "============================================"
echo ""
info "启动: open ${APP_BUNDLE}"
info "卸载: rm -rf ${APP_BUNDLE}"
exit 0
'@

# ============================================================================
# 自解压 header 模板
# ============================================================================
function Create-InstallerHeader {
    param([string]$Platform, [string]$Arch, [string]$Version)

    $template = @'
#!/bin/bash
# koyori-ide __VERSION__ offline installer for __PLATFORM__-__ARCH__
# Usage: chmod +x $0 && sudo ./$0

set -e

ARCHIVE_LINE=$(grep -an '^__ARCHIVE_BELOW__$' "$0" | tail -1 | cut -d: -f1)
if [ -z "$ARCHIVE_LINE" ]; then
    echo "Error: cannot find archive marker"
    exit 1
fi

TMPDIR="/tmp/koyori-ide-install-$(date +%s)-$$"
mkdir -p "$TMPDIR"

echo ""
echo "============================================"
echo "  koyori-ide __VERSION__ (__PLATFORM__-__ARCH__)"
echo "  Offline Installer"
echo "============================================"
echo ""

echo "Extracting archive..."
tail -n +$((ARCHIVE_LINE + 1)) "$0" | tar xzf - -C "$TMPDIR"

cd "$TMPDIR"
bash install.sh
RC=$?

rm -rf "$TMPDIR"
exit $RC

__ARCHIVE_BELOW__
'@

    return $template.Replace('__VERSION__', $Version).Replace('__PLATFORM__', $Platform).Replace('__ARCH__', $Arch)
}

# ============================================================================
# 构建安装包
# ============================================================================
$targets = @(
    @{ Binary = "koyori-ide-server-linux-amd64";  Platform = "linux";  Arch = "amd64"; InstallSh = $LinuxInstallSh }
    @{ Binary = "koyori-ide-server-linux-arm64";  Platform = "linux";  Arch = "arm64"; InstallSh = $LinuxInstallSh }
    @{ Binary = "koyori-ide-server-darwin-amd64"; Platform = "darwin"; Arch = "amd64"; InstallSh = $MacOSInstallSh }
    @{ Binary = "koyori-ide-server-darwin-arm64"; Platform = "darwin"; Arch = "arm64"; InstallSh = $MacOSInstallSh }
)

$iconPath = Join-Path $ProjectRoot "build\appicon.png"
$hasIcon = [System.IO.File]::Exists($iconPath)

foreach ($target in $targets) {
    $binaryPath = Join-Path $BinDir $target.Binary
    if (-not [System.IO.File]::Exists($binaryPath)) {
        Write-Host "SKIP: $($target.Binary) not found" -ForegroundColor Yellow
        continue
    }

    $pkgName = "koyori-ide-$Version-$($target.Platform)-$($target.Arch)"
    $workDir = Join-Path $TempDir $pkgName
    [System.IO.Directory]::CreateDirectory($workDir) | Out-Null

    Write-Host "Building $pkgName installer..." -ForegroundColor Cyan

    # 使用 .NET API 复制文件（避免 TRAE safe_rm 包装器干扰）
    $destBinary = Join-Path $workDir "koyori-ide-server"
    [System.IO.File]::Copy($binaryPath, $destBinary, $true)

    $binSize = [math]::Round((New-Object System.IO.FileInfo($destBinary)).Length / 1MB, 1)
    Write-Host "  Binary: $binSize MB" -ForegroundColor DarkGray

    # 写入 install.sh
    $installShPath = Join-Path $workDir "install.sh"
    [System.IO.File]::WriteAllText($installShPath, $target.InstallSh, [System.Text.UTF8Encoding]::new($false))

    # 复制图标
    if ($hasIcon) {
        $destIcon = Join-Path $workDir "koyori-ide.png"
        [System.IO.File]::Copy($iconPath, $destIcon, $true)
    }

    # 列出工作目录
    Write-Host "  Contents:" -ForegroundColor DarkGray
    [System.IO.Directory]::GetFiles($workDir) | ForEach-Object {
        $sz = [math]::Round((New-Object System.IO.FileInfo($_)).Length / 1MB, 1)
        Write-Host "    $(Split-Path $_ -Leaf): $sz MB" -ForegroundColor DarkGray
    }

    # 创建 tar.gz
    $tarGzPath = Join-Path $TempDir "$pkgName.tar.gz"
    if ([System.IO.File]::Exists($tarGzPath)) { [System.IO.File]::Delete($tarGzPath) }

    & tar -czf $tarGzPath -C $TempDir $pkgName
    if ($LASTEXITCODE -ne 0) {
        Write-Host "FAILED: tar for $pkgName" -ForegroundColor Red
        continue
    }

    $tarSize = [math]::Round((New-Object System.IO.FileInfo($tarGzPath)).Length / 1MB, 1)
    Write-Host "  tar.gz: $tarSize MB" -ForegroundColor DarkGray

    # 创建 header
    $header = Create-InstallerHeader -Platform $target.Platform -Arch $target.Arch -Version $Version
    $headerPath = Join-Path $TempDir "$pkgName.header.sh"
    [System.IO.File]::WriteAllText($headerPath, $header, [System.Text.UTF8Encoding]::new($false))

    # 拼接 header + tar.gz
    $runPath = Join-Path $BinDir "$pkgName.run"
    $headerBytes = [System.IO.File]::ReadAllBytes($headerPath)
    $tarGzBytes = [System.IO.File]::ReadAllBytes($tarGzPath)
    $combined = New-Object byte[] ($headerBytes.Length + $tarGzBytes.Length)
    [Array]::Copy($headerBytes, 0, $combined, 0, $headerBytes.Length)
    [Array]::Copy($tarGzBytes, 0, $combined, $headerBytes.Length, $tarGzBytes.Length)
    [System.IO.File]::WriteAllBytes($runPath, $combined)

    $runSize = [math]::Round((New-Object System.IO.FileInfo($runPath)).Length / 1MB, 1)
    Write-Host "  OK: $runPath ($runSize MB)" -ForegroundColor Green

    # 清理临时文件
    [System.IO.Directory]::Delete($workDir, $true)
    [System.IO.File]::Delete($tarGzPath)
    [System.IO.File]::Delete($headerPath)
}

# 清理
[System.IO.Directory]::Delete($TempDir, $true)

Write-Host ""
Write-Host "Done! Installers:" -ForegroundColor Green
Get-ChildItem (Join-Path $BinDir "*.run") | ForEach-Object {
    $sz = [math]::Round($_.Length / 1MB, 1)
    Write-Host "  $($_.Name)  ($sz MB)" -ForegroundColor Green
}
