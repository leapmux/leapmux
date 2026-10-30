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
function fixtureDirectory(packageRules = []) {
  mkdirSync(resolve(root, '.tmp'), { recursive: true })
  const dir = mkdtempSync(resolve(root, '.tmp/renovate-config-'))
  fixtures.push(dir)
  mkdirSync(resolve(dir, '.tmp'))
  writeFileSync(resolve(dir, 'renovate.json'), readFileSync(resolve(root, 'renovate.json')))
  writeFileSync(resolve(dir, '.tmp/renovate-prepared.json'), JSON.stringify({
    packageRules,
    importFiles: ['backend/cmd/example/main.go', 'desktop/go/example.go'],
  }))
  return dir
}
function fixtureConfig(packageRules = []) {
  const dir = fixtureDirectory(packageRules)
  const result = spawnSync('node', [
    '-e',
    'process.stdout.write(JSON.stringify(require(process.argv[1])))',
    resolve(root, '.github/renovate-config.cjs'),
  ], {
    encoding: 'utf8',
    env: { ...process.env, RENOVATE_TRUSTED_ROOT: dir },
  })
  expect(result.status, result.stderr).toBe(0)
  return JSON.parse(result.stdout)
}

describe('trusted Renovate configuration', () => {
  it('keeps repository groups authoritative after native global and repository preset resolution', () => {
    const directory = fixtureDirectory()
    const source = `
      const root=process.argv[1];
      const {pathToFileURL}=await import('node:url'); const {resolve}=await import('node:path');
      const engine=p=>import(pathToFileURL(resolve(root,'.github/renovate/node_modules/renovate/dist',p)).href);
      await (await engine('logger/index.js')).init();
      const {parseConfigs}=await engine('workers/global/config/parse/index.js');
      const {GlobalConfig}=await engine('config/global.js');
      const {mergeRenovateConfig}=await engine('workers/repository/init/merge.js');
      const {branchifyUpgrades}=await engine('workers/repository/updates/branchify.js');
      const parsedGlobal=await parseConfigs({RENOVATE_CONFIG_FILE:resolve(root,'.github/renovate-config.cjs'),RENOVATE_DRY_RUN:'full',RENOVATE_FORCE:'{"schedule":[]}'},['node','renovate']);
      const config=await mergeRenovateConfig({...GlobalConfig.set(parsedGlobal),repository:'leapmux/leapmux',errors:[],warnings:[]});
      const dep=(depName,sourceUrl)=>({depName,packageName:depName,sourceUrl,depType:'dependencies',currentValue:'1.0.0',updates:[{updateType:'minor',newValue:'1.1.0',newVersion:'1.1.0'}]});
      const {branches}=await branchifyUpgrades({...config,repoIsOnboarded:true},{bun:[{packageFile:'frontend/package.json',deps:[dep('@tauri-apps/api','https://github.com/tauri-apps/tauri'),dep('@xterm/xterm','https://github.com/xtermjs/xterm.js')]}],cargo:[{packageFile:'desktop/rust/Cargo.toml',deps:[dep('tauri','https://github.com/tauri-apps/tauri')]}]});
      console.log(JSON.stringify({dryRun:GlobalConfig.get('dryRun'),schedule:config.schedule,groups:branches.filter(branch=>!branch.isLockFileMaintenance).map(branch=>({group:branch.groupName,deps:branch.upgrades.map(upgrade=>upgrade.depName).sort()})).sort((a,b)=>a.group.localeCompare(b.group)),titles:branches.map(branch=>branch.prTitle)}));
    `
    const result = spawnSync('node', [
      '--input-type=module',
      '-e',
      source,
      root,
    ], {
      encoding: 'utf8',
      env: {
        ...process.env,
        RENOVATE_TRUSTED_ROOT: directory,
        LOG_LEVEL: 'fatal',
        LOG_FORMAT: 'json',
      },
    })
    expect(result.status, result.stderr).toBe(0)
    const { titles, ...configuration } = JSON.parse(result.stdout)
    expect(configuration).toEqual({
      dryRun: 'full',
      schedule: [],
      groups: [
        {
          group: 'frontend',
          deps: ['@xterm/xterm'],
        },
        {
          group: 'Tauri',
          deps: ['@tauri-apps/api', 'tauri'],
        },
      ],
    })
    for (const title of titles) {
      expect(title).toMatch(/^chore\(deps\): Update /)
      expect(title.length).toBeLessThan(72)
    }
  })
  it('passes dry-run flags and schedule overrides to the actual Task command', () => {
    const directory = mkdtempSync(resolve(root, '.tmp/renovate-task-env-'))
    fixtures.push(directory)
    mkdirSync(resolve(directory, 'scripts'))
    const tasks = Bun.YAML.parse(readFileSync(resolve(root, 'Taskfile.yaml'), 'utf8')).tasks
    writeFileSync(resolve(directory, 'Taskfile.yaml'), Bun.YAML.stringify({
      version: '3',
      tasks: { 'renovate': tasks.renovate, 'renovate-dry-run': tasks['renovate-dry-run'] },
    }))
    writeFileSync(resolve(directory, 'scripts/run-renovate.mjs'), `import {writeFileSync} from 'node:fs'; writeFileSync('environment.json',JSON.stringify({dryRun:process.env.RENOVATE_DRY_RUN??null,force:process.env.RENOVATE_FORCE??null,config:process.env.RENOVATE_CONFIG_FILE,format:process.env.LOG_FORMAT}));`)
    const env = { ...process.env }
    delete env.RENOVATE_DRY_RUN
    delete env.RENOVATE_FORCE
    for (const target of ['renovate-dry-run', 'renovate']) {
      const result = spawnSync('task', [target], { cwd: directory, env, encoding: 'utf8' })
      expect(result.status, result.stderr).toBe(0)
      const captured = JSON.parse(readFileSync(resolve(directory, 'environment.json'), 'utf8'))
      expect(captured.config).toBe('.github/renovate-config.cjs')
      expect(captured.format).toBe('json')
      expect(captured.dryRun).toBe(target === 'renovate-dry-run'
        ? 'full'
        : null)
      expect(captured.force && JSON.parse(captured.force)).toEqual(target === 'renovate-dry-run'
        ? { schedule: [] }
        : null)
    }
  })
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
    // Renovate upgrades no key that renovate.json disables, so the sync after
    // an upgrade never rewrites the files of those claims. The runner claims
    // live in the workflows, which the filters keep out: a post-upgrade commit
    // must not reach a workflow.
    const policy = JSON.parse(readFileSync(resolve(root, 'renovate.json'), 'utf8'))
    const frozen = new Set(policy.packageRules
      .filter(rule => rule.enabled === false && rule.matchManagers?.includes('custom.regex'))
      .flatMap(rule => rule.matchDepTypes ?? []))
    expect(frozen).toEqual(new Set(['UBUNTU_RUNNER', 'MACOS_RUNNER', 'WINDOWS_RUNNER']))
    const upgradable = CLAIMS.filter(claim => !frozen.has(claim.key))
    const frozenFiles = new Set(CLAIMS.filter(claim => frozen.has(claim.key)).map(claim => claim.file))
    for (const file of frozenFiles) {
      expect(file.startsWith('.github/workflows/'), file).toBe(true)
      expect(config.postUpgradeTasks.fileFilters.some(pattern => new Bun.Glob(pattern).match(file)), file).toBe(false)
    }
    for (const path of [
      ...new Set(upgradable.map(claim => claim.file)),
      'buf.gen.yaml',
      'backend/go.sum',
      'desktop/go/go.sum',
      'site/go.sum',
      'site/hugo.yaml',
      'backend/cmd/example/main.go',
      'desktop/go/example.go',
    ])
      expect(config.postUpgradeTasks.fileFilters).toContain(path)
    for (const path of [
      'go.work.sum',
      'backend/generated/example.go',
      'backend/internal/hub/generated/frontend/embed.go',
      'frontend/.output/index.html',
      'desktop/rust/target/example',
    ])
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
    for (const other of [
      `${command}; false`,
      `false; ${command}`,
      command.replace('.mjs', 'Xmjs'),
      'task renovate-post-upgrade',
    ])
      expect(allowed.test(other)).toBe(false)
    const { compile } = await renovateModule('util/template/index.js')
    const upgrades = [
      {
        manager: 'gomod',
        packageFile: 'backend/go.mod',
        depName: 'example.com/a/v7',
        newValue: 'v8.0.0',
        newVersion: 'v8.0.0',
        updateType: 'major',
        depType: 'require',
        isVulnerabilityAlert: true,
      },
      { manager: 'bun', packageFile: 'frontend/package.json', updateType: 'lockFileMaintenance' },
    ]
    expect(JSON.parse(compile(config.postUpgradeTasks.dataFileTemplate, { upgrades }))).toEqual(upgrades)
  })
})
