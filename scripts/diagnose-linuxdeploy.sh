#!/usr/bin/env bash
# Keep the complete diagnostic output after a failed Linux desktop build.
# Individual failures must not prevent the remaining diagnostics.
set -u
PROJECT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$PROJECT_DIR" || exit 1
LOG_DIR="$PROJECT_DIR/.tmp/linuxdeploy-diagnostics"
mkdir -p "$LOG_DIR" || exit 1

diagnose() {
    local label="$1"
    shift
    local log="$LOG_DIR/$label.log"
    ( "$@"; echo "EXIT: $?" ) > "$log" 2>&1
    printf '\n%s: %s\n' "$label" "$log"
    tail -80 "$log"
}

diagnose tauri-cache ls -la "$HOME/.cache/tauri"
diagnose appimage-directory ls -la desktop/rust/target/release/bundle/appimage
LINUXDEPLOY="$HOME/.cache/tauri/linuxdeploy-$(uname -m).AppImage"
diagnose wrapper-version "$LINUXDEPLOY" --version
diagnose extracted-plugins "$HOME/.cache/tauri/linuxdeploy-extracted/AppRun" --list-plugins
APPDIR="$(find desktop/rust/target/release/bundle/appimage -maxdepth 1 -name '*.AppDir' -print -quit 2>/dev/null)"
if [ -n "$APPDIR" ]; then
    diagnose direct-linuxdeploy "$LINUXDEPLOY" --appdir "$APPDIR" --output appimage --plugin gtk
else
    printf 'No AppDir exists. Tauri did not reach the linuxdeploy step.\n'
fi
SIDECAR="$(find desktop/go/bin -maxdepth 1 -name 'leapmux-desktop-service-*' -print -quit 2>/dev/null)"
if [ -n "$SIDECAR" ]; then
    diagnose source-file file "$SIDECAR"
    diagnose source-program-headers readelf -l "$SIDECAR"
    diagnose source-linkage ldd "$SIDECAR"
else
    printf 'No sidecar exists under desktop/go/bin/.\n'
fi
if [ -n "$APPDIR" ]; then
    SIDECAR="$(find "$APPDIR" -name 'leapmux-desktop-service-*' -print -quit 2>/dev/null)"
    if [ -n "$SIDECAR" ]; then
        diagnose bundle-file file "$SIDECAR"
        diagnose bundle-linkage ldd "$SIDECAR"
    else
        printf 'No sidecar exists inside the AppDir.\n'
    fi
fi
diagnose gcc-path command -v gcc
diagnose gcc-version gcc --version
diagnose cgo go env CGO_ENABLED
diagnose arm64-loader ls -la /lib/ld-linux-aarch64.so.1
