#!/usr/bin/env bash
#
# sync-generated.sh --out SRC DEST [--out SRC DEST ...] [--base DIR] -- GENERATOR
#
# Runs a code generator into a throwaway staging directory, then publishes each
# of its output trees into the committed tree WITHOUT disturbing files that did
# not change. For every `--out SRC DEST` pair it copies new/changed files,
# deletes files DEST no longer has a source for (orphans left behind when a
# proto message, SQL query file, or spinner is removed), and -- crucially --
# leaves byte-identical files untouched so their mtime is preserved.
#
# That mtime stability is the whole point: a byte-identical regenerate touches
# nothing on disk, so a running Vite dev server (which watches frontend/src/**)
# sees no filesystem event and does NOT trigger a full page reload. The old
# `rm -rf DEST` + regenerate-in-place rewrote every file with a fresh mtime and
# forced a hard refresh on every run, even when the output was identical. Orphan
# cleanup (the reason that code deleted first) is preserved here via --delete.
#
# Arguments:
#   --out SRC DEST  Publish $STAGING/SRC into DEST (repeatable). SRC is relative
#                   to the staging dir; DEST is the committed location.
#   --base DIR      Create the staging dir under DIR instead of $TMPDIR. The
#                   sqlc targets pass `--base backend` because `go tool sqlc`
#                   only resolves inside the Go module, so staging must live
#                   under backend/ (the dot-prefixed name is ignored by Go
#                   tooling and git).
#   -- GENERATOR    Everything after `--` is a shell snippet (pass it single
#                   quoted so $STAGING is not expanded early) that generates
#                   into $STAGING, which is exported for it. It runs in a
#                   subshell, so a `cd "$STAGING"` inside it does not leak into
#                   the publish step below.
#
set -euo pipefail

base="${TMPDIR:-/tmp}"
srcs=()
dests=()

while [ "$#" -gt 0 ]; do
  case "$1" in
    --base)
      base="${2:?--base needs a directory}"
      shift 2
      ;;
    --out)
      srcs+=("${2:?--out needs SRC and DEST}")
      dests+=("${3:?--out needs SRC and DEST}")
      shift 3
      ;;
    --)
      shift
      break
      ;;
    *)
      echo "sync-generated.sh: unexpected argument: $1" >&2
      exit 2
      ;;
  esac
done

if [ "${#srcs[@]}" -eq 0 ]; then
  echo "sync-generated.sh: at least one --out SRC DEST is required" >&2
  exit 2
fi
if [ "$#" -eq 0 ]; then
  echo "sync-generated.sh: missing generator command after --" >&2
  exit 2
fi

STAGING="$(mktemp -d "${base%/}/.gen-stage.XXXXXX")"
export STAGING
trap 'rm -rf "$STAGING"' EXIT

# Run the generator in a subshell so any `cd "$STAGING"` it performs (sqlc)
# stays contained and the publish loop still runs from the original directory.
# The snippet is trusted (it comes from the Taskfile), so eval is fine here.
# shellcheck disable=SC2294
( eval "$*" )

# rsync flags:
#   -r        recurse into the tree
#   -c        transfer by content checksum, not size+mtime -- an unchanged file
#             is skipped and keeps its mtime (no watcher event, no reload)
#   --delete  prune files DEST has that the generator no longer produces
for i in "${!srcs[@]}"; do
  src="$STAGING/${srcs[$i]}"
  dest="${dests[$i]}"
  if [ ! -d "$src" ]; then
    echo "sync-generated.sh: generator did not produce $src" >&2
    exit 1
  fi
  mkdir -p "$dest"
  rsync -rc --delete "$src/" "$dest/"
done
