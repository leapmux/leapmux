import { describe, expect, it } from 'bun:test'
import { matchesCargoRequirement, tauriPairCandidates, tauriReleaseAgeMs } from './renovate-tauri-releases.mjs'

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

describe('tauriReleaseAgeMs', () => {
  it('derives routine and security ages from the trusted configuration', () => {
    const config = { minimumReleaseAge: '4 days', minimumReleaseAgeBuffer: null, vulnerabilityAlerts: { minimumReleaseAge: '3 days' } }
    expect(tauriReleaseAgeMs(config)).toBe(4 * 86400000)
    expect(tauriReleaseAgeMs(config, true)).toBe(3 * 86400000)
    for (const minimumReleaseAge of ['0 days', '-1 days', '', '999999999999999 days'])
      expect(() => tauriReleaseAgeMs({ ...config, minimumReleaseAge })).toThrow()
    expect(() => tauriReleaseAgeMs({ ...config, minimumReleaseAgeBuffer: '30 minutes' })).toThrow()
  })
})

describe('tauriPairCandidates', () => {
  it('keeps routine companions within the selected major while permitting approved or security majors', () => {
    const npm = [release('2.11.2'), release('3.0.0')]
    const cargo = [release('2.11.4'), release('3.0.1')]
    const options = { minimumAgeMs: 3 * 86400000, npmMinimum: '2.10.0', cargoMinimum: '2.10.0', now }
    expect(chooseTauriPair(npm, cargo, options)).toEqual({ npm: '2.11.2', cargo: '2.11.4' })
    expect(chooseTauriPair(npm, cargo, { ...options, maximumMajor: 3 })).toEqual({ npm: '3.0.0', cargo: '3.0.1' })
    expect(chooseTauriPair(npm, cargo, { ...options, npmMinimum: '3.0.0', security: true })).toEqual({ npm: '3.0.0', cargo: '3.0.1' })
  })
  for (const kind of ['routine', 'security', 'maintenance']) {
    it(`keeps ${kind} updates compatible with independent patch versions`, () => {
      const result = chooseTauriPair([release('2.10.1'), release('2.11.2'), release('2.12.0')], [release('2.10.9'), release('2.11.4')], {
        minimumAgeMs: 3 * 86400000,
        npmMinimum: '2.10.0',
        cargoMinimum: '2.10.0',
        security: kind === 'security',
        now,
      })
      expect(result).toEqual(kind === 'security' ? { npm: '2.10.1', cargo: '2.10.9' } : { npm: '2.11.2', cargo: '2.11.4' })
    })
  }
  it('requires fixed versions and excludes pending, missing-timestamp, prerelease, and yanked candidates', () => {
    const npm = [release('2.10.1'), release('2.11.2'), release('2.12.0', '2026-10-25T00:00:00Z'), release('3.0.0-beta.1')]
    const cargo = [release('2.10.9'), release('2.11.4'), release('2.12.0'), { ...release('3.0.0'), yanked: true }]
    expect(chooseTauriPair(npm, cargo, { minimumAgeMs: 3 * 86400000, npmMinimum: '2.11.0', cargoMinimum: '2.10.0', security: true, now })).toEqual({ npm: '2.11.2', cargo: '2.11.4' })
    for (const candidates of [[], [release('2.11.2', null)], [release('2.11.2', '2026-10-25T00:00:00Z')]])
      expect(() => chooseTauriPair(candidates, cargo, { minimumAgeMs: 3 * 86400000, npmMinimum: '2.11.0', cargoMinimum: '2.10.0', now })).toThrow()
    expect(() => chooseTauriPair(npm, cargo, { minimumAgeMs: 3 * 86400000, npmMinimum: '4.0.0', cargoMinimum: '2.10.0', now })).toThrow()
  })
})
