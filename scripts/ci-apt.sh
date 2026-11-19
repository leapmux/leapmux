#!/usr/bin/env bash
set -euo pipefail

# APT's inactivity timeout does not stop a transfer that keeps receiving data.
# Limit the time spent fetching indexes and packages as well.
fetch_apt() {
  local deadline="$1"
  shift
  sudo timeout --kill-after=15s "$deadline" apt-get \
    -o Acquire::Retries=2 \
    -o Acquire::http::Timeout=15 \
    -o Acquire::https::Timeout=15 \
    "$@"
}

case "${1-}" in
  update)
    fetch_apt 3m update --error-on=any
    ;;
  install)
    shift
    fetch_apt 10m install --download-only -y --no-install-recommends "$@"
    # Run dpkg outside the download deadline.
    sudo apt-get install --no-download -y --no-install-recommends "$@"
    ;;
  *)
    printf 'Expected update or install with package names.\n' >&2
    exit 2
    ;;
esac
