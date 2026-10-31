import { spawn } from 'node:child_process'
import { mkdirSync, mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { resolve } from 'node:path'
import process from 'node:process'
import { renovateModule, requireRenovate } from './renovate-engine.mjs'
import { goModJson } from './renovate-lockfile-versions.mjs'
import { readRegistryJson, serialThrottle } from './renovate-release-age.mjs'

// An authorized root is the exact first patched version that a GitHub-reviewed advisory
// lists for a version that the base locks. Only an active root, which the change newly
// introduces, and its closure of exact pins may skip the minimum release age. No code
// here compares versions with `>=`: a later release is never authorized.
const semver = requireRenovate('semver')
const { api: cargoVersioning } = await renovateModule('modules/versioning/cargo/index.js')
const { api: npmVersioning } = await renovateModule('modules/versioning/npm/index.js')

const root = resolve(import.meta.dirname, '..')

/** The GitHub advisory ecosystem of each authorized-map ecosystem. */
export const ADVISORY_ECOSYSTEMS = Object.freeze({ npm: 'npm', crates: 'rust', go: 'go' })
export const AUTHORIZED_ECOSYSTEMS = Object.freeze(Object.keys(ADVISORY_ECOSYSTEMS))

/** Return the authorized-map ecosystem of a GitHub advisory ecosystem, or null for an unmanaged one. */
export function authorizedEcosystem(advisoryEcosystem) {
  return AUTHORIZED_ECOSYSTEMS.find(ecosystem => ADVISORY_ECOSYSTEMS[ecosystem] === advisoryEcosystem) ?? null
}

/** Remove the Go spelling of a version, so that a semver range can match it. */
export function normalizeAdvisoryVersion(version) {
  return String(version).replace(/^v/, '').replace(/\+incompatible$/, '')
}

/** Match a version against an advisory range, including prereleases such as Go pseudo-versions. */
export function isInAdvisoryRange(version, range) {
  if (typeof range !== 'string')
    throw new TypeError('An advisory record requires a vulnerable version range.')
  const normalized = range.replaceAll(',', ' ')
  if (!semver.validRange(normalized))
    throw new Error(`Invalid advisory range: ${range}`)
  const candidate = semver.valid(normalizeAdvisoryVersion(version))
  return candidate !== null && semver.satisfies(candidate, normalized, { includePrerelease: true })
}

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

// Run a command without blocking the event loop, so that an in-process server can answer it.
function runCommand(command, args, options) {
  return new Promise((settle) => {
    let stdout = ''
    let stderr = ''
    const child = spawn(command, args, {
      ...options,
      stdio: ['ignore', 'pipe', 'pipe'],
    })
    child.stdout.setEncoding('utf8').on('data', (data) => {
      stdout += data
    })
    child.stderr.setEncoding('utf8').on('data', (data) => {
      stderr += data
    })
    child.once('error', error => settle({ error, status: null, stdout, stderr }))
    child.once('close', status => settle({ status, stdout, stderr }))
  })
}

/** Read Go module metadata through GOPROXY, outside every workspace and module. */
export async function goMetadata(module, version, { checkout = root, env = process.env, run = runCommand } = {}) {
  mkdirSync(resolve(checkout, '.tmp'), { recursive: true })
  const directory = mkdtempSync(resolve(checkout, '.tmp/go-metadata-'))
  try {
    return classifyGoMetadata(module, version, await run('go', [
      'list',
      '-m',
      '-json',
      '-e',
      `${module}@${version}`,
    ], {
      cwd: directory,
      env: { ...env, GOWORK: 'off', GOTOOLCHAIN: 'local' },
    }))
  }
  finally {
    rmSync(directory, { recursive: true, force: true })
  }
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
    // renovate-go.mjs loads Renovate's configuration modules, which only this path needs.
    const { newModulePath } = await import('./renovate-go.mjs')
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

const advisoryCache = new Map()

function nextAdvisoryPage(header) {
  for (const part of header?.split(/,\s*(?=<)/) ?? []) {
    const entry = /^\s*<([^>]+)>\s*;(.*)$/.exec(part)
    if (!entry || !/\brel\s*=\s*"?next"?/.test(entry[2]))
      continue
    const url = new URL(entry[1])
    if (url.origin !== 'https://api.github.com' || url.pathname !== '/advisories' || url.username || url.password)
      throw new Error(`Unsafe advisory pagination link: ${entry[1]}`)
    return url.href
  }
  return null
}

/** Read every GitHub-reviewed advisory that affects one package, following each page once. */
export function readAdvisories(ecosystem, name, { request = fetch, env = process.env } = {}) {
  const advisoryEcosystem = ADVISORY_ECOSYSTEMS[ecosystem]
  if (!advisoryEcosystem)
    throw new Error(`Unknown advisory ecosystem: ${ecosystem}`)
  const key = `${advisoryEcosystem}\0${name}`
  if (!advisoryCache.has(key)) {
    const read = (async () => {
      const token = env.GITHUB_TOKEN || env.GH_TOKEN
      const query = new URLSearchParams({
        type: 'reviewed',
        ecosystem: advisoryEcosystem,
        affects: name,
        per_page: '100',
      })
      let url = `https://api.github.com/advisories?${query}`
      const advisories = []
      const seen = new Set()
      while (url) {
        if (seen.has(url))
          throw new Error('Advisory pagination cycle detected.')
        seen.add(url)
        const response = await request(url, {
          method: 'GET',
          signal: AbortSignal.timeout(30000),
          headers: {
            'Accept': 'application/vnd.github+json',
            'X-GitHub-Api-Version': '2022-11-28',
            'User-Agent': 'leapmux-dependency-maintenance',
            ...(token
              ? { Authorization: `Bearer ${token}` }
              : {}),
          },
        })
        if (!response.ok)
          throw new Error(`Advisory read failed: ${response.status} ${url}`)
        const batch = await response.json()
        if (!Array.isArray(batch))
          throw new TypeError(`The advisory API returned an invalid list for ${name}.`)
        advisories.push(...batch)
        url = nextAdvisoryPage(response.headers.get('link'))
      }
      return advisories
    })()
    // A failed read must not stay cached for a later caller.
    read.catch(() => advisoryCache.delete(key))
    advisoryCache.set(key, read)
  }
  return advisoryCache.get(key)
}

function patchedIdentifier(record) {
  const value = record.first_patched_version
  return value !== null && typeof value === 'object'
    ? value.identifier ?? null
    : value ?? null
}

/**
 * Return the authorized roots of one package: for each reviewed advisory record of the
 * package whose range contains a base version, the record's exact first patched version,
 * in its head identity.
 */
export async function authorizedFixes({ ecosystem, name, baseVersions }, { advisories = readAdvisories, goMetadata: readGo = goMetadata } = {}) {
  const advisoryEcosystem = ADVISORY_ECOSYSTEMS[ecosystem]
  if (!advisoryEcosystem)
    throw new Error(`Unknown advisory ecosystem: ${ecosystem}`)
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
      if (record.package?.name !== name || record.package?.ecosystem !== advisoryEcosystem)
        continue
      const fixed = patchedIdentifier(record)
      if (fixed === null || !bases.some(version => isInAdvisoryRange(version, record.vulnerable_version_range)))
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

const crateRequests = serialThrottle(1000)

/** Read one crate version's dependency requirements. crates.io allows one request per second. */
export async function readCrateDependencies(name, version, { request = fetch } = {}) {
  const url = `https://crates.io/api/v1/crates/${encodeURIComponent(name)}/${encodeURIComponent(version)}/dependencies`
  const metadata = await crateRequests(() => readRegistryJson(url, { request, description: 'Crate dependency read' }))
  if (!Array.isArray(metadata?.dependencies))
    throw new TypeError(`Invalid crate dependency metadata: ${name}@${version}`)
  return metadata.dependencies
}

/** Read one npm version manifest. */
export async function readNpmManifest(name, version, { request = fetch } = {}) {
  const manifest = await readRegistryJson(`https://registry.npmjs.org/${encodeURIComponent(name)}/${encodeURIComponent(version)}`, { request, description: 'npm manifest read' })
  if (!manifest || typeof manifest !== 'object')
    throw new TypeError(`Invalid npm manifest: ${name}@${version}`)
  return manifest
}

/** Read the go.mod file of one published Go module version through GOPROXY. */
export async function readGoModText(module, version, options = {}) {
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

function exactVersion(requirement) {
  const version = semver.clean(requirement.trim().replace(/^=\s*/, ''))
  if (!version)
    throw new Error(`Invalid exact requirement: ${requirement}`)
  return version
}

/** Return the exact requirements of one version, as `name@version`. */
export async function exactPins(ecosystem, name, version, readers = defaultReaders) {
  if (ecosystem === 'crates') {
    return (await readers.crateDependencies(name, version))
      .filter(dependency => ['normal', 'build'].includes(dependency.kind) && typeof dependency.req === 'string' && cargoVersioning.isSingleVersion(dependency.req))
      .map(dependency => `${dependency.crate_id}@${exactVersion(dependency.req)}`)
  }
  if (ecosystem === 'npm') {
    const manifest = await readers.npmManifest(name, version)
    // npm lets an optional dependency replace a regular dependency of the same name.
    const requirements = {
      ...manifest.dependencies,
      ...manifest.optionalDependencies,
    }
    return Object.entries(requirements)
      .filter(([, requirement]) => typeof requirement === 'string' && npmVersioning.isSingleVersion(requirement))
      .map(([dependency, requirement]) => `${dependency}@${exactVersion(requirement)}`)
  }
  if (ecosystem === 'go') {
    // MVS treats every requirement of a module as the exact version that the module was built against.
    const module = JSON.parse(goModJson(await readers.goModText(name, version)))
    return (module.Require ?? []).map(requirement => `${requirement.Path}@${requirement.Version}`)
  }
  throw new Error(`Unknown advisory ecosystem: ${ecosystem}`)
}

/**
 * Return each root with the transitive closure of its exact pins, as
 * `Map<name@version, Set<name@version>>`. All roots must share one ecosystem.
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
          queue.push(pin)
        }
      }
    }
    seen.delete(key)
    closures.set(key, seen)
  }
  return closures
}

/** Return the active roots and their closures as one set of `name@version`. */
export function activeExemptions(authorizedMap, isActive) {
  const entries = authorizedMap instanceof Map
    ? [...authorizedMap]
    : Object.entries(authorizedMap ?? {})
  const exempt = new Set()
  for (const [key, closure] of entries) {
    if (!isActive(key))
      continue
    exempt.add(key)
    for (const pin of closure)
      exempt.add(pin)
  }
  return exempt
}

/**
 * Merge the roots of some ecosystems of a prepared authorized map into one
 * `Map<root, Set<closure>>`. An identity that two ecosystems share is ambiguous.
 */
export function authorizedRootMap(authorized, ecosystems = AUTHORIZED_ECOSYSTEMS) {
  const roots = new Map()
  for (const ecosystem of ecosystems) {
    for (const [root, closure] of Object.entries(authorized?.[ecosystem] ?? {})) {
      if (!Array.isArray(closure) || closure.some(pin => typeof pin !== 'string'))
        throw new TypeError(`The authorized closure of ${root} must list package identities.`)
      if (roots.has(root))
        throw new Error(`Two ecosystems authorize the same identity: ${root}`)
      roots.set(root, new Set(closure))
    }
  }
  return roots
}

/**
 * Build the authorized map of the prepared file for packages with their base versions:
 * `{ npm: { root: [closure] }, crates: {...}, go: {...} }`.
 */
export async function buildAuthorizedMap(packages, readers = defaultReaders) {
  const authorized = Object.fromEntries(AUTHORIZED_ECOSYSTEMS.map(ecosystem => [ecosystem, {}]))
  for (const ecosystem of AUTHORIZED_ECOSYSTEMS) {
    const roots = []
    for (const entry of packages.filter(candidate => candidate.ecosystem === ecosystem)) {
      for (const fixed of await authorizedFixes(entry, readers))
        roots.push({ ecosystem, ...fixed })
    }
    for (const [key, closure] of await authorizedClosure(roots, readers))
      authorized[ecosystem][key] = [...closure].sort()
  }
  return authorized
}
