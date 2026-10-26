import { readFileSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'

function minor(version) {
  return /^(\d+\.\d+)\./.exec(version)?.[1]
}

export function fixedTauriVersion(alert) {
  const version = alert.security_vulnerability?.first_patched_version?.identifier
  if (version == null)
    return null
  if (typeof version !== 'string' || !/^\d+\.\d+\.\d+(?:-[0-9A-Z.-]+)?(?:\+[0-9A-Z.-]+)?$/i.test(version))
    throw new Error('The Tauri alert has an invalid fixed version.')
  return version
}

export function tauriReleaseAgeMs(config, security = false) {
  const policy = security ? { ...config, ...config.vulnerabilityAlerts } : config
  const days = /^(\d+) days$/.exec(policy.minimumReleaseAge ?? '')
  if (!days || policy.minimumReleaseAgeBuffer != null)
    throw new Error('Tauri preparation requires a whole-day release age without an additional buffer.')
  const age = Number(days[1]) * 86400000
  if (!Number.isSafeInteger(age) || age <= 0)
    throw new Error('Tauri release age must be a positive safe duration.')
  return age
}

export function eligibleTauriReleases(releases, minimum, { now = Date.now(), minimumAgeMs }) {
  if (!Number.isSafeInteger(minimumAgeMs) || minimumAgeMs <= 0)
    throw new Error('Tauri release filtering requires a positive minimum age.')
  return releases.filter(release => /^\d+\.\d+\.\d+$/.test(release.version)
    && !release.yanked && Number.isFinite(Date.parse(release.publishedAt))
    && now - Date.parse(release.publishedAt) >= minimumAgeMs
    && Bun.semver.order(release.version, minimum) >= 0)
}

export async function readTauriReleases(name, request = fetch) {
  const npm = name.startsWith('@tauri-apps/')
  const url = npm ? `https://registry.npmjs.org/${encodeURIComponent(name)}` : `https://crates.io/api/v1/crates/${encodeURIComponent(name)}`
  const response = await request(url, { headers: { 'User-Agent': 'leapmux-dependency-maintenance' }, signal: AbortSignal.timeout(30000) })
  if (!response.ok)
    throw new Error(`Tauri companion metadata read failed: ${response.status} ${url}`)
  const metadata = await response.json()
  return npm
    ? Object.entries(metadata.time).map(([version, publishedAt]) => ({ version, publishedAt }))
    : metadata.versions.map(release => ({ version: release.num, publishedAt: release.created_at, yanked: release.yanked }))
}

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
  const npm = Object.fromEntries(Object.entries(bun.packages).filter(([name]) => name.startsWith('@tauri-apps/')).map(([name, value]) => [name, value[0].slice(value[0].lastIndexOf('@') + 1)]))
  const resolved = Object.fromEntries(cargo.package.filter(pkg => pkg.name.startsWith('tauri')).map(pkg => [pkg.name, pkg.version]))
  const manifest = cargoManifestText ? Bun.TOML.parse(cargoManifestText) : null
  for (const name of Object.keys(npm).map(cargoCompanion).filter(Boolean)) {
    const versions = [...new Set(cargo.package.filter(pkg => pkg.name === name).map(pkg => pkg.version))]
    if (versions.length <= 1)
      continue
    const requirement = cargoDeclaration(manifest, name)?.requirement
    const matching = requirement ? versions.filter(version => Bun.semver.satisfies(version, requirement)) : []
    if (matching.length !== 1)
      throw new Error(`Cannot identify the direct locked Tauri version for ${name}.`)
    resolved[name] = matching[0]
  }
  return { npm, cargo: resolved }
}

export function tauriPairs(versions) {
  return Object.entries(versions.npm).flatMap(([npm, npmVersion]) => {
    const cargo = cargoCompanion(npm)
    return cargo && versions.cargo[cargo] ? [{ npm, cargo, npmVersion, cargoVersion: versions.cargo[cargo] }] : []
  })
}

export function assertTauriCompatibility(versions) {
  for (const { npm, cargo, npmVersion, cargoVersion } of tauriPairs(versions)) {
    if (!minor(npmVersion) || minor(npmVersion) !== minor(cargoVersion))
      throw new Error(`Tauri major/minor mismatch: ${npm} ${npmVersion}, ${cargo} ${cargoVersion}`)
  }
}

/** Find a compatible pair without downgrading main or bypassing release age. */
export function findTauriPair(npmReleases, cargoReleases, { npmMinimum, cargoMinimum, security = false, now = Date.now(), minimumAgeMs, maximumMajor = Math.max(Number(npmMinimum.split('.')[0]), Number(cargoMinimum.split('.')[0])) }) {
  if (!Number.isSafeInteger(maximumMajor) || maximumMajor < 0)
    throw new Error('The selected Tauri major must be a nonnegative safe integer.')
  const eligible = (releases, minimum) => eligibleTauriReleases(releases, minimum, { now, minimumAgeMs })
    .filter(release => security || Number(release.version.split('.')[0]) <= maximumMajor)
    .sort((a, b) => Bun.semver.order(a.version, b.version) * (security ? 1 : -1))
  const npm = eligible(npmReleases, npmMinimum)
  const cargo = eligible(cargoReleases, cargoMinimum)
  for (const npmRelease of npm) {
    const cargoRelease = cargo.find(release => minor(release.version) === minor(npmRelease.version))
    if (cargoRelease)
      return { npm: npmRelease.version, cargo: cargoRelease.version }
  }
  return null
}

export function chooseTauriPair(npmReleases, cargoReleases, options) {
  const pair = findTauriPair(npmReleases, cargoReleases, options)
  if (!pair)
    throw new Error('No compatible Tauri pair satisfies the release age and required versions.')
  return pair
}

export function reconcileCargoRequirement(text, name, target) {
  const declaration = cargoDeclaration(Bun.TOML.parse(text), name)
  const requirement = declaration?.requirement
  if (!requirement)
    throw new Error(`Missing direct Tauri requirement: ${name}`)
  if (Bun.semver.satisfies(target, requirement))
    return text
  const pattern = new RegExp(`^(\\s*${RegExp.escape(declaration.key)}\\s*=\\s*)(.+)$`, 'gm')
  const matches = [...text.matchAll(pattern)]
  if (matches.length !== 1)
    throw new Error(`Expected one Tauri manifest entry for ${name}.`)
  return text.replace(pattern, (_line, prefix, value) => {
    const versionPattern = value.startsWith('"') ? /^"[^"]+"/ : /\bversion\s*=\s*"[^"]+"/
    if (!versionPattern.test(value))
      throw new Error(`Cannot locate the Tauri requirement for ${name}.`)
    return prefix + value.replace(versionPattern, value.startsWith('"') ? `"${target}"` : `version = "${target}"`)
  })
}

export async function reconcileTauri({ root, trusted, run, security = false, upgrades = [], request = fetch }) {
  const readVersions = directory => tauriVersions(readFileSync(resolve(directory, 'frontend/bun.lock'), 'utf8'), readFileSync(resolve(directory, 'desktop/rust/Cargo.lock'), 'utf8'), readFileSync(resolve(directory, 'desktop/rust/Cargo.toml'), 'utf8'))
  const proposed = readVersions(root)
  const baseline = readVersions(trusted)
  const prepared = JSON.parse(readFileSync(resolve(trusted, '.tmp/renovate-prepared.json'), 'utf8'))
  const policy = JSON.parse(readFileSync(resolve(trusted, 'renovate.json'), 'utf8'))
  const minimum = (name, version) => prepared.alerts.filter(alert => alert.state === 'open' && alert.security_vulnerability?.package?.name === name)
    .reduce((result, alert) => {
      const fixed = fixedTauriVersion(alert)
      return fixed && Bun.semver.order(fixed, result) > 0 ? fixed : result
    }, version)
  const selected = (name, baseline) => upgrades.filter(upgrade => (upgrade.packageName ?? upgrade.depName) === name).reduce((version, upgrade) => {
    const target = upgrade.newVersion ?? upgrade.newValue?.replace(/^[~^=]/, '')
    if (!/^\d+\.\d+\.\d+(?:-[0-9A-Z.-]+)?(?:\+[0-9A-Z.-]+)?$/i.test(target ?? ''))
      throw new Error(`Missing selected Tauri version for ${name}.`)
    return Bun.semver.order(target, version) > 0 ? target : version
  }, baseline)
  let bunChanged = false
  for (const pair of tauriPairs(proposed)) {
    const npmBase = baseline.npm[pair.npm]
    const cargoBase = baseline.cargo[pair.cargo]
    if (!npmBase || !cargoBase)
      throw new Error(`Cannot find trusted Tauri baseline for ${pair.npm}.`)
    const targets = prepared.securityTargets ?? {}
    const preparedTarget = security && targets[pair.npm] && targets[pair.cargo] ? { npm: targets[pair.npm], cargo: targets[pair.cargo] } : null
    if (pair.npmVersion === npmBase && pair.cargoVersion === cargoBase && (!preparedTarget || (preparedTarget.npm === npmBase && preparedTarget.cargo === cargoBase)))
      continue
    let target = preparedTarget
    if (!target) {
      const npmSelected = selected(pair.npm, npmBase)
      const cargoSelected = selected(pair.cargo, cargoBase)
      const npmFixed = minimum(pair.npm, npmBase)
      const cargoFixed = minimum(pair.cargo, cargoBase)
      const pairSecurity = security && (npmFixed !== npmBase || cargoFixed !== cargoBase)
      const minimumAgeMs = tauriReleaseAgeMs(policy, pairSecurity)
      const [npmReleases, cargoReleases] = await Promise.all([readTauriReleases(pair.npm, request), readTauriReleases(pair.cargo, request)])
      const maximumMajor = Math.max(Number(npmSelected.split('.')[0]), Number(cargoSelected.split('.')[0]))
      target = chooseTauriPair(npmReleases, cargoReleases, { npmMinimum: pairSecurity ? npmFixed : npmSelected, cargoMinimum: pairSecurity ? cargoFixed : cargoSelected, security: pairSecurity, minimumAgeMs, maximumMajor })
    }
    if (target.npm !== pair.npmVersion) {
      const file = resolve(root, 'frontend/package.json')
      const manifest = JSON.parse(readFileSync(file, 'utf8'))
      const group = manifest.dependencies?.[pair.npm] ? 'dependencies' : 'devDependencies'
      const current = manifest[group]?.[pair.npm]
      if (!current)
        throw new Error(`Missing Tauri companion manifest entry: ${pair.npm}`)
      // An exact companion pin prevents Bun from selecting another minor through a range.
      manifest[group][pair.npm] = target.npm
      writeFileSync(file, `${JSON.stringify(manifest, null, 2)}\n`)
      bunChanged = true
    }
    const cargoFile = resolve(root, 'desktop/rust/Cargo.toml')
    const originalCargo = readFileSync(cargoFile, 'utf8')
    const updatedCargo = reconcileCargoRequirement(originalCargo, pair.cargo, target.cargo)
    if (updatedCargo !== originalCargo)
      writeFileSync(cargoFile, updatedCargo)
    if (target.cargo !== pair.cargoVersion || updatedCargo !== originalCargo)
      run('cargo', ['update', '--package', `${pair.cargo}@${pair.cargoVersion}`, '--precise', target.cargo], { cwd: 'desktop/rust' })
  }
  if (bunChanged)
    run('bun', ['install', '--ignore-scripts'], { cwd: 'frontend' })
  assertTauriCompatibility(readVersions(root))
}
