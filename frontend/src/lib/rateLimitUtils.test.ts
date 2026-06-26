import { describe, expect, it } from 'vitest'
import { codexRateLimitReachedType, codexTierToRateLimitInfo, formatCodexRateLimitReached, formatRateLimitSummary, iterCodexRateLimitTiers, pickUrgentRateLimit } from './rateLimitUtils'

/** Unix seconds `secs` in the future, for a deterministically-positive countdown. */
const future = (secs: number): number => Math.floor(Date.now() / 1000) + secs

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

describe('codexratelimitreachedtype', () => {
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

describe('formatcodexratelimitreached', () => {
  it('maps known reached-types to labels', () => {
    expect(formatCodexRateLimitReached('rate_limit_reached')).toBe('Rate limit reached')
    expect(formatCodexRateLimitReached('workspace_member_credits_depleted')).toBe('Out of credits')
    expect(formatCodexRateLimitReached('workspace_owner_usage_limit_reached')).toBe('Usage limit reached')
  })
  it('falls back to a generic label for an unknown reached-type', () => {
    expect(formatCodexRateLimitReached('some_future_type')).toBe('Rate limit reached')
  })
})

describe('pickurgentratelimit', () => {
  it('returns null when every tier is allowed', () => {
    expect(pickUrgentRateLimit({
      five_hour: { status: 'allowed', utilization: 0.2, resetsAt: future(3600) },
    })).toBeNull()
  })
  it('prefers an exceeded tier over a warning tier', () => {
    const picked = pickUrgentRateLimit({
      seven_day: { status: 'allowed_warning', rateLimitType: 'seven_day', resetsAt: future(3600) },
      five_hour: { status: 'exceeded', rateLimitType: 'five_hour', resetsAt: future(7200) },
    })
    expect(picked?.info.rateLimitType).toBe('five_hour')
  })
  it('among equal severity, prefers the sooner reset', () => {
    const picked = pickUrgentRateLimit({
      a: { status: 'allowed_warning', rateLimitType: 'a', resetsAt: future(7200) },
      b: { status: 'allowed_warning', rateLimitType: 'b', resetsAt: future(600) },
    })
    expect(picked?.info.rateLimitType).toBe('b')
  })
  it('treats a Claude "rejected" status as a blocked (highest-severity) tier', () => {
    const picked = pickUrgentRateLimit({
      seven_day: { status: 'rejected', rateLimitType: 'seven_day', resetsAt: future(3600) },
    })
    expect(picked?.info.rateLimitType).toBe('seven_day')
  })
})

describe('formatratelimitsummary', () => {
  it('summarizes an allowed tier with utilization', () => {
    expect(formatRateLimitSummary({ status: 'allowed', utilization: 0.5 })).toBe('Allowed, 50% used')
  })
  it('summarizes a warning tier with utilization and a countdown', () => {
    const s = formatRateLimitSummary({ status: 'allowed_warning', utilization: 0.85, resetsAt: future(3600) })
    expect(s).toContain('Warning')
    expect(s).toContain('85% used')
    expect(s).toContain('resets in')
  })
  it('omits utilization once exceeded', () => {
    const s = formatRateLimitSummary({ status: 'exceeded', utilization: 1, resetsAt: future(3600) })
    expect(s).toContain('Exceeded')
    expect(s).not.toContain('% used')
  })
  it('treats a Claude "rejected" status as a blocked (exceeded) summary', () => {
    expect(formatRateLimitSummary({ status: 'rejected', utilization: 1 })).toContain('Exceeded')
  })
})
