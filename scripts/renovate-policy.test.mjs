import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { resolve } from 'node:path'
import { describe, expect, it } from 'bun:test'
import { postUpgrade } from './renovate-post-upgrade.mjs'
import { securityCompanions } from './renovate-prepare.mjs'

const requireRenovate = createRequire(new URL('../.github/renovate/package.json', import.meta.url))
// Renovate exposes no public policy-test API. Resolve its installed internals from the pinned manifest.
const engine = relative => import(requireRenovate.resolve(`renovate/dist/${relative}`))
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
const { cronMatches, hasValidSchedule } = await engine('workers/repository/update/branch/schedule.js')
const { generateBranchConfig } = await engine('workers/repository/updates/generate.js')

const root = resolve(import.meta.dirname, '..')
const policy = JSON.parse(readFileSync(resolve(root, 'renovate.json'), 'utf8'))
const config = () => mergeChildConfig(getConfig(), policy)
const rule = dep => applyPackageRules({ ...config(), packageName: dep.depName, ...dep })

describe('renovate policy with the pinned engine', () => {
  it('extracts every tool and image pin, including digits in s6-overlay, with unchanged values', () => {
    const text = readFileSync(resolve(root, 'versions.env'), 'utf8')
    const expected = new Map([...text.matchAll(/^([A-Z_][A-Z0-9_]*)=(.+)$/gm)].map(match => [match[1], match[2]]))
    expected.delete('VERSION')
    const extracted = extractPins(text, 'versions.env', policy.customManagers[0]).deps
    expect(extracted).toHaveLength(expected.size)
    for (const dep of extracted)
      expect(dep.datasource === 'docker' ? `${dep.depName}:${dep.currentValue}` : dep.currentValue).toBe(expected.get(dep.depType))
    const bun = extracted.find(dep => dep.depType === 'BUN_VERSION')
    expect(new RegExp(bun.extractVersion).exec('bun-v1.4.1').groups.version).toBe('1.4.1')
    expect(extracted.find(dep => dep.depType === 'S6_OVERLAY_VERSION').versioning).toBe('loose')
    expect(get('loose').getNewValue({ currentValue: '3.2.3.2', currentVersion: 'v3.2.3.2', newVersion: 'v3.2.3.3' })).toBe('3.2.3.3')
  })
  it('accepts the pinned engine custom-manager metadata and synchronizes only the toolchain claims', async () => {
    const extraction = extractPins(readFileSync(resolve(root, 'versions.env'), 'utf8'), 'versions.env', policy.customManagers[0])
    const dependency = extraction.deps.find(dep => dep.depType === 'BUN_VERSION')
    const manager = getManagerConfig(config(), 'regex').manager
    const calls = []
    await postUpgrade([{ ...dependency, manager, packageFile: 'versions.env', newValue: '1.4.2' }], { run: (command, args) => calls.push([command, args]) })
    expect(calls).toEqual([['task', ['sync-versions']]])
  })
  it('retains indirect Go defaults while enabling tools and Hextra', async () => {
    const extraction = extractPackageFile('module example.com/app\nrequire (\n example.com/direct v1.0.0\n example.com/indirect v1.0.0 // indirect\n example.com/tool v1.0.0 // indirect\n)\ntool example.com/tool/cmd/tool\n')
    expect(extraction.deps.find(dep => dep.depName === 'example.com/indirect').enabled).toBe(false)
    expect(extraction.deps.find(dep => dep.depName === 'example.com/tool').enabled).toBeUndefined()
    const hextra = await rule({ manager: 'gomod', packageFile: 'site/go.mod', depName: 'github.com/imfing/hextra', enabled: false, depType: 'indirect' })
    expect(hextra.enabled).toBe(true)
    const indirect = await rule({ manager: 'gomod', packageFile: 'backend/go.mod', depName: 'example.com/indirect', enabled: false, depType: 'indirect' })
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
    const dep = await applyPackageRules({ ...config(), manager: 'gomod', packageFile: 'desktop/go/go.mod', depName: 'github.com/leapmux/leapmux', packageName: 'github.com/leapmux/leapmux', force: config().vulnerabilityAlerts })
    expect(dep.ignoreDeps).toContain('github.com/leapmux/leapmux')
    for (const security of [false, true]) {
      const packageFiles = { gomod: [{ packageFile: 'desktop/go/go.mod', deps: [{ depName: 'github.com/leapmux/leapmux', packageName: 'github.com/leapmux/leapmux', currentValue: 'v0.0.0', datasource: 'go' }] }] }
      await fetchUpdates(mergeChildConfig(config(), security ? { isVulnerabilityAlert: true, force: config().vulnerabilityAlerts } : {}), packageFiles)
      expect(packageFiles.gomod[0].deps[0].skipReason).toBe('ignored')
      expect(packageFiles.gomod[0].deps[0].updates).toEqual([])
    }
    PackageFiles.clear()
  })
  it('keeps unrelated Tauri majors subject to approval and preserves exact security companion targets', async () => {
    const base = config()
    const targets = { '@tauri-apps/plugin-opener': '2.6.0', 'tauri-plugin-opener': '2.6.1' }
    const packageRules = [...base.packageRules, ...securityCompanions(targets, base.vulnerabilityAlerts)]
    const unrelated = await applyPackageRules({ ...base, packageRules, manager: 'cargo', packageFile: 'desktop/rust/Cargo.toml', depName: 'tauri', packageName: 'tauri' })
    expect(mergeChildConfig(unrelated, unrelated.major).dependencyDashboardApproval).toBe(true)
    expect(unrelated.isVulnerabilityAlert).not.toBe(true)
    const nativeAlertRule = { matchPackageNames: ['tauri-plugin-opener'], isVulnerabilityAlert: true, force: base.vulnerabilityAlerts }
    const affected = await applyPackageRules({ ...base, packageRules: [...packageRules, nativeAlertRule], manager: 'cargo', packageFile: 'desktop/rust/Cargo.toml', depName: 'tauri-plugin-opener', packageName: 'tauri-plugin-opener' })
    expect(mergeChildConfig(affected, affected.major).dependencyDashboardApproval).toBe(false)
    expect(affected.allowedVersions).toBe('2.6.1')
    expect(affected.rangeStrategy).toBe('pin')
  })
  it('filters pending releases even when no older candidate exists', async () => {
    const settings = { ...config(), currentVersion: '1.0.0', depName: 'fixture', packageName: 'fixture', datasource: 'npm' }
    const young = { version: '2.0.0', releaseTimestamp: new Date(Date.now() - 86400000).toISOString() }
    const missing = { version: '2.0.1' }
    for (const candidate of [young, missing]) {
      const result = await filterInternalChecks(settings, get('semver'), 'major', [candidate])
      expect(result.pendingChecks).toBe(true)
    }
    const older = { version: '2.0.0', releaseTimestamp: new Date(Date.now() - 4 * 86400000).toISOString() }
    const result = await filterInternalChecks(settings, get('semver'), 'major', [older, { ...young, version: '2.1.0' }])
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
      const dep = await rule({ manager, packageFile, depName, currentValue: '1.0.0', newValue: '2.0.0', newVersion: '2.0.0', newMajor: 2, updateType: 'major', branchName: 'renovate/fixture', depType: 'require' })
      const branch = generateBranchConfig([dep])
      expect(branch.prTitle).toMatch(/^chore\(deps\): Update /)
      expect(branch.prTitle.length).toBeLessThan(72)
    }
    const body = compile(policy.prBodyTemplate, { table: '- Runtime: 1 → 2', notes: '', changelogs: '', warnings: 'Artifact generation failed.' }, false)
    const maintenance = mergeChildConfig(config(), config().lockFileMaintenance)
    const branch = generateBranchConfig([{ ...maintenance, branchName: 'renovate/lock-file-maintenance', manager: 'bun', packageFile: 'frontend/package.json', updateType: 'lockFileMaintenance', isLockFileMaintenance: true }])
    expect(branch.prTitle).toBe('chore(deps): Update lockfiles')
    expect([...body.matchAll(/^## (.+)$/gm)].map(match => match[1])).toEqual(['Motivation', 'Modifications', 'Result'])
    expect(body).toContain('Artifact generation failed.')
  })
  it('keeps native warning details inside the three required PR sections', () => {
    const warnings = getWarnings({ warnings: [{ topic: 'fixture', message: 'Artifact generation failed.' }] })
    const body = compile(policy.prBodyTemplate, { warnings }, false)
    expect([...body.matchAll(/^#{1,2} (.+)$/gm)].map(match => match[1])).toEqual(['Motivation', 'Modifications', 'Result'])
    expect(body).toContain('Artifact generation failed.')
  })
})
