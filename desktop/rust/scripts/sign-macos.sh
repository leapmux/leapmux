#!/bin/bash
#
# Signs a macOS .app bundle with the Developer ID identity in
# ${MACOS_CODESIGN_IDENTITY}, using hardened runtime + secure timestamp.
#
# When ${MACOS_CODESIGN_IDENTITY} is unset, falls back to ad-hoc signing so
# local-dev builds still produce a runnable bundle without an Apple cert.
#
# Usage: sign-macos.sh <app-path>
#
# Env:
#   MACOS_CODESIGN_IDENTITY   Full identity, e.g. "Developer ID Application: ... (TEAMID)".
#                             Empty/unset → ad-hoc signing.
#   MACOS_ENTITLEMENTS_PATH   Path to the entitlements plist applied to the
#                             main app binary and the bundle wrapper. Required
#                             when MACOS_CODESIGN_IDENTITY is set.

set -euo pipefail

if [ "$#" -ne 1 ]; then
  echo "usage: $0 <app-path>" >&2
  exit 2
fi

APP_PATH="$1"

if [ ! -d "${APP_PATH}" ]; then
  echo "sign-macos: app bundle not found: ${APP_PATH}" >&2
  exit 1
fi

# Ad-hoc fallback for local dev: same behavior as the previous Taskfile branch.
if [ -z "${MACOS_CODESIGN_IDENTITY:-}" ]; then
  echo "sign-macos: MACOS_CODESIGN_IDENTITY unset; using ad-hoc signing." >&2
  codesign --force --deep --sign - "${APP_PATH}"
  codesign --verify --deep --strict --verbose=2 "${APP_PATH}"
  exit 0
fi

if [ -z "${MACOS_ENTITLEMENTS_PATH:-}" ]; then
  echo "sign-macos: MACOS_ENTITLEMENTS_PATH must be set when signing with a Developer ID identity." >&2
  exit 1
fi
if [ ! -f "${MACOS_ENTITLEMENTS_PATH}" ]; then
  echo "sign-macos: entitlements file not found: ${MACOS_ENTITLEMENTS_PATH}" >&2
  exit 1
fi

# Resolve the main executable from the bundle's Info.plist so we apply
# entitlements only to it; nested helpers/sidecars/dylibs get hardened
# runtime without entitlements (least privilege).
INFO_PLIST="${APP_PATH}/Contents/Info.plist"
if [ ! -f "${INFO_PLIST}" ]; then
  echo "sign-macos: missing Info.plist: ${INFO_PLIST}" >&2
  exit 1
fi
MAIN_EXEC_NAME="$(plutil -extract CFBundleExecutable raw -- "${INFO_PLIST}")"
MAIN_EXEC_PATH="${APP_PATH}/Contents/MacOS/${MAIN_EXEC_NAME}"

echo "sign-macos: signing with identity: ${MACOS_CODESIGN_IDENTITY}"
echo "sign-macos: main executable: ${MAIN_EXEC_PATH}"

# Detect Mach-O via `file -b` rather than file mode, so we catch dylibs and
# frameworks whose perms aren't executable. Use a newline-separated list:
# paths inside an .app can contain spaces (the bundle name itself does) but
# never newlines in practice, and BSD awk on the macOS runner does not
# support NUL record separators.
MACH_O_LIST="$(mktemp -t leapmux-sign-list)"
trap 'rm -f "${MACH_O_LIST}"' EXIT

while IFS= read -r -d '' f; do
  desc="$(file -b "$f")"
  case "${desc}" in
    *Mach-O*) printf '%s\n' "$f" >> "${MACH_O_LIST}" ;;
  esac
done < <(find "${APP_PATH}" -type f -print0)

sign_one() {
  local target="$1"
  local with_entitlements="$2"
  if [ "${with_entitlements}" = "1" ]; then
    codesign --force --options runtime --timestamp \
      --entitlements "${MACOS_ENTITLEMENTS_PATH}" \
      --sign "${MACOS_CODESIGN_IDENTITY}" \
      "${target}"
  else
    codesign --force --options runtime --timestamp \
      --sign "${MACOS_CODESIGN_IDENTITY}" \
      "${target}"
  fi
}

# Inside-out: sort Mach-O paths by descending depth (slash count) so nested
# binaries/frameworks/dylibs are signed before any parent that contains them.
# awk -F'/' makes NF == depth+1.
awk -F'/' '{ printf "%05d\t%s\n", NF, $0 }' "${MACH_O_LIST}" \
  | sort -k1,1nr \
  | cut -f2- \
  | while IFS= read -r f; do
      if [ "$f" = "${MAIN_EXEC_PATH}" ]; then
        sign_one "$f" 1
      else
        sign_one "$f" 0
      fi
    done

# Sign the outer .app bundle last with the main-binary entitlements. Use
# --deep here so any nested bundles we missed (frameworks etc.) inherit a
# valid signature. Inner Mach-O are already signed above; --deep won't
# re-sign them.
codesign --force --options runtime --timestamp \
  --entitlements "${MACOS_ENTITLEMENTS_PATH}" \
  --sign "${MACOS_CODESIGN_IDENTITY}" \
  "${APP_PATH}"

# Verify the chain. We deliberately do NOT run `spctl` here: this script
# also runs in the local-dev path before notarization, where spctl would
# reject a notarized-but-not-yet-stapled or merely-signed bundle.
codesign --verify --deep --strict --verbose=2 "${APP_PATH}"

echo "sign-macos: done."
