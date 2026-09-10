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

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
mkdir -p "${PROJECT_ROOT}/.tmp"
STAGING_DIR="$(mktemp -d "${PROJECT_ROOT}/.tmp/dmg-XXXXXX")"
VOLUME_NAME="LeapMux Desktop ${VERSION}"
DMG_TEMP="${STAGING_DIR}/writable.dmg"
MOUNT_POINT="${STAGING_DIR}/volume"
MOUNTED=0

# Window dimensions and icon positions.
WIN_WIDTH=540
WIN_HEIGHT=360
WIN_X=100
WIN_Y=100
ICON_SIZE=128
APP_X=130
APP_Y=150
APPS_X=410
APPS_Y=150

# Background color (warm sand, matching LeapMux theme).
BG_R=0.961
BG_G=0.945
BG_B=0.922

cleanup() {
  local result=$?
  trap - EXIT
  if [ "${MOUNTED}" -eq 1 ]; then
    if ! hdiutil detach "${MOUNT_POINT}" -quiet -force; then
      printf 'Cannot detach the build volume at %s. Staging remains intact.\n' "${MOUNT_POINT}" >&2
      exit 1
    fi
  fi
  rm -rf "${STAGING_DIR}"
  exit "${result}"
}
trap cleanup EXIT

# Calculate the image size from the source. Copy the app only onto the mounted image.
APP_SIZE_KB=$(du -sk "${APP_PATH}" | awk '{print $1}')
DMG_SIZE_KB=$(( APP_SIZE_KB + 20480 ))

hdiutil create \
  -volname "${VOLUME_NAME}" \
  -size "${DMG_SIZE_KB}k" \
  -ov \
  -type UDIF \
  -fs HFS+ \
  "${DMG_TEMP}"

mkdir -p "${MOUNT_POINT}"
hdiutil attach -readwrite -noverify -nobrowse -mountpoint "${MOUNT_POINT}" "${DMG_TEMP}"
MOUNTED=1

# -- 2. Copy contents onto the mounted volume. --
cp -a "${APP_PATH}" "${MOUNT_POINT}/${APP_NAME}"
ln -s /Applications "${MOUNT_POINT}/Applications"

# -- 3. Generate .DS_Store with Node.js. --
node "${SCRIPT_DIR}/generate-dsstore.mjs" \
  "${MOUNT_POINT}/.DS_Store" \
  --bg-color "${BG_R},${BG_G},${BG_B}" \
  --icon-size "${ICON_SIZE}" \
  --window-pos "${WIN_X},${WIN_Y}" \
  --window-size "${WIN_WIDTH},${WIN_HEIGHT}" \
  --icon "${APP_NAME},${APP_X},${APP_Y}" \
  --icon "Applications,${APPS_X},${APPS_Y}"

# -- 4. Finalize: unmount and convert to compressed. --
chmod -Rf go-w "${MOUNT_POINT}" 2>/dev/null || true
sync

# Spotlight can briefly hold the volume after the metadata write.
# Retry only status 16 (resource busy). The completed sync permits a final forced detach.
for attempt in 1 2 3 4 5; do
  if hdiutil detach "${MOUNT_POINT}" -quiet; then
    MOUNTED=0
    break
  else
    detach_result=$?
  fi
  if [ "${detach_result}" -ne 16 ]; then
    exit "${detach_result}"
  fi
  if [ "${attempt}" -eq 5 ]; then
    printf 'The build volume remains busy after %s attempts. Force detach.\n' "${attempt}" >&2
    hdiutil detach "${MOUNT_POINT}" -force -quiet
    MOUNTED=0
    break
  fi
  printf 'The build volume is busy. Retry in %s seconds.\n' "${attempt}" >&2
  sleep "${attempt}"
done

# Preserve the previous artifact if conversion fails.
hdiutil convert "${DMG_TEMP}" -format UDZO -imagekey zlib-level=9 -o "${STAGING_DIR}/result.dmg"
mv -f "${STAGING_DIR}/result.dmg" "${OUTPUT_DMG}"

echo "Created: ${OUTPUT_DMG}"
