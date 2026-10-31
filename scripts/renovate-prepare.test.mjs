import { describe, expect, it } from 'bun:test'
import { prepareAuthorized, prepareTauriSecurity, securityCompanions } from './renovate-prepare.mjs'
import { matchesCargoRequirement } from './renovate-tauri-releases.mjs'
import { crateIndexResponse, isCrateIndexRead } from './test-support.mjs'

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
  return async url => isCrateIndexRead(url)
    ? crateIndexResponse(cargo.map(release => ({
        vers: release.version,
        yanked: release.yanked === true,
        ...(release.publishedAt == null
          ? {}
          : { pubtime: release.publishedAt }),
      })))
    : Response.json(url.includes('/dependencies')
        ? { dependencies: [] }
        : {
            time: Object.fromEntries(npm.map(release => [release.version, release.publishedAt])),
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

describe('prepareAuthorized', () => {
  const files = {
    'frontend/bun.lock': '{ "packages": { "fixed-pkg": ["fixed-pkg@1.2.0", "", {}, "sha512-x"], "other": ["other@3.0.0", "", {}, "sha512-y"] } }',
    '.github/renovate/bun.lock': '{ "packages": { "fixed-pkg": ["fixed-pkg@1.2.3", "", {}, "sha512-z"] } }',
    'desktop/rust/Cargo.lock': '[[package]]\nname = "tauri-plugin-x"\nversion = "2.4.0"\nsource = "registry+https://github.com/rust-lang/crates.io-index"\n',
    'backend/go.mod': 'module github.com/leapmux/leapmux\n\ngo 1.25\n\nrequire github.com/go-jose/go-jose/v4 v4.1.6\n',
  }
  const read = path => files[path] ?? null
  const openAlert = (ecosystem, name, manifest) => ({
    state: 'open',
    dependency: { manifest_path: manifest },
    security_vulnerability: {
      package: { ecosystem, name },
    },
  })
  const record = (ecosystem, name, range, fixed) => ({
    vulnerabilities: [
      {
        package: { ecosystem, name },
        vulnerable_version_range: range,
        first_patched_version: fixed,
      },
    ],
  })
  function readers() {
    const calls = []
    return {
      calls,
      advisories: async (ecosystem, name) => {
        calls.push(`${ecosystem}:${name}`)
        return [
          record('npm', 'fixed-pkg', '< 1.2.3', '1.2.3'),
          record('rust', 'tauri-plugin-x', '< 2.4.1', '2.4.1'),
          record('go', 'github.com/go-jose/go-jose/v4', '< 4.1.7', '4.1.7'),
          record('npm', 'unlocked', '< 9.0.0', '9.0.0'),
        ]
      },
      npmManifest: async name => ({
        dependencies: name === 'fixed-pkg'
          ? { 'fixed-pkg-core': '1.2.3' }
          : {},
      }),
      crateDependencies: async name => name === 'tauri-plugin-x'
        ? [
            { crate_id: 'tauri', req: '=2.11.6', kind: 'normal' },
          ]
        : [],
    }
  }
  const empty = {
    'frontend/bun.lock': {},
    '.github/renovate/bun.lock': {},
    'site/bun.lock': {},
    'desktop/rust/Cargo.lock': {},
  }
  it('writes the roots of the open alerts with their exact pins under the lockfile of each alert, without reading Go', async () => {
    const injected = readers()
    const paths = []
    const authorized = await prepareAuthorized([
      openAlert('npm', 'fixed-pkg', 'frontend/package.json'),
      openAlert('rust', 'tauri-plugin-x', 'desktop/rust/Cargo.lock'),
      openAlert('go', 'github.com/go-jose/go-jose/v4', 'backend/go.mod'),
      {
        ...openAlert('npm', 'other', 'frontend/bun.lock'),
        state: 'fixed',
      },
      openAlert('pip', 'fixed-pkg', 'requirements.txt'),
      { state: 'open' },
    ], {
      read: (path) => {
        paths.push(path)
        return read(path)
      },
      readers: injected,
    })
    expect(authorized).toEqual({
      ...empty,
      'frontend/bun.lock': {
        'fixed-pkg@1.2.3': ['fixed-pkg-core@1.2.3'],
      },
      'desktop/rust/Cargo.lock': {
        'tauri-plugin-x@2.4.1': ['tauri@2.11.6'],
      },
    })
    expect(injected.calls.sort()).toEqual(['crates:tauri-plugin-x', 'npm:fixed-pkg'])
    // Only the lockfile of an alert is read. Required CI computes the Go exemptions itself.
    expect(paths.sort()).toEqual(['desktop/rust/Cargo.lock', 'frontend/bun.lock'])
  })
  it('authorizes a fix only in the lockfile that the alert reports, from that lockfile\'s base versions', async () => {
    const injected = readers()
    // The Renovate tree already locks the patched version, so its alert authorizes nothing,
    // and the vulnerable frontend version authorizes nothing without an alert of its own.
    expect(await prepareAuthorized([
      openAlert('npm', 'fixed-pkg', '.github/renovate/bun.lock'),
    ], { read, readers: injected })).toEqual(empty)
  })
  it('writes an empty map for a local run without reading a lockfile or an advisory', async () => {
    const injected = readers()
    expect(await prepareAuthorized([
      openAlert('npm', 'fixed-pkg', 'frontend/bun.lock'),
    ], {
      local: true,
      read: () => {
        throw new Error('A local run must not read a lockfile.')
      },
      readers: injected,
    })).toEqual(empty)
    expect(injected.calls).toEqual([])
  })
  it('authorizes nothing for an alert whose package the trusted lockfile does not lock, or whose lockfile is absent', async () => {
    const injected = readers()
    expect(await prepareAuthorized([
      openAlert('npm', 'unlocked', 'frontend/bun.lock'),
    ], { read, readers: injected })).toEqual(empty)
    expect(await prepareAuthorized([
      openAlert('npm', 'fixed-pkg', 'frontend/bun.lock'),
    ], {
      read: () => null,
      readers: injected,
    })).toEqual(empty)
    expect(injected.calls).toEqual([])
  })
})
