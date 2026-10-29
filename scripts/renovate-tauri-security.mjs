import { renovateModule } from './renovate-engine.mjs'

const { api: npmVersioning } = await renovateModule('modules/versioning/npm/index.js')
export function fixedTauriVersion(alert) {
  const version = alert.security_vulnerability?.first_patched_version?.identifier
  if (version == null)
    return null
  if (typeof version !== 'string' || !/^\d+\.\d+\.\d+(?:-[0-9A-Z.-]+)?(?:\+[0-9A-Z.-]+)?$/i.test(version))
    throw new Error('The Tauri alert has an invalid fixed version.')
  return version
}

export function lockedTauriVersions(versions, name) {
  const ecosystem = name.startsWith('@tauri-apps/')
    ? 'npm'
    : 'cargo'
  return versions?.occurrences?.[ecosystem]?.[name] ?? (versions?.[ecosystem]?.[name]
    ? [versions[ecosystem][name]]
    : [])
}

/** Preserve all matching advisory ranges, including separately patched release lines. */
export function tauriAdvisories(alerts) {
  if (!Array.isArray(alerts))
    throw new TypeError('Dependabot alerts must be an array.')
  const packages = new Map()
  for (const alert of alerts) {
    const primary = alert.security_vulnerability
    const name = primary?.package?.name
    if (alert.state !== 'open' || !/^(?:@tauri-apps\/|tauri(?:-|$))/.test(name ?? ''))
      continue
    const npm = name.startsWith('@tauri-apps/')
    const ecosystem = npm ? 'npm' : 'rust'
    const paths = npm
      ? ['frontend/package.json', 'frontend/bun.lock']
      : ['desktop/rust/Cargo.toml', 'desktop/rust/Cargo.lock']
    if (primary.package.ecosystem !== ecosystem || !paths.includes(alert.dependency?.manifest_path))
      continue
    const vulnerabilities = alert.security_advisory?.vulnerabilities ?? []
    if (!Array.isArray(vulnerabilities))
      throw new TypeError(`Invalid advisory ranges for ${name}.`)
    const records = [
      primary,
      ...vulnerabilities.filter(entry => entry.package?.name === name && entry.package.ecosystem === ecosystem),
    ]
    const ranges = packages.get(name) ?? []
    for (const record of records) {
      const raw = record.vulnerable_version_range
      const range = typeof raw === 'string'
        ? raw.replaceAll(',', ' ')
        : ''
      if (!range || !npmVersioning.isValid(range))
        throw new Error(`Invalid vulnerable version range for ${name}.`)
      const fixed = fixedTauriVersion({ security_vulnerability: record })
      if (!ranges.some(entry => entry.range === range && entry.fixed === fixed))
        ranges.push({ range, fixed })
    }
    packages.set(name, ranges)
  }
  const affected = (name, version) => (packages.get(name) ?? []).filter(entry => npmVersioning.matches(version, entry.range))
  return {
    isAffected: (name, version) => affected(name, version).length !== 0,
    isSafe: (name, version) => affected(name, version).length === 0,
    minimum: (name, version) => {
      const records = affected(name, version)
      if (!records.length || records.some(entry => entry.fixed === null))
        return null
      return records.reduce((result, entry) => Bun.semver.order(entry.fixed, result) > 0
        ? entry.fixed
        : result, version)
    },
  }
}
