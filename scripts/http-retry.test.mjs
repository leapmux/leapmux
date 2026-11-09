import { describe, expect, it } from 'bun:test'
import { parseRetryAfter, TRANSIENT_STATUSES } from './http-retry.mjs'

describe('parseRetryAfter', () => {
  it('reads the delta seconds of a Retry-After header', () => {
    expect(parseRetryAfter('5')).toBe(5000)
    expect(parseRetryAfter(' 30 ')).toBe(30000)
    expect(parseRetryAfter('0')).toBe(0)
  })

  it('reads the HTTP date of a Retry-After header', () => {
    const now = Date.parse('2026-11-08T10:00:00Z')
    expect(parseRetryAfter('Sun, 08 Nov 2026 10:00:30 GMT', now)).toBe(30000)
    expect(parseRetryAfter('Sun, 08 Nov 2026 09:59:30 GMT', now)).toBe(0)
  })

  it('returns null for an absent or malformed header', () => {
    expect(parseRetryAfter(null)).toBeNull()
    expect(parseRetryAfter('')).toBeNull()
    expect(parseRetryAfter('soon')).toBeNull()
    expect(parseRetryAfter('-1')).toBeNull()
    expect(parseRetryAfter('1.5')).toBeNull()
    expect(parseRetryAfter('1e1')).toBeNull()
    expect(parseRetryAfter('2026-11')).toBeNull()
  })
})

describe('TRANSIENT_STATUSES', () => {
  it('holds the overload and outage statuses and no final answer', () => {
    expect([...TRANSIENT_STATUSES].sort()).toEqual([429, 500, 502, 503, 504])
    for (const status of [200, 400, 401, 403, 404, 501])
      expect(TRANSIENT_STATUSES.has(status)).toBe(false)
  })
})
