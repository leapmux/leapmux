#!/usr/bin/env bun
import { spawnSync } from 'node:child_process'
import { closeSync, mkdirSync, mkdtempSync, openSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import process from 'node:process'
import { validateUpgrades } from './renovate-post-upgrade.mjs'
import { runWithTimeout } from './run-renovate.mjs'

const MAX_QUEUE_MS = 40 * 60 * 1000

export function representativeQueueDuration(cliMs, benchmarkMs) {
  if ([cliMs, benchmarkMs].some(value => !Number.isSafeInteger(value) || value < 0))
    throw new Error('Queue measurements require nonnegative integer milliseconds.')
  const total = cliMs + benchmarkMs
  if (!Number.isSafeInteger(total) || total > MAX_QUEUE_MS)
    throw new Error('The representative queue exceeds the 40-minute activation limit.')
  return total
}

export function selectRebaseUpgrades(records, timings) {
  if (!records.some(record => record.msg === 'Env config' && record.config?.dryRun === 'full' && Array.isArray(record.config.force?.schedule) && record.config.force.schedule.length === 0))
    throw new Error('Rebase measurements require a verified full dry run.')
  const passed = new Set(timings.filter(timing => timing.status === 'passed').flatMap(timing => timing.branches))
  for (const branch of ['renovate/go-security', 'renovate/go-workspace']) {
    const record = records.find(record => record.branch === branch && record.msg === 'Created post-upgrade commands data file.')
    if (!record || !passed.has(branch))
      continue
    const upgrades = typeof record.dataFileContent === 'string'
      ? JSON.parse(record.dataFileContent)
      : record.dataFileContent
    validateUpgrades(upgrades)
    if (upgrades.every(upgrade => upgrade.manager === 'gomod') && upgrades.some(upgrade => upgrade.packageFile !== 'site/go.mod'))
      return upgrades
  }
  return null
}

function benchmarkEnvironment() {
  const allowed = [
    'PATH',
    'HOME',
    'LANG',
    'LC_ALL',
    'TZ',
    'TMPDIR',
    'TMP',
    'TEMP',
    'SystemRoot',
    'COMSPEC',
    'PATHEXT',
    'GOPATH',
    'GOMODCACHE',
    'GOCACHE',
    'GOPROXY',
    'GOSUMDB',
    'CARGO_HOME',
    'RUSTUP_HOME',
    'RUSTUP_TOOLCHAIN',
    'BUN_INSTALL',
    'BUN_INSTALL_CACHE_DIR',
  ]
  return {
    ...Object.fromEntries(allowed.filter(key => process.env[key] !== undefined).map(key => [key, process.env[key]])),
    CI: 'true',
    GOTOOLCHAIN: 'local',
    GIT_TERMINAL_PROMPT: '0',
  }
}

function git(cwd, args, input) {
  const result = spawnSync('git', [
    '-c',
    'core.fsmonitor=false',
    '-c',
    'gc.auto=0',
    '-c',
    'maintenance.auto=false',
    '-c',
    'core.hooksPath=/dev/null',
    ...args,
  ], {
    cwd,
    env: benchmarkEnvironment(),
    input,
    encoding: 'utf8',
    maxBuffer: 32 * 1024 * 1024,
  })
  if (result.status !== 0)
    throw new Error(`Rebase fixture git ${args[0]} failed: ${result.stderr}`)
  return result.stdout
}

async function executePostUpgrade(root, trusted, upgrades, log) {
  const metadata = resolve(root, '.tmp/benchmark-upgrades.json')
  mkdirSync(resolve(root, '.tmp'), { recursive: true })
  writeFileSync(metadata, JSON.stringify(upgrades))
  const descriptor = openSync(log, 'w')
  try {
    const result = await runWithTimeout('bun', [
      resolve(trusted, 'scripts/renovate-post-upgrade.mjs'),
    ], {
      cwd: root,
      env: {
        ...benchmarkEnvironment(),
        RENOVATE_TRUSTED_ROOT: trusted,
        RENOVATE_POST_UPGRADE_COMMAND_DATA_FILE: metadata,
      },
      stdio: ['ignore', descriptor, descriptor],
      timeoutMs: 8 * 60 * 1000,
    })
    if (result.code !== 0) {
      throw new Error(`Rebased Go artifact processing failed: ${result.timedOut
        ? 'eight-minute execution limit exceeded'
        : readFileSync(log, 'utf8').slice(-4000)}`)
    }
  }
  finally {
    closeSync(descriptor)
  }
}

/** Rebase private fixture branches and regenerate their artifacts without any remote push. */
export async function benchmarkRebases({ sourceRoot, trusted = sourceRoot, upgrades, outputRoot, execute = executePostUpgrade }) {
  validateUpgrades(upgrades)
  mkdirSync(outputRoot, { recursive: true })
  const fixture = mkdtempSync(resolve(outputRoot, 'checkout-'))
  const cases = []
  const started = performance.now()
  const post = async (label) => {
    const started = performance.now()
    await execute(fixture, trusted, upgrades.map(upgrade => ({ ...upgrade, branchName: `benchmark/${label}` })), resolve(outputRoot, `${label}.log`))
    return Math.round(performance.now() - started)
  }
  try {
    git(sourceRoot, [
      'clone',
      '--quiet',
      '--no-hardlinks',
      sourceRoot,
      fixture,
    ])
    mkdirSync(resolve(fixture, '.git/no-hooks'))
    for (const [key, value] of Object.entries({
      'core.fsmonitor': 'false',
      'gc.auto': '0',
      'maintenance.auto': 'false',
      'core.hooksPath': resolve(fixture, '.git/no-hooks'),
      'commit.gpgsign': 'false',
      'user.name': 'Renovate validation',
      'user.email': 'renovate-validation@example.invalid',
    }))
      git(fixture, ['config', key, value])
    git(fixture, ['switch', '-c', 'benchmark/base'])
    git(fixture, ['switch', '-c', 'benchmark/dependency'])
    const initialMs = await post('initial')
    git(fixture, ['add', '--update'])
    git(fixture, [
      'commit',
      '--allow-empty',
      '-m',
      'test: Prepare the dependency fixture',
    ])
    for (const label of ['readme-base', 'go-import-base']) {
      git(fixture, ['switch', 'benchmark/base'])
      if (label === 'readme-base') {
        const readme = resolve(fixture, 'README.md')
        writeFileSync(readme, `${readFileSync(readme, 'utf8')}\nRebase validation base.\n`)
        git(fixture, ['add', '--', 'README.md'])
      }
      else {
        const path = 'backend/internal/hub/frontend/renovate_rebase_fixture.go'
        mkdirSync(resolve(fixture, 'backend/internal/hub/frontend'), { recursive: true })
        writeFileSync(resolve(fixture, path), 'package frontend\nimport _ "github.com/google/shlex"\n')
        git(fixture, ['add', '--', path])
      }
      git(fixture, ['commit', '-m', `test: Advance ${label} fixture`])
      git(fixture, ['switch', 'benchmark/dependency'])
      const rebaseStarted = performance.now()
      git(fixture, ['rebase', 'benchmark/base'])
      const rebaseMs = Math.round(performance.now() - rebaseStarted)
      // Explicit prefixes keep git apply independent of the user's diff.noprefix setting.
      const patch = git(fixture, [
        'diff',
        '--binary',
        '--src-prefix=a/',
        '--dst-prefix=b/',
        'benchmark/base',
        'benchmark/dependency',
      ])
      git(fixture, [
        'switch',
        '-C',
        'benchmark/proposal',
        'benchmark/base',
      ])
      // Renovate regenerates the proposal against the new base rather than committing old artifacts again.
      if (patch)
        git(fixture, ['apply', '--index'], patch)
      const postMs = await post(label)
      cases.push({
        label,
        rebaseMs,
        postMs,
        durationMs: rebaseMs + postMs,
      })
      git(fixture, ['add', '--update'])
      git(fixture, [
        'commit',
        '--allow-empty',
        '-m',
        `test: Regenerate ${label} fixture`,
      ])
      git(fixture, [
        'branch',
        '--force',
        'benchmark/dependency',
        'HEAD',
      ])
    }
    return {
      status: 'passed',
      initialMs,
      durationMs: Math.round(performance.now() - started),
      cases,
    }
  }
  finally {
    rmSync(fixture, { recursive: true, force: true })
  }
}

function records(path) {
  return readFileSync(path, 'utf8').split('\n').filter(Boolean).flatMap((line) => {
    try {
      return [
        JSON.parse(line),
      ]
    }
    catch { return [] }
  })
}

if (import.meta.main) {
  const trusted = process.env.RENOVATE_TRUSTED_ROOT
  try {
    if (process.env.GITHUB_ACTIONS !== 'true' || process.env.GITHUB_REPOSITORY !== 'leapmux/leapmux' || process.env.GITHUB_REF !== 'refs/heads/main' || process.env.RENOVATE_DRY_RUN !== 'full' || !trusted)
      throw new Error('Rebase measurements require the upstream main read-only Actions job.')
    const [execution] = records(resolve(trusted, '.tmp/renovate-execution.jsonl'))
    if (execution?.code !== 0 || execution.timedOut)
      throw new Error('Rebase measurements require a completed successful CLI execution.')
    representativeQueueDuration(execution.durationMs, 0)
    const upgrades = selectRebaseUpgrades(records(resolve(trusted, '.tmp/renovate-cli.log')), records(resolve(trusted, '.tmp/renovate-timings.jsonl')))
    const result = upgrades
      ? await benchmarkRebases({
          sourceRoot: trusted,
          upgrades,
          outputRoot: resolve(trusted, '.tmp/renovate-rebase'),
        })
      : { status: 'skipped', reason: 'No successful workspace Go proposal is available for representative rebases.' }
    if (upgrades)
      result.representativeQueueMs = representativeQueueDuration(execution.durationMs, result.durationMs)
    writeFileSync(resolve(trusted, '.tmp/renovate-rebase-benchmark.json'), JSON.stringify(result, null, 2))
    console.log(JSON.stringify(result))
  }
  catch (error) {
    console.error(error.message)
    process.exitCode = 1
  }
}
