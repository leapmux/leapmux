#!/usr/bin/env bun
import { spawn } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { resolve } from 'node:path'
import process from 'node:process'
import { parseArgs } from 'node:util'
import { activeExemptions, buildAuthorizedMap, defaultReaders, splitIdentity } from './renovate-exemptions.mjs'
import { cargoReplacementDowngrades, DEPENDENCY_LOCKFILES, introducedVersions, lockedVersions, lockfileInput, readRevisionFile } from './renovate-lockfile-versions.mjs'
import { readRegistryJson, releaseAgeMs, serialThrottle } from './renovate-release-age.mjs'

// Renovate checks the release age only for the version that it selects. bun install and
// cargo update resolve transitive versions themselves, so Required CI checks every
// version that a Renovate PR adds to a lockfile. Direct pins in versions.env and in
// workflow actions stay under Renovate's own strict selection.

/** The branches that Renovate's security configuration names; only these may exempt a fix. */
export const SECURITY_BRANCHES = Object.freeze(['renovate/security', 'renovate/go-security'])

const root = resolve(import.meta.dirname, '..')

// Run a command without blocking the event loop.
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

const npmDocuments = new Map()
/** Read the publication time of one npm version from its packument. */
export async function readNpmTime(name, version, { request = fetch } = {}) {
  if (!npmDocuments.has(name))
    npmDocuments.set(name, readRegistryJson(`https://registry.npmjs.org/${encodeURIComponent(name)}`, { request, description: 'npm packument read' }))
  return (await npmDocuments.get(name))?.time?.[version] ?? null
}

const crateRequests = serialThrottle(1000)
/** Read the publication time of one crate version. crates.io allows one request per second. */
export async function readCrateTime(name, version, { request = fetch } = {}) {
  const metadata = await crateRequests(() => readRegistryJson(`https://crates.io/api/v1/crates/${encodeURIComponent(name)}/${encodeURIComponent(version)}`, { request, description: 'Crate version read', notFound: null }))
  return metadata?.version?.created_at ?? null
}

/** Read the publication times of Go module versions with one `go list` call through GOPROXY. */
export async function readGoTimes(identities, { checkout = root, env = process.env } = {}) {
  const times = new Map()
  if (identities.length === 0)
    return times
  mkdirSync(resolve(checkout, '.tmp'), { recursive: true })
  const directory = mkdtempSync(resolve(checkout, '.tmp/go-times-'))
  try {
    const result = await runCommand('go', [
      'list',
      '-m',
      '-json',
      '-e',
      ...identities,
    ], {
      cwd: directory,
      env: { ...env, GOWORK: 'off', GOTOOLCHAIN: 'local' },
    })
    if (result.error || result.status !== 0)
      throw new Error(`go list failed: ${result.error?.message ?? result.stderr.trim()}`)
    for (const module of parseJsonStream(result.stdout)) {
      if (!module.Error && typeof module.Time === 'string')
        times.set(`${module.Path}@${module.Version}`, module.Time)
    }
    return times
  }
  finally {
    rmSync(directory, { recursive: true, force: true })
  }
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

export const defaultTimeReaders = Object.freeze({ npmTime: readNpmTime, crateTime: readCrateTime, goTimes: readGoTimes })

function readWorkingFile(checkout, path) {
  const file = resolve(checkout, path)
  return existsSync(file)
    ? readFileSync(file, 'utf8')
    : null
}

/**
 * Check every lockfile version that a Renovate branch adds against the release age of
 * the base policy. Return the violations; a non-Renovate branch returns null without
 * reading any file or registry.
 */
export async function checkDependencyAge({ base, headRef, checkout = root, now = Date.now(), readers = defaultReaders, times = defaultTimeReaders }) {
  if (typeof headRef !== 'string' || !headRef.startsWith('renovate/'))
    return null
  const policy = readRevisionFile(base, 'renovate.json', { cwd: checkout })
  if (policy == null)
    throw new Error(`The base revision ${base} has no renovate.json.`)
  const ageMs = releaseAgeMs(JSON.parse(policy))
  const files = []
  for (const entry of DEPENDENCY_LOCKFILES) {
    const before = readRevisionFile(base, entry.path, { cwd: checkout })
    const after = readWorkingFile(checkout, entry.path)
    // An unchanged or removed file adds no version, so it needs no registry read.
    if (after == null || before === after)
      continue
    const baseInput = lockfileInput(entry.kind, before, { checkout })
    const headInput = lockfileInput(entry.kind, after, { checkout })
    files.push({
      ...entry,
      before,
      after,
      baseInput,
      headInput,
      introduced: introducedVersions(entry.kind, baseInput, headInput),
    })
  }
  const violations = []
  for (const file of files.filter(entry => entry.kind === 'cargo')) {
    // A downgrade fails on every Renovate branch, security branches included.
    for (const { name, from, to } of cargoReplacementDowngrades(file.before, file.after))
      violations.push({ file: file.path, identity: `${name}@${to}`, message: `cargo would downgrade ${name} from ${from} to ${to}.` })
  }
  const introduced = files.flatMap(file => file.introduced.map(identity => ({ file: file.path, ecosystem: file.ecosystem, identity })))
  if (introduced.length === 0)
    return violations
  const exempt = new Set()
  if (SECURITY_BRANCHES.includes(headRef)) {
    // Authorize from the base: a changed package keeps only the fixes of its base versions.
    const packages = new Map()
    for (const file of files) {
      const before = file.baseInput == null
        ? new Map()
        : lockedVersions(file.kind, file.baseInput)
      const after = lockedVersions(file.kind, file.headInput)
      const changed = new Set([...before].filter(([name, versions]) => [...versions].some(version => !after.get(name)?.has(version))).map(([name]) => name))
      for (const identity of file.introduced)
        changed.add(splitIdentity(identity).name)
      for (const name of changed) {
        const key = `${file.ecosystem}\0${name}`
        const entry = packages.get(key) ?? {
          ecosystem: file.ecosystem,
          name,
          baseVersions: new Set(),
        }
        for (const version of before.get(name) ?? [])
          entry.baseVersions.add(version)
        packages.set(key, entry)
      }
    }
    const authorized = await buildAuthorizedMap([
      ...packages.values(),
    ].filter(entry => entry.baseVersions.size > 0), readers)
    for (const [ecosystem, roots] of Object.entries(authorized)) {
      const added = new Set(introduced.filter(entry => entry.ecosystem === ecosystem).map(entry => entry.identity))
      for (const identity of activeExemptions(roots, root => added.has(root)))
        exempt.add(`${ecosystem}\0${identity}`)
    }
  }
  const checked = introduced.filter(entry => !exempt.has(`${entry.ecosystem}\0${entry.identity}`))
  const goTimes = await times.goTimes([
    ...new Set(checked.filter(entry => entry.ecosystem === 'go').map(entry => entry.identity)),
  ], { checkout })
  const published = await mapLimited(checked, 8, async (entry) => {
    const { name, version } = splitIdentity(entry.identity)
    if (entry.ecosystem === 'go')
      return goTimes.get(entry.identity) ?? null
    return entry.ecosystem === 'npm'
      ? times.npmTime(name, version)
      : times.crateTime(name, version)
  })
  for (const [index, entry] of checked.entries()) {
    const time = Date.parse(published[index])
    if (!Number.isFinite(time))
      violations.push({ file: entry.file, identity: entry.identity, message: `${entry.identity} has no release timestamp.` })
    else if (now - time < ageMs)
      violations.push({ file: entry.file, identity: entry.identity, message: `${entry.identity} was published at ${new Date(time).toISOString()}, less than ${ageMs / 86400000} days ago.` })
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
 * one annotation per line and returns 1, which fails Required CI. The other options are
 * the options of checkDependencyAge.
 */
export async function main(argv, { stdout = process.stdout, stderr = process.stderr, ...options } = {}) {
  try {
    const { values } = parseArgs({
      args: argv,
      options: {
        'base': { type: 'string' },
        'head-ref': { type: 'string' },
      },
      strict: true,
    })
    if (!values.base || values['head-ref'] === undefined)
      throw new Error('Usage: check-dependency-age.mjs --base <sha> --head-ref <ref>')
    const violations = await checkDependencyAge({ ...options, base: values.base, headRef: values['head-ref'] })
    if (violations === null) {
      stdout.write(`${values['head-ref'] || 'This ref'} is not a Renovate branch. The release-age check does not apply.\n`)
      return 0
    }
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
