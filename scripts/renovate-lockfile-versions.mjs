import { spawnSync } from 'node:child_process'
import { existsSync, readFileSync } from 'node:fs'
import { posix, resolve } from 'node:path'
import process from 'node:process'
import { ECOSYSTEMS } from './renovate-ecosystems.mjs'
import { renovateModule, requireRenovate } from './renovate-engine.mjs'
import { runGoOutsideModules } from './renovate-go-command.mjs'

// One parser for every lockfile reader. It runs under Node and Bun, so it uses no Bun API.
const JSON5 = requireRenovate('json5')
const semver = requireRenovate('semver')
const { parse: parseToml } = await renovateModule('util/toml.js')

const root = resolve(import.meta.dirname, '..')

const lockfile = (path, ecosystem) => Object.freeze({ path, kind: ECOSYSTEMS[ecosystem].kind, ecosystem })

/**
 * The files that record resolved dependency versions, each with its package-manager
 * kind and the registry ecosystem that publishes its versions.
 */
export const DEPENDENCY_LOCKFILES = Object.freeze([
  lockfile('frontend/bun.lock', 'npm'),
  lockfile('.github/renovate/bun.lock', 'npm'),
  lockfile('site/bun.lock', 'npm'),
  lockfile('desktop/rust/Cargo.lock', 'crates'),
  lockfile('backend/go.mod', 'go'),
  lockfile('desktop/go/go.mod', 'go'),
  lockfile('site/go.mod', 'go'),
])

/**
 * Return the entry of DEPENDENCY_LOCKFILES that records the versions of a dependency
 * file, given as the lockfile or as the manifest beside it, as a Dependabot alert gives
 * it. Return null for any other path.
 */
export function dependencyLockfile(path) {
  return DEPENDENCY_LOCKFILES.find(entry => entry.path === path
    || posix.join(posix.dirname(entry.path), ECOSYSTEMS[entry.ecosystem].manifest) === path) ?? null
}

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
  // Workspace, link, file, git, and tarball sources are not registry versions. A registry
  // version keeps its build metadata, which `semver.valid` drops, so compare it with that part.
  const parsed = semver.parse(version)
  const spelled = parsed && `${parsed.version}${parsed.build.length > 0
    ? `+${parsed.build.join('.')}`
    : ''}`
  return spelled === version
    ? {
        name: identity.slice(0, split),
        version,
      }
    : null
}

/** Run git synchronously with room for a large lockfile in its output. */
export function runGit(args, { cwd }) {
  return spawnSync('git', args, { cwd, encoding: 'utf8', maxBuffer: 64 * 1024 * 1024 })
}

/** Return the text of a file under `directory`, or null when the file is absent. */
export function readWorkingFile(directory, path) {
  const file = resolve(directory, path)
  return existsSync(file)
    ? readFileSync(file, 'utf8')
    : null
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
export async function goModJson(text, { checkout = root, env = process.env, run } = {}) {
  const result = await runGoOutsideModules([
    'mod',
    'edit',
    '-json',
    'go.mod',
  ], {
    checkout,
    env,
    run,
    files: { 'go.mod': text },
  })
  if (result.error || result.status !== 0)
    throw new Error(`go mod edit -json failed: ${result.error?.message ?? result.stderr.trim()}`)
  return result.stdout
}

// Read the registry versions of one parsed input. A gomod input is `go mod edit -json` output.
function versionsOf(kind, text) {
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

/** Return the registry versions that a bun.lock or Cargo.lock text resolves, as `Map<name, Set<version>>`. */
export function lockedVersions(kind, text) {
  if (kind === 'gomod')
    throw new Error('lockedVersions reads bun.lock and Cargo.lock text. Read go.mod through readLockedVersions.')
  return versionsOf(kind, text)
}

/** Return the requirements and the versioned replacements of `go mod edit -json` output. */
export function goModVersions(json) {
  return versionsOf('gomod', json)
}

/**
 * Return the registry versions that one dependency file resolves. A go.mod file runs
 * `go mod edit -json`, so this reader is async. An absent file (null) resolves nothing.
 */
export async function readLockedVersions(kind, text, options = {}) {
  if (text == null)
    return new Map()
  return kind === 'gomod'
    ? goModVersions(await goModJson(text, options))
    : lockedVersions(kind, text)
}

/** Return each version of `Map<name, Set<version>>` as `name@version`. */
export function versionIdentities(versions) {
  return new Set([
    ...versions,
  ].flatMap(([name, set]) => [...set].map(version => `${name}@${version}`)))
}

/** Return each `name@version` of the versions `after` that the versions `before` lack. */
export function introducedVersions(before, after) {
  const known = versionIdentities(before)
  return [
    ...versionIdentities(after),
  ].filter(identity => !known.has(identity)).sort()
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
 * Report each registry crate version of `after` that replaces a removed version of `before`
 * in the same compatibility line with a lower version. Both are Cargo.lock versions, as
 * `Map<name, Set<version>>`. An unchanged duplicate, another line, and an addition beside a
 * kept version are never compared.
 */
export function cargoReplacementDowngrades(before, after) {
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
