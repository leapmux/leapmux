#!/bin/bash
#
# Creates a styled .dmg installer for LeapMux.
#
# Usage: create-dmg.sh <version> <app-path> <output-dmg>
#
# Requires macOS (hdiutil) and Node.js with ds-store package.

set -euo pipefail

VERSION="$1"
APP_PATH="$2"
OUTPUT_DMG="$3"
APP_NAME="$(basename "${APP_PATH}")"

VOLUME_NAME="LeapMux Desktop ${VERSION}"
DMG_TEMP="$(mktemp -u -t leapmux).dmg"
STAGING_DIR="$(mktemp -d -t leapmux-dmg)"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

# Window dimensions and icon positions.
WIN_WIDTH=540
WIN_HEIGHT=360
WIN_X=100
WIN_Y=100
ICON_SIZE=128
TEXT_SIZE=14
APP_X=130
APP_Y=150
APPS_X=410
APPS_Y=150

# Background color (warm sand, matching LeapMux theme).
BG_R=0.961
BG_G=0.945
BG_B=0.922

cleanup() {
  if [ -d "/Volumes/${VOLUME_NAME}" ]; then
    hdiutil detach "/Volumes/${VOLUME_NAME}" -quiet -force 2>/dev/null || true
  fi
  rm -f "${DMG_TEMP}"
  rm -rf "${STAGING_DIR}"
}
trap cleanup EXIT

# -- 0. Detach the previously attached DMGs. --
find /Volumes/ \
  -type d -mindepth 1 -maxdepth 1 -name 'LeapMux*' \
  -exec hdiutil detach {} -quiet -force ';'

# -- 1. Calculate DMG size and create empty read-write DMG. --
cp -a "${APP_PATH}" "${STAGING_DIR}/${APP_NAME}"
APP_SIZE_KB=$(du -sk "${STAGING_DIR}/${APP_NAME}" | awk '{print $1}')
DMG_SIZE_KB=$(( APP_SIZE_KB + 20480 ))

hdiutil create \
  -volname "${VOLUME_NAME}" \
  -size "${DMG_SIZE_KB}k" \
  -ov \
  -type UDIF \
  -fs HFS+ \
  "${DMG_TEMP}"

DEVICE=$(hdiutil attach -readwrite -noverify "${DMG_TEMP}" | grep '/Volumes/' | awk '{print $1}')
MOUNT_POINT="/Volumes/${VOLUME_NAME}"

# -- 2. Copy contents onto the mounted volume. --
cp -a "${APP_PATH}" "${MOUNT_POINT}/${APP_NAME}"
ln -s /Applications "${MOUNT_POINT}/Applications"

# -- 3. Generate .DS_Store with Node.js. --
node "${SCRIPT_DIR}/generate-dsstore.mjs" \
  "${MOUNT_POINT}" \
  "${MOUNT_POINT}/.DS_Store" \
  --bg-color "${BG_R},${BG_G},${BG_B}" \
  --icon-size "${ICON_SIZE}" \
  --text-size "${TEXT_SIZE}" \
  --window-pos "${WIN_X},${WIN_Y}" \
  --window-size "${WIN_WIDTH},${WIN_HEIGHT}" \
  --icon "${APP_NAME},${APP_X},${APP_Y}" \
  --icon "Applications,${APPS_X},${APPS_Y}"

# -- 4. Finalize: unmount and convert to compressed. --
chmod -Rf go-w "${MOUNT_POINT}" 2>/dev/null || true
sync

# Retry detach: mds/Spotlight briefly holds the volume after the .DS_Store
# write, causing `hdiutil detach` to exit 16 ("Resource busy"). Wait and
# retry a few times, then fall back to -force so we don't block the build
# on a transient lock — we've already sync'd, so forced detach is safe.
for attempt in 1 2 3 4 5; do
  if hdiutil detach "${DEVICE}" -quiet; then
    break
  fi
  if [ "${attempt}" -eq 5 ]; then
    echo "create-dmg: detach still busy after ${attempt} attempts; forcing." >&2
    hdiutil detach "${DEVICE}" -force -quiet
    break
  fi
  echo "create-dmg: detach attempt ${attempt} busy; retrying in ${attempt}s..." >&2
  sleep "${attempt}"
done
sleep 1

rm -f "${OUTPUT_DMG}"
hdiutil convert "${DMG_TEMP}" -format UDZO -imagekey zlib-level=9 -o "${OUTPUT_DMG}"

echo "Created: ${OUTPUT_DMG}"
