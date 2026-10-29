import { spawnSync } from 'node:child_process'
import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { resolve } from 'node:path'
import process from 'node:process'
import { describe, expect, it } from 'bun:test'
import { renovateModule as engine } from './renovate-engine.mjs'
import { postUpgrade } from './renovate-post-upgrade.mjs'
import { prepareTauriSecurity, securityCompanions } from './renovate-prepare.mjs'

const requireRenovate = createRequire(new URL('../.github/renovate/package.json', import.meta.url))
const { init: initializeRenovateLogger } = await engine('logger/index.js')
await initializeRenovateLogger()
const { DateTime } = requireRenovate('luxon')
const { getConfig } = await engine('config/defaults.js')
const { getManagerConfig } = await engine('config/index.js')
const { mergeChildConfig } = await engine('config/utils.js')
const { extractPackageFile } = await engine('modules/manager/gomod/extract.js')
const { extractPackageFile: extractPins } = await engine('modules/manager/custom/regex/index.js')
const { get } = await engine('modules/versioning/index.js')
const { applyPackageRules } = await engine('util/package-rules/index.js')
const { compile } = await engine('util/template/index.js')
const { filterInternalChecks } = await engine('workers/repository/process/lookup/filter-checks.js')
const { fetchUpdates } = await engine('workers/repository/process/fetch.js')
const { PackageFiles } = await engine('workers/repository/package-files.js')
const { getWarnings } = await engine('workers/repository/errors-warnings.js')
const { smartTruncate } = await engine('modules/platform/utils/pr-body.js')
const { cronMatches, hasValidSchedule } = await engine('workers/repository/update/branch/schedule.js')
const { generateBranchConfig } = await engine('workers/repository/updates/generate.js')
const { branchifyUpgrades } = await engine('workers/repository/updates/branchify.js')

const root = resolve(import.meta.dirname, '..')
const policy = JSON.parse(readFileSync(resolve(root, 'renovate.json'), 'utf8'))
const config = () => mergeChildConfig(getConfig(), policy)
function rule(dep) {
  return applyPackageRules({
    ...config(),
    packageName: dep.depName,
    ...dep,
  })
}

describe('renovate policy with the pinned engine', () => {
  it('creates one maintenance branch for both Bun lockfiles and Cargo', async () => {
    const files = {
      bun: ['frontend', '.github/renovate'].map(directory => ({
        packageFile: `${directory}/package.json`,
        lockFiles: [`${directory}/bun.lock`],
        deps: [],
      })),
      cargo: [
        {
          packageFile: 'desktop/rust/Cargo.toml',
          lockFiles: ['desktop/rust/Cargo.lock'],
          deps: [],
        },
      ],
    }
    const { branches } = await branchifyUpgrades({
      ...config(),
      errors: [],
      warnings: [],
      repoIsOnboarded: true,
    }, files)
    expect(branches).toHaveLength(1)
    const [branch] = branches
    expect(branch.upgrades.map(upgrade => upgrade.packageFile).sort()).toEqual(['.github/renovate/package.json', 'desktop/rust/Cargo.toml', 'frontend/package.json'])
    expect(branch.upgrades.every(upgrade => upgrade.updateType === 'lockFileMaintenance' && upgrade.isLockFileMaintenance)).toBe(true)
    expect(branch.skipArtifactsUpdate).toBe(false)
    expect(branch.prTitle).toBe('chore(deps): Update lockfiles')
  })
  it('keeps Go security artifacts skipped while Bun and Cargo security companions remain together', async () => {
    const security = { isVulnerabilityAlert: true, force: policy.vulnerabilityAlerts }
    const dependency = (depName, datasource, currentValue, newVersion) => ({
      ...security,
      depName,
      packageName: depName,
      datasource,
      currentValue,
      updates: [
        { updateType: 'minor', newValue: newVersion, newVersion },
      ],
    })
    const packageFiles = {
      gomod: [
        {
          packageFile: 'backend/go.mod',
          deps: [
            dependency('example.com/module', 'go', 'v1.0.0', 'v1.1.0'),
          ],
        },
      ],
      bun: [
        {
          packageFile: 'frontend/package.json',
          deps: [
            dependency('@tauri-apps/api', 'npm', '2.11.0', '2.12.0'),
          ],
        },
      ],
      cargo: [
        {
          packageFile: 'desktop/rust/Cargo.toml',
          deps: [
            dependency('tauri', 'crate', '2.11.0', '2.12.0'),
          ],
        },
      ],
    }
    const result = await branchifyUpgrades({
      ...config(),
      errors: [],
      warnings: [],
      semanticCommits: 'enabled',
      repoIsOnboarded: true,
    }, packageFiles)
    const branches = result.branches.filter(branch => branch.isVulnerabilityAlert)
    expect(branches).toHaveLength(2)
    const go = branches.find(branch => branch.upgrades.every(upgrade => upgrade.manager === 'gomod'))
    const tauri = branches.find(branch => branch.upgrades.some(upgrade => upgrade.manager === 'bun'))
    expect(go.skipArtifactsUpdate).toBe(true)
    expect(go.branchName).toBe('renovate/go-security')
    expect(tauri.upgrades.map(upgrade => upgrade.manager).sort()).toEqual(['bun', 'cargo'])
    expect(tauri.skipArtifactsUpdate).toBe(false)
    for (const branch of branches) {
      expect(branch.schedule).toEqual([])
      expect(branch.dependencyDashboardApproval).toBe(false)
      expect(branch.minimumReleaseAge).toBe('3 days')
      expect(branch.labels).toContain('security')
    }
  })
  it('extracts every tool and image pin, including digits in s6-overlay, with unchanged values', () => {
    const text = readFileSync(resolve(root, 'versions.env'), 'utf8')
    const expected = new Map([
      ...text.matchAll(/^([A-Z_][A-Z0-9_]*)=(.+)$/gm),
    ].map(match => [match[1], match[2]]))
    expected.delete('VERSION')
    const extracted = extractPins(text, 'versions.env', policy.customManagers[0]).deps
    expect(extracted).toHaveLength(expected.size)
    for (const dep of extracted) {
      expect(dep.datasource === 'docker'
        ? `${dep.depName}:${dep.currentValue}`
        : dep.currentValue).toBe(expected.get(dep.depType))
    }
    const bun = extracted.find(dep => dep.depType === 'BUN_VERSION')
    expect(new RegExp(bun.extractVersion).exec('bun-v1.4.1').groups.version).toBe('1.4.1')
    expect(extracted.find(dep => dep.depType === 'S6_OVERLAY_VERSION').versioning).toBe('loose')
    expect(get('loose').getNewValue({ currentValue: '3.2.3.2', currentVersion: 'v3.2.3.2', newVersion: 'v3.2.3.3' })).toBe('3.2.3.3')
  })
  it('extracts the runner images and proposes no update for them', async () => {
    const extracted = extractPins(readFileSync(resolve(root, 'versions.env'), 'utf8'), 'versions.env', policy.customManagers[0]).deps
    const manager = getManagerConfig(config(), 'regex').manager
    const runners = extracted.filter(dep => dep.datasource === 'github-runners')
    expect(runners.map(dep => dep.depType).sort()).toEqual(['MACOS_RUNNER', 'UBUNTU_RUNNER', 'WINDOWS_RUNNER'])
    for (const dep of runners)
      expect((await rule({ ...dep, manager, packageFile: 'versions.env' })).enabled, dep.depType).toBe(false)
    const bun = extracted.find(dep => dep.depType === 'BUN_VERSION')
    expect((await rule({ ...bun, manager, packageFile: 'versions.env' })).enabled, 'an ordinary pin stays enabled').not.toBe(false)
  })
  it('accepts the pinned engine custom-manager metadata and synchronizes only the toolchain claims', async () => {
    const extraction = extractPins(readFileSync(resolve(root, 'versions.env'), 'utf8'), 'versions.env', policy.customManagers[0])
    const dependency = extraction.deps.find(dep => dep.depType === 'BUN_VERSION')
    const manager = getManagerConfig(config(), 'regex').manager
    const calls = []
    await postUpgrade([
      {
        ...dependency,
        manager,
        packageFile: 'versions.env',
        newValue: '1.4.2',
      },
    ], {
      run: (command, args) => calls.push([command, args]),
    })
    expect(calls).toEqual([
      [
        'task',
        ['sync-versions'],
      ],
    ])
  })
  it('retains indirect Go defaults while enabling tools and Hextra', async () => {
    const extraction = extractPackageFile('module example.com/app\nrequire (\n example.com/direct v1.0.0\n example.com/indirect v1.0.0 // indirect\n example.com/tool v1.0.0 // indirect\n)\ntool example.com/tool/cmd/tool\n')
    expect(extraction.deps.find(dep => dep.depName === 'example.com/indirect').enabled).toBe(false)
    expect(extraction.deps.find(dep => dep.depName === 'example.com/tool').enabled).toBeUndefined()
    const hextra = await rule({
      manager: 'gomod',
      packageFile: 'site/go.mod',
      depName: 'github.com/imfing/hextra',
      enabled: false,
      depType: 'indirect',
    })
    expect(hextra.enabled).toBe(true)
    const indirect = await rule({
      manager: 'gomod',
      packageFile: 'backend/go.mod',
      depName: 'example.com/indirect',
      enabled: false,
      depType: 'indirect',
    })
    expect(indirect.enabled).toBe(false)
    const security = mergeChildConfig(indirect, { isVulnerabilityAlert: true, force: config().vulnerabilityAlerts })
    expect(security.enabled).toBe(true)
    expect((await rule({ manager: 'gomod', packageFile: 'desktop/go/go.mod', depName: 'github.com/leapmux/leapmux' })).ignoreDeps).toContain('github.com/leapmux/leapmux')
  })
  it('keeps the dependency-import utility library eligible for routine Go updates', () => {
    const extraction = extractPackageFile(readFileSync(resolve(root, 'backend/go.mod'), 'utf8'))
    const dependency = extraction.deps.find(dep => dep.depName === 'golang.org/x/mod')
    expect(dependency.depType).toBe('require')
    expect(dependency.enabled).not.toBe(false)
  })
  it('requires dashboard approval for routine majors and preserves security overrides after major settings', async () => {
    const normal = mergeChildConfig(config(), config().major)
    expect(normal.dependencyDashboardApproval).toBe(true)
    const security = mergeChildConfig(mergeChildConfig(config(), { force: config().vulnerabilityAlerts }), config().major)
    expect(security.dependencyDashboardApproval).toBe(false)
    expect(security.schedule).toEqual([])
    expect(security.minimumReleaseAge).toBe('3 days')
    expect(security.minimumReleaseAgeBehaviour).toBe('timestamp-required')
    expect(security.internalChecksFilter).toBe('strict')
    expect(security.automerge).toBe(false)
    expect(security.autoApprove).toBe(false)
    expect(security.platformAutomerge).toBe(false)
    expect(security.commitMessageSuffix).toBe('')
    expect(security.vulnerabilityFixStrategy).toBe('lowest')
    expect(security.labels).toContain('security')
  })
  it('keeps the locally replaced self dependency ignored under forced security configuration', async () => {
    const dep = await applyPackageRules({
      ...config(),
      manager: 'gomod',
      packageFile: 'desktop/go/go.mod',
      depName: 'github.com/leapmux/leapmux',
      packageName: 'github.com/leapmux/leapmux',
      force: config().vulnerabilityAlerts,
    })
    expect(dep.ignoreDeps).toContain('github.com/leapmux/leapmux')
    for (const security of [false, true]) {
      const packageFiles = {
        gomod: [
          {
            packageFile: 'desktop/go/go.mod',
            deps: [
              {
                depName: 'github.com/leapmux/leapmux',
                packageName: 'github.com/leapmux/leapmux',
                currentValue: 'v0.0.0',
                datasource: 'go',
              },
            ],
          },
        ],
      }
      await fetchUpdates(mergeChildConfig(config(), security
        ? { isVulnerabilityAlert: true, force: config().vulnerabilityAlerts }
        : {}), packageFiles)
      expect(packageFiles.gomod[0].deps[0].skipReason).toBe('ignored')
      expect(packageFiles.gomod[0].deps[0].updates).toEqual([])
    }
    PackageFiles.clear()
  })
  it('keeps unrelated Tauri majors subject to approval and preserves exact security companion targets', async () => {
    const base = config()
    const targets = { '@tauri-apps/plugin-opener': '2.6.0', 'tauri-plugin-opener': '2.6.1' }
    const packageRules = [
      ...base.packageRules,
      ...securityCompanions(targets, base.vulnerabilityAlerts),
    ]
    const unrelated = await applyPackageRules({
      ...base,
      packageRules,
      manager: 'cargo',
      packageFile: 'desktop/rust/Cargo.toml',
      depName: 'tauri',
      packageName: 'tauri',
    })
    expect(mergeChildConfig(unrelated, unrelated.major).dependencyDashboardApproval).toBe(true)
    expect(unrelated.isVulnerabilityAlert).not.toBe(true)
    const nativeAlertRule = {
      matchPackageNames: ['tauri-plugin-opener'],
      isVulnerabilityAlert: true,
      force: base.vulnerabilityAlerts,
    }
    const affected = await applyPackageRules({
      ...base,
      packageRules: [...packageRules, nativeAlertRule],
      manager: 'cargo',
      packageFile: 'desktop/rust/Cargo.toml',
      depName: 'tauri-plugin-opener',
      packageName: 'tauri-plugin-opener',
    })
    expect(mergeChildConfig(affected, affected.major).dependencyDashboardApproval).toBe(false)
    expect(affected.allowedVersions).toBe('=2.6.1')
    expect(affected.rangeStrategy).toBe('pin')
  })
  it('preserves the safe exact constraint through the real native vulnerability rule in an isolated process', async () => {
    const packageInfo = { name: 'tauri', ecosystem: 'rust' }
    const primary = {
      package: packageInfo,
      severity: 'high',
      vulnerable_version_range: '>= 2.11.0, < 2.12.0',
      first_patched_version: { identifier: '2.12.0' },
    }
    const alerts = [
      {
        state: 'open',
        dependency: { manifest_path: 'desktop/rust/Cargo.lock' },
        security_vulnerability: primary,
        security_advisory: {
          summary: 'Fixture advisory',
          description: 'Fixture description',
          severity: 'high',
          identifiers: [
            { value: 'GHSA-fixture-code-test' },
          ],
          vulnerabilities: [
            primary,
            {
              ...primary,
              vulnerable_version_range: '>= 2.13.0, < 2.13.2',
              first_patched_version: { identifier: '2.13.2' },
            },
          ],
        },
      },
    ]
    const targets = await prepareTauriSecurity(alerts, policy, {
      npm: { '@tauri-apps/api': '2.11.1' },
      cargo: { tauri: '2.11.3' },
    }, {
      now: Date.parse('2026-10-26T00:00:00Z'),
      request: async url => Response.json(url.includes('/dependencies')
        ? { dependencies: [] }
        : url.includes('registry.npmjs.org')
          ? {
              time: { '2.13.0': '2026-10-20T00:00:00Z' },
            }
          : {
              versions: ['2.12.0', '2.13.0', '2.13.2'].map(num => ({ num, created_at: '2026-10-20T00:00:00Z' })),
            }),
    })
    const source = String.raw`
      const root=process.argv[1];
      const {pathToFileURL}=await import('node:url');
      const {resolve}=await import('node:path');
      const engine=p=>import(pathToFileURL(resolve(root,'.github/renovate/node_modules/renovate/dist',p)).href);
      const {init}=await engine('logger/index.js'); await init();
      const {policy,rules,alerts}=JSON.parse(process.argv[2]);
      const {default:api}=await engine('modules/platform/api.js');
      const {setPlatformApi}=await engine('modules/platform/index.js');
      api.set('github',{...api.get('github'),getVulnerabilityAlerts:async()=>alerts}); setPlatformApi('github');
      const {getConfig}=await engine('config/defaults.js');
      const {mergeChildConfig}=await engine('config/utils.js');
      const {detectVulnerabilityAlerts}=await engine('workers/repository/init/vulnerability.js');
      const {applyPackageRules}=await engine('util/package-rules/index.js');
      const {api:cargo}=await engine('modules/versioning/cargo/index.js');
      const config=mergeChildConfig(getConfig(),policy); config.packageRules.push(...rules);
      const native=await detectVulnerabilityAlerts(config);
      const dep=await applyPackageRules({...native,manager:'cargo',datasource:'crate',packageFile:'desktop/rust/Cargo.toml',packageName:'tauri',depName:'tauri',currentVersion:'2.11.3',currentValue:'2'});
      const major=mergeChildConfig(dep,dep.major);
      console.log(JSON.stringify({constraint:dep.allowedVersions,firstFixed:dep.vulnerabilityFixVersion,safe:cargo.matches('2.13.2',dep.allowedVersions),unsafe:cargo.matches('2.13.0',dep.allowedVersions),later:cargo.matches('2.13.3',dep.allowedVersions),approval:major.dependencyDashboardApproval,age:major.minimumReleaseAge,schedule:major.schedule}));
    `
    const result = spawnSync('node', [
      '--input-type=module',
      '-e',
      source,
      root,
      JSON.stringify({
        policy,
        alerts,
        rules: securityCompanions(targets, policy.vulnerabilityAlerts),
      }),
    ], {
      encoding: 'utf8',
      env: { ...process.env, LOG_LEVEL: 'fatal', LOG_FORMAT: 'json' },
    })
    expect(result.status, result.stderr).toBe(0)
    expect(JSON.parse(result.stdout)).toEqual({
      constraint: '=2.13.2',
      firstFixed: '2.12.0',
      safe: true,
      unsafe: false,
      later: false,
      approval: false,
      age: '3 days',
      schedule: [],
    })
  })
  it('filters pending releases even when no older candidate exists', async () => {
    const settings = {
      ...config(),
      currentVersion: '1.0.0',
      depName: 'fixture',
      packageName: 'fixture',
      datasource: 'npm',
    }
    const young = {
      version: '2.0.0',
      releaseTimestamp: new Date(Date.now() - 86400000).toISOString(),
    }
    const missing = { version: '2.0.1' }
    for (const candidate of [young, missing]) {
      const result = await filterInternalChecks(settings, get('semver'), 'major', [candidate])
      expect(result.pendingChecks).toBe(true)
    }
    const older = {
      version: '2.0.0',
      releaseTimestamp: new Date(Date.now() - 4 * 86400000).toISOString(),
    }
    const result = await filterInternalChecks(settings, get('semver'), 'major', [
      older,
      { ...young, version: '2.1.0' },
    ])
    expect(result.release.version).toBe('2.0.0')
    expect(result.pendingChecks).toBe(false)
  })
  it('uses the Monday 02:00 through 06:00 Seoul window and the daily 03:17 trigger', () => {
    expect(hasValidSchedule(policy.schedule)[0]).toBe(true)
    for (const [iso, expected] of [
      ['2026-10-25T16:59:00Z', false],
      ['2026-10-25T17:00:00Z', true],
      ['2026-10-25T18:17:00Z', true],
      ['2026-10-25T20:59:00Z', true],
      ['2026-10-25T21:00:00Z', false],
      ['2026-10-26T18:17:00Z', false],
    ])
      expect(cronMatches(policy.schedule[0], DateTime.fromISO(iso), policy.timezone), iso).toBe(expected)
    const workflow = Bun.YAML.parse(readFileSync(resolve(root, '.github/workflows/renovate.yaml'), 'utf8'))
    const now = DateTime.fromISO('2026-10-26T18:17:00Z')
    expect(cronMatches(workflow.on.schedule[0].cron, now, 'UTC')).toBe(true)
    expect(now.setZone(policy.timezone).toFormat('yyyy-MM-dd HH:mm')).toBe('2026-10-27 03:17')
  })
  it('renders capitalized dependency titles shorter than 72 characters and exactly three PR sections', async () => {
    for (const [manager, packageFile, depName] of [
      ['gomod', 'backend/go.mod', 'github.com/cenkalti/backoff/v7'],
      ['gomod', 'site/go.mod', 'github.com/imfing/hextra'],
      ['bun', 'frontend/package.json', '@bufbuild/protobuf'],
      ['cargo', 'desktop/rust/Cargo.toml', 'tauri-plugin-notification'],
      ['github-actions', '.github/workflows/ci.yaml', 'actions/checkout'],
      ['regex', 'versions.env', 'node'],
    ]) {
      const dep = await rule({
        manager,
        packageFile,
        depName,
        currentValue: '1.0.0',
        newValue: '2.0.0',
        newVersion: '2.0.0',
        newMajor: 2,
        updateType: 'major',
        branchName: 'renovate/fixture',
        depType: 'require',
      })
      const branch = generateBranchConfig([dep])
      expect(branch.prTitle).toMatch(/^chore\(deps\): Update /)
      expect(branch.prTitle.length).toBeLessThan(72)
    }
    const body = compile(policy.prBodyTemplate, {
      table: '- Runtime: 1 → 2',
      notes: '',
      changelogs: '',
      warnings: 'Artifact generation failed.',
    }, false)
    const maintenance = mergeChildConfig(config(), config().lockFileMaintenance)
    const branch = generateBranchConfig([
      {
        ...maintenance,
        branchName: 'renovate/lock-file-maintenance',
        manager: 'bun',
        packageFile: 'frontend/package.json',
        updateType: 'lockFileMaintenance',
        isLockFileMaintenance: true,
      },
    ])
    expect(branch.prTitle).toBe('chore(deps): Update lockfiles')
    expect([
      ...body.matchAll(/^## (.+)$/gm),
    ].map(match => match[1])).toEqual(['Motivation', 'Modifications', 'Result'])
    expect(body).not.toContain('Artifact generation failed.')
    expect(policy.commitBody).toBe('')
    expect(policy.commitTrailers).toEqual([])
    expect(policy.commitBodyTable).toBe(false)
    expect(policy.prFooter).toBe('')
  })
  it('keeps upstream details and warnings out of the three core PR sections', () => {
    const warnings = getWarnings({
      warnings: [
        { topic: 'fixture', message: 'Artifact generation failed.' },
      ],
    })
    const body = compile(policy.prBodyTemplate, {
      table: '- Runtime: 1 → 2',
      warnings,
      notes: 'Security advisory details.',
      changelogs: 'Release note details.',
    }, false)
    expect([
      ...body.matchAll(/^#{1,2} (.+)$/gm),
    ].map(match => match[1])).toEqual(['Motivation', 'Modifications', 'Result'])
    expect(body).not.toContain('Artifact generation failed.')
    expect(body).not.toContain('Security advisory details.')
    expect(body).not.toContain('Release note details.')
  })
  it('keeps the core body short when upstream notes exceed the platform limit', () => {
    const warnings = getWarnings({
      warnings: [
        { topic: 'fixture', message: 'Artifact generation failed.' },
      ],
    })
    for (const largeField of ['notes', 'changelogs']) {
      const body = compile(policy.prBodyTemplate, {
        table: '- Runtime: 1 → 2',
        warnings,
        [largeField]: 'Upstream security and release details.\n'.repeat(1600),
      }, false)
      const rendered = smartTruncate(body, 58000)
      expect(rendered.length).toBeLessThanOrEqual(58000)
      expect([
        ...rendered.matchAll(/^#{1,2} (.+)$/gm),
      ].map(match => match[1])).toEqual(['Motivation', 'Modifications', 'Result'])
      expect(rendered).not.toContain('Artifact generation failed.')
      expect(rendered.length).toBeLessThan(1000)
      expect(rendered).toContain('human review and required CI')
    }
  })
})
