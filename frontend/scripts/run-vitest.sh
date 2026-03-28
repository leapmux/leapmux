#!/usr/bin/env bash
# Disable Node.js v25+ built-in Web Storage to avoid
# "Warning: `--localstorage-file` was provided without a valid path"
# when running tests with jsdom.
NODE_MAJOR=$(node -e 'console.log(process.version.split(".")[0].slice(1))')
if [ "$NODE_MAJOR" -ge 25 ] 2>/dev/null; then
  export NODE_OPTIONS="${NODE_OPTIONS:+$NODE_OPTIONS }--no-experimental-webstorage"
fi

exec vitest "$@"
