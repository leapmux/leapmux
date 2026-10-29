import { describe, expect, it } from 'bun:test'
import { planTauriTargets } from './renovate-tauri-graph.mjs'

const now = Date.parse('2026-10-26T00:00:00Z')
const old = '2026-10-20T00:00:00Z'
const policy = {
  minimumReleaseAge: '3 days',
  minimumReleaseAgeBuffer: null,
  vulnerabilityAlerts: { minimumReleaseAge: '3 days' },
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
    return Response.json(url.includes('registry.npmjs.org')
      ? {
          time: Object.fromEntries(versions.map(version => [version, date])),
        }
      : {
          versions: versions.map(num => ({ num, created_at: date })),
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
      request: async url => Response.json(url.includes('/dependencies')
        ? { dependencies: [] }
        : url.includes('registry.npmjs.org')
          ? {
              time: { '2.5.1': old },
            }
          : {
              versions: [
                { num: '2.5.0', created_at: old },
              ],
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
      { num: '2.5.0', created_at: '2026-10-25T00:00:00Z' },
      { num: '2.5.0' },
      { num: '2.5.0', created_at: old, yanked: true },
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
        request: async url => Response.json(url.includes('/dependencies')
          ? { dependencies: [] }
          : url.includes('registry.npmjs.org')
            ? {
                time: { '2.5.1': old },
              }
            : {
                versions: [release],
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
      request: async url => Response.json(url.includes('/dependencies')
        ? { dependencies: [] }
        : url.includes('registry.npmjs.org')
          ? {
              time: { '2.5.0': old },
            }
          : {
              versions: [
                { num: '2.5.0', created_at: old },
                { num: '2.6.0', created_at: old },
              ],
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
