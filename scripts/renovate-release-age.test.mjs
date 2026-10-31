import { spawnSync } from 'node:child_process'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'bun:test'
import { describeAge, isPublishedBy, publishTimeCutoff, releaseAgeMs, securityReleaseAgeMs } from './renovate-release-age.mjs'

const root = resolve(import.meta.dirname, '..')

describe('releaseAgeMs', () => {
  it('computes the age as Renovate does, including a buffer', () => {
    expect(releaseAgeMs({ minimumReleaseAge: '4 days', minimumReleaseAgeBuffer: null })).toBe(345600000)
    expect(releaseAgeMs({ minimumReleaseAge: '4 days', minimumReleaseAgeBuffer: '2 hours' })).toBe(345600000 + 7200000)
  })
  it('adds Renovate\'s default buffer when the policy omits the buffer, as Renovate\'s merge does', () => {
    expect(releaseAgeMs({ minimumReleaseAge: '4 days' })).toBe(345600000 + 1800000)
  })
  it('rejects a missing, zero, negative, unparsable, or unsafe age', () => {
    for (const minimumReleaseAge of [
      '0 days',
      '-1 days',
      '999999999999999 days',
      '',
      'soon',
      null,
      undefined,
    ])
      expect(() => releaseAgeMs({ minimumReleaseAge, minimumReleaseAgeBuffer: null }), String(minimumReleaseAge)).toThrow('positive duration')
  })
  it('reads the repository policy as three days', () => {
    expect(releaseAgeMs(JSON.parse(readFileSync(resolve(root, 'renovate.json'), 'utf8')))).toBe(3 * 86400000)
  })
})

describe('securityReleaseAgeMs', () => {
  it('reads the security age with the buffer of the policy', () => {
    expect(securityReleaseAgeMs({
      vulnerabilityAlerts: { minimumReleaseAge: '2 hours' },
      minimumReleaseAgeBuffer: null,
    })).toBe(7200000)
    expect(securityReleaseAgeMs({
      vulnerabilityAlerts: { minimumReleaseAge: '2 hours' },
      minimumReleaseAgeBuffer: '30 minutes',
    })).toBe(9000000)
    // An omitted buffer takes Renovate's default, as releaseAgeMs does.
    expect(securityReleaseAgeMs({
      vulnerabilityAlerts: { minimumReleaseAge: '2 hours' },
    })).toBe(releaseAgeMs({ minimumReleaseAge: '2 hours' }))
  })
  it('returns 0 when the policy sets no security age, as the default of Renovate does', () => {
    for (const config of [
      {},
      { vulnerabilityAlerts: {} },
      {
        vulnerabilityAlerts: { minimumReleaseAge: null },
      },
    ])
      expect(securityReleaseAgeMs(config), JSON.stringify(config)).toBe(0)
  })
  it('reads the security age of the repository policy', () => {
    expect(securityReleaseAgeMs(JSON.parse(readFileSync(resolve(root, 'renovate.json'), 'utf8')))).toBe(7200000)
  })
})

describe('describeAge', () => {
  it('names each nonzero unit from days down to milliseconds', () => {
    expect(describeAge(3 * 86400000)).toBe('3 days')
    expect(describeAge(86400000)).toBe('1 day')
    expect(describeAge(3 * 86400000 + 1800000)).toBe('3 days 30 minutes')
    expect(describeAge(3600000 + 60000 + 1000 + 1)).toBe('1 hour 1 minute 1 second 1 millisecond')
    expect(describeAge(1)).toBe('1 millisecond')
  })
  it('rejects a duration that is not a positive safe integer', () => {
    for (const ageMs of [0, -1, 1.5, Number.NaN, 2 ** 53])
      expect(() => describeAge(ageMs), String(ageMs)).toThrow('positive duration')
  })
})

describe('publishTimeCutoff', () => {
  it('subtracts the age from the current time', () => {
    expect(publishTimeCutoff(259200000, Date.parse('2026-10-31T22:20:38Z')).toISOString()).toBe('2026-10-28T22:20:38.000Z')
  })
  it('rejects a nonpositive age and an invalid time', () => {
    expect(() => publishTimeCutoff(0, Date.now())).toThrow('positive release age')
    expect(() => publishTimeCutoff(1000, Number.NaN)).toThrow('finite time')
  })
})

describe('isPublishedBy', () => {
  const cutoff = Date.parse('2026-10-28T22:20:38Z')
  it('admits a release published at or before the cutoff, as Renovate admits an age equal to the minimum', () => {
    expect(isPublishedBy(cutoff, cutoff)).toBe(true)
    expect(isPublishedBy(cutoff - 1, cutoff)).toBe(true)
    expect(isPublishedBy(0, cutoff)).toBe(true)
    expect(isPublishedBy(cutoff + 1, cutoff)).toBe(false)
  })
  it('never admits a release without a finite publication time', () => {
    for (const published of [
      Number.NaN,
      Date.parse('not a date'),
      Number.NEGATIVE_INFINITY,
      Number.POSITIVE_INFINITY,
    ])
      expect(isPublishedBy(published, cutoff), String(published)).toBe(false)
    expect(isPublishedBy(cutoff, Number.NaN)).toBe(false)
  })
})

describe('the release-age policy under Node', () => {
  it('computes the same age', () => {
    const source = `const {releaseAgeMs}=await import(${JSON.stringify(new URL('./renovate-release-age.mjs', import.meta.url).href)}); console.log(releaseAgeMs({minimumReleaseAge:'4 days',minimumReleaseAgeBuffer:'2 hours'}))`
    const result = spawnSync('node', ['--input-type=module', '-e', source], { encoding: 'utf8' })
    expect(result.status, result.stderr).toBe(0)
    expect(Number(result.stdout)).toBe(345600000 + 7200000)
  })
})
