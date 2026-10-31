import { existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { renovateModule, requireRenovate } from './renovate-engine.mjs'
import { activeExemptions, splitIdentity } from './renovate-exemptions.mjs'
import { cargoLine, cargoReplacementDowngrades, introducedVersions, lockedVersions, parseCargoLock, readRevisionFile } from './renovate-lockfile-versions.mjs'
import { readRegistryJson } from './renovate-release-age.mjs'

// `cargo update` resolves a new crate with no release age, and its unstable publish-time
// option exists only on `cargo generate-lockfile`. This repair runs after each
// `cargo update` and moves each young new crate to an old-enough version of its line.
// When that repair fails after a full update, a selective update leaves the crates that
// block it at their versions.
const semver = requireRenovate('semver')
const { api: cargoVersioning } = await renovateModule('modules/versioning/cargo/index.js')

// The repair never retries a forced downgrade: no selection of crates can undo it.
const DOWNGRADE = 'a lower version replaces a locked version'

export class CargoRepairError extends Error {
  constructor(reason, violations, detail = '') {
    const listed = violations.map(violation => violation.text ?? `- ${violation.id}`).join('\n')
    super(`Cargo release-age repair failed: ${reason}${listed
      ? `\n${listed}`
      : ''}${detail
      ? `\n${detail}`
      : ''}`)
    this.name = 'CargoRepairError'
    this.reason = reason
    this.violations = violations
  }
}

function describeViolation(violation) {
  return {
    ...violation,
    text: `- ${violation.id} (${violation.publishedAt
      ? `published ${violation.publishedAt}`
      : 'no release timestamp'})`,
  }
}

function assertNoDowngrade(base, current) {
  const downgrades = cargoReplacementDowngrades(base, current)
  if (downgrades.length > 0) {
    throw new CargoRepairError(DOWNGRADE, downgrades.map(({ name, from, to }) => ({
      id: `${name}@${to}`,
      text: `- cargo would downgrade ${name} from ${from} to ${to}`,
    })))
  }
}

function exactRequirement(requirement) {
  return semver.clean(requirement.trim().replace(/^=\s*/, ''))
}

/** Return each registry crate that a workspace manifest declares with one exact version. */
function workspaceExactDeclarations(run, manifestArgs) {
  const result = run([
    'metadata',
    '--no-deps',
    '--format-version',
    '1',
    ...manifestArgs,
  ])
  if (result.status !== 0)
    throw new Error(`cargo metadata failed: ${result.stderr.trim()}`)
  const declared = new Set()
  for (const pkg of JSON.parse(result.stdout).packages ?? []) {
    for (const dependency of pkg.dependencies ?? []) {
      if (typeof dependency.source === 'string' && dependency.source.startsWith('registry+') && cargoVersioning.isSingleVersion(dependency.req))
        declared.add(`${dependency.name}@${exactRequirement(dependency.req)}`)
    }
  }
  return declared
}

/** Return the workspace Cargo.lock path of a cargo invocation. */
export function cargoLockfile(run, manifestArgs = []) {
  const located = run([
    'locate-project',
    '--workspace',
    '--message-format',
    'plain',
    ...manifestArgs,
  ])
  if (located.status !== 0)
    throw new Error(`cargo locate-project failed: ${located.stderr.trim()}`)
  return join(dirname(located.stdout.trim()), 'Cargo.lock')
}

/**
 * Hold every crate version that the lockfile adds to the release age, without moving a
 * version that the base locks. Exempt only active authorized roots and their closures:
 * a root that the base does not lock, and that one of these holds:
 * - It is the `--precise` target.
 * - A workspace manifest declares it exactly.
 * - The lockfile before this call (`previous`) already holds it.
 * Fail with a CargoRepairError when no repair satisfies the age.
 */
export async function repairCargoLock({ run, git, registry, policy, manifestArgs = [], preciseTarget = null, previous = null, lockfile = cargoLockfile(run, manifestArgs), read = path => readFileSync(path, 'utf8') }) {
  const base = readRevisionFile('HEAD', './Cargo.lock', {
    cwd: dirname(lockfile),
    git,
  })
  const baseVersions = base == null
    ? new Map()
    : lockedVersions('cargo', base)
  const cutoff = Date.parse(policy.cutoff)
  if (!Number.isFinite(cutoff))
    throw new TypeError('The shim policy requires a publication cutoff.')
  let current = read(lockfile)
  // A forced downgrade fails before any repair, even when every version is old enough.
  assertNoDowngrade(base, current)
  const authorized = policy.authorized?.crates ?? {}
  // Read the workspace declarations only when a young version needs an exemption.
  let activation
  const activate = () => {
    if (!activation) {
      let declared
      const held = previous == null
        ? new Map()
        : lockedVersions('cargo', previous)
      const roots = Object.keys(authorized).filter((root) => {
        const { name, version } = splitIdentity(root)
        if (baseVersions.get(name)?.has(version))
          return false
        // Renovate follows a lock-only `--precise` update with `cargo update --workspace`.
        // Every Cargo.lock write passes this repair, so an earlier call of the branch kept
        // a held root only while it was active.
        if (root === preciseTarget || held.get(name)?.has(version))
          return true
        declared ??= workspaceExactDeclarations(run, manifestArgs)
        return declared.has(root)
      })
      activation = {
        roots,
        exempt: activeExemptions(authorized, root => roots.includes(root)),
      }
    }
    return activation
  }
  const violationsOf = async (text) => {
    const young = []
    for (const id of introducedVersions('cargo', base, text)) {
      const { name, version } = splitIdentity(id)
      const release = (await registry(name)).find(entry => entry.num === version)
      const published = Date.parse(release?.created_at)
      if (!Number.isFinite(published) || published > cutoff) {
        young.push(describeViolation({
          id,
          name,
          version,
          publishedAt: release?.created_at ?? null,
        }))
      }
    }
    return young.length === 0
      ? []
      : young.filter(violation => !activate().exempt.has(violation.id))
  }
  const repairs = []
  const rounds = policy.maxRepairRounds
  if (!Number.isSafeInteger(rounds) || rounds <= 0)
    throw new Error('The shim policy requires a positive repair round limit.')
  for (let round = 1; ; round++) {
    const violations = await violationsOf(current)
    if (violations.length === 0)
      return { lockfile, repairs, activeRoots: activation?.roots ?? [] }
    if (round > rounds)
      throw new CargoRepairError('round limit reached', violations)
    const before = current
    for (const violation of violations) {
      const locked = lockedVersions('cargo', current)
      // An earlier repair in this round can remove the version.
      if (!locked.get(violation.name)?.has(violation.version))
        continue
      const line = cargoLine(violation.version)
      const floor = [
        ...(baseVersions.get(violation.name) ?? []),
      ]
        .filter(version => cargoLine(version) === line && !locked.get(violation.name)?.has(version))
        .sort(semver.compare)
        .at(-1)
      const target = (await registry(violation.name))
        .filter(entry => !entry.yanked && semver.valid(entry.num) && entry.num !== violation.version
          && cargoLine(entry.num) === line && Date.parse(entry.created_at) <= cutoff
          && (semver.prerelease(violation.version) || !semver.prerelease(entry.num))
          && (floor === undefined || semver.gte(entry.num, floor)))
        .map(entry => entry.num)
        .sort(semver.rcompare)[0]
      const remaining = () => violations.filter(entry => lockedVersions('cargo', current).get(entry.name)?.has(entry.version))
      if (!target)
        throw new CargoRepairError('no eligible version', remaining())
      const result = run([
        'update',
        ...manifestArgs,
        '-p',
        violation.id,
        '--precise',
        target,
      ])
      if (result.status !== 0)
        throw new CargoRepairError(`cargo rejected --precise ${target}`, remaining(), String(result.stderr ?? '').slice(-2048))
      repairs.push({ from: violation.id, to: `${violation.name}@${target}` })
      current = read(lockfile)
      assertNoDowngrade(base, current)
    }
    if (current === before)
      throw new CargoRepairError('no progress', violations)
  }
}

/**
 * Return the selected crates that can pull a version of one of `names` into the lockfile
 * `text`: each crate of `selected` that depends on one of them, directly or through crates
 * that the lockfile `before` lacks. A crate that `before` already locks at the same version
 * pulls no new version, so the search never passes through one.
 */
export function cargoPullers(text, names, { selected, before }) {
  const locked = lockedVersions('cargo', before)
  const dependents = new Map()
  for (const pkg of parseCargoLock(text).package ?? []) {
    // Only a registry crate can be a selective update spec.
    if (typeof pkg.source !== 'string' || !pkg.source.startsWith('registry+'))
      continue
    // A dependency entry is `name`, or `name version` when several versions are locked.
    for (const dependency of pkg.dependencies ?? []) {
      const name = dependency.split(' ')[0]
      if (!dependents.has(name))
        dependents.set(name, [])
      dependents.get(name).push(pkg)
    }
  }
  const pullers = new Set()
  const visited = new Set(names)
  const queue = [...names]
  while (queue.length > 0) {
    for (const dependent of dependents.get(queue.shift()) ?? []) {
      if (selected.has(dependent.name)) {
        pullers.add(dependent.name)
      }
      else if (!locked.get(dependent.name)?.has(dependent.version) && !visited.has(dependent.name)) {
        visited.add(dependent.name)
        queue.push(dependent.name)
      }
    }
  }
  return pullers
}

/**
 * Return a `name@version` spec for each registry version of `before` that the update to
 * `updated` replaced, except for the excluded crates.
 */
export function selectiveSpecs(before, updated, excluded) {
  const after = lockedVersions('cargo', updated)
  return [
    ...lockedVersions('cargo', before),
  ]
    .filter(([name]) => !excluded.has(name))
    .flatMap(([name, versions]) => [...versions]
      .filter(version => !after.get(name)?.has(version))
      .map(version => `${name}@${version}`))
    .sort()
}

/**
 * Repeat a full update without the crates that block its repair. A crate family that pins
 * itself exactly, as wasm-bindgen does, cannot move back with one `--precise` command per
 * crate. Each round starts from the lockfile before the update, updates only the crates
 * that the full update replaced, and repairs that result. Each round leaves out each crate
 * of the failed repair. It also leaves out each selected crate that pulls a blocked crate
 * in, when an earlier round pulled the blocked crate back, or when the blocked crates are
 * not among the selected crates. The excluded crates keep their versions until a later
 * update.
 */
async function selectiveUpdate({ run, manifestArgs, files, lockfile, before, updated, failure, repair }) {
  const rounds = repair.policy.maxRepairRounds
  let excluded = new Set()
  let error = failure
  for (let round = 1; ; round++) {
    const blocked = error.violations.map(violation => violation.name)
    const previous = selectiveSpecs(before, updated, excluded)
    const leftOut = new Set([
      ...excluded,
      ...blocked,
    ])
    // Many crates depend on a blocked crate without requiring its young version, so the
    // first round leaves them in whenever leaving out the blocked crates changes the update.
    if (round > 1 || selectiveSpecs(before, updated, leftOut).length === previous.length) {
      const selected = new Set(previous.map(spec => spec.slice(0, spec.lastIndexOf('@'))))
      for (const name of cargoPullers(files.read(lockfile), blocked, { selected, before }))
        leftOut.add(name)
    }
    const specs = selectiveSpecs(before, updated, leftOut)
    // An exclusion that removes no spec would repeat the same update and the same failure.
    if (specs.length === previous.length)
      throw error
    excluded = leftOut
    if (round > rounds)
      throw new CargoRepairError('round limit reached', error.violations)
    files.write(lockfile, before)
    if (specs.length > 0) {
      const selected = run([
        'update',
        ...manifestArgs,
        ...specs,
      ])
      if (selected.status !== 0)
        throw new CargoRepairError('cargo rejected the selective update', error.violations, String(selected.stderr ?? '').slice(-2048))
    }
    try {
      return {
        ...await repairCargoLock({
          ...repair,
          run,
          manifestArgs,
          previous: before,
          lockfile,
          read: files.read,
        }),
        excluded: [...excluded].sort(),
      }
    }
    catch (caught) {
      if (!(caught instanceof CargoRepairError) || caught.reason === DOWNGRADE)
        throw caught
      error = caught
    }
  }
}

/**
 * Run one `cargo update` and its repair. A failed repair restores the lockfile to its state
 * before the update, so that a branch never commits the young versions that the update
 * chose. A full update (`selective`), as lockfile maintenance runs, falls back to a
 * selective update when the repair fails. A targeted update must move the crates that it
 * specifies, so its failed repair fails the call.
 */
export async function updateWithRepair({ run, update, manifestArgs = [], selective = false, files = {
  read: path => readFileSync(path, 'utf8'),
  exists: existsSync,
  write: writeFileSync,
  remove: path => rmSync(path, { force: true }),
}, ...repair }) {
  const lockfile = cargoLockfile(run, manifestArgs)
  const before = files.exists(lockfile)
    ? files.read(lockfile)
    : null
  const result = await update()
  if (result.code !== 0 || result.signal)
    return { result, repairs: [], activeRoots: [] }
  const updated = files.read(lockfile)
  const repairOrSelect = async () => {
    try {
      return await repairCargoLock({
        ...repair,
        run,
        manifestArgs,
        previous: before,
        lockfile,
        read: files.read,
      })
    }
    catch (error) {
      // Without an earlier lockfile, no crate can keep its version.
      if (!selective || before === null || !(error instanceof CargoRepairError) || error.reason === DOWNGRADE)
        throw error
      return selectiveUpdate({
        run,
        manifestArgs,
        files,
        lockfile,
        before,
        updated,
        failure: error,
        repair,
      })
    }
  }
  try {
    return {
      result,
      ...await repairOrSelect(),
    }
  }
  catch (error) {
    if (before === null)
      files.remove(lockfile)
    else
      files.write(lockfile, before)
    error.message += `\n${before === null
      ? 'The new Cargo.lock was removed.'
      : 'Cargo.lock was restored to its state before this command.'}`
    throw error
  }
}

/**
 * Read crate releases from the crates API with a disk cache for the run. Requests from
 * every shim process stay at least one second apart, as the crates.io policy asks.
 */
export function cratesRegistry({ api, cacheDirectory, request = fetch, now = () => Date.now(), sleep = ms => new Promise(settle => setTimeout(settle, ms)) }) {
  const last = join(cacheDirectory, '..', 'crates.last')
  return async (name) => {
    const file = join(cacheDirectory, `${encodeURIComponent(name)}.json`)
    if (existsSync(file))
      return JSON.parse(readFileSync(file, 'utf8'))
    mkdirSync(cacheDirectory, { recursive: true })
    const previous = existsSync(last)
      ? Number(readFileSync(last, 'utf8'))
      : Number.NEGATIVE_INFINITY
    const wait = (Number.isFinite(previous)
      ? previous
      : Number.NEGATIVE_INFINITY) + 1000 - now()
    if (wait > 0)
      await sleep(wait)
    writeFileSync(last, String(now()))
    const metadata = await readRegistryJson(`${api}/crates/${encodeURIComponent(name)}`, {
      request,
      description: 'Crate release read',
      notFound: { versions: [] },
    })
    if (!Array.isArray(metadata?.versions))
      throw new TypeError(`Invalid crate release metadata: ${name}`)
    const versions = metadata.versions.map(entry => ({ num: entry.num, created_at: entry.created_at ?? null, yanked: entry.yanked === true }))
    writeFileSync(file, JSON.stringify(versions))
    return versions
  }
}
