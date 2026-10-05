import { describe, expect, it, vi } from 'vitest'
import { geminiSessionArchiveMinutes, waitForMinuteAfter } from './nativeStore'

const SESSION = '647b3f04-50d7-477e-bd85-5204b1413b54'

describe('geminiSessionArchiveMinutes', () => {
  it('reads the minute of every root archive of the session and of no other file', () => {
    expect(geminiSessionArchiveMinutes([
      'session-2026-10-04T12-10-647b3f04.jsonl',
      'session-2026-10-04T12-11-647b3f04.json',
      'session-2026-10-04T12-12-0badf00d.jsonl',
      'session-2026-10-04T12-13-647b3f04.jsonl.tmp',
      'session-2026-10-04T12-14-647b3f04',
      'session-2026-10-04-647b3f04.jsonl',
      '647b3f04-50d7-477e-bd85-5204b1413b54.jsonl',
      'session-2026-10-04T12-15-x647b3f04.jsonl',
    ], SESSION)).toEqual(['2026-10-04T12-10', '2026-10-04T12-11'])
  })

  it('reads no minute from an empty directory', () => {
    expect(geminiSessionArchiveMinutes([], SESSION)).toEqual([])
  })

  it.each(['', 'short', '647b3f/4-50d7'])('refuses the session ID %j', (sessionId) => {
    expect(() => geminiSessionArchiveMinutes([], sessionId)).toThrow('The native Gemini session ID')
  })
})

describe('waitForMinuteAfter', () => {
  function clockAt(time: string, step?: (milliseconds: number) => number) {
    let now = Date.parse(time)
    const slept: number[] = []
    return {
      slept,
      current: () => now,
      clock: {
        now: () => now,
        sleep: vi.fn(async (milliseconds: number) => {
          slept.push(milliseconds)
          now += step ? step(milliseconds) : milliseconds
        }),
      },
    }
  }

  it('sleeps until the next minute begins inside the minute', async () => {
    const fake = clockAt('2026-10-04T12:10:40.250Z')
    await waitForMinuteAfter('2026-10-04T12-10', fake.clock)
    expect(fake.slept).toEqual([19_750])
    expect(fake.current()).toBe(Date.parse('2026-10-04T12:11:00.000Z'))
  })

  it('sleeps a whole minute at the first instant of the minute', async () => {
    const fake = clockAt('2026-10-04T12:10:00.000Z')
    await waitForMinuteAfter('2026-10-04T12-10', fake.clock)
    expect(fake.slept).toEqual([60_000])
  })

  it('sleeps again when a sleep ends early', async () => {
    // The first sleep ends one millisecond early. The next one sleeps in full.
    const fake = clockAt('2026-10-04T12:10:59.000Z', milliseconds => milliseconds > 1 ? milliseconds - 1 : milliseconds)
    await waitForMinuteAfter('2026-10-04T12-10', fake.clock)
    expect(fake.slept).toEqual([1000, 1])
    expect(fake.current()).toBe(Date.parse('2026-10-04T12:11:00.000Z'))
  })

  it.each(['2026-10-04T12:11:00.000Z', '2026-10-05T00:00:00.000Z'])('does not sleep at %s, after the minute', async (time) => {
    const fake = clockAt(time)
    await waitForMinuteAfter('2026-10-04T12-10', fake.clock)
    expect(fake.clock.sleep).not.toHaveBeenCalled()
  })

  it.each(['', '2026-10-04T12:10', '2026-10-04T12-10-00', '2026-13-40T99-99', 'session-2026-10-04T12-10'])('refuses the minute %j', async (minute) => {
    const fake = clockAt('2026-10-04T12:10:00.000Z')
    await expect(waitForMinuteAfter(minute, fake.clock)).rejects.toThrow('is not a UTC minute')
    expect(fake.clock.sleep).not.toHaveBeenCalled()
  })
})
