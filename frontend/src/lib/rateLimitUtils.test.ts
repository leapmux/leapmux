import type { RateLimitInfo } from '~/stores/agentSession.store'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  codexRateLimitReachedType,
  codexTierToRateLimitInfo,
  formatCodexRateLimitReached,
  formatCountdown,
  formatRateLimitMessage,
  formatRateLimitSummary,
  getResetsAt,
  iterCodexRateLimitTiers,
  pickUrgentRateLimit,
} from './rateLimitUtils'

/** Unix seconds `secs` in the future, for a deterministically-positive countdown. */
const future = (secs: number): number => Math.floor(Date.now() / 1000) + secs

const UNKNOWN_TYPE_PREFIX_RE = /^Rate limit \(unknown_type\):/

describe('formatcountdown', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2025-06-01T00:00:00Z'))
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('returns null when remaining time is zero or negative', () => {
    const past = Math.floor(Date.now() / 1000) - 100
    expect(formatCountdown(past)).toBeNull()
  })

  it('returns null when remaining time is exactly now', () => {
    const now = Math.floor(Date.now() / 1000)
    expect(formatCountdown(now)).toBeNull()
  })

  it('formats hours and minutes when days is 0', () => {
    const nowSec = Math.floor(Date.now() / 1000)
    // 3 hours and 25 minutes from now
    const resetsAt = nowSec + 3 * 3600 + 25 * 60
    expect(formatCountdown(resetsAt)).toBe('3:25')
  })

  it('pads minutes to two digits', () => {
    const nowSec = Math.floor(Date.now() / 1000)
    // 1 hour and 5 minutes from now
    const resetsAt = nowSec + 3600 + 5 * 60
    expect(formatCountdown(resetsAt)).toBe('1:05')
  })

  it('formats days, hours, and minutes when days > 0', () => {
    const nowSec = Math.floor(Date.now() / 1000)
    // 2 days, 5 hours, 30 minutes from now
    const resetsAt = nowSec + 2 * 86400 + 5 * 3600 + 30 * 60
    expect(formatCountdown(resetsAt)).toBe('2:05:30')
  })

  it('shows 0:xx for less than an hour', () => {
    const nowSec = Math.floor(Date.now() / 1000)
    const resetsAt = nowSec + 45 * 60
    expect(formatCountdown(resetsAt)).toBe('0:45')
  })
})

describe('getresetsat', () => {
  it('returns resetsAt when not using overage', () => {
    const info: RateLimitInfo = { resetsAt: 12345, overageResetsAt: 99999, isUsingOverage: false }
    expect(getResetsAt(info)).toBe(12345)
  })

  it('returns overageResetsAt when using overage', () => {
    const info: RateLimitInfo = { resetsAt: 12345, overageResetsAt: 99999, isUsingOverage: true }
    expect(getResetsAt(info)).toBe(99999)
  })

  it('returns undefined when resetsAt not set', () => {
    const info: RateLimitInfo = {}
    expect(getResetsAt(info)).toBeUndefined()
  })
})

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
  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2025-06-01T00:00:00Z'))
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('returns null when no rate limits exist', () => {
    expect(pickUrgentRateLimit({})).toBeNull()
  })

  it('returns null when every tier is allowed', () => {
    expect(pickUrgentRateLimit({
      five_hour: { status: 'allowed', utilization: 0.2, resetsAt: future(3600) },
    })).toBeNull()
  })

  it('picks a warning rate limit', () => {
    const resetsAt = Math.floor(Date.now() / 1000) + 3600
    const rateLimits: Record<string, RateLimitInfo> = {
      five_hour: { status: 'allowed_warning', resetsAt, rateLimitType: 'five_hour' },
    }
    const result = pickUrgentRateLimit(rateLimits)
    expect(result).not.toBeNull()
    expect(result!.info.rateLimitType).toBe('five_hour')
    expect(result!.countdown).toBe('1:00')
  })

  it('prefers an exceeded tier over a warning tier', () => {
    const picked = pickUrgentRateLimit({
      seven_day: { status: 'allowed_warning', rateLimitType: 'seven_day', resetsAt: future(3600) },
      five_hour: { status: 'exceeded', rateLimitType: 'five_hour', resetsAt: future(7200) },
    })
    expect(picked?.info.rateLimitType).toBe('five_hour')
  })

  it('prefers exceeded over warning (exceeded in the later-declared tier)', () => {
    const nowSec = Math.floor(Date.now() / 1000)
    const rateLimits: Record<string, RateLimitInfo> = {
      five_hour: { status: 'allowed_warning', resetsAt: nowSec + 1800, rateLimitType: 'five_hour' },
      seven_day: { status: 'exceeded', resetsAt: nowSec + 7200, rateLimitType: 'seven_day' },
    }
    const result = pickUrgentRateLimit(rateLimits)
    expect(result).not.toBeNull()
    expect(result!.info.rateLimitType).toBe('seven_day')
  })

  it('among equal severity, prefers the sooner reset', () => {
    const picked = pickUrgentRateLimit({
      a: { status: 'allowed_warning', rateLimitType: 'a', resetsAt: future(7200) },
      b: { status: 'allowed_warning', rateLimitType: 'b', resetsAt: future(600) },
    })
    expect(picked?.info.rateLimitType).toBe('b')
  })

  it('picks the one with least remaining time when same severity (sooner in the earlier-declared tier)', () => {
    const nowSec = Math.floor(Date.now() / 1000)
    const rateLimits: Record<string, RateLimitInfo> = {
      five_hour: { status: 'allowed_warning', resetsAt: nowSec + 1800, rateLimitType: 'five_hour' },
      seven_day: { status: 'allowed_warning', resetsAt: nowSec + 7200, rateLimitType: 'seven_day' },
    }
    const result = pickUrgentRateLimit(rateLimits)
    expect(result).not.toBeNull()
    expect(result!.info.rateLimitType).toBe('five_hour')
  })

  it('uses overageResetsAt for its countdown when isUsingOverage is true', () => {
    const nowSec = Math.floor(Date.now() / 1000)
    const rateLimits: Record<string, RateLimitInfo> = {
      five_hour: { status: 'exceeded', resetsAt: nowSec - 100, overageResetsAt: nowSec + 3600, isUsingOverage: true, rateLimitType: 'five_hour' },
    }
    const result = pickUrgentRateLimit(rateLimits)
    expect(result).not.toBeNull()
    expect(result!.countdown).toBe('1:00')
  })

  it('treats a Claude "rejected" status as a blocked (highest-severity) tier', () => {
    const picked = pickUrgentRateLimit({
      seven_day: { status: 'rejected', rateLimitType: 'seven_day', resetsAt: future(3600) },
    })
    expect(picked?.info.rateLimitType).toBe('seven_day')
  })
})

describe('formatratelimitmessage', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2025-06-01T00:00:00Z'))
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('returns generic message for empty info', () => {
    expect(formatRateLimitMessage({})).toBe('Rate limit update')
  })

  it('includes type label for known types', () => {
    const msg = formatRateLimitMessage({ rateLimitType: 'five_hour', utilization: 0.5 })
    expect(msg).toContain('5-hour')
  })

  it('includes raw type in parentheses for unknown types', () => {
    const msg = formatRateLimitMessage({ rateLimitType: 'unknown_type', utilization: 0.5 })
    expect(msg).toMatch(UNKNOWN_TYPE_PREFIX_RE)
  })

  it('includes utilization as percentage', () => {
    const msg = formatRateLimitMessage({ utilization: 0.82 })
    expect(msg).toContain('82% used')
  })

  it('includes "reached" for exceeded status', () => {
    const msg = formatRateLimitMessage({ status: 'exceeded', rateLimitType: 'five_hour' })
    expect(msg).toContain('reached')
  })

  it('does not include "reached" for allowed_warning status', () => {
    const msg = formatRateLimitMessage({ status: 'allowed_warning', utilization: 0.8 })
    expect(msg).not.toContain('reached')
  })

  it('includes overage info when applicable', () => {
    const msg = formatRateLimitMessage({ isUsingOverage: true, utilization: 0.5 })
    expect(msg).toContain('overage')
  })

  it('includes reset countdown when resetsAt is set', () => {
    const nowSec = Math.floor(Date.now() / 1000)
    const msg = formatRateLimitMessage({ resetsAt: nowSec + 3600 + 30 * 60, utilization: 0.5 })
    expect(msg).toContain('resets in 1:30')
  })

  it('uses overageResetsAt for the reset countdown when isUsingOverage is true', () => {
    const nowSec = Math.floor(Date.now() / 1000)
    const msg = formatRateLimitMessage({
      isUsingOverage: true,
      resetsAt: nowSec - 100,
      overageResetsAt: nowSec + 7200,
      utilization: 0.5,
    })
    expect(msg).toContain('resets in 2:00')
  })
})

describe('formatratelimitsummary', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2025-06-01T00:00:00Z'))
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('renders allowed with reset time', () => {
    const nowSec = Math.floor(Date.now() / 1000)
    const info: RateLimitInfo = { status: 'allowed', resetsAt: nowSec + 4 * 3600 + 59 * 60 }
    expect(formatRateLimitSummary(info)).toBe('Allowed, resets in 4:59')
  })

  it('renders allowed without reset time', () => {
    const info: RateLimitInfo = { status: 'allowed' }
    expect(formatRateLimitSummary(info)).toBe('Allowed')
  })

  it('summarizes an allowed tier with utilization', () => {
    expect(formatRateLimitSummary({ status: 'allowed', utilization: 0.5 })).toBe('Allowed, 50% used')
  })

  it('renders warning with utilization and reset time', () => {
    const nowSec = Math.floor(Date.now() / 1000)
    const info: RateLimitInfo = { status: 'allowed_warning', utilization: 0.8, resetsAt: nowSec + 3 * 3600 + 22 * 60 }
    expect(formatRateLimitSummary(info)).toBe('Warning, 80% used, resets in 3:22')
  })

  it('renders exceeded without utilization', () => {
    const nowSec = Math.floor(Date.now() / 1000)
    const info: RateLimitInfo = { status: 'exceeded', utilization: 1.0, resetsAt: nowSec + 3600 + 45 * 60 }
    expect(formatRateLimitSummary(info)).toBe('Exceeded, resets in 1:45')
  })

  it('renders exceeded with overage', () => {
    const nowSec = Math.floor(Date.now() / 1000)
    const info: RateLimitInfo = { status: 'exceeded', utilization: 1.0, isUsingOverage: true, overageResetsAt: nowSec + 2 * 3600 + 15 * 60 }
    expect(formatRateLimitSummary(info)).toBe('Exceeded, overage, resets in 2:15')
  })

  it('renders utilization only when status is missing', () => {
    const nowSec = Math.floor(Date.now() / 1000)
    const info: RateLimitInfo = { utilization: 0.5, resetsAt: nowSec + 3600 }
    expect(formatRateLimitSummary(info)).toBe('50% used, resets in 1:00')
  })

  it('returns Unknown for empty info', () => {
    const info: RateLimitInfo = {}
    expect(formatRateLimitSummary(info)).toBe('Unknown')
  })

  it('treats a Claude "rejected" status as a blocked (exceeded) summary', () => {
    expect(formatRateLimitSummary({ status: 'rejected', utilization: 1 })).toContain('Exceeded')
  })
})
