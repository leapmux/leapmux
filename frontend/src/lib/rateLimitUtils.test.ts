import { describe, expect, it } from 'vitest'
import { codexTierToRateLimitInfo, iterCodexRateLimitTiers } from './rateLimitUtils'

describe('codextiertoratelimitinfo', () => {
  it('classifies usage thresholds (allowed / warning / exceeded)', () => {
    expect(codexTierToRateLimitInfo({ usedPercent: 10, windowDurationMins: 300 }).status).toBe('allowed')
    expect(codexTierToRateLimitInfo({ usedPercent: 80, windowDurationMins: 300 }).status).toBe('allowed_warning')
    expect(codexTierToRateLimitInfo({ usedPercent: 100, windowDurationMins: 300 }).status).toBe('exceeded')
  })
  it('maps known window durations to canonical type labels', () => {
    expect(codexTierToRateLimitInfo({ usedPercent: 0, windowDurationMins: 300 }).rateLimitType).toBe('five_hour')
    expect(codexTierToRateLimitInfo({ usedPercent: 0, windowDurationMins: 10080 }).rateLimitType).toBe('seven_day')
  })
})

describe('itercodexratelimittiers', () => {
  it('yields each tier present under params.rateLimits in primary→secondary order', () => {
    const payload = {
      params: {
        rateLimits: {
          primary: { usedPercent: 50, windowDurationMins: 300 },
          secondary: { usedPercent: 90, windowDurationMins: 10080 },
        },
      },
    }
    const entries = [...iterCodexRateLimitTiers(payload)]
    expect(entries.map(e => e.key)).toEqual(['primary', 'secondary'])
    expect(entries[0].info.status).toBe('allowed')
    expect(entries[1].info.status).toBe('allowed_warning')
  })
  it('skips missing tiers without raising', () => {
    const payload = { params: { rateLimits: { primary: { usedPercent: 10, windowDurationMins: 300 } } } }
    const entries = [...iterCodexRateLimitTiers(payload)]
    expect(entries).toHaveLength(1)
    expect(entries[0].key).toBe('primary')
  })
  it('yields nothing when payload has no rate-limits object', () => {
    expect([...iterCodexRateLimitTiers({})]).toEqual([])
    expect([...iterCodexRateLimitTiers({ params: {} })]).toEqual([])
    expect([...iterCodexRateLimitTiers(null)]).toEqual([])
  })
})
