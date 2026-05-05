#!/bin/bash
#
# Notarizes a macOS artifact (a .zip wrapping a .app, or a .dmg) using an
# App Store Connect API key, then optionally staples the ticket to the
# original artifact.
#
# Usage: notarize-macos.sh [--no-staple] <artifact>
#
# `--no-staple` is required when the submitted artifact itself can't carry
# a stapled ticket (e.g. a .zip wrapper around a .app). In that case the
# caller is responsible for stapling the underlying .app afterwards.
#
# Env (all required):
#   APPLE_API_KEY_P8_PATH      Filesystem path to the .p8 API key.
#   APPLE_API_KEY_ID           App Store Connect Key ID (10 chars).
#   APPLE_API_KEY_ISSUER_ID    Issuer ID (UUID).

set -euo pipefail

STAPLE=1
if [ "${1:-}" = "--no-staple" ]; then
  STAPLE=0
  shift
fi

if [ "$#" -ne 1 ]; then
  echo "usage: $0 [--no-staple] <artifact>" >&2
  exit 2
fi

ARTIFACT="$1"

if [ ! -e "${ARTIFACT}" ]; then
  echo "notarize-macos: artifact not found: ${ARTIFACT}" >&2
  exit 1
fi

: "${APPLE_API_KEY_P8_PATH:?APPLE_API_KEY_P8_PATH must be set}"
: "${APPLE_API_KEY_ID:?APPLE_API_KEY_ID must be set}"
: "${APPLE_API_KEY_ISSUER_ID:?APPLE_API_KEY_ISSUER_ID must be set}"

if [ ! -f "${APPLE_API_KEY_P8_PATH}" ]; then
  echo "notarize-macos: API key file not found: ${APPLE_API_KEY_P8_PATH}" >&2
  exit 1
fi

echo "notarize-macos: submitting ${ARTIFACT}"

# Capture the submit exit code manually so we can still print the
# notarization log when the submit failed (without `set -e` aborting first).
SUBMIT_RC=0
RESULT="$(
  xcrun notarytool submit "${ARTIFACT}" \
    --key "${APPLE_API_KEY_P8_PATH}" \
    --key-id "${APPLE_API_KEY_ID}" \
    --issuer "${APPLE_API_KEY_ISSUER_ID}" \
    --wait \
    --output-format json
)" || SUBMIT_RC=$?

SUBMISSION_ID="$(printf '%s' "${RESULT}" | jq -r '.id // empty' 2>/dev/null || true)"
STATUS="$(printf '%s' "${RESULT}" | jq -r '.status // empty' 2>/dev/null || true)"

echo "notarize-macos: submission id=${SUBMISSION_ID:-<none>} status=${STATUS:-<none>} rc=${SUBMIT_RC}"

if [ "${SUBMIT_RC}" -ne 0 ] || [ "${STATUS}" != "Accepted" ]; then
  if [ -n "${SUBMISSION_ID}" ]; then
    echo "notarize-macos: fetching notarization log for ${SUBMISSION_ID}" >&2
    xcrun notarytool log "${SUBMISSION_ID}" \
      --key "${APPLE_API_KEY_P8_PATH}" \
      --key-id "${APPLE_API_KEY_ID}" \
      --issuer "${APPLE_API_KEY_ISSUER_ID}" >&2 || true
  fi
  echo "notarize-macos: submit failed (rc=${SUBMIT_RC} status=${STATUS:-<none>})" >&2
  exit 1
fi

if [ "${STAPLE}" -eq 0 ]; then
  echo "notarize-macos: --no-staple set; skipping stapler."
  exit 0
fi

echo "notarize-macos: stapling ${ARTIFACT}"
xcrun stapler staple "${ARTIFACT}"
xcrun stapler validate "${ARTIFACT}"
echo "notarize-macos: done."
