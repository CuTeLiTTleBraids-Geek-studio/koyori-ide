#!/usr/bin/env bash
# koyori-ide macOS 桌面应用构建脚本
# 在 macOS 上运行此脚本以构建并打包 koyori-ide 桌面应用
#
# 用法:
#   chmod +x build/scripts/build-macos.sh
#   ./build/scripts/build-macos.sh                # 当前架构
#   ./build/scripts/build-macos.sh arm64          # 指定 arm64 (Apple Silicon)
#   ./build/scripts/build-macos.sh amd64          # 指定 amd64 (Intel)
#   ./build/scripts/build-macos.sh universal      # 通用二进制 (arm64 + amd64)
#   ./build/scripts/build-macos.sh --no-dmg       # 跳过 DMG 创建
#
# 产物 (位于 bin/):
#   koyori-ide.app            # .app 应用包
#   koyori-ide                # 裸可执行文件
#   koyori-ide.dmg            # DMG 安装包 (如果 create-dmg 已安装)

set -euo pipefail

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

info()  { echo -e "${BLUE}[INFO]${NC} $*"; }
ok()    { echo -e "${GREEN}[OK]${NC} $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC} $*"; }
fail()  { echo -e "${RED}[ERROR]${NC} $*"; exit 1; }

# 项目根目录
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
cd "$ROOT_DIR"

APP_NAME="koyori-ide"
BIN_DIR="$ROOT_DIR/bin"
CREATE_DMG=true
PRINT_VERSION=false

# 解析参数
ARCH=""
for arg in "$@"; do
    case "$arg" in
        arm64|aarch64) ARCH="arm64" ;;
        amd64|x86_64) ARCH="amd64" ;;
        universal) ARCH="universal" ;;
        --no-dmg) CREATE_DMG=false ;;
        --print-version) PRINT_VERSION=true ;;
        *) warn "未知参数: $arg" ;;
    esac
done

# 默认使用当前架构
if [ -z "$ARCH" ]; then
    ARCH="$(uname -m)"
    case "$ARCH" in
        x86_64) ARCH="amd64" ;;
        arm64) ARCH="arm64" ;;
    esac
fi

# VERSION is the release source of truth. Missing or malformed metadata must
# stop the build instead of silently stamping an unrelated fallback version.
VERSION_FILE="$ROOT_DIR/VERSION"
[ -f "$VERSION_FILE" ] || fail "VERSION file not found: $VERSION_FILE"
VERSION="$(bash "$ROOT_DIR/scripts/read-release-version.sh" "$VERSION_FILE")"

if [ "$PRINT_VERSION" = true ]; then
    printf '%s\n' "$VERSION"
    exit 0
fi

info "构建配置:"
info "  项目目录:   $ROOT_DIR"
info "  目标架构:   $ARCH"
info "  应用名称:   $APP_NAME"
info "  版本号:     $VERSION"
info "  创建 DMG:   $CREATE_DMG"

# ============================================================================
# 1. 检查依赖
# ============================================================================
check_dependencies() {
    info "检查构建依赖..."

    # Go 1.25+
    if ! command -v go &>/dev/null; then
        fail "未安装 Go。请从 https://go.dev/dl/ 安装 Go 1.25+"
    fi
    GO_VERSION=$(go version | awk '{print $3}' | sed 's/go//')
    GO_MAJOR=$(echo "$GO_VERSION" | cut -d. -f1)
    GO_MINOR=$(echo "$GO_VERSION" | cut -d. -f2)
    if [ "$GO_MAJOR" -lt 1 ] || { [ "$GO_MAJOR" -eq 1 ] && [ "$GO_MINOR" -lt 25 ]; }; then
        fail "Go 版本过低 ($GO_VERSION)，需要 1.25+。请从 https://go.dev/dl/ 升级"
    fi
    ok "Go: $GO_VERSION"

    # Vite 8 requires Node ^20.19.0 or >=22.12.0.
    if ! command -v node &>/dev/null; then
        fail "未安装 Node.js。请从 https://nodejs.org/ 安装 Node.js 20.19+ 或 22.12+"
    fi
    NODE_VERSION=$(node --version | sed 's/^v//')
    NODE_MAJOR=$(printf '%s' "$NODE_VERSION" | cut -d. -f1)
    NODE_MINOR=$(printf '%s' "$NODE_VERSION" | cut -d. -f2)
    if ! { [ "$NODE_MAJOR" -eq 20 ] && [ "$NODE_MINOR" -ge 19 ]; } &&
       ! { [ "$NODE_MAJOR" -eq 22 ] && [ "$NODE_MINOR" -ge 12 ]; } &&
       ! [ "$NODE_MAJOR" -gt 22 ]; then
        fail "Node.js 版本不受支持 ($(node --version))，需要 ^20.19.0 或 >=22.12.0"
    fi
    ok "Node.js: $(node --version)"

    # npm
    if ! command -v npm &>/dev/null; then
        fail "未安装 npm"
    fi
    ok "npm: $(npm --version)"

    # Xcode 命令行工具 (提供 clang + macOS SDK + WebKit.framework)
    if ! xcode-select -p &>/dev/null; then
        info "安装 Xcode 命令行工具..."
        xcode-select --install 2>/dev/null || true
        fail "请等待 Xcode 命令行工具安装完成后重新运行此脚本"
    fi
    ok "Xcode 命令行工具: 已安装"

    # create-dmg (可选)
    if [ "$CREATE_DMG" = true ] && ! command -v create-dmg &>/dev/null; then
        warn "未安装 create-dmg，将跳过 DMG 创建"
        warn "安装 create-dmg: brew install create-dmg"
        CREATE_DMG=false
    fi
}

# ============================================================================
# 2. 构建前端
# ============================================================================
build_frontend() {
    info "构建前端..."
    cd "$ROOT_DIR"
    info "使用 go.mod 锁定版本生成并校验 Wails bindings..."
    node scripts/generate-bindings.mjs
    cd "$ROOT_DIR/frontend"

    if [ ! -d "node_modules" ]; then
        info "安装 npm 依赖..."
        npm install
    fi

    info "构建前端生产版本..."
    npx vite build --mode production

    ok "前端构建完成"
    cd "$ROOT_DIR"
}

# ============================================================================
# 3. 构建桌面应用 (CGO + WebKit.framework)
# ============================================================================
build_app() {
    info "构建桌面应用 (CGO_ENABLED=1, WebKit.framework)..."

    export CGO_ENABLED=1
    export CGO_CFLAGS="-mmacosx-version-min=12.0"
    export CGO_LDFLAGS="-mmacosx-version-min=12.0"
    export MACOSX_DEPLOYMENT_TARGET="12.0"

    mkdir -p "$BIN_DIR"

    local LDFLAGS="-w -s"

    if [ "$ARCH" = "universal" ]; then
        info "构建通用二进制 (arm64 + amd64)..."
        info "  构建 arm64..."
        GOARCH=arm64 go build -tags production -trimpath -buildvcs=false \
            -ldflags="$LDFLAGS" -o "$BIN_DIR/${APP_NAME}-arm64" .
        info "  构建 amd64..."
        GOARCH=amd64 go build -tags production -trimpath -buildvcs=false \
            -ldflags="$LDFLAGS" -o "$BIN_DIR/${APP_NAME}-amd64" .
        info "  合并为通用二进制..."
        lipo -create -output "$BIN_DIR/${APP_NAME}" \
            "$BIN_DIR/${APP_NAME}-arm64" "$BIN_DIR/${APP_NAME}-amd64"
        rm "$BIN_DIR/${APP_NAME}-arm64" "$BIN_DIR/${APP_NAME}-amd64"
    else
        info "构建 $ARCH 架构..."
        GOARCH=$ARCH go build -tags production -trimpath -buildvcs=false \
            -ldflags="$LDFLAGS" -o "$BIN_DIR/${APP_NAME}" .
    fi

    ok "可执行文件构建完成: $BIN_DIR/${APP_NAME}"
    ls -lh "$BIN_DIR/${APP_NAME}"
}

# ============================================================================
# 4. 创建 .app 应用包
# ============================================================================
create_app_bundle() {
    info "创建 .app 应用包..."
    local APP_BUNDLE="$BIN_DIR/${APP_NAME}.app"

    # 清理旧的
    rm -rf "$APP_BUNDLE"

    # 创建目录结构
    mkdir -p "$APP_BUNDLE/Contents/MacOS"
    mkdir -p "$APP_BUNDLE/Contents/Resources"

    # 复制可执行文件
    cp "$BIN_DIR/${APP_NAME}" "$APP_BUNDLE/Contents/MacOS/"

    # 复制图标
    if [ -f "$ROOT_DIR/build/darwin/icons.icns" ]; then
        cp "$ROOT_DIR/build/darwin/icons.icns" "$APP_BUNDLE/Contents/Resources/"
    fi
    if [ -f "$ROOT_DIR/build/darwin/Assets.car" ]; then
        cp "$ROOT_DIR/build/darwin/Assets.car" "$APP_BUNDLE/Contents/Resources/"
    fi

    # 复制 Info.plist
    if [ -f "$ROOT_DIR/build/darwin/Info.plist" ]; then
        cp "$ROOT_DIR/build/darwin/Info.plist" "$APP_BUNDLE/Contents/"
        /usr/libexec/PlistBuddy -c "Set :CFBundleVersion $VERSION" "$APP_BUNDLE/Contents/Info.plist"
        /usr/libexec/PlistBuddy -c "Set :CFBundleShortVersionString $VERSION" "$APP_BUNDLE/Contents/Info.plist"
    else
        warn "Info.plist 不存在，生成最小 Info.plist..."
        cat > "$APP_BUNDLE/Contents/Info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleExecutable</key>
    <string>${APP_NAME}</string>
    <key>CFBundleIdentifier</key>
    <string>com.koyori.app</string>
    <key>CFBundleName</key>
    <string>${APP_NAME}</string>
    <key>CFBundleDisplayName</key>
    <string>Koyori IDE</string>
    <key>CFBundleVersion</key>
    <string>${VERSION}</string>
    <key>CFBundleShortVersionString</key>
    <string>${VERSION}</string>
    <key>CFBundlePackageType</key>
    <string>APPL</string>
    <key>LSMinimumSystemVersion</key>
    <string>12.0</string>
    <key>NSHighResolutionCapable</key>
    <true/>
    <key>NSAppTransportSecurity</key>
    <dict>
        <key>NSAllowsArbitraryLoads</key>
        <false/>
    </dict>
</dict>
</plist>
EOF
    fi

    # Ad-hoc 签名（本地运行必需，否则会被 Gatekeeper 拦截）
    info "Ad-hoc 签名..."
    codesign --force --deep --sign - "$APP_BUNDLE"

    ok ".app 应用包创建完成: $APP_BUNDLE"
}

# ============================================================================
# 5. 创建 DMG 安装包
# ============================================================================
create_dmg() {
    if [ "$CREATE_DMG" = false ]; then
        warn "跳过 DMG 创建"
        return 0
    fi

    info "创建 DMG 安装包..."
    local APP_BUNDLE="$BIN_DIR/${APP_NAME}.app"
    local DMG_PATH="$BIN_DIR/${APP_NAME}-${VERSION}-macos-${ARCH}.dmg"

    rm -f "$DMG_PATH"

    local DMG_ARGS=(
        --volname "$APP_NAME"
        --window-pos 200 120
        --window-size 600 400
        --icon-size 100
        --icon "$APP_NAME.app" 175 190
        --hide-extension "$APP_NAME.app"
        --app-drop-link 425 190
    )

    if [ -f "$ROOT_DIR/build/darwin/icons.icns" ]; then
        DMG_ARGS+=(--volicon "$ROOT_DIR/build/darwin/icons.icns")
    fi

    if ! create-dmg "${DMG_ARGS[@]}" "$DMG_PATH" "$APP_BUNDLE"; then
        fail "DMG creation failed"
    fi
    if [ ! -s "$DMG_PATH" ]; then
        fail "DMG creation did not produce a non-empty artifact: $DMG_PATH"
    fi
    ok "DMG created: $DMG_PATH"
    ls -lh "$DMG_PATH"
}

# ============================================================================
# 主流程
# ============================================================================
main() {
    echo ""
    info "========================================="
    info "  koyori-ide macOS 桌面应用构建"
    info "  架构: $ARCH | 版本: $VERSION"
    info "========================================="
    echo ""

    check_dependencies
    node scripts/sync-release-metadata.mjs --check || \
        fail "release metadata is not synchronized with VERSION"
    build_frontend
    build_app
    create_app_bundle
    create_dmg

    echo ""
    ok "构建完成！"
    info "产物 (位于 $BIN_DIR):"
    info "  可执行文件: ${APP_NAME}"
    info "  应用包:    ${APP_NAME}.app"
    if [ -f "$BIN_DIR/${APP_NAME}-${VERSION}-macos-${ARCH}.dmg" ]; then
        info "  安装包:    ${APP_NAME}-${VERSION}-macos-${ARCH}.dmg"
    fi
    echo ""
    info "运行应用: open $BIN_DIR/${APP_NAME}.app"
    info "分发 DMG: 将 .dmg 文件分发给用户，双击挂载后拖拽到 Applications 文件夹"
}

main
