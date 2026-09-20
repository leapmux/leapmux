import { describe, expect, it } from 'vitest'
import {
  codexRateLimitReachedType,
  codexTierToRateLimitInfo,
  formatCodexRateLimitReached,
  iterCodexRateLimitTiers,
} from './rateLimits'

describe('codexTierToRateLimitInfo', () => {
  it('classifies usage thresholds (allowed / warning / exceeded)', () => {
    expect(codexTierToRateLimitInfo({ usedPercent: 10, windowDurationMins: 300 }).status).toBe('allowed')
    expect(codexTierToRateLimitInfo({ usedPercent: 80, windowDurationMins: 300 }).status).toBe('allowed_warning')
    expect(codexTierToRateLimitInfo({ usedPercent: 100, windowDurationMins: 300 }).status).toBe('exceeded')
  })
  it('maps known window durations to canonical type labels', () => {
    expect(codexTierToRateLimitInfo({ usedPercent: 0, windowDurationMins: 300 }).rateLimitType).toBe('five_hour')
    expect(codexTierToRateLimitInfo({ usedPercent: 0, windowDurationMins: 10080 }).rateLimitType).toBe('seven_day')
  })
  it('derives an hour/day type for an unknown numeric window', () => {
    expect(codexTierToRateLimitInfo({ usedPercent: 0, windowDurationMins: 120 }).rateLimitType).toBe('2_hour')
    expect(codexTierToRateLimitInfo({ usedPercent: 0, windowDurationMins: 2880 }).rateLimitType).toBe('2_day')
  })
  it('coerces a non-numeric usedPercent to 0 (allowed), not NaN', () => {
    // A malformed/replayed payload could carry a string/boolean/object. `as number ?? 0`
    // only caught null/undefined; a string would coerce to NaN utilization and an
    // 'allowed' status that disagrees with the backend's typed classification.
    for (const bad of ['95', true, {}, null, undefined] as unknown[]) {
      const info = codexTierToRateLimitInfo({ usedPercent: bad, windowDurationMins: 300 })
      expect(info.status).toBe('allowed')
      expect(info.utilization).toBe(0)
    }
  })
  it('falls back to an empty type (not "NaN_hour") when windowDurationMins is absent or non-numeric', () => {
    expect(codexTierToRateLimitInfo({ usedPercent: 20 }).rateLimitType).toBe('')
    expect(codexTierToRateLimitInfo({ usedPercent: 20, windowDurationMins: 'x' as unknown as number }).rateLimitType).toBe('')
  })
  it('coerces a non-numeric resetsAt to undefined', () => {
    expect(codexTierToRateLimitInfo({ usedPercent: 10, windowDurationMins: 300, resetsAt: 'soon' }).resetsAt).toBeUndefined()
    expect(codexTierToRateLimitInfo({ usedPercent: 10, windowDurationMins: 300, resetsAt: 1234 }).resetsAt).toBe(1234)
  })
  it('converts a 5-hour tier with low usage', () => {
    const info = codexTierToRateLimitInfo({ usedPercent: 4, windowDurationMins: 300, resetsAt: 1774070211 })
    expect(info.rateLimitType).toBe('five_hour')
    expect(info.utilization).toBeCloseTo(0.04)
    expect(info.resetsAt).toBe(1774070211)
    expect(info.status).toBe('allowed')
  })
  it('converts a 7-day tier with low usage', () => {
    const info = codexTierToRateLimitInfo({ usedPercent: 4, windowDurationMins: 10080, resetsAt: 1774525963 })
    expect(info.rateLimitType).toBe('seven_day')
    expect(info.utilization).toBeCloseTo(0.04)
    expect(info.status).toBe('allowed')
  })
  it('derives allowed_warning status when usedPercent >= 80', () => {
    const info = codexTierToRateLimitInfo({ usedPercent: 85, windowDurationMins: 300 })
    expect(info.status).toBe('allowed_warning')
    expect(info.utilization).toBeCloseTo(0.85)
  })
  it('derives exceeded status when usedPercent >= 100', () => {
    const info = codexTierToRateLimitInfo({ usedPercent: 100, windowDurationMins: 300 })
    expect(info.status).toBe('exceeded')
    expect(info.utilization).toBe(1)
  })
  it('handles usedPercent above 100', () => {
    const info = codexTierToRateLimitInfo({ usedPercent: 120, windowDurationMins: 300 })
    expect(info.status).toBe('exceeded')
    expect(info.utilization).toBeCloseTo(1.2)
  })
  it('handles missing usedPercent as 0', () => {
    const info = codexTierToRateLimitInfo({ windowDurationMins: 300 })
    expect(info.utilization).toBe(0)
    expect(info.status).toBe('allowed')
  })
  it('handles missing resetsAt', () => {
    const info = codexTierToRateLimitInfo({ usedPercent: 50, windowDurationMins: 300 })
    expect(info.resetsAt).toBeUndefined()
  })
})

describe('iterCodexRateLimitTiers', () => {
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
    expect(entries[0]?.info.status).toBe('allowed')
    expect(entries[1]?.info.status).toBe('allowed_warning')
  })
  it('skips missing tiers without raising', () => {
    const payload = { params: { rateLimits: { primary: { usedPercent: 10, windowDurationMins: 300 } } } }
    const entries = [...iterCodexRateLimitTiers(payload)]
    expect(entries).toHaveLength(1)
    expect(entries[0]?.key).toBe('primary')
  })
  it('yields nothing when payload has no rate-limits object', () => {
    expect([...iterCodexRateLimitTiers({})]).toEqual([])
    expect([...iterCodexRateLimitTiers({ params: {} })]).toEqual([])
    expect([...iterCodexRateLimitTiers(null)]).toEqual([])
  })
})

describe('codexRateLimitReachedType', () => {
  it('reads the snapshot-level reached-type', () => {
    expect(codexRateLimitReachedType({
      params: { rateLimits: { rateLimitReachedType: 'workspace_owner_credits_depleted', primary: { usedPercent: 20 } } },
    })).toBe('workspace_owner_credits_depleted')
  })
  it('returns undefined when absent, empty, or non-object', () => {
    expect(codexRateLimitReachedType({ params: { rateLimits: { primary: { usedPercent: 10 } } } })).toBeUndefined()
    expect(codexRateLimitReachedType({ params: { rateLimits: { rateLimitReachedType: '' } } })).toBeUndefined()
    expect(codexRateLimitReachedType({})).toBeUndefined()
    expect(codexRateLimitReachedType(null)).toBeUndefined()
  })
})

describe('formatCodexRateLimitReached', () => {
  it('maps known reached-types to labels', () => {
    expect(formatCodexRateLimitReached('rate_limit_reached')).toBe('Rate limit reached')
    expect(formatCodexRateLimitReached('workspace_member_credits_depleted')).toBe('Out of credits')
    expect(formatCodexRateLimitReached('workspace_owner_usage_limit_reached')).toBe('Usage limit reached')
  })
  it('falls back to a generic label for an unknown reached-type', () => {
    expect(formatCodexRateLimitReached('some_future_type')).toBe('Rate limit reached')
  })
})
