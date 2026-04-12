#!/usr/bin/env bash
#
# Patches the cached linuxdeploy AppImage so that it uses the system's `strip`
# instead of its bundled one (binutils 2.35), which cannot handle the
# `.relr.dyn` sections produced by modern toolchains (binutils >= 2.36).
#
# Also patches the GTK plugin to exclude VMware's bundled libraries from
# recursive find, preventing deployment failures caused by VMware's old
# libgdk_pixbuf depending on the missing libcroco-0.6.so.3.
#
# This script is idempotent — it only patches once and skips subsequent runs.
#
set -euo pipefail

CACHE_DIR="${HOME}/.cache/tauri"
APPIMAGE="${CACHE_DIR}/linuxdeploy-x86_64.AppImage"
EXTRACTED="${CACHE_DIR}/linuxdeploy-extracted"
GTK_PLUGIN="${CACHE_DIR}/linuxdeploy-plugin-gtk.sh"

LINUXDEPLOY_URL="https://github.com/tauri-apps/binary-releases/releases/download/linuxdeploy/linuxdeploy-x86_64.AppImage"
GTK_PLUGIN_URL="https://raw.githubusercontent.com/tauri-apps/linuxdeploy-plugin-gtk/master/linuxdeploy-plugin-gtk.sh"

# Download linuxdeploy if not cached yet.
mkdir -p "$CACHE_DIR"
if [[ ! -f "$APPIMAGE" ]]; then
    echo "patch-linuxdeploy: downloading linuxdeploy ..."
    curl -fsSL -o "$APPIMAGE" "$LINUXDEPLOY_URL"
    chmod +x "$APPIMAGE"
fi

# Already patched — the .orig backup and extracted directory only exist
# after a successful patching run.
if [[ -f "${APPIMAGE}.orig" ]] && [[ -d "$EXTRACTED" ]] && [[ -x "$EXTRACTED/AppRun" ]]; then
    echo "patch-linuxdeploy: already patched; skipping."
    exit 0
fi

echo "patch-linuxdeploy: extracting linuxdeploy AppImage ..."
rm -rf "${CACHE_DIR}/squashfs-root" "$EXTRACTED"
(cd "$CACHE_DIR" && "$APPIMAGE" --appimage-extract >/dev/null 2>&1)
mv "${CACHE_DIR}/squashfs-root" "$EXTRACTED"

echo "patch-linuxdeploy: replacing bundled strip with system strip ..."
ln -sf /usr/bin/strip "$EXTRACTED/usr/bin/strip"

echo "patch-linuxdeploy: installing compiled wrapper binary ..."
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
WRAPPER="$SCRIPT_DIR/linuxdeploy-wrapper"
if [[ ! -x "$WRAPPER" ]]; then
    echo "patch-linuxdeploy: compiling wrapper ..."
    gcc -static -O2 -o "$WRAPPER" "$SCRIPT_DIR/linuxdeploy-wrapper.c"
fi
mv "$APPIMAGE" "${APPIMAGE}.orig"
cp "$WRAPPER" "$APPIMAGE"
chmod +x "$APPIMAGE"

# Download and patch the GTK plugin to exclude VMware's bundled libraries
# from the recursive find, preventing deployment failures caused by
# VMware's old libgdk_pixbuf depending on the missing libcroco-0.6.so.3.
if [[ ! -f "$GTK_PLUGIN" ]]; then
    echo "patch-linuxdeploy: downloading GTK plugin ..."
    curl -fsSL -o "$GTK_PLUGIN" "$GTK_PLUGIN_URL"
    chmod +x "$GTK_PLUGIN"
fi
if ! grep -q 'vmware' "$GTK_PLUGIN"; then
    echo "patch-linuxdeploy: patching GTK plugin to exclude VMware libraries ..."
    sed -i 's|find "$directory" \\( -type l -o -type f \\)|find "$directory" -not -path "*/vmware/*" \\( -type l -o -type f \\)|' "$GTK_PLUGIN"
fi

echo "patch-linuxdeploy: done."
