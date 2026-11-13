import { cancelIdle, requestIdle } from './idleCallback'
import { monotonicNow } from './monotonicNow'
import { WORK_SLICE_MS } from './workSlices'

/**
 * The budget of the slice that runs now. Work that runs in slices checks
 * `used` after each step, and stops when it is true. A step must take much less
 * time than a slice (see WORK_SLICE_MS).
 */
export interface SliceBudget {
  /**
   * Count `ms` of work that the slice leaves to the browser, such as the style
   * and the layout of the elements that it inserted. The browser does that work
   * later in the same task, or in the next frame, so it counts against the slice.
   */
  defer: (ms: number) => void
  /** Whether the slice has used its budget: the time that it ran, plus the work that it deferred. */
  used: () => boolean
}

/**
 * Where work that splits itself into slices runs: its first slice in the task
 * that starts the work, and each later slice in a task of its own.
 */
export interface SliceScheduler {
  /**
   * The budget of a slice that runs now, in the caller's task. Every caller in
   * one synchronous run of the task shares one slice, so a task that starts
   * several runs of work, such as the two copies of a chat row, still runs one
   * slice in total.
   */
  startSlice: () => SliceBudget
  /**
   * Run `slice` in a later task, never in the caller's task. While it runs, its
   * budget is the one that `startSlice` returns, so work that it starts shares
   * the slice. Returns a function that cancels it. The function does nothing
   * once the slice has run.
   */
  requestSlice: (slice: (budget: SliceBudget) => void) => () => void
}

/**
 * How long a requested slice waits for an idle period, in milliseconds. On a
 * page that never goes idle, the browser runs the slice then, as an ordinary
 * task, so the work still advances.
 */
export const IDLE_SLICE_TIMEOUT_MS = 100

/**
 * A budget of one work slice from now. With an idle deadline, it also ends
 * where the idle period ends.
 */
function workSliceBudget(deadline?: IdleDeadline): SliceBudget {
  const start = monotonicNow()
  let deferred = 0
  return {
    defer(ms) {
      deferred += ms
    },
    used: () => monotonicNow() - start + deferred >= WORK_SLICE_MS
      || (deadline !== undefined && deadline.timeRemaining() - deferred <= 0),
  }
}

/** The slice that the current task runs, which every caller of `startSlice` in it shares. */
let taskSlice: SliceBudget | undefined

/**
 * The scheduler of the app. Each slice runs for one work slice (WORK_SLICE_MS)
 * at most, counting the work that it defers. A requested slice runs in an idle
 * period of the browser, after the frame and the input that waits. Input that
 * arrives during the slice waits for at most one slice and the work that the
 * slice leaves to the browser, such as a layout. The slice stops where its idle
 * period ends, apart from that work, which it counts only by an estimate (see
 * SliceBudget.defer). A slice that the timeout starts gets one work slice: its
 * idle deadline has no time left, but it runs as an ordinary task.
 */
export const idleSliceScheduler: SliceScheduler = {
  startSlice() {
    if (taskSlice === undefined) {
      taskSlice = workSliceBudget()
      // The slice ends with the synchronous run that started it. A later run,
      // even a later microtask of the same task, starts a slice of its own.
      queueMicrotask(() => {
        taskSlice = undefined
      })
    }
    return taskSlice
  },
  requestSlice(slice) {
    const handle = requestIdle((deadline) => {
      const budget = workSliceBudget(deadline.didTimeout
        ? undefined
        : deadline)
      // Work that the slice starts, such as a gap separator that it mounts,
      // shares this budget through `startSlice`. A budget of its own would let
      // one task run two slices of work before the browser can paint.
      const outer = taskSlice
      taskSlice = budget
      try {
        slice(budget)
      }
      finally {
        taskSlice = outer
      }
    }, { timeout: IDLE_SLICE_TIMEOUT_MS })
    return () => cancelIdle(handle)
  },
}

let activeScheduler: SliceScheduler = idleSliceScheduler

/** The scheduler that sliced work reads when it starts. */
export function sliceScheduler(): SliceScheduler {
  return activeScheduler
}

/**
 * Replace the scheduler for the work that starts after this call. Without an
 * argument, restore the scheduler of the app. `vitest.setup.ts` installs a
 * scheduler without a budget before each test (see `~/test-support/sliceSchedulers`).
 */
export function setSliceSchedulerForTests(scheduler: SliceScheduler = idleSliceScheduler): void {
  activeScheduler = scheduler
}
