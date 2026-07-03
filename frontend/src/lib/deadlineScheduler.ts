import { monotonicNow } from './monotonicNow'

type TimeoutHandle = ReturnType<typeof setTimeout>

export interface DeadlineSchedulerClock {
  now: () => number
  setTimeout: (handler: () => void, delayMs: number) => TimeoutHandle
  clearTimeout: (handle: TimeoutHandle) => void
}

export interface DeadlineScheduler {
  scheduleAt: (deadlineMs: number) => void
  cancel: () => void
}

const DEFAULT_CLOCK: DeadlineSchedulerClock = {
  now: monotonicNow,
  setTimeout: (handler, delayMs) => setTimeout(handler, delayMs),
  clearTimeout: handle => clearTimeout(handle),
}

const TIMER_EPSILON_MS = 0.5

/**
 * Runs work no earlier than the latest requested deadline without recreating the
 * browser timer every time that deadline is pushed later. If the existing timer
 * wakes up before the latest deadline, it simply re-arms itself for the remainder.
 */
export function createDeadlineScheduler(
  onDeadline: () => void,
  clock: DeadlineSchedulerClock = DEFAULT_CLOCK,
): DeadlineScheduler {
  let deadlineMs: number | undefined
  let timer: TimeoutHandle | undefined
  let timerDueMs: number | undefined

  function armTimer(targetMs: number) {
    if (timer !== undefined && timerDueMs !== undefined && timerDueMs <= targetMs + TIMER_EPSILON_MS)
      return

    if (timer !== undefined)
      clock.clearTimeout(timer)

    timerDueMs = targetMs
    timer = clock.setTimeout(onTimer, Math.max(0, targetMs - clock.now()))
  }

  function onTimer() {
    timer = undefined
    timerDueMs = undefined

    const deadline = deadlineMs
    if (deadline === undefined)
      return

    if (clock.now() + TIMER_EPSILON_MS < deadline) {
      armTimer(deadline)
      return
    }

    deadlineMs = undefined
    onDeadline()
  }

  return {
    scheduleAt: (nextDeadlineMs) => {
      deadlineMs = nextDeadlineMs
      armTimer(nextDeadlineMs)
    },
    cancel: () => {
      deadlineMs = undefined
      timerDueMs = undefined
      if (timer !== undefined) {
        clock.clearTimeout(timer)
        timer = undefined
      }
    },
  }
}
