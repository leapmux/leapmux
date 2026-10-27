#!/usr/bin/env bun
import { spawnSync } from 'node:child_process'
import { appendFileSync, cpSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import process from 'node:process'
import { renovateModule } from './renovate-engine.mjs'
import { DEPENDENCY_INPUT_FILES, dependencyConfiguration, isGithubActionSource } from './renovate-inputs.mjs'
import { runWithTimeout } from './run-renovate.mjs'

const { getManagerConfig } = await renovateModule('config/index.js')
const { mergeChildConfig } = await renovateModule('config/utils.js')
const { init: initializeRenovateLogger } = await renovateModule('logger/index.js')
const { isCustomManager } = await renovateModule('modules/manager/custom/index.js')
const { getEnabledManagersList } = await renovateModule('modules/manager/index.js')
const { getMatchingFiles } = await renovateModule('workers/repository/extract/file-match.js')

const repoRoot = resolve(import.meta.dirname, '..')
function git(cwd, args) {
  const result = spawnSync('git', args, { cwd, encoding: 'utf8', maxBuffer: 16 * 1024 * 1024 })
  if (result.status !== 0)
    throw new Error(`Inventory fixture git command failed: ${result.stderr}`)
  return result.stdout
}

/** Match the complete filename inventory with the pinned managers, including their additive defaults. */
export async function selectExtractionFiles(files, policy) {
  const config = await dependencyConfiguration(policy)
  const selected = new Set()
  for (const manager of getEnabledManagersList(config.enabledManagers)) {
    const base = getManagerConfig(config, manager)
    const configurations = isCustomManager(manager) ? config.customManagers.filter(custom => custom.customType === manager).map(custom => mergeChildConfig(base, custom)) : [base]
    for (const configuration of configurations) {
      for (const file of getMatchingFiles(configuration, files)) selected.add(file)
    }
  }
  // Managers also read these files without matching them as dependency manifests.
  for (const file of files) {
    if (/(?:^|\/)(?:\.npmrc|bunfig\.toml|Cargo\.lock|go\.sum|go\.work(?:\.sum)?)$/.test(file)
      || /^\.cargo\/config(?:\.toml)?$/.test(file) || file === '.github/workflows/actions.lock') {
      selected.add(file)
    }
  }
  selected.delete('renovate.json')
  return [...selected].sort()
}

function validateInventory(inventory, files, read, log) {
  const paths = new Set(Object.values(inventory).flat().map(file => file.packageFile))
  const actions = files.filter(file => isGithubActionSource(file) && /^\s*(?:-\s*)?uses:\s*["']?[\w.-]+\//im.test(read(file)))
  for (const file of [...Object.values(DEPENDENCY_INPUT_FILES).flat(), ...actions]) {
    if (!paths.has(file))
      throw new Error(`Renovate missed dependency source ${file}. Read ${log}`)
  }
  const pins = inventory.regex?.find(file => file.packageFile === 'versions.env')?.deps ?? []
  const expectedPins = read('versions.env').split('\n').filter(line => /^[A-Z_][A-Z0-9_]*=/.test(line) && !line.startsWith('VERSION='))
  if (pins.length !== expectedPins.length || pins.some(pin => pin.depType === 'VERSION'))
    throw new Error('Renovate must extract every tool and image pin, excluding application VERSION.')
  return { sourceCount: paths.size, pinCount: pins.length }
}

export async function extractRenovateInventory({ root = repoRoot, trusted = repoRoot, run = runWithTimeout, fullTree = false } = {}) {
  await initializeRenovateLogger()
  mkdirSync(resolve(root, '.tmp'), { recursive: true })
  const diagnostics = mkdtempSync(resolve(root, '.tmp/renovate-extraction-'))
  const fixture = resolve(diagnostics, 'checkout')
  const log = resolve(diagnostics, 'extraction.jsonl')
  mkdirSync(fixture)
  writeFileSync(log, '')
  try {
    const policy = JSON.parse(readFileSync(resolve(root, 'renovate.json'), 'utf8'))
    const files = [...new Set(git(root, ['ls-files', '--cached', '--others', '--exclude-standard', '-z']).split('\0').filter(Boolean))]
    const proposed = files.filter(file => file !== 'renovate.json' && existsSync(resolve(root, file)))
    const selected = fullTree ? proposed : await selectExtractionFiles(proposed, policy)
    for (const file of selected) {
      mkdirSync(dirname(resolve(fixture, file)), { recursive: true })
      cpSync(resolve(root, file), resolve(fixture, file), { dereference: true })
    }
    git(fixture, ['init', '--initial-branch=main'])
    const hooks = resolve(fixture, '.git/no-hooks')
    mkdirSync(hooks)
    for (const [key, value] of Object.entries({ 'core.fsmonitor': 'false', 'gc.auto': '0', 'maintenance.auto': 'false', 'commit.gpgsign': 'false', 'core.hooksPath': hooks, 'user.name': 'Inventory fixture', 'user.email': 'fixture@example.invalid' }))
      git(fixture, ['config', key, value])
    git(fixture, ['add', '--force', '--all'])
    git(fixture, ['commit', '-m', 'test: Record the proposed dependency inventory'])
    const env = { ...process.env, RENOVATE_PLATFORM: 'local', RENOVATE_DRY_RUN: 'extract', RENOVATE_CONFIG_FILE: resolve(trusted, '.github/renovate-config.cjs'), RENOVATE_TRUSTED_ROOT: trusted, LOG_FORMAT: 'json', LOG_LEVEL: 'debug' }
    for (const key of ['RENOVATE_TOKEN', 'GH_TOKEN', 'GITHUB_TOKEN']) delete env[key]
    const result = await run('node', [resolve(trusted, '.github/renovate/node_modules/renovate/dist/renovate.js')], { cwd: fixture, env, stdio: ['ignore', 'pipe', 'pipe'], output: { write: data => appendFileSync(log, data) } })
    if (result.code !== 0)
      throw new Error(`Local extraction failed. Read ${log}`)
    const records = readFileSync(log, 'utf8').split('\n').filter(Boolean).flatMap((line) => {
      try {
        return [JSON.parse(line)]
      }
      catch { return [] }
    })
    const inventory = records.find(record => record.msg === 'Extracted dependencies')?.packageFiles
    if (!inventory)
      throw new Error(`Renovate did not emit an extracted inventory. Read ${log}`)
    const counts = validateInventory(inventory, selected, file => readFileSync(resolve(fixture, file), 'utf8'), log)
    const record = { inventory, fixture, log, snapshotFiles: selected, timing: result, ...counts }
    writeFileSync(resolve(root, '.tmp/renovate-inventory.json'), JSON.stringify(record, null, 2))
    return record
  }
  catch (error) {
    appendFileSync(log, `${JSON.stringify({ level: 'error', msg: error.message })}\n`)
    throw error
  }
  finally {
    rmSync(fixture, { recursive: true, force: true })
  }
}

if (import.meta.main) {
  try {
    const result = await extractRenovateInventory()
    console.log(`Verified ${result.sourceCount} dependency source files and ${result.pinCount} tool/image pins. Inventory: .tmp/renovate-inventory.json`)
  }
  catch (error) {
    console.error(error.message)
    process.exitCode = 1
  }
}
