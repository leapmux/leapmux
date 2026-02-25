import type { RateLimitInfo } from '~/stores/agentSession.store'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { formatCountdown, formatRateLimitMessage, formatRateLimitSummary, getResetsAt, pickUrgentRateLimit } from '~/lib/rateLimitUtils'

describe('formatCountdown', () => {
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

describe('getResetsAt', () => {
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

describe('pickUrgentRateLimit', () => {
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

  it('returns null when all rate limits have status allowed', () => {
    const rateLimits: Record<string, RateLimitInfo> = {
      five_hour: { status: 'allowed', resetsAt: Math.floor(Date.now() / 1000) + 3600 },
    }
    expect(pickUrgentRateLimit(rateLimits)).toBeNull()
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

  it('prefers exceeded over warning', () => {
    const nowSec = Math.floor(Date.now() / 1000)
    const rateLimits: Record<string, RateLimitInfo> = {
      five_hour: { status: 'allowed_warning', resetsAt: nowSec + 1800, rateLimitType: 'five_hour' },
      seven_day: { status: 'exceeded', resetsAt: nowSec + 7200, rateLimitType: 'seven_day' },
    }
    const result = pickUrgentRateLimit(rateLimits)
    expect(result).not.toBeNull()
    expect(result!.info.rateLimitType).toBe('seven_day')
  })

  it('picks the one with least remaining time when same severity', () => {
    const nowSec = Math.floor(Date.now() / 1000)
    const rateLimits: Record<string, RateLimitInfo> = {
      five_hour: { status: 'allowed_warning', resetsAt: nowSec + 1800, rateLimitType: 'five_hour' },
      seven_day: { status: 'allowed_warning', resetsAt: nowSec + 7200, rateLimitType: 'seven_day' },
    }
    const result = pickUrgentRateLimit(rateLimits)
    expect(result).not.toBeNull()
    expect(result!.info.rateLimitType).toBe('five_hour')
  })

  it('uses overageResetsAt when isUsingOverage is true', () => {
    const nowSec = Math.floor(Date.now() / 1000)
    const rateLimits: Record<string, RateLimitInfo> = {
      five_hour: { status: 'exceeded', resetsAt: nowSec - 100, overageResetsAt: nowSec + 3600, isUsingOverage: true, rateLimitType: 'five_hour' },
    }
    const result = pickUrgentRateLimit(rateLimits)
    expect(result).not.toBeNull()
    expect(result!.countdown).toBe('1:00')
  })
})

describe('formatRateLimitMessage', () => {
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

  it('uses overageResetsAt when isUsingOverage is true', () => {
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

describe('formatRateLimitSummary', () => {
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
})
