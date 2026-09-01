import { describe, expect, it } from 'vitest'
import { formatCompactAge, formatLocalDateTime } from '~/lib/dateFormat'

const now = new Date('2026-09-01T12:00:00.000Z')
const ago = (ms: number) => new Date(now.getTime() - ms)

const SECOND = 1000
const MINUTE = 60 * SECOND
const HOUR = 60 * MINUTE
const DAY = 24 * HOUR

describe('formatCompactAge', () => {
  it('counts seconds below a minute', () => {
    expect(formatCompactAge(now, now)).toBe('0s')
    expect(formatCompactAge(ago(1 * SECOND), now)).toBe('1s')
    expect(formatCompactAge(ago(59 * SECOND), now)).toBe('59s')
  })

  it('switches unit exactly at each boundary', () => {
    expect(formatCompactAge(ago(60 * SECOND), now)).toBe('1m')
    expect(formatCompactAge(ago(59 * MINUTE), now)).toBe('59m')
    expect(formatCompactAge(ago(60 * MINUTE), now)).toBe('1h')
    expect(formatCompactAge(ago(23 * HOUR), now)).toBe('23h')
    expect(formatCompactAge(ago(24 * HOUR), now)).toBe('1d')
    expect(formatCompactAge(ago(29 * DAY), now)).toBe('29d')
    expect(formatCompactAge(ago(30 * DAY), now)).toBe('1mo')
    expect(formatCompactAge(ago(359 * DAY), now)).toBe('11mo')
    expect(formatCompactAge(ago(365 * DAY), now)).toBe('1y')
  })

  it('truncates rather than rounding, so an age never reads ahead of itself', () => {
    expect(formatCompactAge(ago(119 * SECOND), now)).toBe('1m')
    expect(formatCompactAge(ago(47 * HOUR), now)).toBe('1d')
  })

  // A clock that moved backwards, or a session store whose timestamp is a
  // moment ahead of this machine, must read as "just now" and never as a
  // negative age.
  it('clamps a future instant to zero', () => {
    expect(formatCompactAge(new Date(now.getTime() + 5 * MINUTE), now)).toBe('0s')
  })

  it('defaults `now` to the current time', () => {
    expect(formatCompactAge(new Date())).toBe('0s')
  })
})

describe('formatLocalDateTime', () => {
  it('includes a timezone suffix', () => {
    const formatted = formatLocalDateTime(new Date('2026-04-14T10:20:30Z'))
    const parts = formatted.trim().split(/\s+/)
    expect(parts.length).toBeGreaterThanOrEqual(5)
    const last = parts.at(-1)
    expect(last).not.toBe('AM')
    expect(last).not.toBe('PM')
  })

  it('leads with the abbreviated weekday', () => {
    expect(formatLocalDateTime(new Date('2026-09-01T12:00:00.000Z'))).toMatch(/^(Mon|Tue|Wed|Thu|Fri|Sat|Sun), /)
  })
})
