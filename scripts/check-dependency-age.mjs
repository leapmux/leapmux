#!/usr/bin/env bun
import { resolve } from 'node:path'
import process from 'node:process'
import { parseArgs } from 'node:util'
import { alertedPackages, readAlerts } from './dependabot-alerts.mjs'
import { activeExemptions, buildAuthorizedMap, defaultReaders, splitIdentity } from './renovate-exemptions.mjs'
import { runGoOutsideModules } from './renovate-go-command.mjs'
import { cargoReplacementDowngrades, DEPENDENCY_LOCKFILES, introducedVersions, readLockedVersions, readRevisionFile } from './renovate-lockfile-versions.mjs'
import { readCrateReleases, readNpmReleases } from './renovate-registry.mjs'
import { describeAge, isPublishedBy, releaseAgeMs } from './renovate-release-age.mjs'

// Renovate checks the release age only for the version that it selects. bun install and
// cargo update resolve transitive versions themselves, and a person can run them too, so
// Required CI checks every version that a pull request adds to a lockfile. Renovate's own
// selection holds a pin in versions.env or in a workflow to the age. A security fix of a
// workflow pin skips the routine age, and nothing here checks such a pin.

/** The branches that Renovate's security configuration creates; only these may exempt a fix. */
export const SECURITY_BRANCHES = Object.freeze(['renovate/security', 'renovate/go-security'])

const repositoryRoot = resolve(import.meta.dirname, '..')

/** Split the concatenated JSON objects that `go list -json` prints. */
export function parseJsonStream(text) {
  const values = []
  let depth = 0
  let start = -1
  let inString = false
  let escaped = false
  for (let index = 0; index < text.length; index++) {
    const character = text[index]
    if (inString) {
      if (escaped)
        escaped = false
      else if (character === '\\')
        escaped = true
      else if (character === '"')
        inString = false
    }
    else if (character === '"') {
      inString = true
    }
    else if (character === '{') {
      if (depth++ === 0)
        start = index
    }
    else if (character === '}' && --depth === 0) {
      values.push(JSON.parse(text.slice(start, index + 1)))
    }
  }
  if (depth !== 0 || inString)
    throw new Error('go list returned truncated JSON.')
  return values
}

/** Read the publication times of Go module versions with one `go list` call through GOPROXY. */
export async function readGoTimes(identities, { checkout = repositoryRoot, env = process.env, run } = {}) {
  const times = new Map()
  if (identities.length === 0)
    return times
  const result = await runGoOutsideModules([
    'list',
    '-m',
    '-json',
    '-e',
    ...identities,
  ], { checkout, env, run })
  if (result.error || result.status !== 0)
    throw new Error(`go list failed: ${result.error?.message ?? result.stderr.trim()}`)
  for (const module of parseJsonStream(result.stdout)) {
    if (!module.Error && typeof module.Time === 'string')
      times.set(`${module.Path}@${module.Version}`, module.Time)
  }
  return times
}

async function mapLimited(items, limit, task) {
  const results = Array.from({ length: items.length })
  let next = 0
  const worker = async () => {
    while (next < items.length) {
      const index = next++
      results[index] = await task(items[index])
    }
  }
  await Promise.all(Array.from({
    length: Math.min(limit, items.length),
  }, worker))
  return results
}

/**
 * Return the publication-time readers of one check. Each set reads each npm packument and
 * each crate index file once, and keeps only the release times. `request` replaces the
 * registry transport, as a test does.
 */
export function timeReaders({ request } = {}) {
  const timeOf = (read) => {
    const releases = new Map()
    return async (name, version) => {
      if (!releases.has(name))
        releases.set(name, read(name, { request }))
      return (await releases.get(name)).find(release => release.version === version)?.publishedAt ?? null
    }
  }
  return Object.freeze({
    npmTime: timeOf(readNpmReleases),
    crateTime: timeOf(readCrateReleases),
    goTimes: readGoTimes,
  })
}

/**
 * Read the publication time of each `{ ecosystem, identity }` entry, in the order of
 * `entries`. A version without a known time reads as null.
 */
export async function publicationTimes(entries, times = timeReaders(), { checkout = repositoryRoot } = {}) {
  const goIdentities = [
    ...new Set(entries.filter(entry => entry.ecosystem === 'go').map(entry => entry.identity)),
  ]
  const goTimes = goIdentities.length === 0
    ? new Map()
    : await times.goTimes(goIdentities, { checkout })
  return mapLimited(entries, 8, async (entry) => {
    if (entry.ecosystem === 'go')
      return goTimes.get(entry.identity) ?? null
    const { name, version } = splitIdentity(entry.identity)
    return entry.ecosystem === 'npm'
      ? times.npmTime(name, version)
      : times.crateTime(name, version)
  })
}

/**
 * Return the identities that a security branch exempts, by the path of each changed
 * lockfile, as `Map<path, Set<name@version>>`: each active authorized root that a lockfile
 * introduces, with its closure. Only a package that an open alert reports on that lockfile
 * may take a fix, and its fixes come from its base versions in that lockfile alone. A
 * package that the lockfile adds, removes, or moves counts as changed.
 */
export async function securityExemptions(files, { alerts = [], readers = defaultReaders, warn } = {}) {
  const alerted = alertedPackages(alerts)
  const packages = []
  for (const file of files) {
    const reported = alerted.get(file.path)
    if (!reported)
      continue
    const changed = new Set([...file.baseVersions].filter(([name, versions]) => [...versions].some(version => !file.headVersions.get(name)?.has(version))).map(([name]) => name))
    for (const identity of file.introduced)
      changed.add(splitIdentity(identity).name)
    for (const name of [...changed].filter(candidate => reported.has(candidate)).sort()) {
      const baseVersions = file.baseVersions.get(name)
      if (baseVersions?.size) {
        packages.push({
          lockfile: file.path,
          name,
          baseVersions: [...baseVersions],
        })
      }
    }
  }
  const authorized = await buildAuthorizedMap(packages, readers, {
    lockfiles: files.map(file => file.path),
    warn,
  })
  return new Map(files.map((file) => {
    const introduced = new Set(file.introduced)
    return [
      file.path,
      activeExemptions(authorized[file.path], {
        base: file.baseVersions,
        selected: root => introduced.has(root),
      }).exempt,
    ]
  }))
}

/**
 * Check every lockfile version that a pull request adds between the `base` and `head`
 * revisions against the release age of the base policy, and return the violations. Only
 * a version younger than the age, or without a release timestamp, needs an exemption, so
 * a security branch reads the open alerts and builds its exemptions only for those. A
 * Renovate branch must also never let cargo lower a version: a person may do that on
 * purpose, with `cargo update --precise`.
 */
export async function checkDependencyAge({ base, head = 'HEAD', headRef, checkout = repositoryRoot, now = Date.now(), readers = defaultReaders, openAlerts = () => readAlerts(process.env.GITHUB_TOKEN), times = timeReaders(), warn }) {
  if (typeof headRef !== 'string')
    throw new TypeError('The release-age check requires the head ref of the pull request.')
  const policy = readRevisionFile(base, 'renovate.json', { cwd: checkout })
  if (policy == null)
    throw new Error(`The base revision ${base} has no renovate.json.`)
  const ageMs = releaseAgeMs(JSON.parse(policy))
  const files = []
  for (const entry of DEPENDENCY_LOCKFILES) {
    const before = readRevisionFile(base, entry.path, { cwd: checkout })
    const after = readRevisionFile(head, entry.path, { cwd: checkout })
    // An unchanged or removed file adds no version, so it needs no registry read.
    if (after == null || before === after)
      continue
    const baseVersions = await readLockedVersions(entry.kind, before, { checkout })
    const headVersions = await readLockedVersions(entry.kind, after, { checkout })
    files.push({
      ...entry,
      baseVersions,
      headVersions,
      introduced: introducedVersions(baseVersions, headVersions),
    })
  }
  const violations = []
  if (headRef.startsWith('renovate/')) {
    for (const file of files.filter(entry => entry.kind === 'cargo')) {
      for (const { name, from, to } of cargoReplacementDowngrades(file.baseVersions, file.headVersions))
        violations.push({ file: file.path, identity: `${name}@${to}`, message: `cargo would downgrade ${name} from ${from} to ${to}.` })
    }
  }
  const introduced = files.flatMap(file => file.introduced.map(identity => ({ file: file.path, ecosystem: file.ecosystem, identity })))
  if (introduced.length === 0)
    return violations
  const published = await publicationTimes(introduced, times, { checkout })
  const young = introduced
    .map((entry, index) => ({
      ...entry,
      time: Date.parse(published[index]),
    }))
    .filter(entry => !isPublishedBy(entry.time, now - ageMs))
  if (young.length === 0)
    return violations
  const exempt = SECURITY_BRANCHES.includes(headRef)
    ? await securityExemptions(files, { alerts: await openAlerts(), readers, warn })
    : new Map()
  for (const entry of young.filter(candidate => !exempt.get(candidate.file)?.has(candidate.identity))) {
    violations.push({
      file: entry.file,
      identity: entry.identity,
      message: Number.isFinite(entry.time)
        ? `${entry.identity} was published at ${new Date(entry.time).toISOString()}, less than ${describeAge(ageMs)} ago.`
        : `${entry.identity} has no release timestamp.`,
    })
  }
  return violations
}

// GitHub workflow commands require these escapes in a message and in a property.
const escapeData = value => value.replaceAll('%', '%25').replaceAll('\r', '%0D').replaceAll('\n', '%0A')
const escapeProperty = value => escapeData(value).replaceAll(':', '%3A').replaceAll(',', '%2C')

/** Render each violation as a GitHub error annotation on its lockfile. */
export function annotations(violations) {
  return violations.map(violation => `::error file=${escapeProperty(violation.file)}::${escapeData(violation.message)}`)
}

/**
 * Run the check for command-line arguments and return the exit code. A violation prints
 * one annotation per line and returns 1, which fails Required CI. A skipped security fix
 * prints a warning annotation. The other options are the options of checkDependencyAge.
 */
export async function main(argv, { stdout = process.stdout, stderr = process.stderr, ...options } = {}) {
  try {
    const { values } = parseArgs({
      args: argv,
      options: {
        'base': { type: 'string' },
        'head': { type: 'string', default: 'HEAD' },
        'head-ref': { type: 'string' },
      },
      strict: true,
    })
    if (!values.base || values['head-ref'] === undefined)
      throw new Error('Usage: check-dependency-age.mjs --base <rev> [--head <rev>] --head-ref <ref>')
    const violations = await checkDependencyAge({
      warn: message => stdout.write(`::warning::${escapeData(message)}\n`),
      ...options,
      base: values.base,
      head: values.head,
      headRef: values['head-ref'],
    })
    if (violations.length > 0) {
      for (const line of annotations(violations))
        stdout.write(`${line}\n`)
      return 1
    }
    stdout.write('Every version that the lockfiles add meets the minimum release age.\n')
    return 0
  }
  catch (error) {
    stderr.write(`${error.message}\n`)
    return 1
  }
}

if (import.meta.main)
  process.exitCode = await main(process.argv.slice(2))
