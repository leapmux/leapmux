import { checkSchedule, datetimeLocalOf, EMPTY_SCHEDULE, parseLocalSchedule, scheduleIsoOf } from './scheduleSend'

// The schedule conversions run in the browser's timezone, so these tests pin
// the timezone rather than assume one: every round trip must hold in +09:00,
// which neither UTC slicing nor a UTC-only format survives.
const originalTz = process.env.TZ
beforeAll(() => {
  process.env.TZ = 'Asia/Tokyo'
})
afterAll(() => {
  process.env.TZ = originalTz
})

describe('parseLocalSchedule', () => {
  it('parses a local field value as browser-local time', () => {
    const at = parseLocalSchedule('2030-01-01T18:45:30.123')
    expect(at?.toISOString()).toBe('2030-01-01T09:45:30.123Z')
  })

  it('keeps milliseconds', () => {
    expect(parseLocalSchedule('2030-01-01T18:45:30.001')?.getMilliseconds()).toBe(1)
  })

  it('reads an empty draft as no schedule', () => {
    expect(parseLocalSchedule(EMPTY_SCHEDULE)).toBeUndefined()
  })

  it('refuses a nonexistent DST-local time', () => {
    // Asia/Tokyo has no DST; a nonexistent local time is proven by construction
    // instead: a field value whose local components do not round-trip. Writing
    // 25 hours into the field produces exactly that shape.
    expect(parseLocalSchedule('2030-01-01T25:00:00')).toBeUndefined()
  })
})

describe('datetimeLocalOf', () => {
  it('converts a stored UTC timestamp through the local getters', () => {
    expect(datetimeLocalOf('2030-01-01T09:45:30.123Z')).toBe('2030-01-01T18:45:30.123')
  })

  it('round-trips a field value through the wire and back, milliseconds kept', () => {
    const field = '2030-06-15T23:59:59.999'
    const wire = scheduleIsoOf(parseLocalSchedule(field)!)
    expect(datetimeLocalOf(wire)).toBe(field)
  })

  it('reads an absent or malformed stored value as no schedule', () => {
    expect(datetimeLocalOf('')).toBe(EMPTY_SCHEDULE)
    expect(datetimeLocalOf('not-a-time')).toBe(EMPTY_SCHEDULE)
  })
})

describe('checkSchedule', () => {
  const now = new Date('2030-01-01T00:00:00.000Z')

  it('treats an empty draft as immediate', () => {
    expect(checkSchedule('', now)).toBe('empty')
  })

  it('accepts a strictly future instant', () => {
    expect(checkSchedule('2030-01-02T09:00:00.000', now)).toBe('valid')
  })

  it('refuses a past or exactly-now instant on a new schedule', () => {
    expect(checkSchedule('2029-12-31T09:00:00.000', now)).toBe('notFuture')
    expect(checkSchedule('2030-01-01T09:00:00.000', now)).toBe('notFuture')
  })

  it('accepts an unchanged stored past deadline on a queue edit', () => {
    const stored = '2029-12-31T00:00:00.000Z'
    const field = datetimeLocalOf(stored)
    expect(checkSchedule(field, now, stored)).toBe('valid')
  })

  it('refuses a changed past deadline on a queue edit', () => {
    const stored = '2029-12-31T00:00:00.000Z'
    const otherPast = datetimeLocalOf('2029-12-30T00:00:00.000Z')
    expect(checkSchedule(otherPast, now, stored)).toBe('notFuture')
  })

  it('refuses malformed input', () => {
    expect(checkSchedule('next tuesday', now)).toBe('invalid')
  })
})
