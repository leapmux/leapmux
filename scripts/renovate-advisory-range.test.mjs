import { spawnSync } from 'node:child_process'
import { describe, expect, it } from 'bun:test'
import { advisoryRange, isInAdvisoryRange, normalizeAdvisoryVersion, patchedIdentifier } from './renovate-advisory-range.mjs'

describe('normalizeAdvisoryVersion', () => {
  it('removes the Go prefix and the incompatible suffix, and keeps every other spelling', () => {
    expect(normalizeAdvisoryVersion('v1.2.3')).toBe('1.2.3')
    expect(normalizeAdvisoryVersion('v2.0.0+incompatible')).toBe('2.0.0')
    expect(normalizeAdvisoryVersion('1.0.0-beta.1')).toBe('1.0.0-beta.1')
    expect(normalizeAdvisoryVersion(42)).toBe('42')
  })
})

describe('isInAdvisoryRange', () => {
  it('matches comma-separated ranges and prerelease Go pseudo-versions', () => {
    expect(isInAdvisoryRange('0.18.1', '>= 0.15.0, < 0.20.0')).toBe(true)
    expect(isInAdvisoryRange('0.20.0', '>= 0.15.0, < 0.20.0')).toBe(false)
    expect(isInAdvisoryRange('v1.0.1-0.20260615104152-545b12e0e8c7', '< 1.0.1')).toBe(true)
    expect(isInAdvisoryRange('v2.0.0+incompatible', '< 2.0.1')).toBe(true)
  })
  it('rejects an invalid, missing, or blank range and never matches an invalid version', () => {
    for (const range of [
      'not a range',
      undefined,
      null,
      '',
      '  ',
    ])
      expect(() => isInAdvisoryRange('1.0.0', range), String(range)).toThrow('Invalid advisory range')
    expect(isInAdvisoryRange('main', '< 1.0.0')).toBe(false)
  })
})

describe('advisoryRange', () => {
  it('joins the comparators of a GitHub range, and returns null for a range that states nothing', () => {
    expect(advisoryRange('>= 0.15.0, < 0.20.0')).toBe('>= 0.15.0  < 0.20.0')
    expect(advisoryRange('= 1.2.3')).toBe('= 1.2.3')
    // semver reads an empty range as every version.
    for (const range of [
      '',
      '   ',
      null,
      undefined,
      42,
      'not a range',
    ])
      expect(advisoryRange(range), String(range)).toBeNull()
  })
})

describe('patchedIdentifier', () => {
  it('reads the object and the string form of the first patched version', () => {
    expect(patchedIdentifier({
      first_patched_version: { identifier: '1.2.3' },
    })).toBe('1.2.3')
    expect(patchedIdentifier({ first_patched_version: '1.2.3' })).toBe('1.2.3')
    for (const record of [
      { first_patched_version: null },
      { first_patched_version: {} },
      {},
      null,
    ])
      expect(patchedIdentifier(record), JSON.stringify(record)).toBeNull()
  })
})

describe('the advisory range under Node', () => {
  it('matches a Go pseudo-version as under Bun', () => {
    const source = `const m=await import(${JSON.stringify(new URL('./renovate-advisory-range.mjs', import.meta.url).href)}); console.log(JSON.stringify(m.isInAdvisoryRange('v1.0.1-0.20260615104152-545b12e0e8c7','< 1.0.1')))`
    const result = spawnSync('node', ['--input-type=module', '-e', source], { encoding: 'utf8' })
    expect(result.status, result.stderr).toBe(0)
    expect(JSON.parse(result.stdout)).toBe(true)
  })
})
