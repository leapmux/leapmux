import { readFileSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { planTauriTargets } from './renovate-tauri-graph.mjs'
import { matchesCargoRequirement, tauriMajorMinor } from './renovate-tauri-releases.mjs'

function cargoCompanion(name) {
  return name === '@tauri-apps/api' ? 'tauri' : name.startsWith('@tauri-apps/plugin-') ? name.replace('@tauri-apps/', 'tauri-') : null
}

function cargoDeclaration(manifest, name) {
  const entry = Object.entries(manifest?.dependencies ?? {}).find(([key, declaration]) => key === name || declaration?.package === name)
  if (!entry)
    return null
  return { key: entry[0], requirement: typeof entry[1] === 'string' ? entry[1] : entry[1]?.version }
}

export function tauriVersions(bunText, cargoText, cargoManifestText) {
  const bun = Bun.JSON5.parse(bunText)
  const cargo = Bun.TOML.parse(cargoText)
  const npm = Object.fromEntries(Object.entries(bun.packages).filter(([name]) => /^@tauri-apps\/[^/]+$/.test(name)).map(([name, value]) => [name, value[0].slice(value[0].lastIndexOf('@') + 1)]))
  const resolved = Object.fromEntries(cargo.package.filter(pkg => pkg.name.startsWith('tauri')).map(pkg => [pkg.name, pkg.version]))
  const occurrences = { npm: {}, cargo: {} }
  for (const value of Object.values(bun.packages)) {
    const identity = value[0]
    const split = identity.lastIndexOf('@')
    const name = identity.slice(0, split)
    if (name.startsWith('@tauri-apps/'))
      occurrences.npm[name] = [...new Set([...(occurrences.npm[name] ?? []), identity.slice(split + 1)])].sort(Bun.semver.order)
  }
  for (const pkg of cargo.package) {
    if (pkg.name.startsWith('tauri'))
      occurrences.cargo[pkg.name] = [...new Set([...(occurrences.cargo[pkg.name] ?? []), pkg.version])].sort(Bun.semver.order)
  }
  const manifest = cargoManifestText ? Bun.TOML.parse(cargoManifestText) : null
  for (const name of Object.keys(npm).map(cargoCompanion).filter(Boolean)) {
    const versions = [...new Set(cargo.package.filter(pkg => pkg.name === name).map(pkg => pkg.version))]
    if (versions.length <= 1 && !manifest)
      continue
    const requirement = cargoDeclaration(manifest, name)?.requirement
    const matching = requirement ? versions.filter(version => matchesCargoRequirement(version, requirement)) : []
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
    return cargo && versions.cargo[cargo] ? [{ npm, cargo, npmVersion, cargoVersion: versions.cargo[cargo] }] : []
  })
}

export function assertTauriCompatibility(versions) {
  for (const { npm, cargo, npmVersion, cargoVersion } of tauriPairs(versions)) {
    if (!tauriMajorMinor(npmVersion) || tauriMajorMinor(npmVersion) !== tauriMajorMinor(cargoVersion))
      throw new Error(`Tauri major/minor mismatch: ${npm} ${npmVersion}, ${cargo} ${cargoVersion}`)
  }
}

export function reconcileCargoRequirement(text, name, target, { exact = false } = {}) {
  const declaration = cargoDeclaration(Bun.TOML.parse(text), name)
  const requirement = declaration?.requirement
  if (!requirement)
    throw new Error(`Missing direct Tauri requirement: ${name}`)
  if (!exact && matchesCargoRequirement(target, requirement))
    return text
  const pattern = new RegExp(`^(\\s*${RegExp.escape(declaration.key)}\\s*=\\s*)(.+)$`, 'gm')
  const matches = [...text.matchAll(pattern)]
  if (matches.length !== 1)
    throw new Error(`Expected one Tauri manifest entry for ${name}.`)
  return text.replace(pattern, (_line, prefix, value) => {
    const versionPattern = value.startsWith('"') ? /^"[^"]+"/ : /\bversion\s*=\s*"[^"]+"/
    if (!versionPattern.test(value))
      throw new Error(`Cannot locate the Tauri requirement for ${name}.`)
    const version = `${exact ? '=' : ''}${target}`
    return prefix + value.replace(versionPattern, value.startsWith('"') ? `"${version}"` : `version = "${version}"`)
  })
}

export async function reconcileTauri({ root, trusted, run, security = false, upgrades = [], request = fetch }) {
  const proposed = readTauriVersions(root)
  const baseline = readTauriVersions(trusted)
  const prepared = JSON.parse(readFileSync(resolve(trusted, '.tmp/renovate-prepared.json'), 'utf8'))
  const policy = JSON.parse(readFileSync(resolve(trusted, 'renovate.json'), 'utf8'))
  const pairs = tauriPairs(proposed).map((pair) => {
    if (!baseline.npm[pair.npm] || !baseline.cargo[pair.cargo])
      throw new Error(`Cannot find trusted Tauri baseline for ${pair.npm}.`)
    return { ...pair, npmVersion: baseline.npm[pair.npm], cargoVersion: baseline.cargo[pair.cargo] }
  })
  const targets = await planTauriTargets(pairs, { policy, alerts: prepared.alerts, upgrades, baseline, proposed, security, preparedTargets: security ? prepared.securityTargets : null, request })
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
    const group = Object.hasOwn(manifest.dependencies ?? {}, pair.npm) ? 'dependencies' : 'devDependencies'
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
