#!/usr/bin/env bun
import { spawnSync } from 'node:child_process'
import { mkdirSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import process from 'node:process'
import { alertedPackages, readAlerts } from './dependabot-alerts.mjs'
import { renovateModule } from './renovate-engine.mjs'
import { buildAuthorizedMap, defaultReaders, PREPARED_LOCKFILES } from './renovate-exemptions.mjs'
import { dependencyIgnorePaths } from './renovate-inputs.mjs'
import { dependencyLockfile, readLockedVersions, readWorkingFile } from './renovate-lockfile-versions.mjs'
import { planTauriTargets } from './renovate-tauri-graph.mjs'
import { assertTauriCompatibility, readTauriVersions, tauriAuthorizedRoots, tauriPairs } from './renovate-tauri.mjs'

const { init: initializeRenovateLogger } = await renovateModule('logger/index.js')

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
        [pair.npm, target.npm],
        [pair.cargo, target.cargo],
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
      packageRules: securityCompanions(securityTargets, config.vulnerabilityAlerts),
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
