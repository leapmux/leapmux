import type { DeadlineSchedulerClock } from './deadlineScheduler'
import { describe, expect, it, vi } from 'vitest'
import { createDeadlineScheduler } from './deadlineScheduler'

interface FakeTimer {
  at: number
  handler: () => void
}

function createFakeClock(): DeadlineSchedulerClock & {
  advanceTo: (nextNowMs: number) => void
  clearTimeoutSpy: ReturnType<typeof vi.fn>
  setTimeoutSpy: ReturnType<typeof vi.fn>
} {
  let nowMs = 0
  let nextHandle = 1
  const timers = new Map<number, FakeTimer>()
  const clearTimeoutSpy = vi.fn()
  const setTimeoutSpy = vi.fn()

  return {
    now: () => nowMs,
    setTimeout: (handler, delayMs) => {
      const handle = nextHandle++
      timers.set(handle, { at: nowMs + delayMs, handler })
      setTimeoutSpy(delayMs, handle)
      return handle as unknown as ReturnType<typeof setTimeout>
    },
    clearTimeout: (handle) => {
      clearTimeoutSpy(handle)
      timers.delete(handle as unknown as number)
    },
    advanceTo: (nextNowMs) => {
      nowMs = nextNowMs
      const ready = Array.from(timers.entries())
        .filter(([, timer]) => timer.at <= nowMs)
        .sort((a, b) => a[1].at - b[1].at)
      for (const [handle, timer] of ready) {
        if (!timers.delete(handle))
          continue
        timer.handler()
      }
    },
    clearTimeoutSpy,
    setTimeoutSpy,
  }
}

describe('createDeadlineScheduler', () => {
  it('extends later deadlines without cancelling the existing timer', () => {
    const clock = createFakeClock()
    const onDeadline = vi.fn()
    const scheduler = createDeadlineScheduler(onDeadline, clock)

    scheduler.scheduleAt(100)
    scheduler.scheduleAt(200)
    scheduler.scheduleAt(300)

    expect(clock.setTimeoutSpy).toHaveBeenCalledTimes(1)
    expect(clock.clearTimeoutSpy).not.toHaveBeenCalled()

    clock.advanceTo(100)

    expect(onDeadline).not.toHaveBeenCalled()
    expect(clock.setTimeoutSpy).toHaveBeenCalledTimes(2)
    expect(clock.setTimeoutSpy).toHaveBeenLastCalledWith(200, 2)

    clock.advanceTo(300)

    expect(onDeadline).toHaveBeenCalledTimes(1)
  })

  it('re-arms earlier when a new earlier deadline replaces a later one', () => {
    const clock = createFakeClock()
    const onDeadline = vi.fn()
    const scheduler = createDeadlineScheduler(onDeadline, clock)

    scheduler.scheduleAt(300)
    scheduler.scheduleAt(100)

    expect(clock.clearTimeoutSpy).toHaveBeenCalledTimes(1)
    expect(clock.setTimeoutSpy).toHaveBeenCalledTimes(2)

    clock.advanceTo(100)

    expect(onDeadline).toHaveBeenCalledTimes(1)
  })

  it('cancels pending work without dispatching it', () => {
    const clock = createFakeClock()
    const onDeadline = vi.fn()
    const scheduler = createDeadlineScheduler(onDeadline, clock)

    scheduler.scheduleAt(100)
    scheduler.cancel()
    clock.advanceTo(100)

    expect(clock.clearTimeoutSpy).toHaveBeenCalledTimes(1)
    expect(onDeadline).not.toHaveBeenCalled()
  })
})
