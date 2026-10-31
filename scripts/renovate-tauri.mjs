import { readFileSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { splitIdentity, validateAuthorizedMap } from './renovate-exemptions.mjs'
import { bunIdentity, parseBunLock, parseCargoLock, parseCargoManifest } from './renovate-lockfile-versions.mjs'
import { planTauriTargets, tauriCrateRequirement } from './renovate-tauri-graph.mjs'
import { matchesCargoRequirement, tauriMajorMinor } from './renovate-tauri-releases.mjs'

/** The lockfiles of the Tauri pairs: the JavaScript side and the Rust side. */
const TAURI_LOCKFILES = Object.freeze(['frontend/bun.lock', 'desktop/rust/Cargo.lock'])

/**
 * The direct crates whose requirement must equal the requirement of the tauri crate.
 * `WebviewWindow::gtk_window()` returns a type of Tauri's gtk, and gtk-sys links the
 * native gtk-3 library, which Cargo permits once in a graph. So the graph resolves only
 * when our gtk requirement admits the gtk version that Tauri requires. Renovate never
 * raises these crates alone, and the plan sets them with the tauri crate.
 */
const TAURI_ALIGNED_CRATES = Object.freeze(['gtk'])

/** The characters of a Cargo version requirement, which the manifest stores in a string. */
const CARGO_REQUIREMENT = /^[\w.*^~<>=,+ -]+$/

/**
 * Return the authorized roots of the Tauri packages of `pairs`, with their closures, as
 * `planTauriTargets` reads them. A root of another package never authorizes a Tauri
 * release, because the plan can select only a Tauri root. The npm names and the crate
 * names of the pairs differ, so the roots of the two lockfiles never collide.
 */
export function tauriAuthorizedRoots(authorized, pairs) {
  const names = new Set(pairs.flatMap(pair => [pair.npm, pair.cargo]))
  const roots = new Map()
  for (const path of TAURI_LOCKFILES) {
    for (const [root, closure] of Object.entries(authorized?.[path] ?? {})) {
      if (names.has(splitIdentity(root).name))
        roots.set(root, new Set(closure))
    }
  }
  return roots
}

function cargoCompanion(name) {
  return name === '@tauri-apps/api'
    ? 'tauri'
    : name.startsWith('@tauri-apps/plugin-')
      ? name.replace('@tauri-apps/', 'tauri-')
      : null
}

/** Return each declaration of the crate `name` in one dependency table. */
function declarationsIn(table, name) {
  return Object.entries(table ?? {}).filter(([key, declaration]) => key === name || declaration?.package === name).map(([key, declaration]) => ({
    key,
    requirement: typeof declaration === 'string'
      ? declaration
      : declaration?.version,
  }))
}

function cargoDeclaration(manifest, name) {
  return declarationsIn(manifest?.dependencies, name)[0] ?? null
}

/** Return each declaration of the crate `name` in `[dependencies]` and in each target table. */
function cargoDeclarations(manifest, name) {
  return [
    manifest?.dependencies,
    ...Object.values(manifest?.target ?? {}).map(target => target?.dependencies),
  ].flatMap(table => declarationsIn(table, name))
}

/** Replace the version requirement of the one entry `key` in a Cargo.toml text. */
function replaceCargoRequirement(text, key, requirement) {
  const pattern = new RegExp(`^(\\s*${RegExp.escape(key)}\\s*=\\s*)(.+)$`, 'gm')
  const matches = [
    ...text.matchAll(pattern),
  ]
  if (matches.length !== 1)
    throw new Error(`Expected one Cargo.toml entry for ${key}.`)
  return text.replace(pattern, (_line, prefix, value) => {
    const versionPattern = value.startsWith('"')
      ? /^"[^"]+"/
      : /\bversion\s*=\s*"[^"]+"/
    if (!versionPattern.test(value))
      throw new Error(`Cannot locate the version requirement of ${key}.`)
    return prefix + value.replace(versionPattern, () => value.startsWith('"')
      ? `"${requirement}"`
      : `version = "${requirement}"`)
  })
}

/** Remove the caret of a requirement with one caret comparator, which means the same without it. */
function bareCargoRequirement(requirement) {
  const trimmed = requirement.trim()
  return /^\^\s*[^\s,]+$/.test(trimmed)
    ? trimmed.replace(/^\^\s*/, '')
    : trimmed
}

/**
 * Set the requirement of the crate `name` in a Cargo.toml text to `requirement`, without
 * a single caret. A declaration that already gives the same requirement stays as it is.
 */
export function alignCargoRequirement(text, name, requirement) {
  if (typeof requirement !== 'string' || !CARGO_REQUIREMENT.test(requirement))
    throw new Error(`Invalid Cargo requirement for ${name}: ${JSON.stringify(requirement)}`)
  const declarations = cargoDeclarations(parseCargoManifest(text), name)
  if (declarations.length !== 1)
    throw new Error(`Expected one declaration of ${name} in Cargo.toml, but found ${declarations.length}.`)
  const [declaration] = declarations
  if (!declaration.requirement)
    throw new Error(`Cannot locate the version requirement of ${name}.`)
  const target = bareCargoRequirement(requirement)
  return bareCargoRequirement(declaration.requirement) === target
    ? text
    : replaceCargoRequirement(text, declaration.key, target)
}

export function tauriVersions(bunText, cargoText, cargoManifestText) {
  const bun = parseBunLock(bunText)
  const cargo = parseCargoLock(cargoText)
  const resolved = Object.fromEntries(cargo.package.filter(pkg => pkg.name.startsWith('tauri')).map(pkg => [pkg.name, pkg.version]))
  const npm = {}
  const occurrences = { npm: {}, cargo: {} }
  for (const [key, value] of Object.entries(bun.packages)) {
    const identity = bunIdentity(value[0])
    // The planner compares and pins the version of each direct Tauri package, so bun.lock
    // must lock it at a registry version of that same package.
    if (/^@tauri-apps\/[^/]+$/.test(key)) {
      if (identity?.name !== key)
        throw new Error(`bun.lock must lock ${key} at an npm registry version, not ${value[0]}.`)
      npm[key] = identity.version
    }
    if (identity?.name.startsWith('@tauri-apps/')) {
      occurrences.npm[identity.name] = [
        ...new Set([
          ...(occurrences.npm[identity.name] ?? []),
          identity.version,
        ]),
      ].sort(Bun.semver.order)
    }
  }
  for (const pkg of cargo.package) {
    if (pkg.name.startsWith('tauri')) {
      occurrences.cargo[pkg.name] = [
        ...new Set([
          ...(occurrences.cargo[pkg.name] ?? []),
          pkg.version,
        ]),
      ].sort(Bun.semver.order)
    }
  }
  const manifest = cargoManifestText
    ? parseCargoManifest(cargoManifestText)
    : null
  for (const name of Object.keys(npm).map(cargoCompanion).filter(Boolean)) {
    const versions = [
      ...new Set(cargo.package.filter(pkg => pkg.name === name).map(pkg => pkg.version)),
    ]
    if (versions.length <= 1 && !manifest)
      continue
    const requirement = cargoDeclaration(manifest, name)?.requirement
    const matching = requirement
      ? versions.filter(version => matchesCargoRequirement(version, requirement))
      : []
    if (matching.length !== 1)
      throw new Error(`Cannot identify the direct locked Tauri version for ${name}.`)
    resolved[name] = matching[0]
  }
  return { npm, cargo: resolved, occurrences }
}

export function readTauriVersions(root) {
  return tauriVersions(readFileSync(resolve(root, 'frontend/bun.lock'), 'utf8'), readFileSync(resolve(root, 'desktop/rust/Cargo.lock'), 'utf8'), readFileSync(resolve(root, 'desktop/rust/Cargo.toml'), 'utf8'))
}

export function tauriPairs(versions) {
  return Object.entries(versions.npm).flatMap(([npm, npmVersion]) => {
    const cargo = cargoCompanion(npm)
    return cargo && versions.cargo[cargo]
      ? [
          {
            npm,
            cargo,
            npmVersion,
            cargoVersion: versions.cargo[cargo],
          },
        ]
      : []
  })
}

/** Require each Rust and JavaScript Tauri pair to share its major and minor version. */
export function assertTauriCompatibility(versions) {
  for (const { npm, cargo, npmVersion, cargoVersion } of tauriPairs(versions)) {
    if (!tauriMajorMinor(npmVersion) || tauriMajorMinor(npmVersion) !== tauriMajorMinor(cargoVersion))
      throw new Error(`Tauri major/minor mismatch: ${npm} ${npmVersion}, ${cargo} ${cargoVersion}`)
  }
}

export function reconcileCargoRequirement(text, name, target, { exact = false } = {}) {
  const declaration = cargoDeclaration(parseCargoManifest(text), name)
  const requirement = declaration?.requirement
  if (!requirement)
    throw new Error(`Missing direct Tauri requirement: ${name}`)
  if (!exact && matchesCargoRequirement(target, requirement))
    return text
  return replaceCargoRequirement(text, declaration.key, `${exact ? '=' : ''}${target}`)
}

/**
 * Align each crate of TAURI_ALIGNED_CRATES that Cargo.toml declares with the requirement
 * of the tauri crate at `version`.
 */
async function alignWithTauri(text, version, request) {
  let aligned = text
  for (const name of TAURI_ALIGNED_CRATES) {
    if (cargoDeclarations(parseCargoManifest(aligned), name).length === 0)
      continue
    if (!version)
      throw new Error(`Cargo.toml declares ${name}, which follows the tauri crate, but the Tauri plan has no tauri crate.`)
    const requirement = await tauriCrateRequirement(name, version, request)
    if (!requirement)
      throw new Error(`tauri ${version} does not depend on ${name}, so Cargo.toml has no ${name} requirement to follow.`)
    aligned = alignCargoRequirement(aligned, name, requirement)
  }
  return aligned
}

export async function reconcileTauri({ root, trusted, run, security = false, upgrades = [], request }) {
  const proposed = readTauriVersions(root)
  const baseline = readTauriVersions(trusted)
  const prepared = JSON.parse(readFileSync(resolve(trusted, '.tmp/renovate-prepared.json'), 'utf8'))
  const policy = JSON.parse(readFileSync(resolve(trusted, 'renovate.json'), 'utf8'))
  if (security && !prepared.authorized)
    throw new Error('Security processing requires the authorized fixes of the prepared file.')
  if (security)
    validateAuthorizedMap(prepared.authorized)
  const pairs = tauriPairs(proposed).map((pair) => {
    if (!baseline.npm[pair.npm] || !baseline.cargo[pair.cargo])
      throw new Error(`Cannot find trusted Tauri baseline for ${pair.npm}.`)
    return { ...pair, npmVersion: baseline.npm[pair.npm], cargoVersion: baseline.cargo[pair.cargo] }
  })
  // Only a security branch may select a young authorized fix.
  const authorizedRoots = security
    ? tauriAuthorizedRoots(prepared.authorized, pairs)
    : new Map()
  const targets = await planTauriTargets(pairs, {
    policy,
    alerts: prepared.alerts,
    upgrades,
    baseline,
    proposed,
    security,
    preparedTargets: security ? prepared.securityTargets : null,
    authorizedRoots,
    request,
  })
  if (!targets)
    throw new Error('No compatible Tauri graph satisfies the release age and required versions.')
  if (targets.length === 0) {
    assertTauriCompatibility(proposed)
    return
  }
  const npmFile = resolve(root, 'frontend/package.json')
  const manifest = JSON.parse(readFileSync(npmFile, 'utf8'))
  const cargoFile = resolve(root, 'desktop/rust/Cargo.toml')
  const originalCargo = readFileSync(cargoFile, 'utf8')
  let cargo = originalCargo
  let bunChanged = false
  let cargoChanged = false
  for (const { pair, target } of targets) {
    const group = Object.hasOwn(manifest.dependencies ?? {}, pair.npm)
      ? 'dependencies'
      : 'devDependencies'
    if (!manifest[group]?.[pair.npm])
      throw new Error(`Missing Tauri companion manifest entry: ${pair.npm}`)
    // Exact requirements prevent either resolver from moving an already planned companion.
    if (manifest[group][pair.npm] !== target.npm) {
      manifest[group][pair.npm] = target.npm
      bunChanged = true
    }
    cargo = reconcileCargoRequirement(cargo, pair.cargo, target.cargo, { exact: true })
    cargoChanged ||= proposed.cargo[pair.cargo] !== target.cargo
  }
  // Read the requirements before any write, so a failed read leaves the branch unchanged.
  const pinned = cargo
  cargo = await alignWithTauri(cargo, targets.find(({ pair }) => pair.cargo === 'tauri')?.target.cargo, request)
  cargoChanged ||= cargo !== pinned
  if (bunChanged)
    writeFileSync(npmFile, `${JSON.stringify(manifest, null, 2)}\n`)
  if (cargo !== originalCargo)
    writeFileSync(cargoFile, cargo)
  if (cargoChanged)
    run('cargo', ['update', '--workspace'], { cwd: 'desktop/rust' })
  if (bunChanged)
    run('bun', ['install', '--ignore-scripts'], { cwd: 'frontend' })
  const resolved = readTauriVersions(root)
  assertTauriCompatibility(resolved)
  for (const { pair, target } of targets) {
    if (resolved.npm[pair.npm] !== target.npm || resolved.cargo[pair.cargo] !== target.cargo)
      throw new Error(`The resolved Tauri pair differs from its exact requirements: ${pair.npm}`)
  }
}
