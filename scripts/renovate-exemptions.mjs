import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import process from 'node:process'
import { nextGithubPage, readGithubJsonResponse } from './github-json.mjs'
import { newModulePath } from './go-module-path.mjs'
import { advisoryRange, isInAdvisoryRange, normalizeAdvisoryVersion, patchedIdentifier } from './renovate-advisory-range.mjs'
import { advisoryEcosystem } from './renovate-ecosystems.mjs'
import { renovateModule, requireRenovate } from './renovate-engine.mjs'
import { runGoOutsideModules } from './renovate-go-command.mjs'
import { DEPENDENCY_LOCKFILES, goModJson } from './renovate-lockfile-versions.mjs'
import { CRATES_API, NPM_REGISTRY, readRegistryJson } from './renovate-registry.mjs'

// An authorized root is the exact first patched version that a GitHub-reviewed advisory
// lists for a version that the base locks. Only an active root, which the change newly
// introduces, and its closure of exact pins may skip the minimum release age. No code
// here compares versions with `>=`: a later release is never authorized.
const semver = requireRenovate('semver')
const { api: cargoVersioning } = await renovateModule('modules/versioning/cargo/index.js')
const { api: npmVersioning } = await renovateModule('modules/versioning/npm/index.js')

const repositoryRoot = resolve(import.meta.dirname, '..')

/** Split `name@version`, where a scoped npm name also starts with `@`. */
export function splitIdentity(identity) {
  const split = identity.lastIndexOf('@')
  if (split <= 0 || split === identity.length - 1)
    throw new Error(`Invalid package identity: ${identity}`)
  return {
    name: identity.slice(0, split),
    version: identity.slice(split + 1),
  }
}

// The Go module proxy protocol escapes each uppercase letter as `!` and its lowercase form.
function escapeModulePath(value) {
  return value.replace(/[A-Z]/g, letter => `!${letter.toLowerCase()}`)
}

/**
 * Classify the output of `go list -m -json -e <module>@<version>`. `-e` exits with 0 even
 * on failure, so the JSON Error field decides. Only an answer that states the version is
 * absent is `not-found`. Every other failure, including the generic `invalid version`
 * wrapper around a transport failure, is `error`.
 */
export function classifyGoMetadata(module, version, result) {
  if (result.error || result.status !== 0)
    return { status: 'error', message: result.error?.message ?? (result.stderr?.trim() || `go list exited with ${result.status}`) }
  let info
  try {
    info = JSON.parse(result.stdout)
  }
  catch {
    return { status: 'error', message: `go list returned malformed output: ${String(result.stdout).slice(0, 200)}` }
  }
  if (!info || typeof info !== 'object')
    return { status: 'error', message: 'go list returned no module record.' }
  if (info.Error === undefined) {
    return info.Version === version
      ? { status: 'found', info }
      : { status: 'error', message: `go list resolved ${module}@${version} to ${JSON.stringify(info.Version ?? null)}.` }
  }
  const message = info.Error?.Err
  if (typeof message !== 'string')
    return { status: 'error', message: 'go list returned an error without a message.' }
  const file = `${RegExp.escape(escapeModulePath(module))}/@v/${RegExp.escape(escapeModulePath(version))}`
  const revision = RegExp.escape(version.replace(/\+incompatible$/, ''))
  // Ignore the module path and version, so that their digits cannot match a status code.
  const rest = message.replaceAll(module, '').replaceAll(version, '')
  const transport = /fatal:|\btls\b|connection|timeout|timed out|\b40[13]\b|\b5\d\d\b/i.test(rest)
  const absent = new RegExp(`${file}\\.(?:info|mod): (?:404 Not Found|410 Gone)`).test(message)
    || message.includes('+incompatible suffix not allowed')
    || new RegExp(`${file}\\.info: no such file or directory`).test(message)
    || (new RegExp(`unknown revision ${revision}(?![\\w.+-])`).test(message) && !transport)
  return {
    status: absent ? 'not-found' : 'error',
    message,
  }
}

/** Read Go module metadata through GOPROXY, outside every workspace and module. */
export async function goMetadata(module, version, { checkout = repositoryRoot, env = process.env, run } = {}) {
  return classifyGoMetadata(module, version, await runGoOutsideModules([
    'list',
    '-m',
    '-json',
    '-e',
    `${module}@${version}`,
  ], { checkout, env, run }))
}

/**
 * Return the one published identity of a Go patched version. A module path that already
 * carries the major version has one candidate. Otherwise the major-suffixed path comes
 * first, then the legacy `+incompatible` version. An `error` answer stops the search.
 */
export async function resolveGoIdentity(name, patched, { goMetadata: read = goMetadata } = {}) {
  const normalized = semver.valid(normalizeAdvisoryVersion(patched))
  if (!normalized)
    throw new Error(`Invalid Go patched version for ${name}: ${patched}`)
  const version = `v${normalized}`
  const major = semver.major(normalized)
  const suffix = /\/v(\d+)$/.exec(name)?.[1]
    ?? (name.startsWith('gopkg.in/')
      ? /\.v(\d+)(?:-unstable)?$/.exec(name)?.[1]
      : undefined)
  const carriesMajor = suffix === undefined
    ? major <= 1
    : Number(suffix) === major
  let candidates = [
    { name, version },
  ]
  if (!carriesMajor) {
    candidates = [
      {
        name: newModulePath({ depName: name, newValue: version }),
        version,
      },
      { name, version: `${version}+incompatible` },
    ]
  }
  for (const candidate of candidates) {
    const result = await read(candidate.name, candidate.version)
    if (result.status === 'found')
      return candidate
    if (result.status !== 'not-found')
      throw new Error(`Cannot read Go module ${candidate.name}@${candidate.version}: ${result.message}`)
  }
  throw new Error(`no published module for ${name} ${patched}`)
}

/**
 * Read every GitHub-reviewed advisory that affects one package, following each page once.
 * The read requires a token: the API allows only 60 anonymous requests an hour.
 */
export async function readAdvisories(ecosystem, name, { request = fetch, env = process.env } = {}) {
  const query = new URLSearchParams({
    type: 'reviewed',
    ecosystem: advisoryEcosystem(ecosystem),
    affects: name,
    per_page: '100',
  })
  const token = env.GITHUB_TOKEN || env.GH_TOKEN
  // A cursor link repeats the query and adds its cursor.
  const accept = url => url.pathname === '/advisories' && !url.searchParams.has('page')
    && [...query].every(([key, value]) => url.searchParams.get(key) === value)
    && [
      url.searchParams.get('after'),
      url.searchParams.get('before'),
    ].some(Boolean)
  let path = `/advisories?${query}`
  const advisories = []
  const seen = new Set()
  while (path) {
    if (seen.has(path))
      throw new Error('Advisory pagination cycle detected.')
    seen.add(path)
    const { data: batch, headers } = await readGithubJsonResponse(path, { token, request, description: 'Advisory read' })
    if (!Array.isArray(batch))
      throw new TypeError(`The advisory API returned an invalid list for ${name}.`)
    advisories.push(...batch)
    path = nextGithubPage(headers.get('link'), accept)
  }
  return advisories
}

/**
 * Return the authorized roots of one package: for each reviewed advisory record of the
 * package whose range contains a base version, the record's exact first patched version,
 * in its head identity. A record without a range proves no base version vulnerable, so it
 * authorizes nothing.
 */
export async function authorizedFixes({ ecosystem, name, baseVersions }, { advisories = readAdvisories, goMetadata: readGo = goMetadata } = {}) {
  const githubEcosystem = advisoryEcosystem(ecosystem)
  const bases = [
    ...(baseVersions ?? []),
  ]
  if (bases.length === 0)
    return []
  const patched = new Set()
  for (const advisory of await advisories(ecosystem, name)) {
    if (advisory.withdrawn_at)
      continue
    for (const record of advisory.vulnerabilities ?? []) {
      if (record.package?.name !== name || record.package?.ecosystem !== githubEcosystem)
        continue
      const fixed = patchedIdentifier(record)
      if (fixed === null || advisoryRange(record.vulnerable_version_range) === null || !bases.some(version => isInAdvisoryRange(version, record.vulnerable_version_range)))
        continue
      if (typeof fixed !== 'string' || !semver.valid(normalizeAdvisoryVersion(fixed)))
        throw new Error(`Invalid patched version for ${name}: ${JSON.stringify(fixed)}`)
      patched.add(fixed)
    }
  }
  const roots = []
  for (const fixed of [...patched].sort()) {
    const root = ecosystem === 'go'
      ? await resolveGoIdentity(name, fixed, { goMetadata: readGo })
      : { name, version: fixed }
    if (!roots.some(entry => entry.name === root.name && entry.version === root.version))
      roots.push(root)
  }
  return roots
}

/** Read one crate version's dependency requirements. */
export async function readCrateDependencies(name, version, { request } = {}) {
  const url = `${CRATES_API}/crates/${encodeURIComponent(name)}/${encodeURIComponent(version)}/dependencies`
  const metadata = await readRegistryJson(url, { request, description: 'Crate dependency read' })
  if (!Array.isArray(metadata?.dependencies))
    throw new TypeError(`Invalid crate dependency metadata: ${name}@${version}`)
  return metadata.dependencies
}

/** Read one npm version manifest. */
async function readNpmManifest(name, version, { request } = {}) {
  const manifest = await readRegistryJson(`${NPM_REGISTRY}/${encodeURIComponent(name)}/${encodeURIComponent(version)}`, { request, description: 'npm manifest read' })
  if (!manifest || typeof manifest !== 'object')
    throw new TypeError(`Invalid npm manifest: ${name}@${version}`)
  return manifest
}

/** Read the go.mod file of one published Go module version through GOPROXY. */
async function readGoModText(module, version, options = {}) {
  const result = await goMetadata(module, version, options)
  if (result.status !== 'found' || typeof result.info.GoMod !== 'string')
    throw new Error(`Cannot read the go.mod file of ${module}@${version}: ${result.message ?? 'no file'}`)
  return readFileSync(result.info.GoMod, 'utf8')
}

export const defaultReaders = Object.freeze({
  advisories: readAdvisories,
  goMetadata,
  crateDependencies: readCrateDependencies,
  npmManifest: readNpmManifest,
  goModText: readGoModText,
})

const REQUIREMENT_VERSIONINGS = Object.freeze({ crates: cargoVersioning, npm: npmVersioning })

/**
 * Return `name@version` for a requirement that admits exactly one version, such as
 * `=1.2.3`, or null for any other requirement. `ecosystem` selects Cargo's or npm's syntax.
 */
export function exactIdentity(ecosystem, name, requirement) {
  const versioning = REQUIREMENT_VERSIONINGS[ecosystem]
  if (!versioning)
    throw new Error(`Unknown requirement ecosystem: ${ecosystem}`)
  if (typeof requirement !== 'string' || !versioning.isSingleVersion(requirement))
    return null
  const version = semver.clean(requirement.trim().replace(/^=\s*/, ''))
  if (!version)
    throw new Error(`Invalid exact requirement: ${requirement}`)
  return `${name}@${version}`
}

/** Return the exact requirements of one version, as `name@version`. */
export async function exactPins(ecosystem, name, version, readers = defaultReaders) {
  if (ecosystem === 'crates') {
    return (await readers.crateDependencies(name, version))
      .filter(dependency => ['normal', 'build'].includes(dependency.kind))
      .map(dependency => exactIdentity('crates', dependency.crate_id, dependency.req))
      .filter(identity => identity !== null)
  }
  if (ecosystem === 'npm') {
    const manifest = await readers.npmManifest(name, version)
    // npm lets an optional dependency replace a regular dependency of the same name.
    const requirements = {
      ...manifest.dependencies,
      ...manifest.optionalDependencies,
    }
    return Object.entries(requirements)
      .map(([dependency, requirement]) => exactIdentity('npm', dependency, requirement))
      .filter(identity => identity !== null)
  }
  if (ecosystem === 'go') {
    // MVS treats every requirement of a module as the exact version that the module was built against.
    const module = JSON.parse(await goModJson(await readers.goModText(name, version)))
    return (module.Require ?? []).map(requirement => `${requirement.Path}@${requirement.Version}`)
  }
  throw new Error(`Unknown advisory ecosystem: ${ecosystem}`)
}

/**
 * Return each root with the closure of its exact pins, as
 * `Map<name@version, Set<name@version>>`. All roots must share one ecosystem. A crate or
 * npm closure follows the exact pins of each pin. A Go closure holds the requirements of
 * the root's go.mod only: since Go 1.17, a go.mod lists every module that the packages of
 * its module need, and the module graph prunes the requirements of those modules. A walk
 * through their go.mod files would instead reach every version that any of them ever required.
 */
export async function authorizedClosure(roots, readers = defaultReaders) {
  const ecosystems = new Set(roots.map(entry => entry.ecosystem))
  if (ecosystems.size > 1)
    throw new Error('An authorized closure requires roots of one ecosystem.')
  const closures = new Map()
  for (const entry of roots) {
    const key = `${entry.name}@${entry.version}`
    if (closures.has(key))
      continue
    const seen = new Set([key])
    const queue = [key]
    while (queue.length > 0) {
      const current = splitIdentity(queue.shift())
      for (const pin of await exactPins(entry.ecosystem, current.name, current.version, readers)) {
        if (!seen.has(pin)) {
          seen.add(pin)
          if (entry.ecosystem !== 'go')
            queue.push(pin)
        }
      }
    }
    seen.delete(key)
    closures.set(key, seen)
  }
  return closures
}

/**
 * Return the active roots of one ecosystem of the prepared map (`{ root: [closure] }`) and
 * the identities that they exempt. A root is active when `base` (`Map<name, Set<version>>`)
 * does not lock it and `selected(root)` holds. `selected` runs only for a root that the base
 * does not lock.
 */
export function activeExemptions(authorized, { base, selected } = {}) {
  if (!(base instanceof Map) || typeof selected !== 'function')
    throw new TypeError('Active exemptions require the base versions and a selection.')
  const roots = []
  const exempt = new Set()
  for (const [root, closure] of Object.entries(authorized ?? {})) {
    const { name, version } = splitIdentity(root)
    if (base.get(name)?.has(version) || !selected(root))
      continue
    roots.push(root)
    exempt.add(root)
    for (const pin of closure)
      exempt.add(pin)
  }
  return {
    roots: roots.sort(),
    exempt,
  }
}

/**
 * The lockfiles of the prepared authorized map: those that the bun shim, the cargo shim,
 * and the Tauri planner resolve. Required CI builds the exemptions of every dependency
 * lockfile itself, Go included.
 */
export const PREPARED_LOCKFILES = Object.freeze(DEPENDENCY_LOCKFILES.filter(entry => ['npm', 'crates'].includes(entry.ecosystem)).map(entry => entry.path))

const isPlainObject = value => value !== null && typeof value === 'object' && !Array.isArray(value)

/**
 * Check a prepared authorized map that a JSON file carried: an object that keys the roots
 * of each lockfile of PREPARED_LOCKFILES by its path, with each root, a package identity,
 * mapped to the array of the identities of its closure. Return the map, or fail on the
 * first violation.
 */
export function validateAuthorizedMap(authorized) {
  if (!isPlainObject(authorized))
    throw new TypeError('The authorized map must map each lockfile path to its roots.')
  for (const [path, roots] of Object.entries(authorized)) {
    if (!PREPARED_LOCKFILES.includes(path))
      throw new Error(`The authorized map lists an unexpected lockfile: ${path}`)
    if (!isPlainObject(roots))
      throw new TypeError(`The authorized roots of ${path} must map each root to its closure.`)
    for (const [root, closure] of Object.entries(roots)) {
      splitIdentity(root)
      if (!Array.isArray(closure) || closure.some(pin => typeof pin !== 'string'))
        throw new TypeError(`The authorized closure of ${root} must list package identities.`)
      for (const pin of closure)
        splitIdentity(pin)
    }
  }
  return authorized
}

/**
 * Build the authorized map of the packages, as `{ [lockfile path]: { root: [closure] } }`
 * with one entry for each path of `lockfiles`. Each package is `{ lockfile, name,
 * baseVersions }`, so its fixes come from the base versions of its own lockfile alone. A
 * package whose fixes cannot be read, and a root whose closure cannot be read, authorize
 * nothing. `warn` reports each of them, and the release age then holds their versions as
 * it holds any other.
 */
export async function buildAuthorizedMap(packages, readers = defaultReaders, { lockfiles, warn = message => process.stderr.write(`${message}\n`) } = {}) {
  if (!Array.isArray(lockfiles))
    throw new TypeError('An authorized map requires the paths of its lockfiles.')
  const entries = lockfiles.map(path => DEPENDENCY_LOCKFILES.find(entry => entry.path === path) ?? path)
  const unknown = entries.find(entry => typeof entry === 'string')
  if (unknown !== undefined)
    throw new Error(`Unknown dependency lockfile: ${unknown}`)
  const stray = packages.find(pkg => !lockfiles.includes(pkg.lockfile))
  if (stray)
    throw new Error(`The package ${stray.name} belongs to no lockfile of the map: ${stray.lockfile}`)
  const authorized = Object.fromEntries(lockfiles.map(path => [path, {}]))
  // Two lockfiles of one ecosystem can authorize one root. Read its closure once.
  const closures = new Map(entries.map(entry => [
    entry.ecosystem,
    new Map(),
  ]))
  const closureOf = async (ecosystem, root) => {
    const key = `${root.name}@${root.version}`
    if (!closures.get(ecosystem).has(key)) {
      try {
        closures.get(ecosystem).set(key, [
          ...(await authorizedClosure([
            { ecosystem, ...root },
          ], readers)).get(key),
        ].sort())
      }
      catch (error) {
        warn(`Skipped the authorized fix ${key}: ${error.message}`)
        closures.get(ecosystem).set(key, null)
      }
    }
    return closures.get(ecosystem).get(key)
  }
  for (const entry of entries) {
    for (const pkg of packages.filter(candidate => candidate.lockfile === entry.path)) {
      let roots
      try {
        roots = await authorizedFixes({ ecosystem: entry.ecosystem, name: pkg.name, baseVersions: pkg.baseVersions }, readers)
      }
      catch (error) {
        warn(`Skipped the security fixes of ${pkg.name} in ${entry.path}: ${error.message}`)
        continue
      }
      for (const root of roots) {
        const closure = await closureOf(entry.ecosystem, root)
        if (closure)
          authorized[entry.path][`${root.name}@${root.version}`] = closure
      }
    }
  }
  return authorized
}
