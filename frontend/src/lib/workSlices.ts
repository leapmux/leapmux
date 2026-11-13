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
 * Elsewhere a message on a fresh `MessageChannel` resumes in a task of its own.
 *
 * A zero-delay timer cannot take the place of the message. The browser clamps a timer
 * that nests five or more levels deep to at least 4 ms, and a run of slices nests one
 * timer in the next. A hidden page fares worse: the browser runs its timers about once
 * each second at most, so the hash of a 10 MiB attachment, 21 slices long, takes 21
 * seconds or more.
 */
export function yieldToEventLoop(): Promise<void> {
  const scheduler = (globalThis as { scheduler?: TaskScheduler }).scheduler
  if (typeof scheduler?.yield === 'function')
    return scheduler.yield()
  return new Promise((resolve) => {
    const { port1, port2 } = new MessageChannel()
    port1.onmessage = () => {
      // A port with a listener keeps a Node process alive until it closes.
      port1.close()
      resolve()
    }
    port2.postMessage(null)
  })
}
