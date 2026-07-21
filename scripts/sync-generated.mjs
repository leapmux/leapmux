#!/usr/bin/env bun
// sync-generated.mjs — Run a code generator into a throwaway staging directory,
// then publish its output trees into the committed tree WITHOUT disturbing files
// that did not change.
//
// Usage:
//   bun scripts/sync-generated.mjs \
//     [--base DIR] [--cwd-staging] \
//     [--copy SRC DEST]... \
//     --out SRC DEST [--out SRC DEST]... \
//     [-- GENERATOR ARG...]
//
// For every `--out SRC DEST` pair it copies new/changed files, deletes files
// DEST no longer has a source for (orphans left behind when a proto message, SQL
// query file, or spinner is removed), and -- crucially -- leaves byte-identical
// files untouched so their mtime is preserved.
//
// That mtime stability is the whole point: a byte-identical regenerate touches
// nothing on disk, so a running Vite dev server (which watches frontend/src/**)
// sees no filesystem event and does NOT trigger a full page reload. Deleting the
// dest and regenerating in place would rewrite every file with a fresh mtime and
// force a hard refresh on every run, even when the output was identical.
//
// This is the cross-platform (Windows-friendly) replacement for the old
// sync-generated.sh: no mktemp, rsync, cp, or cd -- all of it is done with Node
// filesystem APIs, and the generator is spawned directly (no shell), so there is
// nothing here that only exists on Unix.
//
// Flags:
//   --base DIR      Create the staging dir under DIR instead of the OS temp dir.
//                   The sqlc targets pass `--base backend` because `go tool sqlc`
//                   only resolves inside the Go module, so staging must live
//                   under backend/ (the dot-prefixed name is ignored by Go
//                   tooling and git).
//   --cwd-staging   Run the generator with its working directory set to the
//                   staging dir (sqlc reads sqlc.yaml from cwd and writes its
//                   relative `out:` there).
//   --copy SRC DEST Copy SRC (file or directory, recursively) into STAGING/DEST
//                   before running the generator. Repeatable. Used to stage a
//                   generator's inputs (sqlc.yaml + db) or, when there is no
//                   generator, the material to publish (spinner JSON).
//   --out SRC DEST  Publish STAGING/SRC into DEST (repeatable). SRC is relative
//                   to the staging dir; DEST is the committed location.
//   -- GENERATOR    Everything after `--` is the generator command and its args,
//                   spawned directly (no shell). The literal token `{STAGING}`
//                   in any argument is replaced with the staging path. Optional:
//                   omit it to only stage (via --copy) and publish.

import process from 'node:process'
import { spawnSync } from 'node:child_process'
import { cpSync, existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, statSync, copyFileSync } from 'node:fs'
import { delimiter, dirname, join } from 'node:path'
import { tmpdir } from 'node:os'

// ---------------------------------------------------------------------------
// Argument parsing
// ---------------------------------------------------------------------------

function usage(message) {
  process.stderr.write(`sync-generated: ${message}\n`)
  process.exit(2)
}

/** @type {{base: string, cwdStaging: boolean, copies: Array<{src: string, dest: string}>, outs: Array<{src: string, dest: string}>, generator: string[]}} */
const opts = {
  base: tmpdir(),
  cwdStaging: false,
  copies: [],
  outs: [],
  generator: [],
}

const argv = process.argv.slice(2)
for (let i = 0; i < argv.length; i++) {
  const arg = argv[i]
  switch (arg) {
    case '--base':
      opts.base = argv[++i] ?? usage('--base needs a directory')
      break
    case '--cwd-staging':
      opts.cwdStaging = true
      break
    case '--copy': {
      const src = argv[++i]
      const dest = argv[++i]
      if (src == null || dest == null) usage('--copy needs SRC and DEST')
      opts.copies.push({ src, dest })
      break
    }
    case '--out': {
      const src = argv[++i]
      const dest = argv[++i]
      if (src == null || dest == null) usage('--out needs SRC and DEST')
      opts.outs.push({ src, dest })
      break
    }
    case '--':
      opts.generator = argv.slice(i + 1)
      i = argv.length
      break
    default:
      usage(`unexpected argument: ${arg}`)
  }
}

if (opts.outs.length === 0) usage('at least one --out SRC DEST is required')

// ---------------------------------------------------------------------------
// Publish: the `rsync -rc --delete` equivalent
// ---------------------------------------------------------------------------

/** True if both paths are files with identical bytes. */
function sameContent(a, b) {
  const sb = statSync(b, { throwIfNoEntry: false })
  if (!sb || !sb.isFile()) return false
  if (statSync(a).size !== sb.size) return false
  return readFileSync(a).equals(readFileSync(b))
}

/** Copy src -> dest only when the bytes differ, so unchanged files keep mtime. */
function copyIfChanged(src, dest) {
  const existing = statSync(dest, { throwIfNoEntry: false })
  if (existing && !existing.isFile()) {
    // Dest is a directory where a file now belongs -- replace it.
    rmSync(dest, { recursive: true, force: true })
  } else if (existing && sameContent(src, dest)) {
    return
  }
  copyFileSync(src, dest)
}

/**
 * Recursively make destDir match srcDir by content: copy new/changed files,
 * leave byte-identical files untouched (preserving mtime), and delete any
 * entry destDir has that srcDir does not (orphan prune).
 */
function syncTree(srcDir, destDir) {
  mkdirSync(destDir, { recursive: true })

  const srcEntries = readdirSync(srcDir, { withFileTypes: true })
  const srcNames = new Set(srcEntries.map(e => e.name))

  // Prune orphans first.
  for (const entry of readdirSync(destDir, { withFileTypes: true })) {
    if (!srcNames.has(entry.name)) {
      rmSync(join(destDir, entry.name), { recursive: true, force: true })
    }
  }

  for (const entry of srcEntries) {
    const src = join(srcDir, entry.name)
    const dest = join(destDir, entry.name)
    if (entry.isDirectory()) {
      const existing = statSync(dest, { throwIfNoEntry: false })
      if (existing && !existing.isDirectory()) rmSync(dest, { force: true })
      syncTree(src, dest)
    } else if (entry.isFile()) {
      copyIfChanged(src, dest)
    }
  }
}

// ---------------------------------------------------------------------------
// Generator execution
// ---------------------------------------------------------------------------

/**
 * Resolve a bare command name to a full path on PATH so spawnSync can run it
 * without a shell. On Windows this appends the PATHEXT extensions (.EXE, ...).
 * A command that already contains a path separator is returned as-is.
 */
function resolveExecutable(cmd) {
  if (cmd.includes('/') || cmd.includes('\\')) return cmd
  const exts = process.platform === 'win32'
    ? (process.env.PATHEXT ?? '.COM;.EXE;.BAT;.CMD').split(';').filter(Boolean)
    : ['']
  for (const dir of (process.env.PATH ?? '').split(delimiter).filter(Boolean)) {
    for (const ext of exts) {
      const candidate = join(dir, cmd + ext)
      if (statSync(candidate, { throwIfNoEntry: false })?.isFile()) return candidate
    }
  }
  return cmd // Fall back and let spawn surface a clear ENOENT.
}

function runGenerator(staging) {
  const [cmd, ...rest] = opts.generator
  const args = rest.map(a => a.replaceAll('{STAGING}', staging))
  const result = spawnSync(resolveExecutable(cmd), args, {
    cwd: opts.cwdStaging ? staging : process.cwd(),
    stdio: 'inherit',
    env: { ...process.env, STAGING: staging },
  })
  if (result.error) {
    process.stderr.write(`sync-generated: failed to run generator "${cmd}": ${result.error.message}\n`)
    process.exit(1)
  }
  if (result.status !== 0) {
    process.exit(result.status ?? 1)
  }
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

mkdirSync(opts.base, { recursive: true })
const staging = mkdtempSync(join(opts.base, '.gen-stage-'))

try {
  for (const { src, dest } of opts.copies) {
    const target = join(staging, dest)
    mkdirSync(dirname(target), { recursive: true })
    cpSync(src, target, { recursive: true })
  }

  if (opts.generator.length > 0) runGenerator(staging)

  for (const { src, dest } of opts.outs) {
    const source = join(staging, src)
    if (!existsSync(source)) {
      process.stderr.write(`sync-generated: generator did not produce ${source}\n`)
      process.exit(1)
    }
    syncTree(source, dest)
  }
} finally {
  rmSync(staging, { recursive: true, force: true })
}
