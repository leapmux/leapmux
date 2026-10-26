#!/usr/bin/env bun
import { spawnSync } from 'node:child_process'
import { appendFileSync, cpSync, mkdirSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import process from 'node:process'
import { runWithTimeout } from './run-renovate.mjs'

const root = resolve(import.meta.dirname, '..')
function git(cwd, args) {
  const result = spawnSync('git', args, { cwd, encoding: 'utf8' })
  if (result.status !== 0)
    throw new Error(`Inventory fixture git command failed: ${result.stderr}`)
  return result.stdout
}

// Local Renovate reads Git's inventory. A private fixture includes new implementation
// files without staging or committing the contributor's working tree.
mkdirSync(resolve(root, '.tmp'), { recursive: true })
const fixture = mkdtempSync(resolve(root, '.tmp/renovate-extraction-'))
const files = git(root, ['ls-files', '--cached', '--others', '--exclude-standard', '-z']).split('\0').filter(Boolean)
for (const file of files) {
  // The local platform always reads repository config. Its policy already comes from
  // trusted global config, so copying it would merge the custom managers twice.
  if (file === 'renovate.json')
    continue
  // Deleted tracked files do not belong in the proposed inventory.
  const source = Bun.file(resolve(root, file))
  if (!await source.exists())
    continue
  mkdirSync(dirname(resolve(fixture, file)), { recursive: true })
  cpSync(resolve(root, file), resolve(fixture, file), { dereference: true })
}
git(fixture, ['init', '--initial-branch=main'])
for (const [key, value] of Object.entries({ 'core.fsmonitor': 'false', 'gc.auto': '0', 'maintenance.auto': 'false', 'commit.gpgsign': 'false', 'user.name': 'Inventory fixture', 'user.email': 'fixture@example.invalid' }))
  git(fixture, ['config', key, value])
git(fixture, ['add', '--all'])
git(fixture, ['commit', '-m', 'test: Record the proposed dependency inventory'])
const env = { ...process.env, RENOVATE_PLATFORM: 'local', RENOVATE_DRY_RUN: 'extract', RENOVATE_CONFIG_FILE: resolve(root, '.github/renovate-config.cjs'), RENOVATE_TRUSTED_ROOT: root, LOG_FORMAT: 'json', LOG_LEVEL: 'debug' }
for (const key of ['RENOVATE_TOKEN', 'GH_TOKEN', 'GITHUB_TOKEN']) delete env[key]
const log = resolve(fixture, 'extraction.jsonl')
const output = { write: data => appendFileSync(log, data) }
// Renovate's local platform takes its repository from the process directory.
const previous = process.cwd()
process.chdir(fixture)
const result = await runWithTimeout('node', [resolve(root, '.github/renovate/node_modules/renovate/dist/renovate.js')], { env, stdio: ['ignore', 'pipe', 'pipe'], output })
process.chdir(previous)
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
const paths = new Set(Object.values(inventory).flat().map(file => file.packageFile))
for (const file of ['backend/go.mod', 'desktop/go/go.mod', 'site/go.mod', 'frontend/package.json', '.github/renovate/package.json', 'desktop/rust/Cargo.toml', 'versions.env', ...files.filter(file => /^\.github\/(?:workflows\/[^/]+\.ya?ml|actions\/[^/]+\/action\.ya?ml)$/.test(file) && /^\s*(?:-\s*)?uses:\s*[\w-]+\//im.test(readFileSync(resolve(root, file), 'utf8')))]) {
  if (!paths.has(file))
    throw new Error(`Renovate missed dependency source ${file}. Read ${log}`)
}
const pins = inventory.regex?.find(file => file.packageFile === 'versions.env')?.deps ?? []
const expectedPins = readFileSync(resolve(root, 'versions.env'), 'utf8').split('\n').filter(line => /^[A-Z_][A-Z0-9_]*=/.test(line) && !line.startsWith('VERSION='))
if (pins.length !== expectedPins.length || pins.some(pin => pin.depType === 'VERSION'))
  throw new Error('Renovate must extract every tool and image pin, excluding application VERSION.')
writeFileSync(resolve(root, '.tmp/renovate-inventory.json'), JSON.stringify({ inventory, fixture, timing: result }, null, 2))
console.log(`Verified ${paths.size} dependency source files and ${pins.length} tool/image pins. Inventory: .tmp/renovate-inventory.json`)
