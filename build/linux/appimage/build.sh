#!/usr/bin/env bash
# Copyright (c) 2018-Present Lea Anthony
# SPDX-License-Identifier: MIT

# Fail script on any error
set -euxo pipefail

# Define variables
APP_DIR="${APP_NAME}.AppDir"
readonly LINUXDEPLOY_VERSION="1-alpha-20251107-1"
readonly LINUXDEPLOY_X86_64_SHA256="c20cd71e3a4e3b80c3483cef793cda3f4e990aca14014d23c544ca3ce1270b4d"
readonly LINUXDEPLOY_AARCH64_SHA256="620095110d693282b8ebeb244a95b5e911cf8f65f76c88b4b47d16ae6346fcff"

# Create AppDir structure
mkdir -p "${APP_DIR}/usr/bin"
cp -r "${APP_BINARY}" "${APP_DIR}/usr/bin/"
cp "${ICON_PATH}" "${APP_DIR}/"
cp "${DESKTOP_FILE}" "${APP_DIR}/"

case "$(uname -m)" in
    x86_64)
        LINUXDEPLOY="linuxdeploy-x86_64.AppImage"
        LINUXDEPLOY_SHA256="$LINUXDEPLOY_X86_64_SHA256"
        ;;
    aarch64|arm64)
        LINUXDEPLOY="linuxdeploy-aarch64.AppImage"
        LINUXDEPLOY_SHA256="$LINUXDEPLOY_AARCH64_SHA256"
        ;;
    *)
        echo "Unsupported architecture: $(uname -m)" >&2
        exit 1
        ;;
esac

if [ ! -f "$LINUXDEPLOY" ]; then
    wget -q -4 -O "${LINUXDEPLOY}.tmp" \
        "https://github.com/linuxdeploy/linuxdeploy/releases/download/${LINUXDEPLOY_VERSION}/${LINUXDEPLOY}"
    mv "${LINUXDEPLOY}.tmp" "$LINUXDEPLOY"
fi
echo "${LINUXDEPLOY_SHA256}  ${LINUXDEPLOY}" | sha256sum -c -
chmod +x "$LINUXDEPLOY"

./"$LINUXDEPLOY" --appdir "${APP_DIR}" --output appimage

# Rename the generated AppImage
mv "${APP_NAME}*.AppImage" "${APP_NAME}.AppImage"
