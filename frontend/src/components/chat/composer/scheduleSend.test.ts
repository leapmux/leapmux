import { PINNED_TEST_TIMEZONE } from '../../../../vitest.node'
import {
  checkSchedule,
  datetimeLocalOf,
  EMPTY_SCHEDULE,
  localFieldValueOf,
  parseLocalSchedule,
  scheduleRefusalMessage,
} from './scheduleSend'

// The schedule conversions run in the browser's timezone, so this file runs in
// the `node-timezone` project, which pins America/New_York (UTC-05:00, or
// UTC-04:00 in summer). Neither UTC slicing nor a UTC-only format survives a
// round trip there, and its DST transitions give the two cases a local field
// cannot express directly. In 2030, they fall on these days:
// - March 10: 02:00 jumps to 03:00, so 02:30 does not exist.
// - November 3: 02:00 falls back to 01:00, so 01:30 happens twice, at 05:30Z
//   and at 06:30Z.

describe('the pinned test timezone', () => {
  it('is in effect, so no case below passes in the host timezone', () => {
    expect(new Intl.DateTimeFormat().resolvedOptions().timeZone).toBe(PINNED_TEST_TIMEZONE)
    expect(new Date(2030, 0, 1).getTimezoneOffset()).toBe(300)
  })
})

describe('parseLocalSchedule', () => {
  it('parses a local field value as browser-local time', () => {
    expect(parseLocalSchedule('2030-01-01T18:45:30.123')?.toISOString()).toBe('2030-01-01T23:45:30.123Z')
  })

  it('parses the whole-minute form that the browser writes when the seconds are zero', () => {
    expect(parseLocalSchedule('2030-01-01T18:45')?.toISOString()).toBe('2030-01-01T23:45:00.000Z')
  })

  it('parses seconds that carry no fraction', () => {
    expect(parseLocalSchedule('2030-01-01T18:45:30')?.toISOString()).toBe('2030-01-01T23:45:30.000Z')
  })

  it('reads a short fraction as tenths or hundredths of a second', () => {
    expect(parseLocalSchedule('2030-01-01T18:45:30.1')?.getMilliseconds()).toBe(100)
    expect(parseLocalSchedule('2030-01-01T18:45:30.12')?.getMilliseconds()).toBe(120)
    expect(parseLocalSchedule('2030-01-01T18:45:30.001')?.getMilliseconds()).toBe(1)
  })

  it('reads an empty draft as no schedule', () => {
    expect(parseLocalSchedule(EMPTY_SCHEDULE)).toBeUndefined()
  })

  it('refuses a local time that the spring-forward transition skips', () => {
    // The Date constructor accepts 02:30 and moves it to 03:30.
    expect(new Date(2030, 2, 10, 2, 30).getHours()).toBe(3)
    expect(parseLocalSchedule('2030-03-10T02:30')).toBeUndefined()
  })

  it('refuses a calendar date or an hour that does not exist', () => {
    // The constructor normalizes both, to March 2 and to the next midnight.
    expect(parseLocalSchedule('2030-02-30T10:00')).toBeUndefined()
    expect(parseLocalSchedule('2030-01-01T24:00')).toBeUndefined()
    expect(parseLocalSchedule('2030-01-01T18:60')).toBeUndefined()
  })

  it('refuses a value that is not a datetime-local value', () => {
    expect(parseLocalSchedule('next tuesday')).toBeUndefined()
    expect(parseLocalSchedule('2030-01-01 18:45')).toBeUndefined()
    expect(parseLocalSchedule('2030-01-01T18:45Z')).toBeUndefined()
    expect(parseLocalSchedule('2030-01-01T23:45:30.123Z')).toBeUndefined()
    expect(parseLocalSchedule('2030-01-01T18:45:30.1234')).toBeUndefined()
    expect(parseLocalSchedule('2030-1-01T18:45')).toBeUndefined()
  })

  it('reads the repeated fall-back hour as its earlier instant', () => {
    expect(parseLocalSchedule('2030-11-03T01:30')?.toISOString()).toBe('2030-11-03T05:30:00.000Z')
  })

  it('reads a year below 100 as that year, not as a year of the 1900s', () => {
    expect(parseLocalSchedule('0050-06-01T12:00')?.getFullYear()).toBe(50)
  })
})

describe('localFieldValueOf', () => {
  it('writes every component in the local timezone, seconds and milliseconds included', () => {
    expect(localFieldValueOf(new Date('2030-01-01T23:45:00.000Z'))).toBe('2030-01-01T18:45:00.000')
  })

  it('pads a year below 1000 to four digits', () => {
    const at = new Date(2030, 0, 1)
    at.setFullYear(50)
    expect(localFieldValueOf(at)).toBe('0050-01-01T00:00:00.000')
  })
})

describe('datetimeLocalOf', () => {
  it('converts a stored UTC timestamp through the local getters', () => {
    expect(datetimeLocalOf('2030-01-01T23:45:30.123Z')).toBe('2030-01-01T18:45:30.123')
  })

  it('round-trips a field value through the wire and back, milliseconds kept', () => {
    const field = '2030-06-15T23:59:59.999'
    const wire = parseLocalSchedule(field)!.toISOString()
    expect(datetimeLocalOf(wire)).toBe(field)
  })

  it('renders both instants of the repeated fall-back hour as one local value', () => {
    expect(datetimeLocalOf('2030-11-03T05:30:00.000Z')).toBe('2030-11-03T01:30:00.000')
    expect(datetimeLocalOf('2030-11-03T06:30:00.000Z')).toBe('2030-11-03T01:30:00.000')
  })

  it('reads an absent or malformed stored value as no schedule', () => {
    expect(datetimeLocalOf('')).toBe(EMPTY_SCHEDULE)
    expect(datetimeLocalOf('not-a-time')).toBe(EMPTY_SCHEDULE)
  })
})

describe('checkSchedule', () => {
  // Midnight of January 1, 2030, local time.
  const now = new Date('2030-01-01T05:00:00.000Z')

  it('treats an empty draft as immediate', () => {
    expect(checkSchedule('', now)).toEqual({ verdict: 'empty', wire: '' })
  })

  it('accepts a strictly future instant, whole minutes included, with its wire value', () => {
    expect(checkSchedule('2030-01-02T09:00:00.000', now)).toEqual({ verdict: 'valid', wire: '2030-01-02T14:00:00.000Z' })
    expect(checkSchedule('2030-01-01T00:01', now)).toEqual({ verdict: 'valid', wire: '2030-01-01T05:01:00.000Z' })
  })

  it('refuses a past or exactly-now instant on a new schedule, and keeps its wire value', () => {
    expect(checkSchedule('2029-12-31T23:59', now)).toEqual({ verdict: 'notFuture', wire: '2030-01-01T04:59:00.000Z' })
    expect(checkSchedule('2030-01-01T00:00', now)).toEqual({ verdict: 'notFuture', wire: '2030-01-01T05:00:00.000Z' })
  })

  it('reads a draft that holds the stored past deadline of an edit as unchanged', () => {
    const stored = '2029-12-31T05:00:00.000Z'
    expect(checkSchedule(datetimeLocalOf(stored), now, stored)).toEqual({ verdict: 'unchanged', wire: stored })
  })

  it('reads the stored instant in the whole-minute form as unchanged', () => {
    const stored = '2029-12-31T05:00:00.000Z'
    expect(checkSchedule('2029-12-31T00:00', now, stored)).toEqual({ verdict: 'unchanged', wire: stored })
  })

  it('reads the later instant of the repeated fall-back hour as unchanged, with the stored value', () => {
    // The field value parses back to 05:30Z, one hour before the stored instant.
    const stored = '2030-11-03T06:30:00.000Z'
    expect(checkSchedule(datetimeLocalOf(stored), new Date('2030-11-03T06:00:00.000Z'), stored)).toEqual({ verdict: 'unchanged', wire: stored })
  })

  it('reads an empty draft of an unscheduled item as unchanged, and a cleared schedule as empty', () => {
    expect(checkSchedule('', now, '')).toEqual({ verdict: 'unchanged', wire: '' })
    expect(checkSchedule('', now, '2030-01-01T23:45:00.000Z')).toEqual({ verdict: 'empty', wire: '' })
  })

  it('judges a changed draft of an edit on its own instant', () => {
    expect(checkSchedule('2030-01-02T18:45', now, '2030-01-01T23:45:00.000Z')).toEqual({ verdict: 'valid', wire: '2030-01-02T23:45:00.000Z' })
    const otherPast = datetimeLocalOf('2029-12-30T05:00:00.000Z')
    expect(checkSchedule(otherPast, now, '2029-12-31T05:00:00.000Z')).toEqual({ verdict: 'notFuture', wire: '2029-12-30T05:00:00.000Z' })
  })

  it('judges a draft of an unscheduled item on its own instant', () => {
    expect(checkSchedule('2030-01-01T18:45', now, '')).toEqual({ verdict: 'valid', wire: '2030-01-01T23:45:00.000Z' })
    expect(checkSchedule('2029-12-31T00:00', now, '')).toEqual({ verdict: 'notFuture', wire: '2029-12-31T05:00:00.000Z' })
  })

  it('refuses malformed input and carries no wire value', () => {
    expect(checkSchedule('next tuesday', now)).toEqual({ verdict: 'invalid' })
    expect(checkSchedule('2030-03-10T02:30', now)).toEqual({ verdict: 'invalid' })
  })
})

describe('scheduleRefusalMessage', () => {
  it('states each refusal in its own words', () => {
    expect(scheduleRefusalMessage('invalid')).toBe('Enter a real local date and time.')
    expect(scheduleRefusalMessage('notFuture')).toBe('Pick a time in the future.')
  })
})
