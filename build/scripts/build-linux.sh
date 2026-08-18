#!/usr/bin/env bash
# koyori-ide Linux 桌面应用构建脚本
# 在 Linux 上运行此脚本以构建并打包 koyori-ide 桌面应用
#
# 用法:
#   chmod +x build/scripts/build-linux.sh
#   ./build/scripts/build-linux.sh                # 当前架构，构建所有包格式
#   ./build/scripts/build-linux.sh amd64          # 指定 amd64
#   ./build/scripts/build-linux.sh arm64          # 指定 arm64
#   ./build/scripts/build-linux.sh appimage       # 仅构建 AppImage
#   ./build/scripts/build-linux.sh deb            # 仅构建 deb 包
#   ./build/scripts/build-linux.sh rpm            # 仅构建 rpm 包
#   ./build/scripts/build-linux.sh bare           # 仅构建裸可执行文件
#
# 产物 (位于 bin/):
#   koyori-ide                              # 裸可执行文件
#   koyori-ide-<version>-linux-<arch>.AppImage  # AppImage (免安装)
#   koyori-ide-<version>-linux-<arch>.deb  # Debian/Ubuntu 安装包
#   koyori-ide-<version>-linux-<arch>.rpm  # Fedora/RHEL 安装包

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
PKG_TYPE="all"
ARCH="$(uname -m)"
readonly LINUXDEPLOY_VERSION="1-alpha-20251107-1"
readonly LINUXDEPLOY_X86_64_SHA256="c20cd71e3a4e3b80c3483cef793cda3f4e990aca14014d23c544ca3ce1270b4d"
readonly LINUXDEPLOY_AARCH64_SHA256="620095110d693282b8ebeb244a95b5e911cf8f65f76c88b4b47d16ae6346fcff"
readonly NFPM_VERSION="v2.44.1"
PRINT_VERSION=false

# 解析参数
for arg in "$@"; do
    case "$arg" in
        amd64|x86_64) ARCH="amd64" ;;
        arm64|aarch64) ARCH="arm64" ;;
        appimage|deb|rpm|bare) PKG_TYPE="$arg" ;;
        --print-version) PRINT_VERSION=true ;;
        "") : ;;
        *) warn "未知参数: $arg" ;;
    esac
done

# 统一架构名称
case "$ARCH" in
    x86_64) ARCH="amd64" ;;
    aarch64) ARCH="arm64" ;;
esac

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
info "  包格式:     $PKG_TYPE"
info "  应用名称:   $APP_NAME"
info "  版本号:     $VERSION"

# ============================================================================
# 1. 检查并安装依赖
# ============================================================================
check_and_install_deps() {
    info "检查构建依赖..."

    # 检测包管理器
    local PKG_MGR=""
    if command -v apt-get &>/dev/null; then
        PKG_MGR="apt"
    elif command -v dnf &>/dev/null; then
        PKG_MGR="dnf"
    elif command -v yum &>/dev/null; then
        PKG_MGR="yum"
    elif command -v pacman &>/dev/null; then
        PKG_MGR="pacman"
    elif command -v zypper &>/dev/null; then
        PKG_MGR="zypper"
    else
        fail "无法识别包管理器。请手动安装: gcc, pkg-config, libwebkit2gtk-4.1-dev (或 libwebkitgtk-6.0-dev), libgtk-3-dev (或 libgtk-4-dev)"
    fi
    info "  包管理器: $PKG_MGR"

    # Go 1.25+
    if ! command -v go &>/dev/null; then
        fail "未安装 Go。请从 https://go.dev/dl/ 安装 Go 1.25+"
    fi
    GO_VERSION=$(go version | awk '{print $3}' | sed 's/go//')
    GO_MAJOR=$(echo "$GO_VERSION" | cut -d. -f1)
    GO_MINOR=$(echo "$GO_VERSION" | cut -d. -f2)
    if [ "$GO_MAJOR" -lt 1 ] || { [ "$GO_MAJOR" -eq 1 ] && [ "$GO_MINOR" -lt 25 ]; }; then
        fail "Go 版本过低 ($GO_VERSION)，需要 1.25+"
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

    # C 编译器和 GTK/WebKit 开发库
    local NEED_INSTALL=0
    if ! command -v gcc &>/dev/null; then
        NEED_INSTALL=1
    fi
    if ! pkg-config --exists webkit2gtk-4.1 2>/dev/null && \
       ! pkg-config --exists webkitgtk-6.0 2>/dev/null; then
        NEED_INSTALL=1
    fi

    if [ "$NEED_INSTALL" -eq 1 ]; then
        info "安装系统依赖 (需要 sudo 权限)..."
        case "$PKG_MGR" in
            apt)
                # 优先尝试 GTK4 + WebKitGTK 6.0 (Ubuntu 24.04+), 回退到 GTK3 + WebKit2GTK 4.1
                sudo apt-get update
                sudo apt-get install -y \
                    build-essential pkg-config \
                    libgtk-4-dev libwebkitgtk-6.0-dev \
                    2>/dev/null || {
                    info "回退到 GTK3 + WebKit2GTK 4.1..."
                    sudo apt-get install -y \
                        build-essential pkg-config \
                        libgtk-3-dev libwebkit2gtk-4.1-dev
                }
                ;;
            dnf|yum)
                sudo $PKG_MGR install -y \
                    gcc pkg-config \
                    gtk4-devel webkitgtk6.0-devel \
                    2>/dev/null || {
                    info "回退到 GTK3 + WebKit2GTK 4.1..."
                    sudo $PKG_MGR install -y \
                        gcc pkg-config \
                        gtk3-devel webkit2gtk4.1-devel
                }
                ;;
            pacman)
                sudo pacman -S --noconfirm \
                    base-devel pkgconf \
                    gtk4 webkitgtk-6.0 \
                    2>/dev/null || {
                    info "回退到 GTK3 + WebKit2GTK 4.1..."
                    sudo pacman -S --noconfirm \
                        base-devel pkgconf \
                        gtk3 webkit2gtk-4.1
                }
                ;;
            zypper)
                sudo zypper install -y \
                    gcc pkg-config \
                    gtk4-devel webkitgtk-6_0-devel \
                    2>/dev/null || {
                    info "回退到 GTK3 + WebKit2GTK 4.1..."
                    sudo zypper install -y \
                        gcc pkg-config \
                        gtk3-devel libwebkit2gtk-4_1-devel
                }
                ;;
        esac
    fi
    ok "gcc: $(gcc --version | head -1)"
    ok "pkg-config: $(pkg-config --version)"

    # 检测 WebKit 版本并设置构建标签
    WEBKIT_TAG=""
    # Keep package metadata aligned with the library stack selected below.
    # The GTK3 fallback must not emit GTK4 runtime dependencies.
    GTK_DEB_DEP=""
    WEBKIT_DEB_DEP=""
    GTK_RPM_DEP=""
    WEBKIT_RPM_DEP=""
    GTK_ARCH_DEP=""
    WEBKIT_ARCH_DEP=""
    if pkg-config --exists webkitgtk-6.0 2>/dev/null; then
        ok "WebKit: webkitgtk-6.0 (GTK4)"
        WEBKIT_TAG=""
        GTK_DEB_DEP="libgtk-4-1"
        WEBKIT_DEB_DEP="libwebkitgtk-6.0-4"
        GTK_RPM_DEP="gtk4"
        WEBKIT_RPM_DEP="webkitgtk6.0"
        GTK_ARCH_DEP="gtk4"
        WEBKIT_ARCH_DEP="webkitgtk-6.0"
    elif pkg-config --exists webkit2gtk-4.1 2>/dev/null; then
        ok "WebKit: webkit2gtk-4.1 (GTK3)"
        WEBKIT_TAG="gtk3"
        GTK_DEB_DEP="libgtk-3-0"
        WEBKIT_DEB_DEP="libwebkit2gtk-4.1-0"
        GTK_RPM_DEP="gtk3"
        WEBKIT_RPM_DEP="webkit2gtk4.1"
        GTK_ARCH_DEP="gtk3"
        WEBKIT_ARCH_DEP="webkit2gtk-4.1"
    else
        fail "WebKit 未安装。请手动安装 libwebkitgtk-6.0-dev 或 libwebkit2gtk-4.1-dev"
    fi
    export GTK_DEB_DEP WEBKIT_DEB_DEP GTK_RPM_DEP WEBKIT_RPM_DEP GTK_ARCH_DEP WEBKIT_ARCH_DEP
    export WEBKIT_TAG
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
# 3. 构建桌面应用 (CGO + WebKitGTK)
# ============================================================================
build_app() {
    info "构建桌面应用 (CGO_ENABLED=1, WebKitGTK)..."

    export CGO_ENABLED=1
    export GOOS="linux"
    export GOARCH="$ARCH"

    mkdir -p "$BIN_DIR"

    # 构建标签：production + 可选的 gtk3
    local TAGS="production"
    if [ -n "${WEBKIT_TAG:-}" ]; then
        TAGS="production,${WEBKIT_TAG}"
    fi

    go build -tags "$TAGS" -trimpath -buildvcs=false \
        -ldflags="-w -s" -o "$BIN_DIR/${APP_NAME}" .

    ok "可执行文件构建完成: $BIN_DIR/${APP_NAME}"
    ls -lh "$BIN_DIR/${APP_NAME}"
}

# ============================================================================
# 4. 生成 .desktop 文件
# ============================================================================
generate_desktop_file() {
    info "生成 .desktop 文件..."
    mkdir -p "$ROOT_DIR/build/linux"
    cat > "$ROOT_DIR/build/linux/${APP_NAME}.desktop" <<EOF
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
    ok ".desktop 文件已生成"
}

# ============================================================================
# 5. 创建 AppImage
# ============================================================================
create_appimage() {
    if [ "$PKG_TYPE" != "all" ] && [ "$PKG_TYPE" != "appimage" ]; then
        return 0
    fi

    info "创建 AppImage..."

    local APPIMAGE_WORKDIR="$ROOT_DIR/build/linux/appimage"
    mkdir -p "$APPIMAGE_WORKDIR"
    cd "$APPIMAGE_WORKDIR"

    local APP_DIR="${APP_NAME}.AppDir"
    rm -rf "$APP_DIR"
    mkdir -p "$APP_DIR/usr/bin"

    # 复制文件
    cp "$BIN_DIR/${APP_NAME}" "$APP_DIR/usr/bin/"
    cp "$ROOT_DIR/build/appicon.png" "$APP_DIR/${APP_NAME}.png"
    cp "$ROOT_DIR/build/linux/${APP_NAME}.desktop" "$APP_DIR/"

    # 下载 linuxdeploy
    local LINUXDEPLOY=""
    local LINUXDEPLOY_SHA256=""
    case "$ARCH" in
        amd64)
            LINUXDEPLOY="linuxdeploy-x86_64.AppImage"
            LINUXDEPLOY_SHA256="$LINUXDEPLOY_X86_64_SHA256"
            ;;
        arm64)
            LINUXDEPLOY="linuxdeploy-aarch64.AppImage"
            LINUXDEPLOY_SHA256="$LINUXDEPLOY_AARCH64_SHA256"
            ;;
        *) fail "不支持的架构: $ARCH" ;;
    esac

    if [ ! -f "$LINUXDEPLOY" ]; then
        info "下载 $LINUXDEPLOY..."
        wget -q -4 -O "${LINUXDEPLOY}.tmp" \
            "https://github.com/linuxdeploy/linuxdeploy/releases/download/${LINUXDEPLOY_VERSION}/${LINUXDEPLOY}"
        mv "${LINUXDEPLOY}.tmp" "$LINUXDEPLOY"
    fi
    echo "${LINUXDEPLOY_SHA256}  ${LINUXDEPLOY}" | sha256sum -c -
    chmod +x "$LINUXDEPLOY"

    # 运行 linuxdeploy
    ARCH_DEPLOY="$ARCH" ./"$LINUXDEPLOY" --appdir "$APP_DIR" --output appimage

    # 重命名
    local OUTPUT_NAME="${APP_NAME}-${VERSION}-linux-${ARCH}.AppImage"
    mv "${APP_NAME}"*.AppImage "$BIN_DIR/$OUTPUT_NAME"

    ok "AppImage 创建完成: $BIN_DIR/$OUTPUT_NAME"
    ls -lh "$BIN_DIR/$OUTPUT_NAME"
    cd "$ROOT_DIR"
}

# ============================================================================
# 6. 生成 nfpm 配置并构建 deb/rpm
# ============================================================================
generate_nfpm_config() {
    local NFPM_DIR="$ROOT_DIR/build/linux/nfpm"
    mkdir -p "$NFPM_DIR/scripts"

    # 生成 nfpm 配置（应用名不含 .exe 后缀）
    cat > "$NFPM_DIR/${APP_NAME}.yaml" <<EOF
name: "${APP_NAME}"
arch: "${ARCH}"
platform: "linux"
version: "${VERSION}"
section: "default"
priority: "extra"
maintainer: "Koyori IDE Contributors"
description: "AI-Powered Coding Desktop App for Professional Developers"
vendor: "Koyori IDE Contributors"
homepage: "https://github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide"
license: "MIT"
release: "1"

contents:
  - src: "./bin/${APP_NAME}"
    dst: "/usr/local/bin/${APP_NAME}"
  - src: "./build/appicon.png"
    dst: "/usr/share/icons/hicolor/128x128/apps/${APP_NAME}.png"
  - src: "./build/linux/${APP_NAME}.desktop"
    dst: "/usr/share/applications/${APP_NAME}.desktop"

# Runtime dependencies match the WebKit/GTK stack detected in
# check_and_install_deps. This is GTK4/WebKitGTK 6.0 on the preferred path and
# GTK3/WebKit2GTK 4.1 on the fallback path.
depends:
  - ${GTK_DEB_DEP}
  - ${WEBKIT_DEB_DEP}

overrides:
  rpm:
    depends:
      - ${GTK_RPM_DEP}
      - ${WEBKIT_RPM_DEP}
  archlinux:
    depends:
      - ${GTK_ARCH_DEP}
      - ${WEBKIT_ARCH_DEP}

scripts:
  postinstall: "./build/linux/nfpm/scripts/postinstall.sh"
  postremove: "./build/linux/nfpm/scripts/postremove.sh"
EOF

    # 生成 postinstall 脚本（如果不存在）
    if [ ! -f "$NFPM_DIR/scripts/postinstall.sh" ]; then
        cat > "$NFPM_DIR/scripts/postinstall.sh" <<'SCRIPT'
#!/bin/sh
if command -v update-desktop-database >/dev/null 2>&1; then
  update-desktop-database -q /usr/share/applications
fi
if command -v gtk-update-icon-cache >/dev/null 2>&1; then
  gtk-update-icon-cache -q /usr/share/icons/hicolor 2>/dev/null || true
fi
exit 0
SCRIPT
        chmod +x "$NFPM_DIR/scripts/postinstall.sh"
    fi

    # 生成 postremove 脚本（如果不存在）
    if [ ! -f "$NFPM_DIR/scripts/postremove.sh" ]; then
        cat > "$NFPM_DIR/scripts/postremove.sh" <<'SCRIPT'
#!/bin/sh
if command -v update-desktop-database >/dev/null 2>&1; then
  update-desktop-database -q /usr/share/applications
fi
if command -v gtk-update-icon-cache >/dev/null 2>&1; then
  gtk-update-icon-cache -q /usr/share/icons/hicolor 2>/dev/null || true
fi
exit 0
SCRIPT
        chmod +x "$NFPM_DIR/scripts/postremove.sh"
    fi
}

# ============================================================================
# 7. 创建 deb 包
# ============================================================================
ensure_nfpm() {
    if command -v nfpm &>/dev/null; then
        return 0
    fi

    info "安装 nfpm..."
    go install "github.com/goreleaser/nfpm/v2/cmd/nfpm@${NFPM_VERSION}" || \
        fail "nfpm installation failed"
    command -v nfpm &>/dev/null || \
        fail "nfpm was installed but is not available on PATH"
}

create_deb() {
    if [ "$PKG_TYPE" != "all" ] && [ "$PKG_TYPE" != "deb" ]; then
        return 0
    fi

    info "创建 deb 包..."

    ensure_nfpm

    generate_nfpm_config

    cd "$ROOT_DIR"
    local DEB_FILE="${APP_NAME}-${VERSION}-linux-${ARCH}.deb"
    rm -f "$BIN_DIR/$DEB_FILE"
    nfpm pkg --config "build/linux/nfpm/${APP_NAME}.yaml" --packager deb --target "$BIN_DIR/$DEB_FILE" || \
        fail "deb package creation failed: $BIN_DIR/$DEB_FILE"
    [ -s "$BIN_DIR/$DEB_FILE" ] || \
        fail "nfpm did not create the expected deb: $BIN_DIR/$DEB_FILE"
    ok "deb 包创建完成: $BIN_DIR/$DEB_FILE"
    ls -lh "$BIN_DIR/$DEB_FILE" 2>/dev/null || true
}

# ============================================================================
# 8. 创建 rpm 包
# ============================================================================
create_rpm() {
    if [ "$PKG_TYPE" != "all" ] && [ "$PKG_TYPE" != "rpm" ]; then
        return 0
    fi

    info "创建 rpm 包..."

    ensure_nfpm

    # 确保 nfpm 配置已生成
    if [ ! -f "$ROOT_DIR/build/linux/nfpm/${APP_NAME}.yaml" ]; then
        generate_nfpm_config
    fi

    cd "$ROOT_DIR"
    local RPM_FILE="${APP_NAME}-${VERSION}-linux-${ARCH}.rpm"
    rm -f "$BIN_DIR/$RPM_FILE"
    nfpm pkg --config "build/linux/nfpm/${APP_NAME}.yaml" --packager rpm --target "$BIN_DIR/$RPM_FILE" || \
        fail "rpm package creation failed: $BIN_DIR/$RPM_FILE"
    [ -s "$BIN_DIR/$RPM_FILE" ] || \
        fail "nfpm did not create the expected rpm: $BIN_DIR/$RPM_FILE"
    ok "rpm 包创建完成: $BIN_DIR/$RPM_FILE"
    ls -lh "$BIN_DIR/$RPM_FILE" 2>/dev/null || true
}

# ============================================================================
# 主流程
# ============================================================================
main() {
    echo ""
    info "========================================="
    info "  koyori-ide Linux 桌面应用构建"
    info "  架构: $ARCH | 版本: $VERSION | 包: $PKG_TYPE"
    info "========================================="
    echo ""

    check_and_install_deps
    node scripts/sync-release-metadata.mjs --check || \
        fail "release metadata is not synchronized with VERSION"
    build_frontend
    build_app

    if [ "$PKG_TYPE" != "bare" ]; then
        generate_desktop_file
        create_appimage
        create_deb
        create_rpm
    fi

    echo ""
    ok "构建完成！"
    info "产物 (位于 $BIN_DIR):"
    info "  可执行文件:  ${APP_NAME}"
    if [ -f "$BIN_DIR/${APP_NAME}-${VERSION}-linux-${ARCH}.AppImage" ]; then
        info "  AppImage:    ${APP_NAME}-${VERSION}-linux-${ARCH}.AppImage (免安装，直接运行)"
    fi
    if ls "$BIN_DIR"/${APP_NAME}-*-linux-*.deb 1>/dev/null 2>&1; then
        info "  deb 包:      $(basename $(ls $BIN_DIR/${APP_NAME}-*-linux-*.deb 2>/dev/null | head -1))"
    fi
    if ls "$BIN_DIR"/${APP_NAME}-*.rpm 1>/dev/null 2>&1; then
        info "  rpm 包:      $(basename $(ls $BIN_DIR/${APP_NAME}-*.rpm 2>/dev/null | head -1))"
    fi
    echo ""
    info "运行应用:"
    info "  直接运行:    $BIN_DIR/${APP_NAME}"
    info "  AppImage:    chmod +x $BIN_DIR/${APP_NAME}-${VERSION}-linux-${ARCH}.AppImage && $BIN_DIR/${APP_NAME}-${VERSION}-linux-${ARCH}.AppImage"
    info "  安装 deb:    sudo dpkg -i $BIN_DIR/${APP_NAME}-*-linux-*.deb"
    info "  安装 rpm:    sudo rpm -i $BIN_DIR/${APP_NAME}-*.rpm"
}

main
