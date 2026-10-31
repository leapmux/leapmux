import { spawnSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import process from 'node:process'
import { renovateModule, requireRenovate } from './renovate-engine.mjs'

// One parser for every lockfile reader. It runs under Node and Bun, so it uses no Bun API.
const JSON5 = requireRenovate('json5')
const semver = requireRenovate('semver')
const { parse: parseToml } = await renovateModule('util/toml.js')

const root = resolve(import.meta.dirname, '..')

/**
 * The files that record resolved dependency versions, each with its package-manager
 * kind and the registry ecosystem that publishes its versions.
 */
export const DEPENDENCY_LOCKFILES = Object.freeze([
  Object.freeze({ path: 'frontend/bun.lock', kind: 'bun', ecosystem: 'npm' }),
  Object.freeze({ path: '.github/renovate/bun.lock', kind: 'bun', ecosystem: 'npm' }),
  Object.freeze({ path: 'desktop/rust/Cargo.lock', kind: 'cargo', ecosystem: 'crates' }),
  Object.freeze({ path: 'backend/go.mod', kind: 'gomod', ecosystem: 'go' }),
  Object.freeze({ path: 'desktop/go/go.mod', kind: 'gomod', ecosystem: 'go' }),
  Object.freeze({ path: 'site/go.mod', kind: 'gomod', ecosystem: 'go' }),
])

export function parseBunLock(text) {
  return JSON5.parse(text)
}

export function parseCargoLock(text) {
  return parseToml(text)
}

export function parseCargoManifest(text) {
  return parseToml(text)
}

/** Split a bun.lock identity into a registry name and version, or return null for any other source. */
export function bunIdentity(identity) {
  if (typeof identity !== 'string')
    throw new TypeError('A bun.lock package entry must start with its identity.')
  const split = identity.lastIndexOf('@')
  if (split <= 0)
    return null
  const version = identity.slice(split + 1)
  // Workspace, link, file, git, and tarball sources are not registry versions.
  return semver.valid(version) === version
    ? {
        name: identity.slice(0, split),
        version,
      }
    : null
}

function runGit(args, { cwd }) {
  return spawnSync('git', args, { cwd, encoding: 'utf8', maxBuffer: 64 * 1024 * 1024 })
}

/**
 * Return the text of a file at a git revision, or null when the revision lacks the file.
 * A path that starts with `./` is relative to `cwd`. A missing revision fails.
 */
export function readRevisionFile(revision, path, { cwd = root, git = runGit } = {}) {
  const spec = `${revision}:${path}`
  if (git(['cat-file', '-e', spec], { cwd }).status !== 0) {
    const verified = git([
      'rev-parse',
      '--verify',
      '--quiet',
      `${revision}^{commit}`,
    ], { cwd })
    if (verified.error || verified.status !== 0)
      throw new Error(`Cannot read the git revision ${revision} in ${cwd}.`)
    return null
  }
  const shown = git(['show', spec], { cwd })
  if (shown.error || shown.status !== 0)
    throw new Error(`git show ${spec} failed: ${shown.error?.message ?? shown.stderr.trim()}`)
  return shown.stdout
}

/** Run `go mod edit -json` on go.mod text, which is the input of the gomod reader. */
export function goModJson(text, { checkout = root } = {}) {
  mkdirSync(resolve(checkout, '.tmp'), { recursive: true })
  const directory = mkdtempSync(resolve(checkout, '.tmp/go-mod-json-'))
  try {
    const file = resolve(directory, 'go.mod')
    writeFileSync(file, text)
    const result = spawnSync('go', ['mod', 'edit', '-json', file], {
      cwd: directory,
      encoding: 'utf8',
      env: { ...process.env, GOWORK: 'off', GOTOOLCHAIN: 'local' },
      maxBuffer: 16 * 1024 * 1024,
    })
    if (result.error || result.status !== 0)
      throw new Error(`go mod edit -json failed: ${result.error?.message ?? result.stderr.trim()}`)
    return result.stdout
  }
  finally {
    rmSync(directory, { recursive: true, force: true })
  }
}

/**
 * Return the registry versions that one lockfile resolves, as `Map<name, Set<version>>`.
 * The gomod kind reads `go mod edit -json` output: its requirements and versioned replacements.
 */
export function lockedVersions(kind, text) {
  const versions = new Map()
  const add = (name, version) => {
    if (typeof name !== 'string' || !name || typeof version !== 'string' || !version)
      throw new TypeError(`Invalid ${kind} lockfile entry: ${JSON.stringify([name, version])}`)
    if (!versions.has(name))
      versions.set(name, new Set())
    versions.get(name).add(version)
  }
  if (kind === 'bun') {
    for (const entry of Object.values(parseBunLock(text).packages ?? {})) {
      if (!Array.isArray(entry))
        throw new TypeError('A bun.lock package entry must be an array.')
      const identity = bunIdentity(entry[0])
      if (identity)
        add(identity.name, identity.version)
    }
  }
  else if (kind === 'cargo') {
    for (const pkg of parseCargoLock(text).package ?? []) {
      // Workspace members have no source, and git or path sources are not registry versions.
      if (typeof pkg.source === 'string' && pkg.source.startsWith('registry+'))
        add(pkg.name, pkg.version)
    }
  }
  else if (kind === 'gomod') {
    const module = JSON.parse(text)
    for (const requirement of module.Require ?? [])
      add(requirement.Path, requirement.Version)
    for (const replacement of module.Replace ?? []) {
      // A replacement without a version points at a local directory.
      if (replacement.New?.Version)
        add(replacement.New.Path, replacement.New.Version)
    }
  }
  else {
    throw new Error(`Unknown lockfile kind: ${kind}`)
  }
  return versions
}

/** Return the input of `lockedVersions` for a file's text: gomod reads `go mod edit -json` output. */
export function lockfileInput(kind, text, options = {}) {
  return kind === 'gomod' && text != null
    ? goModJson(text, options)
    : text
}

/**
 * Merge the versions of every dependency lockfile by ecosystem, as
 * `{ npm: Map<name, Set<version>>, crates: ..., go: ... }`. `read(path)` returns the
 * text of a repository file, or null when the file is absent.
 */
export function ecosystemVersions(read, options = {}) {
  const merged = Object.fromEntries(DEPENDENCY_LOCKFILES.map(({ ecosystem }) => [
    ecosystem,
    new Map(),
  ]))
  for (const { path, kind, ecosystem } of DEPENDENCY_LOCKFILES) {
    const text = read(path)
    if (text == null)
      continue
    for (const [name, versions] of lockedVersions(kind, lockfileInput(kind, text, options))) {
      const known = merged[ecosystem].get(name) ?? new Set()
      for (const version of versions)
        known.add(version)
      merged[ecosystem].set(name, known)
    }
  }
  return merged
}

/** Return each `name@version` that `head` locks and `base` does not. An absent base locks nothing. */
export function introducedVersions(kind, base, head) {
  const before = base == null
    ? new Map()
    : lockedVersions(kind, base)
  return [
    ...lockedVersions(kind, head),
  ].flatMap(([name, versions]) => [...versions]
    .filter(version => !before.get(name)?.has(version))
    .map(version => `${name}@${version}`)).sort()
}

/** Return the Cargo compatibility line of a version: `1`, `0.3`, or `0.0.4`. */
export function cargoLine(version) {
  const match = /^(\d+)(?:\.(\d+))?(?:\.(\d+))?(?:[-+].*)?$/.exec(version ?? '')
  if (!match)
    throw new Error(`Invalid Cargo version: ${version}`)
  const [major, minor, patch] = match.slice(1).map(part => Number(part ?? 0))
  return major > 0
    ? `${major}`
    : minor > 0
      ? `0.${minor}`
      : `0.0.${patch}`
}

/**
 * Report each registry crate version that replaces a removed base version of the same
 * compatibility line with a lower version. An unchanged duplicate, another line, and an
 * addition beside a kept version are never compared.
 */
export function cargoReplacementDowngrades(base, head) {
  // An absent base locks nothing, so nothing can be replaced.
  if (base == null)
    return []
  const before = lockedVersions('cargo', base)
  const after = lockedVersions('cargo', head)
  const downgrades = []
  for (const [name, headVersions] of after) {
    const baseVersions = before.get(name)
    if (!baseVersions)
      continue
    const lines = new Set([
      ...baseVersions,
      ...headVersions,
    ].map(cargoLine))
    for (const line of lines) {
      const removed = [...baseVersions].filter(version => cargoLine(version) === line && !headVersions.has(version))
      if (removed.length === 0)
        continue
      const floor = removed.sort(semver.compare).at(-1)
      for (const version of [...headVersions].filter(version => cargoLine(version) === line && !baseVersions.has(version))) {
        if (semver.lt(version, floor))
          downgrades.push({ name, from: floor, to: version })
      }
    }
  }
  return downgrades.sort((a, b) => a.name.localeCompare(b.name) || semver.compare(a.to, b.to))
}
