#!/usr/bin/env bun
import { spawnSync } from 'node:child_process'
import { mkdirSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import process from 'node:process'
import { alertedPackages, readAlerts } from './dependabot-alerts.mjs'
import { renovateModule, requireRenovate } from './renovate-engine.mjs'
import { buildAuthorizedMap, defaultReaders, PREPARED_LOCKFILES } from './renovate-exemptions.mjs'
import { dependencyIgnorePaths } from './renovate-inputs.mjs'
import { bunIdentity, dependencyLockfile, parseBunLock, readLockedVersions, readWorkingFile } from './renovate-lockfile-versions.mjs'
import { NPM_REGISTRY, readRegistryJson } from './renovate-registry.mjs'
import { isPublishedBy, publishTimeCutoff, releaseAgeMs } from './renovate-release-age.mjs'
import { planTauriTargets } from './renovate-tauri-graph.mjs'
import { assertTauriCompatibility, readTauriVersions, tauriAuthorizedRoots, tauriPairs } from './renovate-tauri.mjs'

const semver = requireRenovate('semver')

const { init: initializeRenovateLogger } = await renovateModule('logger/index.js')

const SOLID_REFRESH_COMPATIBILITY = Object.freeze({
  matchManagers: ['bun'],
  matchFileNames: ['frontend/package.json'],
  matchPackageNames: ['solid-refresh'],
})

export function solidRefreshCompatibilityRule(packument, { currentVersion, pluginVersionRange, policy, now = Date.now() }) {
  const versions = packument?.versions
  const times = packument?.time
  if (!versions || typeof versions !== 'object' || !times || typeof times !== 'object')
    throw new TypeError('Invalid vite-plugin-solid npm packument.')
  const current = semver.valid(currentVersion)
  const pluginRange = semver.validRange(pluginVersionRange)
  if (!current || !pluginRange || !versions[current])
    throw new Error('Cannot determine the current vite-plugin-solid compatibility range.')
  const cutoff = publishTimeCutoff(releaseAgeMs(policy), now).getTime()
  const proposed = Object.keys(versions)
    .filter((version) => {
      const parsed = semver.valid(version)
      return parsed !== null
        && semver.prerelease(parsed) === null
        && semver.satisfies(parsed, pluginRange)
        && semver.gt(parsed, current)
        && isPublishedBy(Date.parse(times[version]), cutoff)
    })
    .sort(semver.rcompare)[0] ?? current
  const range = versions[proposed]?.dependencies?.['solid-refresh']
  if (typeof range !== 'string' || semver.validRange(range) === null)
    throw new Error(`vite-plugin-solid@${proposed} has no valid solid-refresh dependency range.`)
  return {
    ...SOLID_REFRESH_COMPATIBILITY,
    allowedVersions: range,
  }
}

export function securityCompanions(targets, vulnerabilityAlerts) {
  const force = { ...vulnerabilityAlerts }
  // vulnerabilityFixStrategy belongs only in vulnerabilityAlerts. The prepared exact
  // targets already select the lowest compatible fixed versions for companions.
  delete force.vulnerabilityFixStrategy
  // Match only the affected pair. Other Tauri packages retain routine major approval.
  return Object.entries(targets).map(([name, version]) => ({
    matchPackageNames: [name],
    matchFileNames: [
      name.startsWith('@tauri-apps/')
        ? 'frontend/package.json'
        : 'desktop/rust/Cargo.toml',
    ],
    allowedVersions: `=${version}`,
    isVulnerabilityAlert: true,
    force,
  }))
}

export async function prepareTauriSecurity(alerts, config, baseline, { authorized = {}, request, now = Date.now() } = {}) {
  const pairs = tauriPairs(baseline)
  const targets = await planTauriTargets(pairs, {
    policy: config,
    alerts,
    baseline,
    security: true,
    authorizedRoots: tauriAuthorizedRoots(authorized, pairs),
    request,
    now,
  })
  if (!targets)
    return {}
  return Object.fromEntries(targets.flatMap(({ pair, target }) => target.npm === pair.npmVersion && target.cargo === pair.cargoVersion
    ? []
    : [
        [
          pair.npm,
          target.npm,
        ],
        [
          pair.cargo,
          target.cargo,
        ],
      ]))
}

/**
 * Authorize the fixes of the open alerts, for each lockfile of PREPARED_LOCKFILES: for each
 * package that an open alert reports on that lockfile, and that the trusted lockfile
 * locks, the exact patched versions and their exact pins. `read(path)` returns the text of
 * a trusted file, or null when the file is absent. A local run reads no alert and no
 * lockfile, and authorizes nothing.
 */
export async function prepareAuthorized(alerts, { local = false, read, readers = defaultReaders } = {}) {
  if (local)
    return buildAuthorizedMap([], readers, { lockfiles: PREPARED_LOCKFILES })
  const alerted = alertedPackages(alerts)
  const packages = []
  for (const path of PREPARED_LOCKFILES.filter(candidate => alerted.has(candidate))) {
    const versions = await readLockedVersions(dependencyLockfile(path).kind, read(path))
    for (const name of [
      ...alerted.get(path),
    ].sort()) {
      const baseVersions = versions.get(name)
      if (baseVersions?.size) {
        packages.push({
          lockfile: path,
          name,
          baseVersions: [...baseVersions],
        })
      }
    }
  }
  return buildAuthorizedMap(packages, readers, { lockfiles: PREPARED_LOCKFILES })
}

if (import.meta.main) {
  try {
    await initializeRenovateLogger()
    const root = resolve(import.meta.dirname, '..')
    const config = await Bun.file(resolve(root, 'renovate.json')).json()
    const local = process.argv.includes('--local')
    const frontendManifest = JSON.parse(readWorkingFile(root, 'frontend/package.json'))
    const frontendLock = parseBunLock(readWorkingFile(root, 'frontend/bun.lock'))
    const pluginIdentity = bunIdentity(frontendLock.packages?.['vite-plugin-solid']?.[0])
    if (!pluginIdentity)
      throw new Error('Cannot determine the locked vite-plugin-solid version.')
    const solidRefreshRules = local
      ? []
      : [
          solidRefreshCompatibilityRule(
            await readRegistryJson(`${NPM_REGISTRY}/vite-plugin-solid`, { description: 'vite-plugin-solid npm packument read' }),
            {
              currentVersion: pluginIdentity.version,
              pluginVersionRange: frontendManifest.devDependencies?.['vite-plugin-solid'],
              policy: config,
            },
          ),
        ]
    const alerts = local
      ? []
      : await readAlerts(process.env.GH_TOKEN)
    const authorized = await prepareAuthorized(alerts, {
      local,
      read: path => readWorkingFile(root, path),
    })
    const baseline = readTauriVersions(root)
    assertTauriCompatibility(baseline)
    const securityTargets = await prepareTauriSecurity(alerts, config, baseline, { authorized })
    const listed = spawnSync('git', ['ls-files', '-z'], { cwd: root, encoding: 'utf8', maxBuffer: 16 * 1024 * 1024 })
    if (listed.status !== 0)
      throw new Error('Cannot enumerate tracked Go files.')
    const files = listed.stdout.split('\0').filter(Boolean)
    const importFiles = files.filter(file => /^(?:backend|desktop\/go|site)\//.test(file) && file.endsWith('.go') && !file.split('/').includes('generated'))
    mkdirSync(resolve(root, '.tmp'), { recursive: true })
    writeFileSync(resolve(root, '.tmp/renovate-prepared.json'), JSON.stringify({
      packageRules: [
        ...securityCompanions(securityTargets, config.vulnerabilityAlerts),
        ...solidRefreshRules,
      ],
      securityTargets,
      importFiles,
      ignorePaths: await dependencyIgnorePaths(files, config),
      alerts,
      authorized,
    }))
  }
  catch (error) {
    console.error(error.message)
    process.exitCode = 1
  }
}
