#!/usr/bin/env bun
import { spawnSync } from 'node:child_process'
import { appendFileSync, readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import process from 'node:process'
import { needsNotice } from './notice-inputs.mjs'
import { refreshSite } from './renovate-site.mjs'
import { reconcileTauri } from './renovate-tauri.mjs'

const GO_FILES = ['backend/go.mod', 'desktop/go/go.mod', 'site/go.mod']
// Renovate uses "regex" in post-update metadata for the configured custom.regex manager.
const INPUT_FILES = { gomod: GO_FILES, bun: ['frontend/package.json', '.github/renovate/package.json'], cargo: ['desktop/rust/Cargo.toml'], regex: ['versions.env'] }
const GENPROTO = 'google.golang.org/genproto'

export function validateUpgrades(upgrades) {
  if (!Array.isArray(upgrades) || upgrades.length === 0 || upgrades.length > 10000)
    throw new Error('Post-update metadata requires a nonempty list of at most 10000 upgrades.')
  for (const upgrade of upgrades) {
    if (!upgrade || typeof upgrade !== 'object' || typeof upgrade.manager !== 'string')
      throw new Error('An upgrade must specify its manager.')
    if (!['gomod', 'bun', 'cargo', 'github-actions', 'regex'].includes(upgrade.manager))
      throw new Error(`Unsupported dependency manager: ${upgrade.manager}`)
    if (upgrade.updateType === 'lockFileMaintenance' && !['bun', 'cargo'].includes(upgrade.manager))
      throw new Error('Only Bun and Cargo support coordinated lockfile maintenance.')
    if (!(INPUT_FILES[upgrade.manager] ?? []).includes(upgrade.packageFile) && !(upgrade.manager === 'github-actions' && /^\.github\/(?:workflows\/[^/]+\.ya?ml|actions\/[^/]+\/action\.ya?ml)$/.test(upgrade.packageFile)))
      throw new Error(`Unexpected dependency path: ${upgrade.packageFile}`)
    if (upgrade.updateType === 'lockFileMaintenance')
      continue
    if (upgrade.manager === 'gomod' && (upgrade.depName === 'github.com/leapmux/leapmux' || ['golang', 'toolchain'].includes(upgrade.depType)))
      throw new Error('The self dependency and Go directives cannot change through the Go manager.')
    if (typeof upgrade.depName !== 'string' || /[\s\0]/.test(upgrade.depName) || upgrade.depName.length > 512)
      throw new Error('Invalid dependency name.')
    if (typeof upgrade.newValue !== 'string' || /[\s\0]/.test(upgrade.newValue) || upgrade.newValue.startsWith('-'))
      throw new Error(`Invalid target version for ${upgrade.depName}.`)
    if (upgrade.manager === 'gomod' && !/^v\d+\.[\w.+-]+$/.test(upgrade.newValue))
      throw new Error(`Invalid Go target version for ${upgrade.depName}.`)
    if (upgrade.manager === 'regex' && (!/^[A-Z_][A-Z0-9_]*$/.test(upgrade.depType ?? '') || upgrade.depType === 'VERSION' || !/^\d+(?:\.\d+){1,3}$/.test(upgrade.newValue)))
      throw new Error('Invalid tool or image pin metadata.')
  }
  const toolchains = upgrades.filter(upgrade => upgrade.packageFile === 'versions.env' && !['ALPINE_IMAGE', 'UBUNTU_IMAGE', 'S6_OVERLAY_VERSION'].includes(upgrade.depType))
  if (toolchains.length && (upgrades.length !== 1 || toolchains.length !== 1))
    throw new Error('A toolchain update must have its own branch.')
  return { toolchain: toolchains.length === 1 }
}

export function replacementVersion(moduleText) {
  const values = [...moduleText.matchAll(/^\s*(?:replace\s+)?google\.golang\.org\/genproto\s*=>\s*google\.golang\.org\/genproto\s+(\S+)/gm)].map(match => match[1])
  if (values.length !== 1)
    throw new Error('Each workspace module must contain exactly one genproto replacement.')
  return values[0]
}

export function verifyReplacements(texts) {
  const versions = texts.map(replacementVersion)
  if (new Set(versions).size !== 1)
    throw new Error('Conflicting genproto replacements; both modules must change together before workspace commands.')
}

export function newModulePath(upgrade) {
  const old = upgrade.depName
  const major = Number(/^v(\d+)\./.exec(upgrade.newValue)?.[1])
  if (!Number.isSafeInteger(major))
    throw new Error(`Invalid Go module version: ${upgrade.newValue}`)
  if (upgrade.newValue.endsWith('+incompatible'))
    return old
  if (old.startsWith('gopkg.in/'))
    return old.replace(/\.v\d+(?:-unstable)?$/, `.v${major}`)
  const base = old.replace(/\/v\d+$/, '')
  return major >= 2 ? `${base}/v${major}` : base
}

/** Execute each stage through an injected runner so fixtures observe ordering and failures. */
export async function postUpgrade(upgrades, { root = process.cwd(), trusted = process.env.RENOVATE_TRUSTED_ROOT, run: injectedRun, read = path => readFileSync(resolve(root, path), 'utf8'), write, reconcile = reconcileTauri } = {}) {
  const { toolchain } = validateUpgrades(upgrades)
  const run = injectedRun ?? ((command, args, options = {}) => {
    const result = spawnSync(command, args, {
      cwd: resolve(root, options.cwd ?? '.'),
      encoding: 'utf8',
      input: options.input,
      env: { ...process.env, ...(options.env ?? {}), GOTOOLCHAIN: 'local' },
      maxBuffer: 64 * 1024 * 1024,
      stdio: ['pipe', 'pipe', 'pipe'],
    })
    if (!options.capture) {
      process.stdout.write(result.stdout ?? '')
      process.stderr.write(result.stderr ?? '')
    }
    if (result.status !== 0 || result.error) {
      const details = [result.error?.message, result.stdout, result.stderr].filter(Boolean).join('\n').slice(-16384)
      throw new Error(`${command} ${args.join(' ')} failed (${result.status}): ${details}`)
    }
    return result.stdout ?? ''
  })
  if (toolchain) {
    run('task', ['sync-versions'])
    return
  }
  if (!trusted)
    throw new Error('Post-update processing requires the trusted main checkout.')
  const hasGo = upgrades.some(upgrade => upgrade.manager === 'gomod')
  const hasFrontend = upgrades.some(upgrade => upgrade.manager === 'bun' && upgrade.packageFile === 'frontend/package.json')
  const hasCargo = upgrades.some(upgrade => upgrade.manager === 'cargo')
  if (hasGo) {
    let texts = ['backend/go.mod', 'desktop/go/go.mod'].map(read)
    const targets = [...new Set(upgrades.filter(upgrade => upgrade.manager === 'gomod' && upgrade.depName === GENPROTO && upgrade.depType === 'replace').map(upgrade => upgrade.newValue))]
    if (targets.length > 1)
      throw new Error('The genproto update specifies conflicting versions.')
    // Renovate can report only one replacement occurrence. Apply its single target to both
    // module files without entering workspace mode, then validate before any workspace command.
    if (targets.length === 1) {
      for (const cwd of ['backend', 'desktop/go'])
        run('go', ['mod', 'edit', `-replace=${GENPROTO}=${GENPROTO}@${targets[0]}`], { cwd, env: { GOWORK: 'off' } })
      texts = ['backend/go.mod', 'desktop/go/go.mod'].map(read)
    }
    verifyReplacements(texts)
    const goUpgrades = upgrades.filter(upgrade => upgrade.manager === 'gomod' && upgrade.newValue && !['golang', 'toolchain'].includes(upgrade.depType))
    const changes = new Map()
    for (const upgrade of goUpgrades) {
      const target = newModulePath(upgrade)
      if (target === upgrade.depName)
        continue
      const key = JSON.stringify([upgrade.depName, target, upgrade.newValue])
      const change = changes.get(key) ?? { old: upgrade.depName, new: target, version: upgrade.newValue, directories: [] }
      change.directories = [...new Set([...change.directories, ...(upgrade.packageFile === 'site/go.mod' ? ['site'] : ['backend', 'desktop/go'])])]
      changes.set(key, change)
    }
    const unique = [...changes.values()]
    run(resolve(trusted, `.tmp/leapmux-dependency-imports${process.platform === 'win32' ? '.exe' : ''}`), [], { input: JSON.stringify(unique) })
    for (const upgrade of goUpgrades) {
      if (upgrade.depType === 'replace')
        continue
      run('go', ['mod', 'edit', `-require=${newModulePath(upgrade)}@${upgrade.newValue}`], { cwd: upgrade.packageFile.replace('/go.mod', ''), env: { GOWORK: 'off' } })
    }
    // Updated tools may import newly moved transitive modules. Resolve all metadata and
    // checksums before go tool sqlc or any other updated tool runs.
    for (const cwd of ['backend', 'desktop/go', 'site'])
      run('go', ['mod', 'download', 'all'], { cwd, env: { GOWORK: 'off', GOFLAGS: '-mod=mod' } })
    run('go', ['work', 'sync'])
    verifyReplacements(['backend/go.mod', 'desktop/go/go.mod'].map(read))
  }
  if (hasGo || hasFrontend)
    run('task', ['sync-protobuf-generators'])
  if (hasFrontend || hasCargo)
    await reconcile({ root, trusted, run, upgrades, security: upgrades.some(upgrade => upgrade.isVulnerabilityAlert) })
  if (hasGo) {
    run('task', ['prepare-backend'])
    for (const cwd of ['backend', 'desktop/go'])
      run('go', ['mod', 'tidy'], { cwd })
    run('go', ['work', 'sync'])
    refreshSite({ root, run, read, write })
  }
  const changed = run('git', ['diff', '--name-only', '-z', 'HEAD', '--'], { capture: true }).split('\0').filter(Boolean)
  if (needsNotice(changed))
    run('task', ['generate-notice'])
}

if (import.meta.main) {
  const started = Date.now()
  let status = 'failed'
  let upgrades = []
  try {
    const file = process.env.RENOVATE_POST_UPGRADE_COMMAND_DATA_FILE
    if (!file)
      throw new Error('Renovate did not supply structured post-update metadata.')
    upgrades = JSON.parse(readFileSync(file, 'utf8'))
    await postUpgrade(upgrades)
    status = 'passed'
  }
  catch (error) {
    console.error(error.message)
    process.exitCode = 1
  }
  finally {
    if (process.env.RENOVATE_TRUSTED_ROOT) {
      const entries = Array.isArray(upgrades) ? upgrades : []
      appendFileSync(resolve(process.env.RENOVATE_TRUSTED_ROOT, '.tmp/renovate-timings.jsonl'), `${JSON.stringify({ started, durationMs: Date.now() - started, status, branches: [...new Set(entries.map(upgrade => upgrade?.branchName))], dependencies: entries.map(upgrade => upgrade?.depName), updateTypes: [...new Set(entries.map(upgrade => upgrade?.updateType))] })}\n`)
    }
  }
}
