import { describe, expect, it } from 'bun:test'
import { planTauriTargets, tauriCrateRequirement } from './renovate-tauri-graph.mjs'
import { crateIndexResponse, isCrateIndexRead } from './test-support.mjs'

const now = Date.parse('2026-10-26T00:00:00Z')
const old = '2026-10-20T00:00:00Z'
const policy = {
  minimumReleaseAge: '3 days',
  minimumReleaseAgeBuffer: null,
  vulnerabilityAlerts: { minimumReleaseAge: '2 hours', minimumReleaseAgeBehaviour: 'timestamp-optional' },
}
const core = {
  npm: '@tauri-apps/api',
  cargo: 'tauri',
  npmVersion: '2.11.0',
  cargoVersion: '2.11.0',
}
const plugin = {
  npm: '@tauri-apps/plugin-opener',
  cargo: 'tauri-plugin-opener',
  npmVersion: '2.5.0',
  cargoVersion: '2.5.0',
}
const proposed = {
  npm: { [core.npm]: core.npmVersion, [plugin.npm]: plugin.npmVersion },
  cargo: { [core.cargo]: core.cargoVersion, [plugin.cargo]: '2.6.0' },
}

function registry({ required = '2.12', coreVersion = '2.12.0', corePublished = old } = {}) {
  return async (url) => {
    if (url.includes('/dependencies')) {
      return Response.json({
        dependencies: url.includes('plugin-opener')
          ? [
              {
                crate_id: 'tauri',
                req: required,
                kind: 'normal',
                optional: false,
              },
            ]
          : [],
      })
    }
    const versions = url.includes('plugin-opener')
      ? ['2.6.0']
      : [coreVersion]
    const date = url.includes('plugin-opener')
      ? old
      : corePublished
    return isCrateIndexRead(url)
      ? crateIndexResponse(versions.map(vers => ({ vers, pubtime: date })))
      : Response.json({
          time: Object.fromEntries(versions.map(version => [version, date])),
        })
  }
}

describe('planTauriTargets', () => {
  it('finishes an incompatible graph containing both prepared and native updates', async () => {
    let reads = 0
    const preparedPlugin = new Proxy(plugin, {
      get(target, property) {
      // Fail deterministically if an exhausted singleton domain repeats.
        if (property === 'cargo' && ++reads > 100)
          throw new Error('The prepared domain repeated without progress.')
        return Reflect.get(target, property)
      },
    })
    const result = await planTauriTargets([core, preparedPlugin], {
      policy,
      security: true,
      proposed: {
        npm: { [core.npm]: '2.12.0', [plugin.npm]: '2.6.0' },
        cargo: { [core.cargo]: '2.12.0', [plugin.cargo]: '2.6.0' },
      },
      preparedTargets: { [plugin.npm]: '2.6.0', [plugin.cargo]: '2.6.0' },
      upgrades: [
        { depName: core.cargo, newVersion: '2.12.0', isVulnerabilityAlert: true },
      ],
      request: registry({ required: '3', coreVersion: '2.12.0' }),
      now,
    })
    expect(result).toBeNull()
  })
  it('does not grant unrelated security branches permission for a Tauri major', async () => {
    expect(await planTauriTargets([core, plugin], {
      policy,
      proposed,
      security: true,
      upgrades: [
        { depName: plugin.cargo, newVersion: '2.6.0', isVulnerabilityAlert: true },
      ],
      request: registry({ required: '3', coreVersion: '3.0.0' }),
      now,
    })).toBeNull()
  })
  it('processes selected upgrades even when native artifact generation left the lockfiles unchanged', async () => {
    const result = await planTauriTargets([plugin], {
      policy,
      upgrades: [
        { depName: plugin.cargo, newVersion: '2.6.0' },
      ],
      request: registry({ required: '2' }),
      now,
    })
    expect(result).toEqual([
      {
        pair: plugin,
        target: { npm: '2.6.0', cargo: '2.6.0' },
      },
    ])
  })
  it('fixes one side of a pair without requiring an unavailable fix for its unchanged partner', async () => {
    const alerts = [
      {
        state: 'open',
        dependency: { manifest_path: 'frontend/package.json' },
        security_vulnerability: {
          package: { name: plugin.npm, ecosystem: 'npm' },
          vulnerable_version_range: '< 2.5.1',
          first_patched_version: { identifier: '2.5.1' },
        },
      },
      {
        state: 'open',
        dependency: { manifest_path: 'desktop/rust/Cargo.lock' },
        security_vulnerability: {
          package: { name: plugin.cargo, ecosystem: 'rust' },
          vulnerable_version_range: '< 2.6.0',
          first_patched_version: null,
        },
      },
    ]
    const result = await planTauriTargets([plugin], {
      policy,
      alerts,
      security: true,
      now,
      request: async url => isCrateIndexRead(url)
        ? crateIndexResponse([
            { vers: '2.5.0', pubtime: old },
          ])
        : Response.json(url.includes('/dependencies')
            ? { dependencies: [] }
            : {
                time: { '2.5.1': old },
              }),
    })
    expect(result).toEqual([
      {
        pair: plugin,
        target: { npm: '2.5.1', cargo: '2.5.0' },
      },
    ])
  })
  it('keeps an unchanged trusted partner without treating its release as a new candidate', async () => {
    const vulnerability = {
      package: { name: plugin.npm, ecosystem: 'npm' },
      vulnerable_version_range: '< 2.5.1',
      first_patched_version: { identifier: '2.5.1' },
    }
    for (const release of [
      { vers: '2.5.0', pubtime: '2026-10-25T00:00:00Z' },
      { vers: '2.5.0' },
      { vers: '2.5.0', pubtime: old, yanked: true },
    ]) {
      const result = await planTauriTargets([plugin], {
        policy,
        security: true,
        alerts: [
          {
            state: 'open',
            dependency: { manifest_path: 'frontend/package.json' },
            security_vulnerability: vulnerability,
          },
        ],
        now,
        request: async url => isCrateIndexRead(url)
          ? crateIndexResponse([release])
          : Response.json(url.includes('/dependencies')
              ? { dependencies: [] }
              : {
                  time: { '2.5.1': old },
                }),
      })
      expect(result).toEqual([
        {
          pair: plugin,
          target: { npm: '2.5.1', cargo: '2.5.0' },
        },
      ])
    }
  })
  it('keeps an unapproved core major and an under-age core out of a plugin graph', async () => {
    for (const request of [
      registry({ required: '3', coreVersion: '3.0.0' }),
      registry({ corePublished: '2026-10-25T00:00:00Z' }),
    ]) {
      expect(await planTauriTargets([core, plugin], {
        policy,
        proposed,
        upgrades: [
          { depName: plugin.cargo, newVersion: '2.6.0' },
        ],
        request,
        now,
      })).toBeNull()
    }
  })
  it('includes a higher compatible core in the lowest security graph', async () => {
    const vulnerability = {
      package: { name: plugin.cargo, ecosystem: 'rust' },
      vulnerable_version_range: '< 2.6.0',
      first_patched_version: { identifier: '2.6.0' },
    }
    const result = await planTauriTargets([core, plugin], {
      policy,
      security: true,
      alerts: [
        {
          state: 'open',
          dependency: { manifest_path: 'desktop/rust/Cargo.lock' },
          security_vulnerability: vulnerability,
        },
      ],
      request: registry(),
      now,
    })
    expect(Object.fromEntries(result.map(({ pair, target }) => [pair.cargo, target]))).toEqual({
      [plugin.cargo]: { npm: '2.6.0', cargo: '2.6.0' },
      [core.cargo]: { npm: '2.12.0', cargo: '2.12.0' },
    })
  })
  it('permits a core major only when a genuine security fix requires that companion', async () => {
    const vulnerability = {
      package: { name: plugin.cargo, ecosystem: 'rust' },
      vulnerable_version_range: '< 2.6.0',
      first_patched_version: { identifier: '2.6.0' },
    }
    const result = await planTauriTargets([core, plugin], {
      policy,
      security: true,
      alerts: [
        {
          state: 'open',
          dependency: { manifest_path: 'desktop/rust/Cargo.lock' },
          security_vulnerability: vulnerability,
        },
      ],
      request: registry({ required: '3', coreVersion: '3.0.0' }),
      now,
    })
    expect(result.find(entry => entry.pair.cargo === 'tauri').target).toEqual({ npm: '3.0.0', cargo: '3.0.0' })
    expect(await planTauriTargets([core], {
      policy,
      security: true,
      upgrades: [
        { depName: core.cargo, newVersion: '3.0.0', isVulnerabilityAlert: true },
      ],
      request: registry({ coreVersion: '3.0.0' }),
      now,
    })).toBeNull()
  })
  it('requires a native security major to remove a vulnerable locked occurrence', async () => {
    const vulnerability = {
      package: { name: core.cargo, ecosystem: 'rust' },
      vulnerable_version_range: '>= 3.0.0, < 3.0.1',
      first_patched_version: { identifier: '3.0.1' },
    }
    const alerts = [
      {
        state: 'open',
        dependency: { manifest_path: 'desktop/rust/Cargo.lock' },
        security_vulnerability: vulnerability,
      },
    ]
    const baseline = {
      npm: { [core.npm]: '2.11.0' },
      cargo: { tauri: '2.11.0' },
      occurrences: {
        cargo: {
          tauri: ['2.11.0', '3.0.0'],
        },
      },
    }
    const proposed = {
      npm: { [core.npm]: '2.11.0' },
      cargo: { tauri: '3.0.1' },
      occurrences: {
        cargo: {
          tauri: ['3.0.1'],
        },
      },
    }
    const options = {
      policy,
      baseline,
      proposed,
      security: true,
      alerts,
      upgrades: [
        { depName: core.cargo, newVersion: '3.0.1', isVulnerabilityAlert: true },
      ],
      request: registry({ coreVersion: '3.0.1' }),
      now,
    }
    expect((await planTauriTargets([core], options))[0].target).toEqual({ npm: '3.0.1', cargo: '3.0.1' })
    expect(await planTauriTargets([core], {
      ...options,
      proposed: {
        ...proposed,
        occurrences: {
          cargo: {
            tauri: ['3.0.0', '3.0.1'],
          },
        },
      },
    })).toBeNull()
  })
  it('does not undo an indirect native update when its compatible JavaScript companion is unavailable', async () => {
    const result = await planTauriTargets([plugin], {
      policy,
      proposed: {
        npm: { [plugin.npm]: '2.5.0' },
        cargo: { [plugin.cargo]: '2.6.0' },
      },
      security: true,
      request: async url => isCrateIndexRead(url)
        ? crateIndexResponse([
            { vers: '2.5.0', pubtime: old },
            { vers: '2.6.0', pubtime: old },
          ])
        : Response.json(url.includes('/dependencies')
            ? { dependencies: [] }
            : {
                time: { '2.5.0': old },
              }),
      now,
    })
    expect(result).toBeNull()
  })
  it('preserves snapshot targets without promoting another pending advisory', async () => {
    const vulnerability = {
      package: { name: core.cargo, ecosystem: 'rust' },
      vulnerable_version_range: '< 2.12.0',
      first_patched_version: { identifier: '2.12.0' },
    }
    const result = await planTauriTargets([core, plugin], {
      policy,
      proposed,
      security: true,
      alerts: [
        {
          state: 'open',
          dependency: { manifest_path: 'desktop/rust/Cargo.lock' },
          security_vulnerability: vulnerability,
        },
      ],
      preparedTargets: { [plugin.npm]: '2.6.0', [plugin.cargo]: '2.6.0' },
      request: () => {
        throw new Error('Unexpected snapshot registry read.')
      },
      now,
    })
    expect(result.find(entry => entry.pair.cargo === core.cargo).target).toEqual({ npm: '2.11.0', cargo: '2.11.0' })
  })
  it('rejects incomplete, downgraded, incompatible, or superseded prepared targets', async () => {
    for (const preparedTargets of [
      { [plugin.npm]: '2.6.0' },
      { [plugin.npm]: '2.4.0', [plugin.cargo]: '2.4.0' },
      { [plugin.npm]: '2.6.0', [plugin.cargo]: '2.7.0' },
    ]) {
      await expect(planTauriTargets([plugin], {
        policy,
        security: true,
        preparedTargets,
        now,
      })).rejects.toThrow('prepared Tauri pair')
    }
    await expect(planTauriTargets([plugin], {
      policy,
      security: true,
      preparedTargets: { [plugin.npm]: '2.6.0', [plugin.cargo]: '2.6.0' },
      upgrades: [
        { depName: plugin.cargo, newVersion: '2.7.0' },
      ],
      now,
    })).rejects.toThrow('below the selected')
  })
  it('returns unchanged graphs without registry reads and reports invalid dependency metadata', async () => {
    const request = () => {
      throw new Error('Unexpected registry request.')
    }
    expect(await planTauriTargets([], { policy, request })).toEqual([])
    expect(await planTauriTargets([plugin], { policy, request })).toEqual([])
    await expect(planTauriTargets([plugin], {
      policy,
      proposed,
      request: async url => url.includes('/dependencies')
        ? Response.json({})
        : registry()(url),
      now,
    })).rejects.toThrow('dependency metadata')
  })
})

// Old enough for a security fix (two hours), and too young for a routine update (three days).
const young = '2026-10-25T21:00:00Z'
// Too young for a security fix as well.
const tooYoung = '2026-10-25T23:00:00Z'

// Serve npm publication times, the crate index, and crate dependencies from one table.
function catalog({ releases, dependencies = {} }) {
  return async (url) => {
    const { host, pathname } = new URL(url)
    if (pathname.endsWith('/dependencies')) {
      const [name, version] = pathname.split('/').slice(4, 6).map(decodeURIComponent)
      return Response.json({
        dependencies: (dependencies[`${name}@${version}`] ?? []).map(([crate_id, req]) => ({
          crate_id,
          req,
          kind: 'normal',
          optional: false,
        })),
      })
    }
    if (host === 'registry.npmjs.org') {
      return Response.json({
        time: Object.fromEntries(releases[decodeURIComponent(pathname.slice(1))] ?? []),
      })
    }
    return crateIndexResponse((releases[pathname.split('/').at(-1)] ?? []).map(([vers, pubtime]) => ({ vers, pubtime })))
  }
}

function rustAlert(name, range, fixed) {
  return {
    state: 'open',
    dependency: { manifest_path: 'desktop/rust/Cargo.lock' },
    security_vulnerability: {
      package: { name, ecosystem: 'rust' },
      vulnerable_version_range: range,
      first_patched_version: { identifier: fixed },
    },
  }
}

const targetsOf = result => result && Object.fromEntries(result.map(({ pair, target }) => [pair.cargo, target]))

describe('planTauriTargets with authorized security exemptions', () => {
  const pluginFix = ({ npmCompanion = old, closure = ['tauri@2.12.0'], fixPublished = young, corePublished = young } = {}) => ({
    policy,
    security: true,
    alerts: [
      rustAlert(plugin.cargo, '< 2.6.0', '2.6.0'),
    ],
    authorizedRoots: new Map([
      [
        'tauri-plugin-opener@2.6.0',
        new Set(closure),
      ],
    ]),
    request: catalog({
      releases: {
        [plugin.cargo]: [
          ['2.5.0', old],
          ['2.6.0', fixPublished],
        ],
        [plugin.npm]: [
          ['2.5.0', old],
          ['2.6.0', npmCompanion],
        ],
        [core.cargo]: [
          ['2.11.0', old],
          ['2.12.0', corePublished],
        ],
        [core.npm]: [
          ['2.11.0', old],
          ['2.12.0', old],
        ],
      },
      dependencies: {
        'tauri-plugin-opener@2.6.0': [
          ['tauri', '=2.12.0'],
        ],
      },
    }),
    now,
  })
  it('selects a three-hour-old plugin fix and the young core crate that it pins exactly when the npm companion is old enough', async () => {
    expect(targetsOf(await planTauriTargets([core, plugin], pluginFix()))).toEqual({
      [plugin.cargo]: { npm: '2.6.0', cargo: '2.6.0' },
      [core.cargo]: { npm: '2.12.0', cargo: '2.12.0' },
    })
  })
  it('holds back an authorized fix or a pinned crate younger than the security age, as Renovate does', async () => {
    // A fix that waits keeps the vulnerable pair.
    expect(await planTauriTargets([core, plugin], pluginFix({ fixPublished: tooYoung }))).toEqual([])
    // A fix whose pinned crate waits has no compatible graph, as for a crate that no root pins.
    expect(await planTauriTargets([core, plugin], pluginFix({ corePublished: tooYoung }))).toBeNull()
  })
  it('admits an authorized fix without a publication time, as the timestamp-optional security policy does', async () => {
    expect(targetsOf(await planTauriTargets([core, plugin], pluginFix({ fixPublished: undefined })))).toEqual({
      [plugin.cargo]: { npm: '2.6.0', cargo: '2.6.0' },
      [core.cargo]: { npm: '2.12.0', cargo: '2.12.0' },
    })
  })
  it('keeps the vulnerable pair when the npm companion of the fix is young and not pinned exactly', async () => {
    expect(await planTauriTargets([core, plugin], pluginFix({ npmCompanion: young }))).toEqual([])
  })
  it('rejects a young core crate that no selected root pins', async () => {
    expect(await planTauriTargets([core, plugin], pluginFix({ closure: [] }))).toBeNull()
  })
  it('rejects a young target above the patched version', async () => {
    // 2.6.1 fits the old core baseline, so its own age is the only reason to reject it.
    const options = {
      ...pluginFix(),
      upgrades: [
        { depName: plugin.cargo, newVersion: '2.6.1', isVulnerabilityAlert: true },
      ],
      request: catalog({
        releases: {
          [plugin.cargo]: [
            ['2.6.0', young],
            ['2.6.1', young],
          ],
          [plugin.npm]: [
            ['2.6.0', old],
            ['2.6.1', old],
          ],
          [core.cargo]: [
            ['2.11.0', old],
          ],
          [core.npm]: [
            ['2.11.0', old],
          ],
        },
        dependencies: {
          'tauri-plugin-opener@2.6.1': [
            ['tauri', '2.11'],
          ],
        },
      }),
    }
    expect(await planTauriTargets([core, plugin], options)).toBeNull()
    expect(targetsOf(await planTauriTargets([core, plugin], {
      ...options,
      authorizedRoots: new Map([
        [
          'tauri-plugin-opener@2.6.1',
          new Set(),
        ],
      ]),
    }))).toEqual({
      // A security plan takes the lowest npm companion of the minor.
      [plugin.cargo]: { npm: '2.6.0', cargo: '2.6.1' },
      [core.cargo]: { npm: '2.11.0', cargo: '2.11.0' },
    })
  })
  it('keeps every changed young version out with an empty exemption map', async () => {
    const options = {
      ...pluginFix(),
      authorizedRoots: new Map(),
    }
    expect(await planTauriTargets([core, plugin], options)).toEqual([])
    expect(await planTauriTargets([core, plugin], {
      ...options,
      security: false,
      alerts: [],
      upgrades: [
        { depName: plugin.cargo, newVersion: '2.6.0' },
      ],
    })).toBeNull()
  })
  it('keeps a young baseline pair unchanged with an empty exemption map after its alert closes', async () => {
    const result = await planTauriTargets([core, plugin], {
      policy,
      upgrades: [
        { depName: plugin.cargo, newVersion: '2.6.0' },
      ],
      authorizedRoots: new Map(),
      request: catalog({
        releases: {
          [plugin.cargo]: [
            ['2.6.0', old],
          ],
          [plugin.npm]: [
            ['2.6.0', old],
          ],
          [core.cargo]: [
            ['2.11.0', young],
          ],
          [core.npm]: [
            ['2.11.0', young],
          ],
        },
        dependencies: {
          'tauri-plugin-opener@2.6.0': [
            ['tauri', '2.11'],
          ],
        },
      }),
      now,
    })
    expect(targetsOf(result)).toEqual({
      [plugin.cargo]: { npm: '2.6.0', cargo: '2.6.0' },
      [core.cargo]: { npm: '2.11.0', cargo: '2.11.0' },
    })
  })
  it('keeps a young baseline crate eligible with an empty exemption map when a routine update replans its pair', async () => {
    const result = await planTauriTargets([core], {
      policy,
      upgrades: [
        { depName: core.npm, newVersion: '2.11.1' },
      ],
      authorizedRoots: new Map(),
      request: catalog({
        releases: {
          [core.cargo]: [
            ['2.11.0', young],
          ],
          [core.npm]: [
            ['2.11.0', old],
            ['2.11.1', old],
          ],
        },
      }),
      now,
    })
    expect(targetsOf(result)).toEqual({
      [core.cargo]: { npm: '2.11.1', cargo: '2.11.0' },
    })
  })
  it('backtracks past a conditional companion whose root the plan cannot select', async () => {
    const result = await planTauriTargets([
      { ...core, npmVersion: '2.10.0', cargoVersion: '2.10.0' },
    ], {
      policy,
      security: true,
      alerts: [
        rustAlert(core.cargo, '< 2.11.0', '2.11.0'),
      ],
      authorizedRoots: new Map([
        [
          'tauri-plugin-b@1.0.1',
          new Set(['@tauri-apps/api@2.11.0']),
        ],
      ]),
      request: catalog({
        releases: {
          [core.cargo]: [
            ['2.10.0', old],
            ['2.11.0', old],
          ],
          [core.npm]: [
            ['2.10.0', old],
            ['2.11.0', young],
            ['2.11.1', old],
          ],
        },
      }),
      now,
    })
    expect(result[0].target).toEqual({ npm: '2.11.1', cargo: '2.11.0' })
  })
  describe('a root whose domain follows the release that it authorizes', () => {
    const pluginA = {
      npm: '@tauri-apps/plugin-a',
      cargo: 'tauri-plugin-a',
      npmVersion: '2.0.0',
      cargoVersion: '2.0.0',
    }
    const pluginB = {
      npm: '@tauri-apps/plugin-b',
      cargo: 'tauri-plugin-b',
      npmVersion: '1.0.0',
      cargoVersion: '1.0.0',
    }
    const authorizedRoots = new Map([
      [
        'tauri-plugin-b@1.0.1',
        new Set(['tauri-plugin-a@2.0.1']),
      ],
    ])
    const releases = {
      [pluginA.cargo]: [
        ['2.0.0', old],
        ['2.0.1', young],
      ],
      [pluginA.npm]: [
        ['2.0.0', old],
      ],
      [pluginB.cargo]: [
        ['1.0.0', old],
        ['1.0.1', young],
      ],
      [pluginB.npm]: [
        ['1.0.0', old],
      ],
    }
    const options = {
      policy,
      security: true,
      alerts: [
        rustAlert(pluginB.cargo, '< 1.0.1', '1.0.1'),
      ],
      authorizedRoots,
      request: catalog({
        releases,
        dependencies: {
          'tauri-plugin-b@1.0.1': [
            ['tauri-plugin-a', '=2.0.1'],
          ],
        },
      }),
      now,
    }
    const expected = {
      [pluginA.cargo]: { npm: '2.0.0', cargo: '2.0.1' },
      [pluginB.cargo]: { npm: '1.0.0', cargo: '1.0.1' },
    }
    it('finds the plan when the authorized release is assigned first', async () => {
      expect(targetsOf(await planTauriTargets([pluginA, pluginB], options))).toEqual(expected)
    })
    it('finds the same plan in the reverse order', async () => {
      expect(targetsOf(await planTauriTargets([pluginB, pluginA], options))).toEqual(expected)
    })
    it('rejects the young release when no candidate holds its root', async () => {
      expect(await planTauriTargets([pluginA, pluginB], {
        ...options,
        alerts: [],
        upgrades: [
          { depName: pluginA.cargo, newVersion: '2.0.1' },
        ],
        request: catalog({
          releases: {
            ...releases,
            [pluginB.cargo]: [
              ['1.0.0', old],
            ],
          },
        }),
      })).toBeNull()
    })
  })
  it('rejects authorized roots that are not a map of sets', async () => {
    for (const authorizedRoots of [
      {},
      null,
      new Map([
        [
          'a@1.0.0',
          ['b@1.0.0'],
        ],
      ]),
    ])
      await expect(planTauriTargets([plugin], { policy, authorizedRoots })).rejects.toThrow('authorized Tauri roots')
  })
})

describe('tauriCrateRequirement', () => {
  // Answer the crates.io dependency list of tauri@2.12.0, and record each URL.
  const registry = (dependencies) => {
    const urls = []
    return {
      urls,
      request: async (url) => {
        urls.push(url)
        return Response.json({ dependencies })
      },
    }
  }
  const gtk = (req, extra = {}) => ({
    crate_id: 'gtk',
    req,
    kind: 'normal',
    optional: false,
    target: 'cfg(target_os = "linux")',
    ...extra,
  })
  it('returns the requirement of the planned tauri release on the crate', async () => {
    const { urls, request } = registry([
      {
        crate_id: 'serde',
        req: '^1',
        kind: 'normal',
        optional: false,
      },
      gtk('^0.19'),
    ])
    expect(await tauriCrateRequirement('gtk', '2.12.0', request)).toBe('^0.19')
    expect(urls).toEqual(['https://crates.io/api/v1/crates/tauri/2.12.0/dependencies'])
  })
  it('returns one requirement that two target tables repeat', async () => {
    const { request } = registry([
      gtk('^0.19'),
      gtk('^0.19', { target: 'cfg(target_os = "freebsd")' }),
    ])
    expect(await tauriCrateRequirement('gtk', '2.12.0', request)).toBe('^0.19')
  })
  it('returns null when the release has no required dependency on the crate', async () => {
    for (const dependencies of [
      [],
      [
        gtk('^0.19', { optional: true }),
      ],
      [
        gtk('^0.19', { kind: 'dev' }),
      ],
      [
        gtk('^0.19', { kind: 'build' }),
      ],
    ]) {
      const { request } = registry(dependencies)
      expect(await tauriCrateRequirement('gtk', '2.12.0', request), JSON.stringify(dependencies)).toBeNull()
    }
  })
  it('fails when the release gives two different requirements on the crate', async () => {
    const { request } = registry([
      gtk('^0.18'),
      gtk('^0.19', { target: 'cfg(target_os = "freebsd")' }),
    ])
    await expect(tauriCrateRequirement('gtk', '2.12.0', request)).rejects.toThrow('tauri 2.12.0 gives more than one requirement for gtk: ^0.18, ^0.19')
  })
  it('fails for a dependency entry without a requirement', async () => {
    const { request } = registry([
      { crate_id: 'gtk', kind: 'normal', optional: false },
    ])
    await expect(tauriCrateRequirement('gtk', '2.12.0', request)).rejects.toThrow('Invalid Tauri dependency requirement: tauri@2.12.0')
  })
})
