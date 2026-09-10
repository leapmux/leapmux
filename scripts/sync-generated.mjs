#!/usr/bin/env bun
// Run a generator in a private staging directory and publish its output.
// Copy changed files and remove outputs that no longer have a source.
// Identical files retain their modification times, so Vite does not reload unchanged sources.
// Node filesystem APIs and direct process arguments keep this script portable across platforms.
//
// Usage:
//   bun scripts/sync-generated.mjs \
//     [--base DIR] [--cwd-staging] \
//     [--copy SRC DEST]... \
//     --out SRC DEST [--out SRC DEST]... \
//     [-- GENERATOR ARG...]
//
// Flags:
//   --base DIR      Create staging below DIR. The default is the project's .tmp directory.
//                   SQL generation uses backend/ because go tool resolves sqlc inside its module.
//                   The staging directory starts with a dot, so Go and Git ignore it.
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

import { spawnSync } from 'node:child_process'
import { copyFileSync, cpSync, existsSync, lstatSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, statSync } from 'node:fs'
import { delimiter, dirname, join, resolve } from 'node:path'
import process from 'node:process'

// ---------------------------------------------------------------------------
// Argument parsing
// ---------------------------------------------------------------------------

function usage(message) {
  process.stderr.write(`sync-generated: ${message}\n`)
  process.exit(2)
}

/** @type {{base: string, cwdStaging: boolean, copies: Array<{src: string, dest: string}>, outs: Array<{src: string, dest: string}>, generator: string[]}} */
const opts = {
  base: resolve(import.meta.dirname, '..', '.tmp'),
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
      opts.base = resolve(argv[++i] ?? usage('--base needs a directory'))
      break
    case '--cwd-staging':
      opts.cwdStaging = true
      break
    case '--copy': {
      const src = argv[++i]
      const dest = argv[++i]
      if (src == null || dest == null)
        usage('--copy needs SRC and DEST')
      opts.copies.push({ src, dest })
      break
    }
    case '--out': {
      const src = argv[++i]
      const dest = argv[++i]
      if (src == null || dest == null)
        usage('--out needs SRC and DEST')
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

if (opts.outs.length === 0)
  usage('at least one --out SRC DEST is required')

// ---------------------------------------------------------------------------
// Publish: the `rsync -rc --delete` equivalent
// ---------------------------------------------------------------------------

/** True if both paths are files with identical bytes. */
function sameContent(a, b) {
  const sb = statSync(b, { throwIfNoEntry: false })
  if (!sb || !sb.isFile())
    return false
  if (statSync(a).size !== sb.size)
    return false
  return readFileSync(a).equals(readFileSync(b))
}

/** Copy src -> dest only when the bytes differ, so unchanged files keep mtime. */
function copyIfChanged(src, dest) {
  const existing = lstatSync(dest, { throwIfNoEntry: false })
  if (existing && !existing.isFile()) {
    // Replace a directory or symlink where a regular file belongs.
    rmSync(dest, { recursive: true, force: true })
  }
  else if (existing && sameContent(src, dest)) {
    return
  }
  copyFileSync(src, dest)
}

/**
 * Make destDir match srcDir recursively.
 * Copy changed files and remove destination entries that the source no longer contains.
 * Identical files retain their modification times.
 */
function syncTree(srcDir, destDir) {
  const existing = lstatSync(destDir, { throwIfNoEntry: false })
  if (existing && !existing.isDirectory())
    rmSync(destDir, { recursive: true, force: true })
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
      const existing = lstatSync(dest, { throwIfNoEntry: false })
      if (existing && !existing.isDirectory())
        rmSync(dest, { force: true })
      syncTree(src, dest)
    }
    else if (entry.isFile()) {
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
  if (cmd.includes('/') || cmd.includes('\\'))
    return cmd
  const exts = process.platform === 'win32'
    ? (process.env.PATHEXT ?? '.COM;.EXE;.BAT;.CMD').split(';').filter(Boolean)
    : ['']
  for (const dir of (process.env.PATH ?? '').split(delimiter).filter(Boolean)) {
    for (const ext of exts) {
      const candidate = join(dir, cmd + ext)
      if (statSync(candidate, { throwIfNoEntry: false })?.isFile())
        return candidate
    }
  }
  return cmd // Let spawn report ENOENT when no executable matches.
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
    process.exitCode = 1
    return false
  }
  if (result.status !== 0) {
    process.exitCode = result.status ?? 1
    return false
  }
  return true
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

// Validate every source before publication. A missing output must not publish only part of a generation.
function validateTree(path) {
  if (!lstatSync(path).isDirectory())
    throw new Error(`Generator output is not a directory: ${path}`)
  for (const entry of readdirSync(path, { withFileTypes: true })) {
    if (entry.isDirectory())
      validateTree(join(path, entry.name))
    else if (!entry.isFile())
      throw new Error(`Generator output is not a regular file: ${join(path, entry.name)}`)
  }
}

function main() {
  mkdirSync(opts.base, { recursive: true })
  const staging = mkdtempSync(join(opts.base, '.gen-stage-'))

  try {
    for (const { src, dest } of opts.copies) {
      const target = join(staging, dest)
      mkdirSync(dirname(target), { recursive: true })
      cpSync(src, target, { recursive: true })
    }

    if (opts.generator.length > 0 && !runGenerator(staging))
      return

    for (const { src } of opts.outs) {
      const source = join(staging, src)
      if (!existsSync(source))
        throw new Error(`Generator did not produce ${source}`)
      validateTree(source)
    }
    for (const { src, dest } of opts.outs)
      syncTree(join(staging, src), dest)
  }
  finally {
    rmSync(staging, { recursive: true, force: true })
  }
}

try {
  main()
}
catch (error) {
  process.stderr.write(`sync-generated: ${error.message}\n`)
  process.exitCode = 1
}
