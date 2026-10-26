#!/usr/bin/env bun
import { spawnSync } from 'node:child_process'
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import process from 'node:process'
import { findTauriPair, fixedTauriVersion, readTauriReleases, tauriPairs, tauriReleaseAgeMs, tauriVersions } from './renovate-tauri.mjs'

export function securityCompanions(targets, vulnerabilityAlerts) {
  const force = { ...vulnerabilityAlerts }
  // vulnerabilityFixStrategy belongs only in vulnerabilityAlerts. The prepared exact
  // targets already select the lowest compatible fixed versions for companions.
  delete force.vulnerabilityFixStrategy
  // Match only the affected pair. Other Tauri packages retain routine major approval.
  return Object.entries(targets).map(([name, version]) => ({
    matchPackageNames: [name],
    matchFileNames: [name.startsWith('@tauri-apps/') ? 'frontend/package.json' : 'desktop/rust/Cargo.toml'],
    allowedVersions: version,
    isVulnerabilityAlert: true,
    force,
  }))
}

export async function prepareTauriSecurity(alerts, config, baseline, { request = fetch, now = Date.now() } = {}) {
  if (!Array.isArray(alerts))
    throw new Error('Dependabot alerts must be an array.')
  const grouped = new Map()
  for (const alert of alerts) {
    const name = alert.security_vulnerability?.package?.name
    if (alert.state !== 'open' || !/^(?:@tauri-apps\/|tauri(?:-|$))/.test(name ?? ''))
      continue
    const fixed = fixedTauriVersion(alert)
    if (fixed && (!grouped.has(name) || Bun.semver.order(fixed, grouped.get(name)) > 0))
      grouped.set(name, fixed)
  }
  const minimumAgeMs = tauriReleaseAgeMs(config, true)
  const minimum = (name, current) => grouped.has(name) && Bun.semver.order(grouped.get(name), current) > 0 ? grouped.get(name) : current
  const pairs = await Promise.all(tauriPairs(baseline).map(async (pair) => {
    const npmMinimum = minimum(pair.npm, pair.npmVersion)
    const cargoMinimum = minimum(pair.cargo, pair.cargoVersion)
    if (npmMinimum === pair.npmVersion && cargoMinimum === pair.cargoVersion)
      return []
    const [npmReleases, cargoReleases] = await Promise.all([readTauriReleases(pair.npm, request), readTauriReleases(pair.cargo, request)])
    const target = findTauriPair(npmReleases, cargoReleases, { npmMinimum, cargoMinimum, minimumAgeMs, security: true, now })
    return target ? [[pair.npm, target.npm], [pair.cargo, target.cargo]] : []
  }))
  return Object.fromEntries(pairs.flat())
}

export async function readAlerts(token, request = fetch) {
  if (!token)
    throw new Error('A read-only App token is required to read active Dependabot alerts.')
  const alerts = []
  for (let page = 1; ; page++) {
    const response = await request(`https://api.github.com/repos/leapmux/leapmux/dependabot/alerts?state=open&per_page=100&page=${page}`, { signal: AbortSignal.timeout(30000), headers: {
      'Authorization': `Bearer ${token}`,
      'Accept': 'application/vnd.github+json',
      'X-GitHub-Api-Version': '2022-11-28',
    } })
    if (!response.ok)
      throw new Error(`Dependabot alert read failed: ${response.status}`)
    const batch = await response.json()
    if (!Array.isArray(batch))
      throw new Error('Dependabot returned an invalid alert list.')
    alerts.push(...batch)
    if (batch.length < 100)
      return alerts
  }
}

if (import.meta.main) {
  try {
    const root = resolve(import.meta.dirname, '..')
    const config = await Bun.file(resolve(root, 'renovate.json')).json()
    const alerts = process.argv.includes('--local') ? [] : await readAlerts(process.env.GH_TOKEN)
    const baseline = tauriVersions(readFileSync(resolve(root, 'frontend/bun.lock'), 'utf8'), readFileSync(resolve(root, 'desktop/rust/Cargo.lock'), 'utf8'), readFileSync(resolve(root, 'desktop/rust/Cargo.toml'), 'utf8'))
    const securityTargets = await prepareTauriSecurity(alerts, config, baseline)
    const listed = spawnSync('git', ['ls-files', '-z', '--', 'backend', 'desktop/go', 'site'], { cwd: root, encoding: 'utf8' })
    if (listed.status !== 0)
      throw new Error('Cannot enumerate tracked Go files.')
    const importFiles = listed.stdout.split('\0').filter(file => file.endsWith('.go') && !file.split('/').includes('generated'))
    mkdirSync(resolve(root, '.tmp'), { recursive: true })
    writeFileSync(resolve(root, '.tmp/renovate-prepared.json'), JSON.stringify({
      packageRules: securityCompanions(securityTargets, config.vulnerabilityAlerts),
      securityTargets,
      importFiles,
      alerts,
    }))
  }
  catch (error) {
    console.error(error.message)
    process.exitCode = 1
  }
}
