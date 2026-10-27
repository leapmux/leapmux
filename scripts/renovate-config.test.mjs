import { spawnSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import process from 'node:process'
import { afterEach, describe, expect, it } from 'bun:test'
import { renovateModule } from './renovate-engine.mjs'
import { securityCompanions } from './renovate-prepare.mjs'
import { CLAIMS } from './sync-versions.mjs'

const { init: initializeRenovateLogger } = await renovateModule('logger/index.js')
await initializeRenovateLogger()

const root = resolve(import.meta.dirname, '..')
const fixtures = []
afterEach(() => {
  for (const fixture of fixtures.splice(0)) rmSync(fixture, { recursive: true, force: true })
})
function fixtureConfig(packageRules = []) {
  mkdirSync(resolve(root, '.tmp'), { recursive: true })
  const dir = mkdtempSync(resolve(root, '.tmp/renovate-config-'))
  fixtures.push(dir)
  mkdirSync(resolve(dir, '.tmp'))
  writeFileSync(resolve(dir, 'renovate.json'), readFileSync(resolve(root, 'renovate.json')))
  writeFileSync(resolve(dir, '.tmp/renovate-prepared.json'), JSON.stringify({ packageRules, importFiles: ['backend/cmd/example/main.go', 'desktop/go/example.go'] }))
  const result = spawnSync('node', ['-e', 'process.stdout.write(JSON.stringify(require(process.argv[1])))', resolve(root, '.github/renovate-config.cjs')], { encoding: 'utf8', env: { ...process.env, RENOVATE_TRUSTED_ROOT: dir } })
  expect(result.status, result.stderr).toBe(0)
  return JSON.parse(result.stdout)
}

describe('trusted Renovate configuration', () => {
  it('loads the pinned engine through the shared resolver under Node', () => {
    const source = `const {renovateModule}=await import(${JSON.stringify(new URL('./renovate-engine.mjs', import.meta.url).href)}); const {api}=await renovateModule('modules/versioning/cargo/index.js'); console.log(JSON.stringify(api.matches('2.11.3','2.11.0')))`
    const result = spawnSync('node', ['--input-type=module', '-e', source], { encoding: 'utf8' })
    expect(result.status, result.stderr).toBe(0)
    expect(JSON.parse(result.stdout)).toBe(true)
  })
  it('validates a nonempty security configuration with the pinned global validator', async () => {
    const policy = JSON.parse(readFileSync(resolve(root, 'renovate.json'), 'utf8'))
    const config = fixtureConfig(securityCompanions({ '@tauri-apps/api': '3.0.0', 'tauri': '3.0.1' }, policy.vulnerabilityAlerts))
    const { validateConfig } = await renovateModule('config/validation.js')
    const result = await validateConfig('global', config)
    expect(result.errors).toEqual([])
    expect(result.warnings).toEqual([])
  })
  it('covers every version claim and import path while excluding generated code and work sums', () => {
    const config = fixtureConfig()
    for (const path of [...new Set(CLAIMS.map(claim => claim.file)), 'buf.gen.yaml', 'backend/go.sum', 'desktop/go/go.sum', 'site/go.sum', 'site/hugo.yaml', 'backend/cmd/example/main.go', 'desktop/go/example.go'])
      expect(config.postUpgradeTasks.fileFilters).toContain(path)
    for (const path of ['go.work.sum', 'backend/generated/example.go', 'backend/internal/hub/generated/frontend/embed.go', 'frontend/.output/index.html', 'desktop/rust/target/example'])
      expect(config.postUpgradeTasks.fileFilters.some(pattern => new Bun.Glob(pattern).match(path))).toBe(false)
    expect(config.allowScripts).toBe(false)
    expect(config.exposeAllEnv).toBe(false)
    expect(config.customEnvVariables.GOTOOLCHAIN).toBe('local')
    expect(config.requireConfig).toBe('ignored')
  })
  it('allows only the exact trusted command and renders structured metadata with the pinned engine', async () => {
    const config = fixtureConfig()
    const command = config.postUpgradeTasks.commands[0]
    const allowed = new RegExp(config.allowedCommands[0])
    expect(allowed.test(command)).toBe(true)
    for (const other of [`${command}; false`, `false; ${command}`, command.replace('.mjs', 'Xmjs'), 'task renovate-post-upgrade'])
      expect(allowed.test(other)).toBe(false)
    const { compile } = await renovateModule('util/template/index.js')
    const upgrades = [{ manager: 'gomod', packageFile: 'backend/go.mod', depName: 'example.com/a/v7', newValue: 'v8.0.0', newVersion: 'v8.0.0', updateType: 'major', depType: 'require', isVulnerabilityAlert: true }, { manager: 'bun', packageFile: 'frontend/package.json', updateType: 'lockFileMaintenance' }]
    expect(JSON.parse(compile(config.postUpgradeTasks.dataFileTemplate, { upgrades }))).toEqual(upgrades)
  })
})
