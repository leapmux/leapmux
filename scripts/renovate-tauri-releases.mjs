import { renovateModule } from './renovate-engine.mjs'

const { api: cargoVersioning } = await renovateModule('modules/versioning/cargo/index.js')
export function tauriMajorMinor(version) {
  return /^(\d+\.\d+)\./.exec(version)?.[1]
}

export function matchesCargoRequirement(version, requirement) {
  if (typeof requirement !== 'string' || !cargoVersioning.isValid(requirement))
    throw new Error(`Invalid Cargo version requirement: ${requirement}`)
  return cargoVersioning.matches(version, requirement)
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
  if (npm ? !metadata.time || typeof metadata.time !== 'object' : !Array.isArray(metadata.versions))
    throw new Error(`Invalid Tauri release metadata: ${name}`)
  return npm
    ? Object.entries(metadata.time).map(([version, publishedAt]) => ({ version, publishedAt }))
    : metadata.versions.map(release => ({ version: release.num, publishedAt: release.created_at, yanked: release.yanked }))
}

/** List compatible pairs without downgrading main or bypassing release age. */
export function tauriPairCandidates(npmReleases, cargoReleases, { npmMinimum, cargoMinimum, existing = {}, security = false, preferLowest = security, safe = () => true, npmName, cargoName, now = Date.now(), minimumAgeMs, maximumMajor = Math.max(Number(npmMinimum.split('.')[0]), Number(cargoMinimum.split('.')[0])) }) {
  if (!Number.isSafeInteger(maximumMajor) || maximumMajor < 0)
    throw new Error('The selected Tauri major must be a nonnegative safe integer.')
  const direction = preferLowest ? 1 : -1
  const eligible = (releases, minimum, name, current) => {
    const candidates = eligibleTauriReleases(releases, minimum, { now, minimumAgeMs })
    // Keeping a trusted locked version introduces no release; every changed version still needs a timestamp.
    if (current && cargoVersioning.isVersion(current) && Bun.semver.order(current, minimum) >= 0 && !candidates.some(release => release.version === current))
      candidates.push({ version: current })
    return candidates.filter(release => (security || Number(release.version.split('.')[0]) <= maximumMajor) && safe(name, release.version))
      .sort((a, b) => Bun.semver.order(a.version, b.version) * direction)
  }
  const npm = eligible(npmReleases, npmMinimum, npmName, existing.npm)
  const cargo = eligible(cargoReleases, cargoMinimum, cargoName, existing.cargo)
  const npmByMinor = new Map()
  for (const npmRelease of npm) {
    const minor = tauriMajorMinor(npmRelease.version)
    if (!npmByMinor.has(minor))
      npmByMinor.set(minor, npmRelease.version)
  }
  return cargo.flatMap(release => npmByMinor.has(tauriMajorMinor(release.version)) ? [{ npm: npmByMinor.get(tauriMajorMinor(release.version)), cargo: release.version }] : [])
    .sort((a, b) => (Bun.semver.order(a.npm, b.npm) || Bun.semver.order(a.cargo, b.cargo)) * direction)
}
