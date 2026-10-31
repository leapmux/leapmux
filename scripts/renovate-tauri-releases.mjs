import { renovateModule } from './renovate-engine.mjs'
import { readCrateReleases, readNpmReleases } from './renovate-registry.mjs'
import { isPublishedBy } from './renovate-release-age.mjs'

const { api: cargoVersioning } = await renovateModule('modules/versioning/cargo/index.js')
export function tauriMajorMinor(version) {
  return /^(\d+\.\d+)\./.exec(version)?.[1]
}

export function matchesCargoRequirement(version, requirement) {
  if (typeof requirement !== 'string' || !cargoVersioning.isValid(requirement))
    throw new Error(`Invalid Cargo version requirement: ${requirement}`)
  return cargoVersioning.matches(version, requirement)
}

/**
 * Return the releases at or above the minimum that the policy admits. `authorizedBy` is
 * null for a release that needs no other selection: an old-enough release or an authorized
 * root. A young release in the closure of authorized roots lists those roots, and a plan
 * may select it only together with one of them. An authorized root or closure member must
 * still meet the release age of a security fix, `securityMinimumAgeMs`, unless it has no
 * publication time: Renovate holds a younger security fix back, as its
 * `timestamp-optional` behaviour admits a fix without a timestamp. Every other young
 * release is excluded.
 */
export function eligibleTauriReleases(releases, minimum, { name, now = Date.now(), minimumAgeMs, securityMinimumAgeMs = 0, authorized = new Map() }) {
  if (!Number.isSafeInteger(minimumAgeMs) || minimumAgeMs <= 0)
    throw new Error('Tauri release filtering requires a positive minimum age.')
  if (!Number.isSafeInteger(securityMinimumAgeMs) || securityMinimumAgeMs < 0)
    throw new Error('Tauri release filtering requires a nonnegative security age.')
  return releases.flatMap((release) => {
    if (!/^\d+\.\d+\.\d+$/.test(release.version) || release.yanked || Bun.semver.order(release.version, minimum) < 0)
      return []
    const published = Date.parse(release.publishedAt)
    if (isPublishedBy(published, now - minimumAgeMs)) {
      return [
        { ...release, authorizedBy: null },
      ]
    }
    if (Number.isFinite(published) && !isPublishedBy(published, now - securityMinimumAgeMs))
      return []
    const key = `${name}@${release.version}`
    if (authorized.has(key)) {
      return [
        { ...release, authorizedBy: null },
      ]
    }
    const roots = new Set([...authorized].filter(([, closure]) => closure.has(key)).map(([root]) => root))
    return roots.size > 0
      ? [
          { ...release, authorizedBy: roots },
        ]
      : []
  })
}

/** Read the releases of a Tauri companion: an npm package or a crate. */
export async function readTauriReleases(name, request) {
  return name.startsWith('@tauri-apps/')
    ? readNpmReleases(name, { request })
    : readCrateReleases(name, { request })
}

/**
 * List compatible pairs without downgrading main. Every changed release is old enough,
 * unless an authorized root or its closure admits it (see `eligibleTauriReleases`). A pair
 * that holds a conditionally eligible release lists, in `requires`, one set of authorizing
 * roots for each such release.
 */
export function tauriPairCandidates(npmReleases, cargoReleases, { npmMinimum, cargoMinimum, existing = {}, security = false, preferLowest = security, safe = () => true, npmName, cargoName, now = Date.now(), minimumAgeMs, securityMinimumAgeMs, authorized = new Map(), maximumMajor = Math.max(Number(npmMinimum.split('.')[0]), Number(cargoMinimum.split('.')[0])) }) {
  if (!Number.isSafeInteger(maximumMajor) || maximumMajor < 0)
    throw new Error('The selected Tauri major must be a nonnegative safe integer.')
  const direction = preferLowest
    ? 1
    : -1
  const eligible = (releases, minimum, name, current) => {
    const candidates = eligibleTauriReleases(releases, minimum, {
      name,
      now,
      minimumAgeMs,
      securityMinimumAgeMs,
      authorized,
    })
    // Keeping a trusted locked version introduces no release, so it needs no timestamp and no authorization.
    if (current && cargoVersioning.isVersion(current) && Bun.semver.order(current, minimum) >= 0) {
      const kept = candidates.find(release => release.version === current)
      if (kept)
        kept.authorizedBy = null
      else
        candidates.push({ version: current, authorizedBy: null })
    }
    return candidates.filter(release => (security || Number(release.version.split('.')[0]) <= maximumMajor) && safe(name, release.version))
      .sort((a, b) => Bun.semver.order(a.version, b.version) * direction)
  }
  const npm = eligible(npmReleases, npmMinimum, npmName, existing.npm)
  const cargo = eligible(cargoReleases, cargoMinimum, cargoName, existing.cargo)
  // Keep the first unconditional npm release of each minor. A conditional release before it
  // stays too, because its eligibility depends on a selection that the search makes later.
  const npmByMinor = new Map()
  for (const npmRelease of npm) {
    const minor = tauriMajorMinor(npmRelease.version)
    const kept = npmByMinor.get(minor) ?? []
    if (kept.some(release => release.authorizedBy === null))
      continue
    kept.push(npmRelease)
    npmByMinor.set(minor, kept)
  }
  return cargo.flatMap(cargoRelease => (npmByMinor.get(tauriMajorMinor(cargoRelease.version)) ?? []).map((npmRelease) => {
    const requires = [npmRelease, cargoRelease].flatMap(release => release.authorizedBy
      ? [release.authorizedBy]
      : [])
    return {
      npm: npmRelease.version,
      cargo: cargoRelease.version,
      ...(requires.length > 0
        ? { requires }
        : {}),
    }
  }))
    .sort((a, b) => (Bun.semver.order(a.npm, b.npm) || Bun.semver.order(a.cargo, b.cargo)) * direction)
}
