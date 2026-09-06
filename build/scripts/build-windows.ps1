<#
.SYNOPSIS
    koyori-ide Windows 桌面应用构建脚本。
.DESCRIPTION
    在 Windows 上构建并打包 koyori-ide 桌面应用，产出 NSIS 安装程序。
    使用与 CI 一致的直接命令链路（wails3 generate syso -> go build -> makensis），
    与 scripts/build-windows-gui.ps1 相比额外完成 NSIS 安装程序打包。

.PARAMETER Arch
    目标架构：amd64（默认）或 arm64。

.PARAMETER InstallScope
    NSIS 安装范围：machine（默认，装到 Program Files，需要管理员）或 user（装到
    %LOCALAPPDATA%，免 UAC 弹窗）。

.PARAMETER SkipDeps
    跳过依赖检查（仅当已手动确认 go/node/wails3/makensis 均可用时使用）。

.EXAMPLE
    powershell -ExecutionPolicy Bypass -File build/scripts/build-windows.ps1
    powershell -ExecutionPolicy Bypass -File build/scripts/build-windows.ps1 -Arch amd64 -InstallScope user

.NOTES
    产物（位于 bin/）:
      koyori-ide.exe                  # 裸可执行文件（GUI 子系统，含图标与版本信息）
      koyori-ide-<arch>-installer.exe # NSIS 安装程序
#>

[CmdletBinding()]
param(
    [ValidateSet("amd64", "arm64")]
    [string]$Arch = "amd64",

    [ValidateSet("machine", "user")]
    [string]$InstallScope = "machine",

    [switch]$SkipDeps,

    [switch]$SkipNSIS
)

$ErrorActionPreference = "Stop"

# ============================================================================
# 颜色输出辅助
# ============================================================================
function Write-Info  { Write-Host "[INFO]  $args" -ForegroundColor Cyan }
function Write-Ok    { Write-Host "[OK]    $args" -ForegroundColor Green }
function Write-Warn  { Write-Host "[WARN]  $args" -ForegroundColor Yellow }
function Write-Fail  { Write-Host "[ERROR] $args" -ForegroundColor Red; exit 1 }

# ============================================================================
# 项目根目录
# ============================================================================
$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$RootDir = (Resolve-Path (Join-Path $ScriptDir "..\..")).Path
Set-Location $RootDir

$AppName = "koyori-ide"
$BinDir = Join-Path $RootDir "bin"

# 版本号单一事实来源（与 release 元数据一致）
$VersionFile = Join-Path $RootDir "VERSION"
if (-not (Test-Path $VersionFile)) {
    Write-Fail "VERSION file not found: $VersionFile"
}
$Version = (Get-Content $VersionFile -Raw).Trim()
if ($Version -notmatch '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$') {
    Write-Fail "VERSION contains an invalid SemVer value: $Version"
}

Write-Info "构建配置:"
Write-Info "  项目目录:   $RootDir"
Write-Info "  目标架构:   $Arch"
Write-Info "  应用名称:   $AppName"
Write-Info "  版本号:     $Version"
Write-Info "  安装范围:   $InstallScope"

# ============================================================================
# 1. 检查依赖
# ============================================================================
if (-not $SkipDeps) {
    Write-Info "检查构建依赖..."

    # Go 1.25+
    if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
        Write-Fail "未安装 Go。请从 https://go.dev/dl/ 安装 Go 1.25+"
    }
    $GoVersionRaw = (go version) -replace '^.*go([0-9.]+).*$', '$1'
    $GoMinor = [int]($GoVersionRaw -split '\.')[1]
    if ($GoMinor -lt 25) {
        Write-Fail "Go 版本过低 ($GoVersionRaw)，需要 1.25+。请从 https://go.dev/dl/ 升级"
    }
    Write-Ok "Go: $GoVersionRaw"

    # 将 go install 安装的 wails3 加入 PATH（CI/本地均适用）
    $GoBin = Join-Path (go env GOPATH) "bin"
    if ($env:Path -notlike "*$GoBin*") {
        $env:Path = "$GoBin;$env:Path"
    }

    # Node.js 18+
    if (-not (Get-Command node -ErrorAction SilentlyContinue)) {
        Write-Fail "未安装 Node.js。请从 https://nodejs.org/ 安装 Node.js 18+"
    }
    $NodeMajor = [int]((node --version) -replace '^v', '' -replace '\..*$', '')
    if ($NodeMajor -lt 18) {
        Write-Fail "Node.js 版本过低 ($(node --version))，需要 18+"
    }
    Write-Ok "Node.js: $(node --version)"

    # npm
    if (-not (Get-Command npm -ErrorAction SilentlyContinue)) {
        Write-Fail "未安装 npm"
    }
    Write-Ok "npm: $(npm --version)"

    # wails3 CLI（与 go.mod 锁定版本一致）
    if (-not (Get-Command wails3 -ErrorAction SilentlyContinue)) {
        Write-Warn "未找到 wails3 CLI，尝试安装 v3.0.0-alpha2.111（与 go.mod 锁定一致）..."
        go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-alpha2.111
        if (-not (Get-Command wails3 -ErrorAction SilentlyContinue)) {
            Write-Fail "wails3 安装失败。请手动执行: go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-alpha2.111"
        }
    }
    $oldEap = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    $WailsVersion = (& wails3 version 2>&1 | Out-String).Trim()
    $ErrorActionPreference = $oldEap
    Write-Ok "wails3: $WailsVersion"

    if (-not $SkipNSIS) {
        # NSIS 编译器
        if (-not (Get-Command makensis -ErrorAction SilentlyContinue)) {
            Write-Fail "未安装 NSIS (makensis)。请执行: choco install nsis -y  或从 https://nsis.sourceforge.io/Download 安装并加入 PATH"
        }
        Write-Ok "makensis: $((Get-Command makensis).Source)"
    }
}

# ============================================================================
# 2. 生成并校验 Wails bindings（与 CI 一致的门禁）
# ============================================================================
Write-Info "生成并校验 Wails bindings..."
node scripts/generate-bindings.mjs
if ($LASTEXITCODE -ne 0) { Write-Fail "bindings 生成失败" }
node scripts/check-bindings.mjs
if ($LASTEXITCODE -ne 0) { Write-Fail "bindings 校验失败" }
Write-Ok "bindings 就绪"

# ============================================================================
# 3. 构建前端生产版本
# ============================================================================
Write-Info "构建前端生产版本..."
Push-Location (Join-Path $RootDir "frontend")
try {
    if (-not (Test-Path "node_modules")) {
        npm ci
        if ($LASTEXITCODE -ne 0) { Write-Fail "前端依赖安装失败" }
    }
    npm run build
    if ($LASTEXITCODE -ne 0) { Write-Fail "前端构建失败" }
} finally {
    Pop-Location
}
Write-Ok "前端构建完成"

# ============================================================================
# 4. 生成 syso（图标/版本/清单）并构建 GUI 可执行文件
# ============================================================================
Write-Info "生成 Windows .syso（图标/版本信息）..."
New-Item -ItemType Directory -Force -Path $BinDir | Out-Null
$SysoPath = Join-Path $RootDir "wails_windows_$Arch.syso"
try {
    wails3 generate syso `
        -arch $Arch `
        -icon (Join-Path $RootDir "build\windows\icon.ico") `
        -manifest (Join-Path $RootDir "build\windows\wails.exe.manifest") `
        -info (Join-Path $RootDir "build\windows\info.json") `
        -out $SysoPath
    if ($LASTEXITCODE -ne 0) { throw "wails3 generate syso failed" }
} catch {
    Write-Fail "syso 生成失败: $_"
}

Write-Info "构建 GUI 可执行文件（go build -tags production -H=windowsgui）..."
$ExePath = Join-Path $BinDir "$AppName.exe"
$env:CGO_ENABLED = "0"
$env:GOOS = "windows"
$env:GOARCH = $Arch
# 注意: -H=windowsgui 必须与 -s -w 放在同一个 -ldflags 值内（= 形式），否则会被忽略
$ldflags = "-s -w -H=windowsgui"
go build -tags production -trimpath -buildvcs=false -ldflags $ldflags -o $ExePath .
if ($LASTEXITCODE -ne 0) {
    Write-Fail "go build 失败（exit $LASTEXITCODE）"
}

# 清理 syso（*.syso 已被 gitignore，但保持工作区干净）
Remove-Item -Force -ErrorAction SilentlyContinue (Join-Path $RootDir "wails_windows_*.syso")
Remove-Item -Force -ErrorAction SilentlyContinue (Join-Path $RootDir "*.syso")

if (-not (Test-Path $ExePath)) {
    Write-Fail "未找到构建产物: $ExePath"
}

# PE 子系统校验：必须是 GUI（2），否则启动会出现黑色 CMD 窗口
$peBytes = [System.IO.File]::ReadAllBytes($ExePath)
$peOffset = [BitConverter]::ToInt32($peBytes, 0x3C)
$subsystem = [BitConverter]::ToUInt16($peBytes, $peOffset + 0x18 + 0x44)
if ($subsystem -ne 2) {
    Write-Fail "可执行文件仍是 CONSOLE 子系统（$subsystem），启动会出现 CMD 窗口，中止"
}
Write-Ok "GUI 可执行文件构建完成: $ExePath"

if (-not $SkipNSIS) {
# ============================================================================
# 5. 打包 NSIS 安装程序
# ============================================================================
Write-Info "生成 WebView2 bootstrapper..."
wails3 generate webview2bootstrapper -dir (Join-Path $RootDir "build\windows\nsis")
if ($LASTEXITCODE -ne 0) { Write-Fail "WebView2 bootstrapper 生成失败（需要网络下载）" }

Write-Info "编译 NSIS 安装程序..."
$ArgFlag = if ($Arch -eq "amd64") { "AMD64" } else { "ARM64" }
$nsisArgs = @()
if ($InstallScope -eq "user") {
    $nsisArgs += "-DWAILS_INSTALL_SCOPE=user"
    $nsisArgs += "-DREQUEST_EXECUTION_LEVEL=user"
}
# 字面引号包裹值，与 Taskfile 的 makensis 调用一致（路径含空格时 NSIS 也能正确解析）
$nsisArgs += ('-DARG_WAILS_{0}_BINARY="{1}\bin\{2}.exe"' -f $ArgFlag, $RootDir, $AppName)
$nsisArgs += "project.nsi"
Push-Location (Join-Path $RootDir "build\windows\nsis")
try {
    & makensis @nsisArgs
    if ($LASTEXITCODE -ne 0) { Write-Fail "makensis 编译失败（exit $LASTEXITCODE）" }
} finally {
    Pop-Location
}

$InstallerPath = Join-Path $BinDir "$AppName-$Arch-installer.exe"
if (-not (Test-Path $InstallerPath)) {
    Write-Warn "未在 $BinDir 找到 $AppName-$Arch-installer.exe，尝试模糊匹配..."
    $InstallerPath = (Get-ChildItem $BinDir -Filter "$AppName-*-installer.exe" | Select-Object -First 1).FullName
}
if (-not $InstallerPath -or -not (Test-Path $InstallerPath)) {
    Write-Fail "NSIS 安装程序未生成"
}
Write-Ok "NSIS 安装程序: $InstallerPath"
}

# ============================================================================
# 6. 汇总产物
# ============================================================================
Write-Host ""
Write-Info "========================================="
Write-Info "  koyori-ide Windows 构建完成"
Write-Info "  架构: $Arch | 版本: $Version"
Write-Info "========================================="
Write-Host ""
Get-ChildItem $BinDir -Filter "$AppName*" | ForEach-Object {
    $SizeMb = [math]::Round($_.Length / 1MB, 1)
    Write-Info "  $($_.Name)  ($SizeMb MB)"
}
Write-Host ""
if ($InstallerPath) {
Write-Info "分发: 将 $(Split-Path $InstallerPath -Leaf) 分发给用户，双击安装（$InstallScope 范围）"
Write-Info "静默安装: $(Split-Path $InstallerPath -Leaf) /S"
Write-Info "卸载: 控制面板 -> 卸载程序 或 安装目录下 uninstall.exe"
}
Write-Ok "构建完成！"
