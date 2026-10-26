import { describe, expect, it } from 'bun:test'
import { prepareTauriSecurity, readAlerts, securityCompanions } from './renovate-prepare.mjs'

const policy = { groupName: 'security fixes', minimumReleaseAge: '3 days', dependencyDashboardApproval: false, rangeStrategy: 'pin' }
const config = { minimumReleaseAge: '3 days', minimumReleaseAgeBuffer: null, vulnerabilityAlerts: policy }
const now = Date.parse('2026-10-26T00:00:00Z')
const old = '2026-10-20T00:00:00Z'
const baseline = { npm: { '@tauri-apps/api': '2.5.0', '@tauri-apps/plugin-opener': '2.3.0', '@tauri-apps/cli': '2.9.0' }, cargo: { 'tauri': '2.5.0', 'tauri-plugin-opener': '2.3.0' } }
const alert = (name, fixed = '2.6.0') => ({ state: 'open', security_vulnerability: { package: { name }, first_patched_version: fixed === null ? null : { identifier: fixed } } })
function registry(npm, cargo) {
  return async url => Response.json(url.includes('registry.npmjs.org')
    ? { time: Object.fromEntries(npm.map(release => [release.version, release.publishedAt])) }
    : { versions: cargo.map(release => ({ num: release.version, created_at: release.publishedAt, yanked: release.yanked })) })
}

describe('securityCompanions', () => {
  it('promotes only the affected pair and constrains each native update to its prepared version', () => {
    const targets = { '@tauri-apps/plugin-opener': '2.6.1', 'tauri-plugin-opener': '2.6.4' }
    const rules = securityCompanions(targets, policy)
    expect(rules).toHaveLength(2)
    for (const rule of rules) {
      expect(rule.matchPackageNames).toHaveLength(1)
      expect(rule.allowedVersions).toBe(targets[rule.matchPackageNames[0]])
      expect(rule.isVulnerabilityAlert).toBe(true)
      expect(rule.force).toEqual(policy)
    }
    const promoted = rules.flatMap(rule => rule.matchPackageNames)
    for (const name of ['tauri', 'tauri-build', '@tauri-apps/api', '@tauri-apps/cli'])
      expect(promoted).not.toContain(name)
    expect(securityCompanions({}, policy)).toEqual([])
  })
})

describe('prepareTauriSecurity', () => {
  it('keeps young, timestamp-free, yanked, and incompatible fixes pending', async () => {
    const npm = [{ version: '2.6.0', publishedAt: old }]
    for (const cargo of [
      [{ version: '2.6.0', publishedAt: '2026-10-25T00:00:00Z' }],
      [{ version: '2.6.0' }],
      [{ version: '2.6.0', publishedAt: old, yanked: true }],
      [{ version: '2.7.0', publishedAt: old }],
    ]) {
      const targets = await prepareTauriSecurity([alert('tauri')], config, baseline, { now, request: registry(npm, cargo) })
      expect(targets).toEqual({})
      expect(securityCompanions(targets, policy)).toEqual([])
    }
  })
  it('chooses the lowest compatible eligible fix, including a major fix', async () => {
    const releases = [{ version: '2.6.0', publishedAt: '2026-10-25T00:00:00Z' }, { version: '3.0.0', publishedAt: old }, { version: '4.0.0', publishedAt: old }]
    const targets = await prepareTauriSecurity([alert('tauri')], config, baseline, { now, request: registry(releases, releases) })
    expect(targets).toEqual({ '@tauri-apps/api': '3.0.0', 'tauri': '3.0.0' })
  })
  it('requires every advisory fix and reads only the affected pair', async () => {
    let requests = 0
    const request = async (url) => {
      requests++
      expect(url).toContain('plugin-opener')
      return registry([{ version: '2.6.0', publishedAt: old }], [{ version: '2.6.0', publishedAt: old }])(url)
    }
    const targets = await prepareTauriSecurity([alert('tauri-plugin-opener'), alert('tauri-plugin-opener', '2.7.0'), alert('typescript')], config, baseline, { now, request })
    expect(requests).toBe(2)
    expect(targets).toEqual({})
  })
  it('does not promote already-fixed, closed, or unfixed alerts, or independent CLI updates', async () => {
    const request = async () => {
      throw new Error('Unexpected registry read.')
    }
    const targets = await prepareTauriSecurity([alert('tauri', null), alert('tauri', '2.4.0'), { ...alert('tauri'), state: 'dismissed' }, alert('@tauri-apps/cli')], config, baseline, { now, request })
    expect(targets).toEqual({})
  })
  it('handles JavaScript alerts and exposes failed registry reads', async () => {
    const releases = [{ version: '2.6.0', publishedAt: old }]
    expect(await prepareTauriSecurity([alert('@tauri-apps/api')], config, baseline, { now, request: registry(releases, releases) })).toEqual({ '@tauri-apps/api': '2.6.0', 'tauri': '2.6.0' })
    await expect(prepareTauriSecurity([alert('tauri')], config, baseline, { request: async () => new Response('', { status: 503 }) })).rejects.toThrow('503')
    await expect(prepareTauriSecurity({}, config, baseline)).rejects.toThrow('array')
  })
})

describe('readAlerts', () => {
  it('reads all pages without mutations', async () => {
    const calls = []
    const alerts = await readAlerts('fixture', async (url, options) => {
      expect(options.method).toBeUndefined()
      calls.push(url)
      return Response.json(calls.length === 1 ? Array.from({ length: 100 }, () => alert('tauri')) : [alert('typescript')])
    })
    expect(alerts).toHaveLength(101)
    expect(calls[1]).toContain('page=2')
  })
  it('fails on missing credentials, denied access, and malformed data', async () => {
    await expect(readAlerts('')).rejects.toThrow()
    await expect(readAlerts('fixture', async () => new Response('', { status: 403 }))).rejects.toThrow('403')
    await expect(readAlerts('fixture', async () => Response.json({}))).rejects.toThrow()
  })
})
