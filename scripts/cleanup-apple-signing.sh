#!/usr/bin/env bash
# Remove the credentials that apple-signing-setup registered before creation.
# A missing keychain or a failed deletion must not prevent API-key removal.
set -euo pipefail
if [ -n "${KEYCHAIN_PATH:-}" ] && [ -e "$KEYCHAIN_PATH" ]; then
    security delete-keychain "$KEYCHAIN_PATH" 2>/dev/null || true
fi
if [ -n "${APPLE_API_KEY_P8_PATH:-}" ]; then
    rm -f "$APPLE_API_KEY_P8_PATH"
fi
