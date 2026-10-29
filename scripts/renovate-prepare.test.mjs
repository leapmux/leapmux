import { describe, expect, it } from 'bun:test'
import { prepareTauriSecurity, readAlerts, securityCompanions } from './renovate-prepare.mjs'
import { matchesCargoRequirement } from './renovate-tauri-releases.mjs'

const policy = {
  groupName: 'security fixes',
  minimumReleaseAge: '3 days',
  dependencyDashboardApproval: false,
  rangeStrategy: 'pin',
}
const config = { minimumReleaseAge: '3 days', minimumReleaseAgeBuffer: null, vulnerabilityAlerts: policy }
const now = Date.parse('2026-10-26T00:00:00Z')
const old = '2026-10-20T00:00:00Z'
const baseline = {
  npm: { '@tauri-apps/api': '2.5.0', '@tauri-apps/plugin-opener': '2.3.0', '@tauri-apps/cli': '2.9.0' },
  cargo: { 'tauri': '2.5.0', 'tauri-plugin-opener': '2.3.0' },
}
function alert(name, fixed = '2.6.0') {
  return {
    state: 'open',
    dependency: {
      manifest_path: name.startsWith('@')
        ? 'frontend/package.json'
        : 'desktop/rust/Cargo.lock',
    },
    security_vulnerability: {
      package: {
        name,
        ecosystem: name.startsWith('@')
          ? 'npm'
          : 'rust',
      },
      vulnerable_version_range: `< ${fixed ?? '999.0.0'}`,
      first_patched_version: fixed === null
        ? null
        : { identifier: fixed },
    },
  }
}
function registry(npm, cargo) {
  return async url => Response.json(url.includes('/dependencies')
    ? { dependencies: [] }
    : url.includes('registry.npmjs.org')
      ? {
          time: Object.fromEntries(npm.map(release => [release.version, release.publishedAt])),
        }
      : {
          versions: cargo.map(release => ({ num: release.version, created_at: release.publishedAt, yanked: release.yanked })),
        })
}

describe('securityCompanions', () => {
  it('constrains Cargo candidates to the exact prepared version rather than a caret range', () => {
    const [rule] = securityCompanions({ tauri: '2.11.5' }, policy)
    expect(matchesCargoRequirement('2.11.5', rule.allowedVersions)).toBe(true)
    expect(matchesCargoRequirement('2.11.6', rule.allowedVersions)).toBe(false)
    expect(matchesCargoRequirement('2.12.0', rule.allowedVersions)).toBe(false)
  })
  it('promotes only the affected pair and constrains each native update to its prepared version', () => {
    const targets = { '@tauri-apps/plugin-opener': '2.6.1', 'tauri-plugin-opener': '2.6.4' }
    const rules = securityCompanions(targets, policy)
    expect(rules).toHaveLength(2)
    for (const rule of rules) {
      expect(rule.matchPackageNames).toHaveLength(1)
      expect(rule.allowedVersions).toBe(`=${targets[rule.matchPackageNames[0]]}`)
      expect(rule.isVulnerabilityAlert).toBe(true)
      expect(rule.force).toEqual(policy)
    }
    const promoted = rules.flatMap(rule => rule.matchPackageNames)
    for (const name of [
      'tauri',
      'tauri-build',
      '@tauri-apps/api',
      '@tauri-apps/cli',
    ])
      expect(promoted).not.toContain(name)
    expect(securityCompanions({}, policy)).toEqual([])
  })
})

describe('prepareTauriSecurity', () => {
  it('excludes every affected range in an advisory when choosing the lowest compatible fix', async () => {
    const packageInfo = { name: 'tauri', ecosystem: 'rust' }
    const vulnerability = (range, fixed) => ({
      package: packageInfo,
      vulnerable_version_range: range,
      first_patched_version: { identifier: fixed },
    })
    const current = {
      npm: { '@tauri-apps/api': '2.11.1' },
      cargo: { tauri: '2.11.3' },
    }
    const primary = vulnerability('>= 2.11.0, < 2.12.0', '2.12.0')
    const advisory = {
      state: 'open',
      dependency: { manifest_path: 'desktop/rust/Cargo.lock' },
      security_vulnerability: primary,
      security_advisory: {
        vulnerabilities: [
          primary,
          vulnerability('>= 2.13.0, < 2.13.2', '2.13.2'),
        ],
      },
    }
    const npm = [
      { version: '2.13.0', publishedAt: old },
    ]
    const cargo = ['2.12.0', '2.13.0', '2.13.2'].map(version => ({ version, publishedAt: old }))
    expect(await prepareTauriSecurity([advisory], config, current, {
      now,
      request: registry(npm, cargo),
    })).toEqual({ '@tauri-apps/api': '2.13.0', 'tauri': '2.13.2' })
    expect(await prepareTauriSecurity([advisory], config, current, {
      now,
      request: registry(npm, cargo.slice(0, 2)),
    })).toEqual({})
  })
  it('does not promote an unaffected direct occurrence or an alert from another dependency graph', async () => {
    const current = {
      npm: { '@tauri-apps/api': '2.11.1' },
      cargo: { tauri: '2.11.3' },
    }
    const primary = {
      package: { name: 'tauri', ecosystem: 'rust' },
      vulnerable_version_range: '>= 3.0.0, < 3.0.1',
      first_patched_version: { identifier: '3.0.1' },
    }
    const advisory = {
      state: 'open',
      dependency: { manifest_path: 'desktop/rust/Cargo.lock' },
      security_vulnerability: primary,
    }
    const releases = [
      { version: '3.0.1', publishedAt: old },
    ]
    for (const candidate of [
      advisory,
      {
        ...advisory,
        dependency: { manifest_path: 'other/Cargo.lock' },
      },
      {
        ...advisory,
        security_vulnerability: {
          ...primary,
          package: { name: 'tauri', ecosystem: 'go' },
        },
      },
    ]) {
      expect(await prepareTauriSecurity([candidate], config, current, {
        now,
        request: registry(releases, releases),
      })).toEqual({})
    }
  })
  it('keeps young, timestamp-free, yanked, and incompatible fixes pending', async () => {
    const npm = [
      { version: '2.6.0', publishedAt: old },
    ]
    for (const cargo of [
      [
        { version: '2.6.0', publishedAt: '2026-10-25T00:00:00Z' },
      ],
      [
        { version: '2.6.0' },
      ],
      [
        { version: '2.6.0', publishedAt: old, yanked: true },
      ],
      [
        { version: '2.7.0', publishedAt: old },
      ],
    ]) {
      const targets = await prepareTauriSecurity([
        alert('tauri'),
      ], config, baseline, {
        now,
        request: registry(npm, cargo),
      })
      expect(targets).toEqual({})
      expect(securityCompanions(targets, policy)).toEqual([])
    }
  })
  it('chooses the lowest compatible eligible fix, including a major fix', async () => {
    const releases = [
      { version: '2.6.0', publishedAt: '2026-10-25T00:00:00Z' },
      { version: '3.0.0', publishedAt: old },
      { version: '4.0.0', publishedAt: old },
    ]
    const targets = await prepareTauriSecurity([
      alert('tauri'),
    ], config, baseline, {
      now,
      request: registry(releases, releases),
    })
    expect(targets).toEqual({ '@tauri-apps/api': '3.0.0', 'tauri': '3.0.0' })
  })
  it('requires every advisory fix and reads only the affected pair', async () => {
    let requests = 0
    const request = async (url) => {
      requests++
      expect(url).toContain('plugin-opener')
      return registry([
        { version: '2.6.0', publishedAt: old },
      ], [
        { version: '2.6.0', publishedAt: old },
      ])(url)
    }
    const targets = await prepareTauriSecurity([
      alert('tauri-plugin-opener'),
      alert('tauri-plugin-opener', '2.7.0'),
      alert('typescript'),
    ], config, baseline, { now, request })
    expect(requests).toBe(2)
    expect(targets).toEqual({})
  })
  it('does not promote already-fixed, closed, or unfixed alerts, or independent CLI updates', async () => {
    const request = async () => {
      throw new Error('Unexpected registry read.')
    }
    const targets = await prepareTauriSecurity([
      alert('tauri', null),
      alert('tauri', '2.4.0'),
      {
        ...alert('tauri'),
        state: 'dismissed',
      },
      alert('@tauri-apps/cli'),
    ], config, baseline, { now, request })
    expect(targets).toEqual({})
  })
  it('handles JavaScript alerts and exposes failed registry reads', async () => {
    const releases = [
      { version: '2.6.0', publishedAt: old },
    ]
    expect(await prepareTauriSecurity([
      alert('@tauri-apps/api'),
    ], config, baseline, {
      now,
      request: registry(releases, releases),
    })).toEqual({ '@tauri-apps/api': '2.6.0', 'tauri': '2.6.0' })
    await expect(prepareTauriSecurity([
      alert('tauri'),
    ], config, baseline, {
      request: async () => new Response('', { status: 503 }),
    })).rejects.toThrow('503')
    await expect(prepareTauriSecurity({}, config, baseline)).rejects.toThrow('array')
  })
})

describe('readAlerts', () => {
  it('follows GitHub canonical repository links using the authenticated repository identity', async () => {
    const calls = []
    const alerts = await readAlerts('fixture', async (url) => {
      calls.push(url)
      const parsed = new URL(url)
      if (parsed.pathname === '/repos/leapmux/leapmux')
        return Response.json({ id: 1151167922 })
      if (parsed.pathname === '/repositories/1151167922/dependabot/alerts') {
        return Response.json([
          alert('typescript'),
        ])
      }
      return Response.json([
        alert('tauri'),
      ], {
        headers: { link: '<https://api.github.com/repositories/1151167922/dependabot/alerts?state=open&per_page=100&after=cursor>; rel="next"' },
      })
    })
    expect(alerts).toHaveLength(2)
    expect(calls).toHaveLength(3)
    expect(calls[0]).toBe('https://api.github.com/repos/leapmux/leapmux')
  })
  const readTestAlerts = (token, request) => readAlerts(token, async (url, options) => {
    if (new URL(url).pathname === '/repos/leapmux/leapmux')
      return Response.json({ id: 1151167922 })
    return request(url, options)
  })

  it('follows opaque cursor links even after a short page and stops without a next link', async () => {
    const calls = []
    const cursor = 'opaque+/cursor=='
    const alerts = await readTestAlerts('fixture', async (url, options) => {
      expect(options.method).toBe('GET')
      const parsed = new URL(url)
      if (parsed.searchParams.has('page'))
        return Response.json({ message: 'Pagination using the page parameter is not supported.' }, { status: 400 })
      calls.push(url)
      if (calls.length === 1) {
        return Response.json([
          alert('tauri'),
        ], {
          headers: { link: `<https://api.github.com/repos/leapmux/leapmux/dependabot/alerts?state=open&per_page=100&after=${encodeURIComponent(cursor)}>; rel="next"` },
        })
      }
      expect(parsed.searchParams.get('after')).toBe(cursor)
      return Response.json(Array.from({ length: 100 }, () => alert('typescript')))
    })
    expect(alerts).toHaveLength(101)
    expect(calls).toHaveLength(2)
  })
  it('handles an empty response without another request', async () => {
    let requests = 0
    expect(await readTestAlerts('fixture', async () => {
      requests++
      return Response.json([])
    })).toEqual([])
    expect(requests).toBe(1)
  })
  it('rejects unsafe or malformed next links before forwarding credentials', async () => {
    const path = '/repos/leapmux/leapmux/dependabot/alerts?state=open&per_page=100&after=cursor'
    for (const link of [
      `<https://other.invalid${path}>; rel="next"`,
      `<http://api.github.com${path}>; rel="next"`,
      `<https://user@api.github.com${path}>; rel="next"`,
      `<https://api.github.com/repos/other/repo/dependabot/alerts?state=open&per_page=100&after=cursor>; rel="next"`,
      '<https://api.github.com/repositories/1/dependabot/alerts?state=open&per_page=100&after=cursor>; rel="next"',
      `<https://api.github.com${path}&page=2>; rel="next"`,
      `<https://api.github.com${path.replace('state=open', 'state=closed')}>; rel="next"`,
      `<https://api.github.com${path}#fragment>; rel="next"`,
      `<https://api.github.com${path.replace('&after=cursor', '')}>; rel="next"`,
      'invalid link header',
      `<https://api.github.com${path}>; rel="next", <https://api.github.com${path}&extra=1>; rel="next"`,
    ]) {
      let requests = 0
      await expect(readTestAlerts('fixture', async () => {
        requests++
        return Response.json([
          alert('tauri'),
        ], {
          headers: { link },
        })
      })).rejects.toThrow('pagination')
      expect(requests).toBe(1)
    }
  })
  it('rejects cursor cycles without retrying the repeated page', async () => {
    let requests = 0
    await expect(readTestAlerts('fixture', async () => {
      requests++
      if (requests > 2)
        throw new Error('The pagination cycle was not detected.')
      return Response.json([
        alert('tauri'),
      ], {
        headers: { link: '<https://api.github.com/repos/leapmux/leapmux/dependabot/alerts?state=open&per_page=100&after=cursor>; rel="next"' },
      })
    })).rejects.toThrow('pagination cycle')
    expect(requests).toBe(2)
  })
  it('fails on missing credentials, denied access, and malformed data', async () => {
    await expect(readTestAlerts('')).rejects.toThrow()
    await expect(readTestAlerts('fixture', async () => new Response('', { status: 403 }))).rejects.toThrow('403')
    await expect(readTestAlerts('fixture', async () => Response.json({}))).rejects.toThrow()
  })
  it('rejects an invalid repository identity before requesting alerts', async () => {
    for (const id of [
      null,
      0,
      -1,
      '1151167922',
      Number.MAX_SAFE_INTEGER + 1,
    ]) {
      let requests = 0
      await expect(readAlerts('fixture', async (url) => {
        requests++
        expect(new URL(url).pathname).toBe('/repos/leapmux/leapmux')
        return Response.json({ id })
      })).rejects.toThrow('repository identity')
      expect(requests).toBe(1)
    }
  })
})
