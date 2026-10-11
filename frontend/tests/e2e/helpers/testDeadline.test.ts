import { afterEach, describe, expect, it } from 'vitest'
import { currentTestDeadline, nativeCommandTimeout, startTestDeadline, startWaitLimitForTests, WAIT_REPORT_MARGIN_MS, waitTimeoutBeforeTestDeadline } from './testDeadline'

const ends: Array<() => void> = []

/** Start a record that the test cleanup ends, so no record leaks into the next test. */
function start(startedAt: number, timeout: () => number): () => void {
  const end = startTestDeadline(startedAt, timeout)
  ends.push(end)
  return end
}

afterEach(() => {
  for (const end of ends.splice(0))
    end()
})

describe('currentTestDeadline', () => {
  it('returns no deadline when no test runs', () => {
    expect(currentTestDeadline()).toBeUndefined()
  })

  it('returns the start plus the timeout', () => {
    start(1_000_000, () => 120_000)
    expect(currentTestDeadline()).toBe(1_120_000)
  })

  it('returns no deadline for a timeout of 0, which Playwright reads as no timeout', () => {
    start(1_000_000, () => 0)
    expect(currentTestDeadline()).toBeUndefined()
  })

  it('reads a timeout that the test changes while it runs', () => {
    let timeout = 120_000
    start(1_000_000, () => timeout)
    timeout = 240_000
    expect(currentTestDeadline()).toBe(1_240_000)
  })

  it('returns no deadline after the test ends', () => {
    const end = start(1_000_000, () => 120_000)
    end()
    expect(currentTestDeadline()).toBeUndefined()
  })

  it('keeps the record of a later test when an earlier end function runs late', () => {
    const endEarlier = start(1_000_000, () => 120_000)
    start(2_000_000, () => 60_000)
    endEarlier()
    expect(currentTestDeadline()).toBe(2_060_000)
  })
})

describe('startTestDeadline', () => {
  it.each([Number.NaN, Number.POSITIVE_INFINITY])('rejects the start %s, which states no time', (startedAt) => {
    expect(() => startTestDeadline(startedAt, () => 120_000)).toThrow('A test start must be a finite time')
    expect(currentTestDeadline()).toBeUndefined()
  })
})

describe('startWaitLimitForTests', () => {
  it('limits a wait to the stated time from now, and the end function removes the limit', () => {
    const before = Date.now()
    const end = startWaitLimitForTests(300)
    ends.push(end)
    const limit = waitTimeoutBeforeTestDeadline()
    expect(limit).toBeGreaterThan(0)
    expect(limit).toBeLessThanOrEqual(300)
    expect(limit).toBeGreaterThanOrEqual(300 - (Date.now() - before))
    end()
    expect(waitTimeoutBeforeTestDeadline()).toBe(0)
  })

  it.each([0, -1, Number.NaN, Number.POSITIVE_INFINITY])('refuses the limit %s, which states no finite wait', (limitMs) => {
    expect(() => startWaitLimitForTests(limitMs)).toThrow('A wait limit must be a positive number of milliseconds')
    expect(currentTestDeadline()).toBeUndefined()
  })
})

describe('waitTimeoutBeforeTestDeadline', () => {
  it('returns the time left before the deadline, less the report margin', () => {
    start(1_000_000, () => 120_000)
    expect(waitTimeoutBeforeTestDeadline(1_010_000)).toBe(120_000 - 10_000 - WAIT_REPORT_MARGIN_MS)
  })

  it('returns 0, which Playwright reads as no limit, when the test has no deadline', () => {
    expect(waitTimeoutBeforeTestDeadline(1_010_000)).toBe(0)
    start(1_000_000, () => 0)
    expect(waitTimeoutBeforeTestDeadline(1_010_000)).toBe(0)
  })

  it.each([
    { label: 'inside the margin', now: 1_120_000 - WAIT_REPORT_MARGIN_MS + 1 },
    { label: 'exactly at the margin', now: 1_120_000 - WAIT_REPORT_MARGIN_MS },
    { label: 'past the deadline', now: 1_200_000 },
  ])('returns 1 so that a wait $label fails at once, never 0 which would remove the limit', ({ now }) => {
    start(1_000_000, () => 120_000)
    expect(waitTimeoutBeforeTestDeadline(now)).toBe(1)
  })
})

describe('nativeCommandTimeout', () => {
  it('returns the limit when no test deadline exists', () => {
    expect(nativeCommandTimeout(undefined, 60_000, 0)).toBe(60_000)
  })

  it('caps a large remaining time at the limit', () => {
    expect(nativeCommandTimeout(Number.MAX_SAFE_INTEGER, 60_000, 0)).toBe(60_000)
  })

  it('returns the whole milliseconds left before the deadline when they are fewer than the limit', () => {
    expect(nativeCommandTimeout(100, 60_000, 99)).toBe(1)
    expect(nativeCommandTimeout(100.9, 60_000, 50)).toBe(50)
  })

  it.each([100, 100.5])('refuses a deadline of %s that leaves no whole millisecond at time 100', (deadline) => {
    expect(() => nativeCommandTimeout(deadline, 60_000, 100)).toThrow('The test deadline leaves no time for the native command.')
  })

  it.each([Number.NaN, Number.POSITIVE_INFINITY, Number.NEGATIVE_INFINITY])('refuses the deadline %s, which is not a finite time', (deadline) => {
    expect(() => nativeCommandTimeout(deadline, 60_000, 0)).toThrow('a finite test deadline and current time')
  })

  it('refuses a current time that is not finite, also when no test deadline exists', () => {
    expect(() => nativeCommandTimeout(undefined, 60_000, Number.NaN)).toThrow('a finite test deadline and current time')
  })

  it.each([0, -1, 1.5, Number.NaN, Number.POSITIVE_INFINITY])('refuses the limit %s, which is not a positive whole number of milliseconds', (limit) => {
    expect(() => nativeCommandTimeout(undefined, limit, 0)).toThrow('positive whole number of milliseconds')
  })

  it('reads the current time when the caller gives none', () => {
    const deadline = Date.now() + 1_000_000
    const timeout = nativeCommandTimeout(deadline, 2_000_000)
    expect(timeout).toBeGreaterThan(990_000)
    expect(timeout).toBeLessThanOrEqual(1_000_000)
  })
})
