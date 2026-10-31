#!/usr/bin/env bun
import { spawnSync } from 'node:child_process'
import { appendFileSync, readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import process from 'node:process'
import { needsNotice } from './notice-inputs.mjs'
import { finishGoUpgrades, prepareGoUpgrades } from './renovate-go.mjs'
import { isDependencyInput } from './renovate-inputs.mjs'
import { reconcileTauri } from './renovate-tauri.mjs'

export function validateUpgrades(upgrades) {
  if (!Array.isArray(upgrades) || upgrades.length === 0 || upgrades.length > 10000)
    throw new Error('Post-update metadata requires a nonempty list of at most 10000 upgrades.')
  for (const upgrade of upgrades) {
    if (!upgrade || typeof upgrade !== 'object' || typeof upgrade.manager !== 'string')
      throw new Error('An upgrade must specify its manager.')
    if (![
      'gomod',
      'bun',
      'cargo',
      'github-actions',
      'regex',
    ].includes(upgrade.manager)) {
      throw new Error(`Unsupported dependency manager: ${upgrade.manager}`)
    }
    if (upgrade.updateType === 'lockFileMaintenance' && !['bun', 'cargo'].includes(upgrade.manager))
      throw new Error('Only Bun and Cargo support coordinated lockfile maintenance.')
    if (!isDependencyInput(upgrade.manager, upgrade.packageFile))
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

/** Execute each stage through an injected runner so fixtures observe ordering and failures. */
export async function postUpgrade(upgrades, { root = process.cwd(), trusted = process.env.RENOVATE_TRUSTED_ROOT, run: injectedRun, read = path => readFileSync(resolve(root, path), 'utf8'), write, reconcile = reconcileTauri } = {}) {
  const { toolchain } = validateUpgrades(upgrades)
  const run = injectedRun ?? ((command, args, options = {}) => {
    const result = spawnSync(command, args, {
      cwd: resolve(root, options.cwd ?? '.'),
      encoding: 'utf8',
      input: options.input,
      env: {
        ...process.env,
        ...(options.env ?? {}),
        // A Go requirement that the installed toolchain cannot meet fails. Go never
        // downloads a different toolchain here.
        GOTOOLCHAIN: 'local',
      },
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
  // A toolchain branch only rewrites the documented versions. CI installs and validates
  // the proposed toolchain.
  if (toolchain) {
    run('task', ['sync-versions'])
    return
  }
  if (!trusted)
    throw new Error('Post-update processing requires the trusted main checkout.')
  const goState = prepareGoUpgrades(upgrades, { trusted, run, read })
  const hasFrontend = upgrades.some(upgrade => upgrade.manager === 'bun' && upgrade.packageFile === 'frontend/package.json')
  const hasCargo = upgrades.some(upgrade => upgrade.manager === 'cargo')
  if (goState.hasWorkspaceGo || hasFrontend)
    run('task', ['sync-protobuf-generators'])
  if (hasFrontend || hasCargo) {
    await reconcile({
      root,
      trusted,
      run,
      upgrades,
      security: upgrades.some(upgrade => upgrade.isVulnerabilityAlert),
    })
  }
  finishGoUpgrades(goState, { root, run, read, write })
  const changed = run('git', [
    'diff',
    '--no-renames',
    '--name-only',
    '-z',
    'HEAD',
    '--',
  ], { capture: true }).split('\0').filter(Boolean)
  if (needsNotice(changed))
    run('task', ['generate-notice'])
}

if (import.meta.main) {
  const started = Date.now()
  const stopwatch = performance.now()
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
      const entries = Array.isArray(upgrades)
        ? upgrades
        : []
      appendFileSync(resolve(process.env.RENOVATE_TRUSTED_ROOT, '.tmp/renovate-timings.jsonl'), `${JSON.stringify({
        started,
        durationMs: Math.round(performance.now() - stopwatch),
        status,
        branches: [
          ...new Set(entries.map(upgrade => upgrade?.branchName)),
        ],
        dependencies: entries.map(upgrade => upgrade?.depName),
        updateTypes: [
          ...new Set(entries.map(upgrade => upgrade?.updateType)),
        ],
      })}\n`)
    }
  }
}
