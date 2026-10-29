#!/usr/bin/env bun
import { spawnSync } from 'node:child_process'
import { mkdirSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import process from 'node:process'
import { readGithubJsonResponse } from './github-json.mjs'
import { renovateModule } from './renovate-engine.mjs'
import { dependencyIgnorePaths } from './renovate-inputs.mjs'
import { planTauriTargets } from './renovate-tauri-graph.mjs'
import { assertTauriCompatibility, readTauriVersions, tauriPairs } from './renovate-tauri.mjs'

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

export async function prepareTauriSecurity(alerts, config, baseline, { request = fetch, now = Date.now() } = {}) {
  const targets = await planTauriTargets(tauriPairs(baseline), {
    policy: config,
    alerts,
    baseline,
    security: true,
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

const REPOSITORY_API_PATH = '/repos/leapmux/leapmux'
const ALERTS_API_PATH = `${REPOSITORY_API_PATH}/dependabot/alerts`

function nextAlertPage(header, canonicalPath) {
  if (!header)
    return null
  let next = null
  for (const part of header.split(/,\s*(?=<)/)) {
    const entry = /^\s*<([^>]+)>(.*)$/.exec(part)
    const relation = entry && /;\s*rel\s*=\s*(?:"([^"]*)"|([^;\s]+))/i.exec(entry[2])
    if (!relation)
      throw new Error('Invalid Dependabot pagination header.')
    if (!(relation[1] ?? relation[2]).split(/\s+/).includes('next'))
      continue
    let url
    try {
      url = new URL(entry[1], 'https://api.github.com')
    }
    catch {
      throw new Error('Invalid Dependabot pagination URL.')
    }
    if (next || url.origin !== 'https://api.github.com' || url.username || url.password || url.hash
      || ![ALERTS_API_PATH, canonicalPath].includes(url.pathname) || url.searchParams.has('page')
      || url.searchParams.get('state') !== 'open' || url.searchParams.get('per_page') !== '100'
      || ![
        url.searchParams.get('after'),
        url.searchParams.get('before'),
      ].some(Boolean)) {
      throw new Error('Unsafe or ambiguous Dependabot pagination link.')
    }
    url.searchParams.sort()
    next = `${url.pathname}${url.search}`
  }
  return next
}

export async function readAlerts(token, request = fetch) {
  if (!token)
    throw new Error('A read-only App token is required to read active Dependabot alerts.')
  const { data: repository } = await readGithubJsonResponse(REPOSITORY_API_PATH, { token, request, description: 'Dependabot repository read' })
  if (!Number.isSafeInteger(repository?.id) || repository.id <= 0)
    throw new Error('Dependabot returned an invalid repository identity.')
  const canonicalPath = `/repositories/${repository.id}/dependabot/alerts`
  const alerts = []
  const seen = new Set()
  let path = `${ALERTS_API_PATH}?state=open&per_page=100`
  while (path) {
    if (seen.has(path))
      throw new Error('Dependabot pagination cycle detected.')
    seen.add(path)
    const { data: batch, headers } = await readGithubJsonResponse(path, { token, request, description: 'Dependabot alert read' })
    if (!Array.isArray(batch))
      throw new TypeError('Dependabot returned an invalid alert list.')
    alerts.push(...batch)
    path = nextAlertPage(headers.get('link'), canonicalPath)
  }
  return alerts
}

if (import.meta.main) {
  try {
    await initializeRenovateLogger()
    const root = resolve(import.meta.dirname, '..')
    const config = await Bun.file(resolve(root, 'renovate.json')).json()
    const alerts = process.argv.includes('--local')
      ? []
      : await readAlerts(process.env.GH_TOKEN)
    const baseline = readTauriVersions(root)
    assertTauriCompatibility(baseline)
    const securityTargets = await prepareTauriSecurity(alerts, config, baseline)
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
    }))
  }
  catch (error) {
    console.error(error.message)
    process.exitCode = 1
  }
}
