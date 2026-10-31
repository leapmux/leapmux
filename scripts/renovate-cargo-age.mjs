import { existsSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { agedIndexArgs, startAgedIndex } from './renovate-crate-index-proxy.mjs'
import { activeExemptions, exactIdentity, splitIdentity } from './renovate-exemptions.mjs'
import { cargoReplacementDowngrades, introducedVersions, lockedVersions, parseCargoLock, readRevisionFile, versionIdentities } from './renovate-lockfile-versions.mjs'
import { readCrateReleases } from './renovate-registry.mjs'
import { isPublishedBy } from './renovate-release-age.mjs'

// Each `cargo update` resolves through the aged crate index of
// renovate-crate-index-proxy.mjs, which serves no version younger than the release age, so
// cargo selects only old-enough versions. A check after the update fails closed: it
// rejects a young version that still reached the lockfile, and a version that cargo
// lowered. When that check fails after a full update, a selective update leaves the crates
// that block it at their versions.

export class CargoAgeError extends Error {
  constructor(reason, violations, detail = '') {
    const listed = violations.map(violation => violation.text ?? `- ${violation.id}`).join('\n')
    super(`Cargo release-age check failed: ${reason}${listed
      ? `\n${listed}`
      : ''}${detail
      ? `\n${detail}`
      : ''}`)
    this.name = 'CargoAgeError'
    this.reason = reason
    this.violations = violations
  }
}

// A targeted update fails on a downgrade: its manifest or its selected crates force it.
// A full update can also lower a locked version that is yanked, or that a new version of a
// dependent pins exactly. Its selective update then keeps the crate at its locked version.
function assertNoDowngrade(baseVersions, current) {
  const downgrades = cargoReplacementDowngrades(baseVersions, lockedVersions('cargo', current))
  if (downgrades.length > 0) {
    throw new CargoAgeError('a lower version replaces a locked version', downgrades.map(({ name, from, to }) => ({
      id: `${name}@${to}`,
      name,
      version: to,
      text: `- cargo would downgrade ${name} from ${from} to ${to}`,
    })))
  }
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
      const identity = typeof dependency.source === 'string' && dependency.source.startsWith('registry+')
        ? exactIdentity('crates', dependency.name, dependency.req)
        : null
      if (identity)
        declared.add(identity)
    }
  }
  return declared
}

/** Return the workspace Cargo.lock path of a cargo invocation. */
function cargoLockfile(run, manifestArgs = []) {
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

/** Return the path of a Cargo.lock in its repository, as the prepared authorized map keys it. */
function repositoryPath(lockfile, git) {
  const prefix = git(['rev-parse', '--show-prefix'], {
    cwd: dirname(lockfile),
  })
  if (prefix.error || prefix.status !== 0)
    throw new Error(`Cannot find the repository of ${lockfile}: ${prefix.error?.message ?? prefix.stderr.trim()}`)
  return `${prefix.stdout.trim()}Cargo.lock`
}

/**
 * Return what the release age admits for one `cargo update` of `lockfile`:
 * - `cutoff`: the latest publication time that the age admits.
 * - `baseVersions`: the versions that the Cargo.lock of HEAD locks.
 * - `activeRoots` and `exempt`: the active authorized roots and the identities that they
 *   exempt. A root is active when the base does not lock it and one of these holds:
 *   - It is the `--precise` target.
 *   - A workspace manifest declares it exactly.
 *   - The lockfile before this call (`previous`) already holds it.
 * - `allowed`: the identities that the aged index serves at any age. These are the
 *   versions of the base and of `previous`, so that cargo never has to move a crate that
 *   the update leaves alone, and the exempt ones. The check after the update still
 *   rejects a young version that the base lacks and no root exempts.
 */
export function cargoAgePolicy({ run, git, policy, manifestArgs = [], preciseTarget = null, lockfile, previous = null }) {
  const cutoff = Date.parse(policy.cutoff)
  if (!Number.isFinite(cutoff))
    throw new TypeError('The shim policy requires a publication cutoff.')
  const base = readRevisionFile('HEAD', './Cargo.lock', {
    cwd: dirname(lockfile),
    git,
  })
  const baseVersions = base == null
    ? new Map()
    : lockedVersions('cargo', base)
  const held = previous == null
    ? new Map()
    : lockedVersions('cargo', previous)
  let declared
  const { roots, exempt } = activeExemptions(policy.authorized?.[repositoryPath(lockfile, git)], {
    base: baseVersions,
    selected: (root) => {
      const { name, version } = splitIdentity(root)
      // Renovate follows a lock-only `--precise` update with `cargo update --workspace`.
      // Every Cargo.lock write passes this check, so an earlier call of the branch kept a
      // held root only while it was active.
      if (root === preciseTarget || held.get(name)?.has(version))
        return true
      declared ??= workspaceExactDeclarations(run, manifestArgs)
      return declared.has(root)
    },
  })
  return {
    cutoff,
    baseVersions,
    activeRoots: roots,
    exempt,
    allowed: new Set([
      ...versionIdentities(baseVersions),
      ...versionIdentities(held),
      ...exempt,
    ]),
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

/**
 * Fail with a CargoAgeError when the Cargo.lock text `current` lowers a version of the
 * base, or adds a version that `age` does not exempt and that was not published by its
 * cutoff, including a version without a publication time. `releases(name)` returns the
 * index entries of one crate.
 */
export async function checkCargoLock(current, age, releases) {
  assertNoDowngrade(age.baseVersions, current)
  const candidates = introducedVersions(age.baseVersions, lockedVersions('cargo', current)).filter(id => !age.exempt.has(id))
  const names = [
    ...new Set(candidates.map(id => splitIdentity(id).name)),
  ]
  const entries = new Map(await Promise.all(names.map(async name => [name, await releases(name)])))
  const young = candidates.flatMap((id) => {
    const { name, version } = splitIdentity(id)
    const release = entries.get(name).find(entry => entry.version === version)
    return isPublishedBy(Date.parse(release?.publishedAt), age.cutoff)
      ? []
      : [
          describeViolation({
            id,
            name,
            version,
            publishedAt: release?.publishedAt ?? null,
          }),
        ]
  })
  if (young.length > 0)
    throw new CargoAgeError('a version is younger than the release age', young)
}

/** Return a reader of crate releases from the sparse index `index` that reads each crate once. */
export function crateReleases({ index, request } = {}) {
  const read = new Map()
  return (name) => {
    if (!read.has(name))
      read.set(name, readCrateReleases(name, { index, request }))
    return read.get(name)
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
 * Repeat a full update without the crates that fail its check. Each round starts from the
 * lockfile before the update, updates only the crates that the full update replaced, and
 * checks that result. Each round leaves out each crate of the failed check. It also leaves
 * out each selected crate that pulls a blocked crate in, when an earlier round pulled the
 * blocked crate back, or when the blocked crates are not among the selected crates. Every
 * round leaves out at least one more crate, so the rounds end. The excluded crates keep
 * their versions until a later update.
 */
async function selectiveUpdate({ run, cargoArgs, manifestArgs, files, lockfile, before, updated, failure, check }) {
  let excluded = new Set()
  let error = failure
  for (let round = 1; ; round++) {
    const blocked = error.violations.map(violation => violation.name)
    const previousSpecs = selectiveSpecs(before, updated, excluded)
    const leftOut = new Set([
      ...excluded,
      ...blocked,
    ])
    // Many crates depend on a blocked crate without requiring its young version, so the
    // first round leaves them in whenever leaving out the blocked crates changes the update.
    if (round > 1 || selectiveSpecs(before, updated, leftOut).length === previousSpecs.length) {
      const selected = new Set(previousSpecs.map(spec => splitIdentity(spec).name))
      for (const name of cargoPullers(files.read(lockfile), blocked, { selected, before }))
        leftOut.add(name)
    }
    const specs = selectiveSpecs(before, updated, leftOut)
    // An exclusion that removes no spec would repeat the same update and the same failure.
    if (specs.length === previousSpecs.length)
      throw error
    excluded = leftOut
    files.write(lockfile, before)
    if (specs.length > 0) {
      const selectiveResult = run([
        ...cargoArgs,
        'update',
        ...manifestArgs,
        ...specs,
      ])
      if (selectiveResult.status !== 0)
        throw new CargoAgeError('cargo rejected the selective update', error.violations, String(selectiveResult.stderr ?? '').slice(-2048))
    }
    try {
      await check(files.read(lockfile))
      return {
        excluded: [...excluded].sort(),
      }
    }
    catch (caught) {
      if (!(caught instanceof CargoAgeError))
        throw caught
      error = caught
    }
  }
}

/**
 * Run one `cargo update` through the aged index, and check its lockfile. `update(cargoArgs)`
 * runs the update with the cargo options that select the aged index. A failed check
 * restores the lockfile to its state before the update, so that a branch never commits a
 * version that the age holds back. A full update (`fullUpdate`), as lockfile maintenance
 * runs, falls back to a selective update when the check fails. A targeted update must move
 * the crates that it specifies, so its failed check fails the call. `releases(name)`
 * returns the index entries of one crate for the check.
 */
export async function updateWithAge({ run, update, git, policy, manifestArgs = [], preciseTarget = null, fullUpdate = false, files = {
  read: path => readFileSync(path, 'utf8'),
  exists: existsSync,
  write: writeFileSync,
  remove: path => rmSync(path, { force: true }),
}, releases = crateReleases({ index: policy.cratesIndex }), startIndex = startAgedIndex }) {
  const lockfile = cargoLockfile(run, manifestArgs)
  const before = files.exists(lockfile)
    ? files.read(lockfile)
    : null
  const age = cargoAgePolicy({
    run,
    git,
    policy,
    manifestArgs,
    preciseTarget,
    lockfile,
    previous: before,
  })
  const index = await startIndex({ upstream: policy.cratesIndex, cutoffMs: age.cutoff, allowed: age.allowed })
  try {
    const cargoArgs = agedIndexArgs(index.url)
    const result = await update(cargoArgs)
    if (result.code !== 0 || result.signal)
      return { result, activeRoots: [] }
    const updated = files.read(lockfile)
    const check = text => checkCargoLock(text, age, releases)
    try {
      try {
        await check(updated)
        return { result, lockfile, activeRoots: age.activeRoots }
      }
      catch (error) {
        // Without an earlier lockfile, no crate can keep its version.
        if (!fullUpdate || before === null || !(error instanceof CargoAgeError))
          throw error
        return {
          result,
          lockfile,
          activeRoots: age.activeRoots,
          ...await selectiveUpdate({
            run,
            cargoArgs,
            manifestArgs,
            files,
            lockfile,
            before,
            updated,
            failure: error,
            check,
          }),
        }
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
  finally {
    await index.close()
  }
}
