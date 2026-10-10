#!/usr/bin/env node
import { execFileSync } from 'node:child_process'
/**
 * verify-agent-source-freeze: record and validate the source identity a
 * discovery and report share.
 *
 * `--record <out.json>` freezes the identity: HEAD, every tracked source
 * path with its mode and SHA-256, the set of new and deleted files against a
 * previous identity, and the discovery manifest digest. `--verify` compares a
 * tree against a recorded identity and fails on modified bytes, changed
 * modes, new files, deleted files, or a changed discovery manifest.
 */
import { createHash } from 'node:crypto'
import { readFileSync, statSync, writeFileSync } from 'node:fs'
import { isAbsolute, join } from 'node:path'
import process from 'node:process'

/** The tracked paths of the source tree, sorted, one per line. */
function trackedPaths(cwd) {
  const out = execFileSync('git', ['ls-files'], { cwd, encoding: 'utf8' })
  return out.split('\n').filter(line => line !== '').sort()
}

/** The tracked AND untracked-but-not-ignored paths, so a stray new file fails a freeze. */
function presentPaths(cwd) {
  const out = execFileSync('git', ['ls-files', '--cached', '--others', '--exclude-standard'], { cwd, encoding: 'utf8' })
  return out.split('\n').filter(line => line !== '').sort()
}

function fileIdentity(root, path) {
  const bytes = readFileSync(join(root, path))
  const mode = statSync(join(root, path)).mode & 0o777
  return { path, mode, sha256: createHash('sha256').update(bytes).digest('hex') }
}

export function recordIdentity(cwd, discoveryPath, previous) {
  const head = execFileSync('git', ['rev-parse', 'HEAD'], { cwd, encoding: 'utf8' }).trim()
  const files = trackedPaths(cwd).map(path => fileIdentity(cwd, path))
  const discovery = discoveryPath
    ? { path: discoveryPath, sha256: createHash('sha256').update(readFileSync(discoveryPath)).digest('hex') }
    : undefined
  const identity = { head, files, discovery }
  if (previous) {
    const before = new Map(previous.files.map(file => [file.path, file]))
    identity.newFiles = files.filter(file => !before.has(file.path)).map(file => file.path)
    identity.deletedFiles = [...before.keys()].filter(path => !files.some(file => file.path === path))
  }
  else {
    identity.newFiles = []
    identity.deletedFiles = []
  }
  return identity
}

export function verifyIdentity(cwd, recorded) {
  const failures = []
  const head = execFileSync('git', ['rev-parse', 'HEAD'], { cwd, encoding: 'utf8' }).trim()
  if (head !== recorded.head)
    failures.push(`HEAD moved: the record froze ${recorded.head}, the tree is ${head}`)
  const current = new Map(presentPaths(cwd).map(path => [path, fileIdentity(cwd, path)]))
  const frozen = new Map(recorded.files.map(file => [file.path, file]))
  for (const [path, file] of frozen) {
    const live = current.get(path)
    if (!live) {
      failures.push(`deleted file ${path}`)
      continue
    }
    if (live.sha256 !== file.sha256)
      failures.push(`modified bytes in ${path}`)
    if (live.mode !== file.mode)
      failures.push(`changed mode of ${path} (${file.mode} -> ${live.mode})`)
  }
  for (const path of current.keys()) {
    if (!frozen.has(path))
      failures.push(`new file ${path}`)
  }
  if (recorded.discovery) {
    let digest
    try {
      const manifestPath = isAbsolute(recorded.discovery.path) ? recorded.discovery.path : join(cwd, recorded.discovery.path)
      digest = createHash('sha256').update(readFileSync(manifestPath)).digest('hex')
    }
    catch {
      failures.push(`the discovery manifest ${recorded.discovery.path} is absent since the freeze`)
    }
    if (digest !== undefined && digest !== recorded.discovery.sha256)
      failures.push(`the discovery manifest ${recorded.discovery.path} changed since the freeze`)
  }
  return failures
}

function main(argv) {
  const options = {}
  for (let i = 2; i < argv.length; i++) {
    const [key, value] = argv[i].startsWith('--') ? [argv[i].slice(2), undefined] : ['_arg', argv[i]]
    options[key] = value ?? argv[++i]
  }
  const cwd = options.cwd ?? process.cwd()
  if (options.record) {
    const previous = options.previous ? JSON.parse(readFileSync(options.previous, 'utf8')) : undefined
    const identity = recordIdentity(cwd, options.discovery, previous)
    writeFileSync(options.record, `${JSON.stringify(identity, null, 2)}\n`)
    console.log(`Froze ${identity.files.length} source files at ${identity.head.slice(0, 12)} (${identity.newFiles.length} new, ${identity.deletedFiles.length} deleted).`)
    return
  }
  if (!options.verify) {
    console.error('Usage: verify-agent-source-freeze --record <out.json> [--discovery <manifest>] [--previous <identity.json>] | --verify <identity.json> [--cwd <dir>]')
    process.exitCode = 2
    return
  }
  const recorded = JSON.parse(readFileSync(options.verify, 'utf8'))
  const failures = verifyIdentity(cwd, recorded)
  if (failures.length > 0) {
    console.error(`Agent source freeze failed with ${failures.length} difference(s):`)
    for (const failure of failures.slice(0, 50))
      console.error(`  - ${failure}`)
    if (failures.length > 50)
      console.error(`  ... and ${failures.length - 50} more`)
    process.exitCode = 1
    return
  }
  console.log(`Verified ${recorded.files.length} frozen source files at ${recorded.head.slice(0, 12)}.`)
}

if (process.argv[1] && process.argv[1].endsWith('verify-agent-source-freeze.mjs')) {
  try {
    main(process.argv)
  }
  catch (error) {
    console.error(error.message)
    process.exitCode = 1
  }
}
