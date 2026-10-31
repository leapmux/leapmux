import { describe, expect, it } from 'bun:test'
import { eligibleTauriReleases, matchesCargoRequirement, readTauriReleases, tauriPairCandidates } from './renovate-tauri-releases.mjs'

const now = Date.parse('2026-10-26T00:00:00Z')
const release = (version, publishedAt = '2026-10-20T00:00:00Z') => ({ version, publishedAt })
function chooseTauriPair(npm, cargo, options) {
  const target = tauriPairCandidates(npm, cargo, options)[0]
  if (!target)
    throw new Error('No compatible Tauri pair.')
  return target
}

describe('matchesCargoRequirement', () => {
  it('uses Cargo requirements for partial, zero-major, wildcard, comparator, and prerelease versions', () => {
    for (const [requirement, accepted, rejected] of [
      ['2.11', '2.12.0', '3.0.0'],
      ['0.2', '0.2.7', '0.3.0'],
      ['0.0.3', '0.0.3', '0.0.4'],
      ['0.0', '0.0.9', '0.1.0'],
      ['0', '0.9.0', '1.0.0'],
      ['2.*', '2.12.0', '3.0.0'],
      ['2.11.*', '2.11.3', '2.12.0'],
      ['>= 2.11, < 3', '2.12.0', '3.0.0'],
      ['=2.11.0', '2.11.0', '2.11.3'],
      ['~2.11.0', '2.11.3', '2.12.0'],
      ['1.0.0-alpha', '1.0.0-beta', '1.0.1-beta'],
      ['1', '1.2.0', '1.2.0-beta'],
    ]) {
      expect(matchesCargoRequirement(accepted, requirement), requirement).toBe(true)
      expect(matchesCargoRequirement(rejected, requirement), requirement).toBe(false)
    }
    for (const requirement of [null, 'invalid', '>='])
      expect(() => matchesCargoRequirement('2.0.0', requirement)).toThrow('Invalid Cargo')
  })
})

describe('eligibleTauriReleases', () => {
  const young = '2026-10-25T23:00:00Z'
  const options = { name: 'tauri', now, minimumAgeMs: 3 * 86400000 }
  it('admits old-enough releases and authorized roots without another selection', () => {
    const authorized = new Map([
      [
        'tauri@2.12.1',
        new Set(),
      ],
    ])
    expect(eligibleTauriReleases([
      release('2.12.0'),
      release('2.12.1', young),
      release('2.12.2', young),
    ], '2.11.0', { ...options, authorized })).toEqual([
      {
        ...release('2.12.0'),
        authorizedBy: null,
      },
      {
        ...release('2.12.1', young),
        authorizedBy: null,
      },
    ])
  })
  it('lists the authorizing roots of a young closure member and excludes every other young release', () => {
    const authorized = new Map([
      [
        'tauri-plugin-a@2.0.1',
        new Set(['tauri@2.12.1']),
      ],
      [
        'tauri-plugin-b@1.0.1',
        new Set(['tauri@2.12.1', 'tauri@2.12.2']),
      ],
    ])
    const result = eligibleTauriReleases([
      release('2.12.1', young),
      release('2.12.2', young),
      release('2.12.3', young),
      release('2.12.4', null),
    ], '2.11.0', { ...options, authorized })
    expect(result.map(entry => [
      entry.version,
      [...entry.authorizedBy].sort(),
    ])).toEqual([
      [
        '2.12.1',
        ['tauri-plugin-a@2.0.1', 'tauri-plugin-b@1.0.1'],
      ],
      [
        '2.12.2',
        ['tauri-plugin-b@1.0.1'],
      ],
    ])
  })
  it('excludes a young release without an exemption, a yanked release, and a release below the minimum', () => {
    expect(eligibleTauriReleases([
      release('2.12.1', young),
      {
        ...release('2.12.0'),
        yanked: true,
      },
      release('2.10.0'),
    ], '2.11.0', options)).toEqual([])
    expect(() => eligibleTauriReleases([], '2.11.0', { ...options, minimumAgeMs: 0 })).toThrow('positive minimum age')
  })
})

describe('readTauriReleases', () => {
  it('reads npm publication times and crate release metadata with the shared registry reader', async () => {
    const calls = []
    const request = async (url, init) => {
      calls.push([url, init.headers['User-Agent']])
      return Response.json(url.includes('registry.npmjs.org')
        ? {
            time: { '2.12.0': '2026-10-20T00:00:00Z' },
          }
        : {
            versions: [
              { num: '2.12.0', created_at: '2026-10-20T00:00:00Z', yanked: true },
            ],
          })
    }
    expect(await readTauriReleases('@tauri-apps/api', request)).toEqual([
      { version: '2.12.0', publishedAt: '2026-10-20T00:00:00Z' },
    ])
    expect(await readTauriReleases('tauri', request)).toEqual([
      { version: '2.12.0', publishedAt: '2026-10-20T00:00:00Z', yanked: true },
    ])
    expect(calls).toEqual([
      ['https://registry.npmjs.org/%40tauri-apps%2Fapi', 'leapmux-dependency-maintenance'],
      ['https://crates.io/api/v1/crates/tauri', 'leapmux-dependency-maintenance'],
    ])
    await expect(readTauriReleases('tauri', async () => new Response('', { status: 503 }))).rejects.toThrow('Tauri companion metadata read failed: 503')
    await expect(readTauriReleases('tauri', async () => Response.json({}))).rejects.toThrow('Invalid Tauri release metadata')
  })
})

describe('tauriPairCandidates', () => {
  it('keeps routine companions within the selected major while permitting approved or security majors', () => {
    const npm = [
      release('2.11.2'),
      release('3.0.0'),
    ]
    const cargo = [
      release('2.11.4'),
      release('3.0.1'),
    ]
    const options = {
      minimumAgeMs: 3 * 86400000,
      npmMinimum: '2.10.0',
      cargoMinimum: '2.10.0',
      now,
    }
    expect(chooseTauriPair(npm, cargo, options)).toEqual({ npm: '2.11.2', cargo: '2.11.4' })
    expect(chooseTauriPair(npm, cargo, { ...options, maximumMajor: 3 })).toEqual({ npm: '3.0.0', cargo: '3.0.1' })
    expect(chooseTauriPair(npm, cargo, { ...options, npmMinimum: '3.0.0', security: true })).toEqual({ npm: '3.0.0', cargo: '3.0.1' })
  })
  for (const kind of ['routine', 'security', 'maintenance']) {
    it(`keeps ${kind} updates compatible with independent patch versions`, () => {
      const result = chooseTauriPair([
        release('2.10.1'),
        release('2.11.2'),
        release('2.12.0'),
      ], [
        release('2.10.9'),
        release('2.11.4'),
      ], {
        minimumAgeMs: 3 * 86400000,
        npmMinimum: '2.10.0',
        cargoMinimum: '2.10.0',
        security: kind === 'security',
        now,
      })
      expect(result).toEqual(kind === 'security'
        ? { npm: '2.10.1', cargo: '2.10.9' }
        : { npm: '2.11.2', cargo: '2.11.4' })
    })
  }
  it('keeps conditional npm releases of a minor until its first unconditional release', () => {
    const young = '2026-10-25T23:00:00Z'
    const authorized = new Map([
      [
        'tauri-plugin-b@1.0.1',
        new Set(['@tauri-apps/api@2.11.0', '@tauri-apps/api@2.11.2']),
      ],
    ])
    const candidates = tauriPairCandidates([
      release('2.11.0', young),
      release('2.11.1'),
      release('2.11.2', young),
    ], [
      release('2.11.0'),
    ], {
      minimumAgeMs: 3 * 86400000,
      npmMinimum: '2.11.0',
      cargoMinimum: '2.11.0',
      npmName: '@tauri-apps/api',
      cargoName: 'tauri',
      preferLowest: true,
      authorized,
      now,
    })
    expect(candidates).toEqual([
      {
        npm: '2.11.0',
        cargo: '2.11.0',
        requires: [
          new Set(['tauri-plugin-b@1.0.1']),
        ],
      },
      { npm: '2.11.1', cargo: '2.11.0' },
    ])
  })
  it('keeps a trusted locked version unconditional even when it is young', () => {
    const young = '2026-10-25T23:00:00Z'
    const authorized = new Map([
      [
        'tauri-plugin-b@1.0.1',
        new Set(['tauri@2.11.0']),
      ],
    ])
    expect(tauriPairCandidates([
      release('2.11.0'),
    ], [
      release('2.11.0', young),
    ], {
      minimumAgeMs: 3 * 86400000,
      npmMinimum: '2.11.0',
      cargoMinimum: '2.11.0',
      cargoName: 'tauri',
      existing: { cargo: '2.11.0' },
      authorized,
      now,
    })).toEqual([
      { npm: '2.11.0', cargo: '2.11.0' },
    ])
  })
  it('requires fixed versions and excludes pending, missing-timestamp, prerelease, and yanked candidates', () => {
    const npm = [
      release('2.10.1'),
      release('2.11.2'),
      release('2.12.0', '2026-10-25T00:00:00Z'),
      release('3.0.0-beta.1'),
    ]
    const cargo = [
      release('2.10.9'),
      release('2.11.4'),
      release('2.12.0'),
      {
        ...release('3.0.0'),
        yanked: true,
      },
    ]
    expect(chooseTauriPair(npm, cargo, {
      minimumAgeMs: 3 * 86400000,
      npmMinimum: '2.11.0',
      cargoMinimum: '2.10.0',
      security: true,
      now,
    })).toEqual({ npm: '2.11.2', cargo: '2.11.4' })
    for (const candidates of [
      [],
      [
        release('2.11.2', null),
      ],
      [
        release('2.11.2', '2026-10-25T00:00:00Z'),
      ],
    ]) {
      expect(() => chooseTauriPair(candidates, cargo, {
        minimumAgeMs: 3 * 86400000,
        npmMinimum: '2.11.0',
        cargoMinimum: '2.10.0',
        now,
      })).toThrow()
    }
    expect(() => chooseTauriPair(npm, cargo, {
      minimumAgeMs: 3 * 86400000,
      npmMinimum: '4.0.0',
      cargoMinimum: '2.10.0',
      now,
    })).toThrow()
  })
})
