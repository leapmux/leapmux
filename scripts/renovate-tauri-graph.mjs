import { readCrateDependencies, splitIdentity } from './renovate-exemptions.mjs'
import { releaseAgeMs, securityReleaseAgeMs } from './renovate-release-age.mjs'
import { matchesCargoRequirement, readTauriReleases, tauriMajorMinor, tauriPairCandidates } from './renovate-tauri-releases.mjs'
import { lockedTauriVersions, tauriAdvisories } from './renovate-tauri-security.mjs'

function selectedVersion(upgrades, name, baseline) {
  return upgrades.filter(upgrade => (upgrade.packageName ?? upgrade.depName) === name).reduce((version, upgrade) => {
    const target = upgrade.newVersion ?? upgrade.newValue?.replace(/^[~^=]/, '')
    if (!/^\d+\.\d+\.\d+(?:-[0-9A-Z.-]+)?(?:\+[0-9A-Z.-]+)?$/i.test(target ?? ''))
      throw new Error(`Missing selected Tauri version for ${name}.`)
    return Bun.semver.order(target, version) > 0
      ? target
      : version
  }, baseline)
}

async function readDependencies(name, version, request) {
  const dependencies = await readCrateDependencies(name, version, { request })
  return dependencies.filter(dependency => dependency.kind === 'normal' && !dependency.optional).map((dependency) => {
    if (typeof dependency.crate_id !== 'string' || typeof dependency.req !== 'string')
      throw new TypeError(`Invalid Tauri dependency requirement: ${name}@${version}`)
    return { name: dependency.crate_id, requirement: dependency.req }
  })
}

function preparedPair(pair, targets, advisories) {
  const npm = targets[pair.npm]
  const cargo = targets[pair.cargo]
  if (!npm && !cargo)
    return null
  if (!npm || !cargo || !/^\d+\.\d+\.\d+$/.test(npm) || !/^\d+\.\d+\.\d+$/.test(cargo)
    || tauriMajorMinor(npm) !== tauriMajorMinor(cargo)
    || Bun.semver.order(npm, pair.npmVersion) < 0 || Bun.semver.order(cargo, pair.cargoVersion) < 0) {
    throw new Error(`Invalid prepared Tauri pair: ${pair.npm}`)
  }
  if ((npm !== pair.npmVersion && !advisories.isSafe(pair.npm, npm)) || (cargo !== pair.cargoVersion && !advisories.isSafe(pair.cargo, cargo)))
    throw new Error(`A prepared Tauri target is still vulnerable: ${pair.npm}`)
  return { npm, cargo }
}

/**
 * Select the whole companion graph before any manifest or lockfile changes. Every changed
 * version is old enough, an authorized root, or in the closure of an authorized root that
 * the same plan introduces. `authorizedRoots` maps each root to the set of its closure.
 */
export async function planTauriTargets(pairs, { policy, alerts = [], upgrades = [], baseline, proposed, security = false, preparedTargets = null, authorizedRoots = new Map(), request, now = Date.now() }) {
  if (!(authorizedRoots instanceof Map) || [
    ...authorizedRoots.values(),
  ].some(closure => !(closure instanceof Set))) {
    throw new TypeError('The authorized Tauri roots require a map from each root to the set of its closure.')
  }
  const advisories = tauriAdvisories(alerts)
  if (preparedTargets && Object.keys(preparedTargets).length > 0 && !security)
    throw new Error('Prepared Tauri security targets require security processing.')
  const maximum = (a, b) => Bun.semver.order(a, b) >= 0
    ? a
    : b
  const nativeRemediation = (name, selected) => security && proposed
    && upgrades.some(upgrade => (upgrade.packageName ?? upgrade.depName) === name && upgrade.isVulnerabilityAlert)
    && advisories.isSafe(name, selected) && lockedTauriVersions(proposed, name).includes(selected)
    && lockedTauriVersions(baseline, name).some(version => advisories.isAffected(name, version) && !lockedTauriVersions(proposed, name).includes(version))
  const contexts = pairs.map((pair) => {
    const npmSelected = maximum(selectedVersion(upgrades, pair.npm, pair.npmVersion), proposed?.npm[pair.npm] ?? pair.npmVersion)
    const cargoSelected = maximum(selectedVersion(upgrades, pair.cargo, pair.cargoVersion), proposed?.cargo[pair.cargo] ?? pair.cargoVersion)
    const approved = upgrades.filter(upgrade => !upgrade.isVulnerabilityAlert)
    const maximumMajor = Math.max(Number(selectedVersion(approved, pair.npm, pair.npmVersion).split('.')[0]), Number(selectedVersion(approved, pair.cargo, pair.cargoVersion).split('.')[0]))
    const nativeChanged = proposed && (proposed.npm[pair.npm] !== pair.npmVersion || proposed.cargo[pair.cargo] !== pair.cargoVersion)
    const prepared = preparedPair(pair, preparedTargets ?? {}, advisories)
    if (prepared && (Bun.semver.order(prepared.npm, npmSelected) < 0 || Bun.semver.order(prepared.cargo, cargoSelected) < 0))
      throw new Error(`The prepared Tauri pair is below the selected update: ${pair.npm}`)
    const useAdvisory = security && (!preparedTargets || nativeChanged || prepared)
    const npmFixed = useAdvisory
      ? advisories.minimum(pair.npm, pair.npmVersion)
      : null
    const cargoFixed = useAdvisory
      ? advisories.minimum(pair.cargo, pair.cargoVersion)
      : null
    const securityRoot = !!(security && (npmFixed || cargoFixed || prepared || nativeRemediation(pair.npm, npmSelected) || nativeRemediation(pair.cargo, cargoSelected)))
    return {
      pair,
      prepared,
      npmSelected,
      cargoSelected,
      npmFixed,
      cargoFixed,
      maximumMajor,
      securityRoot,
      changed: !!(nativeChanged || npmSelected !== pair.npmVersion || cargoSelected !== pair.cargoVersion || npmFixed || cargoFixed || prepared),
    }
  })
  if (!contexts.some(context => context.changed))
    return []
  if (security && contexts.every(context => !context.changed || context.prepared))
    return contexts.map(({ pair, prepared }) => ({ pair, target: prepared ?? { npm: pair.npmVersion, cargo: pair.cargoVersion } }))

  // Registry reads for independent pairs overlap; all package-manager writes remain sequential.
  const hasSecurityRoot = contexts.some(context => context.securityRoot)
  // A security fix waits as a routine update does, except for the authorized exemptions,
  // which wait for the release age of a security fix.
  const age = releaseAgeMs(policy)
  const securityAge = securityReleaseAgeMs(policy)
  const loadDomain = async (context) => {
    const { pair, prepared, npmSelected, cargoSelected, npmFixed, cargoFixed, maximumMajor } = context
    if (prepared) {
      return {
        ...context,
        candidates: [prepared],
        expanded: true,
      }
    }
    const [npmReleases, cargoReleases] = await Promise.all([
      readTauriReleases(pair.npm, request),
      readTauriReleases(pair.cargo, request),
    ])
    const minimum = (selected, fixed) => fixed && Bun.semver.order(fixed, selected) > 0
      ? fixed
      : selected
    const candidates = tauriPairCandidates(npmReleases, cargoReleases, {
      npmMinimum: minimum(npmSelected, npmFixed),
      cargoMinimum: minimum(cargoSelected, cargoFixed),
      existing: {
        npm: !npmFixed && npmSelected === pair.npmVersion
          ? pair.npmVersion
          : undefined,
        cargo: !cargoFixed && cargoSelected === pair.cargoVersion
          ? pair.cargoVersion
          : undefined,
      },
      npmName: pair.npm,
      cargoName: pair.cargo,
      security: hasSecurityRoot,
      preferLowest: context.securityRoot || !context.changed,
      safe: (name, version) => advisories.isSafe(name, version) || (name === pair.npm
        ? !npmFixed && npmSelected === pair.npmVersion && version === pair.npmVersion
        : !cargoFixed && cargoSelected === pair.cargoVersion && version === pair.cargoVersion),
      now,
      minimumAgeMs: age,
      securityMinimumAgeMs: securityAge,
      authorized: authorizedRoots,
      maximumMajor,
    })
    candidates.sort((a, b) => Number(Number(a.cargo.split('.')[0]) > maximumMajor) - Number(Number(b.cargo.split('.')[0]) > maximumMajor))
    const baseline = { npm: pair.npmVersion, cargo: pair.cargoVersion }
    const mayKeepBaseline = npmSelected === pair.npmVersion && cargoSelected === pair.cargoVersion
    if (mayKeepBaseline && (!context.changed || (security && (!context.securityRoot || candidates.length === 0))))
      candidates.unshift(baseline)
    return { ...context, candidates, expanded: true }
  }
  const domains = await Promise.all(contexts.map(context => context.changed
    ? loadDomain(context)
    : {
        ...context,
        candidates: [
          { npm: context.pair.npmVersion, cargo: context.pair.cargoVersion },
        ],
        expanded: false,
      }))
  if (domains.some(domain => domain.candidates.length === 0))
    return null
  if (domains.every(({ pair, candidates }) => candidates.length === 1 && candidates[0].npm === pair.npmVersion && candidates[0].cargo === pair.cargoVersion))
    return []
  // Plugins constrain the core. Visiting them first avoids trying core patches that cannot satisfy them.
  domains.sort((a, b) => Number(a.pair.cargo === 'tauri') - Number(b.pair.cargo === 'tauri'))
  const dependencyCache = new Map()
  const dependencies = (name, version) => {
    const key = `${name}@${version}`
    if (!dependencyCache.has(key))
      dependencyCache.set(key, readDependencies(name, version, request))
    return dependencyCache.get(key)
  }
  const assigned = []
  const authorizedMajors = () => {
    const authorized = new Set(assigned.filter(entry => entry.securityRoot).map(entry => entry.pair.cargo))
    let changed = true
    while (changed) {
      changed = false
      for (const entry of assigned.filter(entry => authorized.has(entry.pair.cargo))) {
        for (const dependency of entry.requirements) {
          const peer = assigned.find(candidate => candidate.pair.cargo === dependency.name)
          if (peer && !authorized.has(peer.pair.cargo) && !matchesCargoRequirement(peer.pair.cargoVersion, dependency.requirement)) {
            authorized.add(peer.pair.cargo)
            changed = true
          }
        }
      }
    }
    return assigned.every(entry => Number(entry.target.cargo.split('.')[0]) <= entry.maximumMajor || authorized.has(entry.pair.cargo))
  }
  // A conditional release needs one of its roots, introduced by the same plan. An
  // unassigned domain may still supply a root: a root's domain can follow the release
  // that it authorizes, because the order puts plugins first, not dependencies.
  const supplies = (root) => {
    const { name, version } = splitIdentity(root)
    const domain = domains.find(candidate => candidate.pair.npm === name || candidate.pair.cargo === name)
    if (!domain)
      return false
    const entry = assigned.find(candidate => candidate.pair === domain.pair)
    if (!entry)
      return true
    return domain.pair.npm === name
      ? entry.target.npm === version && version !== entry.pair.npmVersion
      : entry.target.cargo === version && version !== entry.pair.cargoVersion
  }
  const authorizationPossible = () => assigned.every(entry => (entry.target.requires ?? []).every(roots => [...roots].some(supplies)))
  const search = async (index) => {
    if (index === domains.length) {
      return authorizedMajors() && authorizationPossible()
        ? assigned.map(entry => ({
            pair: entry.pair,
            target: { npm: entry.target.npm, cargo: entry.target.cargo },
          }))
        : null
    }
    const { pair, candidates, maximumMajor, securityRoot } = domains[index]
    for (const target of candidates) {
      const requirements = await dependencies(pair.cargo, target.cargo)
      const compatible = assigned.every(entry => requirements.every(dependency => dependency.name !== entry.pair.cargo || matchesCargoRequirement(entry.target.cargo, dependency.requirement))
        && entry.requirements.every(dependency => dependency.name !== pair.cargo || matchesCargoRequirement(target.cargo, dependency.requirement)))
      if (!compatible)
        continue
      assigned.push({
        pair,
        target,
        requirements,
        maximumMajor,
        securityRoot,
      })
      const result = authorizationPossible()
        ? await search(index + 1)
        : null
      if (result)
        return result
      assigned.pop()
    }
    if (!domains[index].expanded) {
      domains[index] = await loadDomain(domains[index])
      return search(index)
    }
    return null
  }
  return search(0)
}
