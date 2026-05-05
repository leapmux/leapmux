#!/bin/bash
#
# Notarizes a macOS artifact (a .zip wrapping a .app, a .dmg, or a .zip
# wrapping a CLI binary) using an App Store Connect API key, then
# optionally staples the ticket to the original artifact.
#
# Usage: notarize-macos.sh [--no-staple] <artifact>
#
# `--no-staple` is required when the submitted artifact itself can't carry
# a stapled ticket (e.g. a .zip wrapper around a .app, or a raw Mach-O
# binary which can't be stapled at all). In that case the caller is
# responsible for stapling the underlying .app afterwards (or accepting
# the unstapled state for raw binaries).
#
# Resilience: notarytool's `--wait` combines the upload and the
# server-side poll into one process, so a transient network blip during
# the poll (e.g. NSURLErrorNotConnectedToInternet, code -1009) takes the
# whole pipeline down with `submit failed (rc=1 status=<none>)` even
# though Apple already accepted the upload and notarization is
# proceeding fine on their side. We split the two phases:
#
#   1. `notarytool submit`   — uploads, returns the submission ID.
#                              Retried up to 3x; a duplicate upload on
#                              retry is harmless (Apple just notarizes
#                              both copies and we use the latest ID).
#   2. `notarytool wait <id>` — polls Apple for terminal status. Pure
#                              read; retrying resumes against the same
#                              submission state, no work duplicated.
#
# `stapler staple` also calls Apple's CDN to fetch the ticket, so it
# gets its own retry loop on transient failures.
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

# Auth args reused across every notarytool invocation below.
NOTARY_AUTH=(
  --key "${APPLE_API_KEY_P8_PATH}"
  --key-id "${APPLE_API_KEY_ID}"
  --issuer "${APPLE_API_KEY_ISSUER_ID}"
)

# --- Submit phase -----------------------------------------------------
# Upload the artifact and capture the assigned submission ID. Retried up
# to 3 times with linear backoff. On the rare case where the upload
# succeeded but our process couldn't read the response (network died
# right after Apple acknowledged), the retry creates a duplicate
# submission — Apple notarizes both and we use whichever ID we ended up
# capturing. Harmless at our scale.

echo "notarize-macos: submitting ${ARTIFACT}"
SUBMISSION_ID=""
for attempt in 1 2 3; do
  rc=0
  RESULT="$(
    xcrun notarytool submit "${ARTIFACT}" \
      "${NOTARY_AUTH[@]}" \
      --output-format json
  )" || rc=$?
  SUBMISSION_ID="$(printf '%s' "${RESULT}" | jq -r '.id // empty' 2>/dev/null || true)"
  if [ -n "${SUBMISSION_ID}" ]; then
    echo "notarize-macos: submitted; submission id=${SUBMISSION_ID}"
    break
  fi
  if [ "${attempt}" -lt 3 ]; then
    delay=$((attempt * 30))
    echo "notarize-macos: submit attempt ${attempt} failed (rc=${rc}); retrying in ${delay}s..." >&2
    sleep "${delay}"
  fi
done

if [ -z "${SUBMISSION_ID}" ]; then
  echo "notarize-macos: submit failed after 3 attempts." >&2
  exit 1
fi

# --- Wait phase -------------------------------------------------------
# Poll until the submission reaches a terminal status. Retried up to 5
# times with linear backoff (60s/120s/180s/240s = 10 min total): this is
# where transient network errors during the long Apple-side poll have
# historically shown up, and Apple's notarization queue itself is happy
# to keep going while we sit out the blip.

STATUS=""
for attempt in 1 2 3 4 5; do
  rc=0
  RESULT="$(
    xcrun notarytool wait "${SUBMISSION_ID}" \
      "${NOTARY_AUTH[@]}" \
      --output-format json
  )" || rc=$?
  STATUS="$(printf '%s' "${RESULT}" | jq -r '.status // empty' 2>/dev/null || true)"
  if [ -n "${STATUS}" ]; then
    break
  fi
  if [ "${attempt}" -lt 5 ]; then
    delay=$((attempt * 60))
    echo "notarize-macos: wait attempt ${attempt} failed (rc=${rc}); retrying in ${delay}s..." >&2
    sleep "${delay}"
  fi
done

echo "notarize-macos: submission id=${SUBMISSION_ID} status=${STATUS:-<none>}"

if [ "${STATUS}" != "Accepted" ]; then
  echo "notarize-macos: fetching notarization log for ${SUBMISSION_ID}" >&2
  xcrun notarytool log "${SUBMISSION_ID}" "${NOTARY_AUTH[@]}" >&2 || true
  echo "notarize-macos: notarization failed (status=${STATUS:-<none>})" >&2
  exit 1
fi

if [ "${STAPLE}" -eq 0 ]; then
  echo "notarize-macos: --no-staple set; skipping stapler."
  exit 0
fi

# --- Staple phase -----------------------------------------------------
# stapler fetches the ticket from Apple's CDN, so it gets its own
# transient-network retry budget. Validation is a local operation but
# we keep it inside the success path for symmetry with the original
# script's behaviour.

echo "notarize-macos: stapling ${ARTIFACT}"
stapled=0
for attempt in 1 2 3; do
  if xcrun stapler staple "${ARTIFACT}"; then
    stapled=1
    break
  fi
  if [ "${attempt}" -lt 3 ]; then
    delay=$((attempt * 30))
    echo "notarize-macos: stapler attempt ${attempt} failed; retrying in ${delay}s..." >&2
    sleep "${delay}"
  fi
done

if [ "${stapled}" -ne 1 ]; then
  echo "notarize-macos: stapler failed after 3 attempts." >&2
  exit 1
fi

xcrun stapler validate "${ARTIFACT}"
echo "notarize-macos: done."
