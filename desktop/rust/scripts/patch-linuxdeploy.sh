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
# Also patches the GStreamer plugin's AppRun hook generator, which has
# two upstream bugs that combine to break WebKitGTK media init inside
# the AppImage; see the GStreamer section below for details.
#
# This script is idempotent — it only patches once and skips subsequent runs.
#
set -euo pipefail

# linuxdeploy ships arch-specific AppImages. `uname -m` returns `x86_64` on
# Intel/AMD hosts and `aarch64` on 64-bit ARM — matching the asset naming on
# tauri-apps/binary-releases.
ARCH="$(uname -m)"

CACHE_DIR="${HOME}/.cache/tauri"
APPIMAGE="${CACHE_DIR}/linuxdeploy-${ARCH}.AppImage"
EXTRACTED="${CACHE_DIR}/linuxdeploy-extracted"
GTK_PLUGIN="${CACHE_DIR}/linuxdeploy-plugin-gtk.sh"
GSTREAMER_PLUGIN="${CACHE_DIR}/linuxdeploy-plugin-gstreamer.sh"

LINUXDEPLOY_URL="https://github.com/tauri-apps/binary-releases/releases/download/linuxdeploy/linuxdeploy-${ARCH}.AppImage"
GTK_PLUGIN_URL="https://raw.githubusercontent.com/tauri-apps/linuxdeploy-plugin-gtk/master/linuxdeploy-plugin-gtk.sh"
GSTREAMER_PLUGIN_URL="https://raw.githubusercontent.com/linuxdeploy/linuxdeploy-plugin-gstreamer/master/linuxdeploy-plugin-gstreamer.sh"

# Download linuxdeploy if not cached yet.
mkdir -p "$CACHE_DIR"
if [[ ! -f "$APPIMAGE" ]]; then
    echo "patch-linuxdeploy: downloading linuxdeploy ..."
    curl -fsSL -o "$APPIMAGE" "$LINUXDEPLOY_URL"
    chmod +x "$APPIMAGE"
fi

# Patch the linuxdeploy AppImage itself (replace bundled strip, swap in
# the wrapper binary) — the .orig backup and extracted directory only
# exist after a successful patching run, so use that as the marker.
# The plugin-script patches below run independently each time, so we
# can't `exit 0` here — they have their own grep-based idempotency.
if [[ -f "${APPIMAGE}.orig" ]] && [[ -d "$EXTRACTED" ]] && [[ -x "$EXTRACTED/AppRun" ]]; then
    echo "patch-linuxdeploy: linuxdeploy AppImage already patched; skipping."
else
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
fi

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

# Patch the GStreamer plugin's AppRun hook generator. The upstream
# script (linuxdeploy/linuxdeploy-plugin-gstreamer) emits a hook with
# two problems that together break WebKitGTK media init inside the
# AppImage:
#
#   1) The scanner / PTP helper paths point to "${APPDIR}/usr/lib/
#      gstreamer1.0/gstreamer-1.0/" — that doubled segment is the
#      plugin's `helpers_target_dir`, but on distros without a multiarch
#      helpers dir (e.g. Arch Linux) the plugin's helpers copy step
#      leaves it empty; the scanner actually lands alongside the
#      plugins in "${APPDIR}/usr/lib/gstreamer-1.0/" via the plugins
#      copy loop. The hook therefore points GStreamer at a non-existent
#      binary, and gst_plugin_loader_spawn() falls back to the
#      build-time `GST_PLUGIN_SCANNER_INSTALLED` path baked into
#      libgstreamer — which also doesn't resolve inside the AppImage.
#   2) GST_REGISTRY_REUSE_PLUGIN_SCANNER="no" forces GStreamer to
#      respawn the (broken) scanner once per plugin. With ~270 bundled
#      plugins that's ~270 fork-exec failures and a flood of "External
#      plugin loader failed" warnings, adding ~5s to startup.
#
# Note: the `_1_0` suffix on GST_PLUGIN_SCANNER_1_0 / GST_PTP_HELPER_1_0
# is *not* a typo. GStreamer reads the suffixed name first and falls
# back to the unsuffixed one (gstreamer/gst/gstpluginloader.c:~469 and
# libs/gst/net/gstptpclock.c:~2777) — same pattern as
# GST_PLUGIN_SYSTEM_PATH_1_0. Leave the names alone.
#
# Replace GST_REGISTRY_REUSE_PLUGIN_SCANNER with GST_REGISTRY_FORK so
# plugins load in-process and the helper is never spawned. Also fix
# the doubled path so the helper *would* work if anything ever
# bypasses GST_REGISTRY_FORK.
if [[ ! -f "$GSTREAMER_PLUGIN" ]]; then
    echo "patch-linuxdeploy: downloading GStreamer plugin ..."
    curl -fsSL -o "$GSTREAMER_PLUGIN" "$GSTREAMER_PLUGIN_URL"
    chmod +x "$GSTREAMER_PLUGIN"
fi
if ! grep -q 'GST_REGISTRY_FORK' "$GSTREAMER_PLUGIN"; then
    echo "patch-linuxdeploy: patching GStreamer plugin AppRun hook ..."
    sed -i 's|GST_REGISTRY_REUSE_PLUGIN_SCANNER|GST_REGISTRY_FORK|' "$GSTREAMER_PLUGIN"
    sed -i 's|${APPDIR}/usr/lib/gstreamer1.0/gstreamer-1.0/|${APPDIR}/usr/lib/gstreamer-1.0/|g' "$GSTREAMER_PLUGIN"
fi

echo "patch-linuxdeploy: done."
