import { monotonicNow } from './monotonicNow'

/**
 * The longest stretch of main-thread work, in milliseconds, between two yields to the
 * event loop: half a frame at 60 Hz, so that input and paint still run in time.
 */
export const WORK_SLICE_MS = 8

/**
 * One run of main-thread work that splits itself into slices.
 *
 * The work checks `sliceUsed` between two of its steps, and awaits `nextSlice` when
 * the check is true. A step must take much less time than a slice.
 */
export interface WorkSlicer {
  /** True when the current slice has run for its full length. */
  sliceUsed: () => boolean
  /** Yield to the event loop, then start the next slice. */
  nextSlice: () => Promise<void>
}

/** Start a run of work whose first slice starts now. */
export function createWorkSlicer(sliceMs = WORK_SLICE_MS, now: () => number = monotonicNow): WorkSlicer {
  let sliceStart = now()
  return {
    sliceUsed: () => now() - sliceStart >= sliceMs,
    async nextSlice() {
      await yieldToEventLoop()
      sliceStart = now()
    },
  }
}

interface TaskScheduler {
  yield?: () => Promise<void>
}

/**
 * Resolve in a later task, so that the browser handles input and paints in between.
 *
 * `scheduler.yield` resumes ahead of other queued tasks where the browser has it.
 * Elsewhere a zero-delay timer resumes behind them.
 */
export function yieldToEventLoop(): Promise<void> {
  const scheduler = (globalThis as { scheduler?: TaskScheduler }).scheduler
  if (typeof scheduler?.yield === 'function')
    return scheduler.yield()
  return new Promise(resolve => setTimeout(resolve, 0))
}
