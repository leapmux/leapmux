/**
 * Coalesce high-frequency events into one handler dispatch per animation
 * frame. Useful for pointermove handlers on 120Hz devices where the raw
 * event rate produces redundant reactivity work.
 *
 * Semantics:
 * - `push(e)`: replaces any prior pending event for the next frame; if no
 *   frame is already scheduled, schedules one.
 * - `flush()`: cancels the pending frame and dispatches the latest event
 *   synchronously. Used at finalize time so commit sees the final event.
 *   No-op if no event is pending.
 * - `abort()`: cancels the pending frame and discards the event without
 *   dispatching. Used on hard-abort paths where committing stale state
 *   would be wrong.
 */
export interface RafCoalescer<E> {
  push: (e: E) => void
  flush: () => void
  abort: () => void
}

export function createRafCoalescer<E>(handler: (e: E) => void): RafCoalescer<E> {
  let pending: E | null = null
  let rafId: number | null = null

  const dispatch = (): void => {
    rafId = null
    if (pending !== null) {
      const e = pending
      pending = null
      handler(e)
    }
  }

  return {
    push: (e) => {
      pending = e
      if (rafId === null)
        rafId = requestAnimationFrame(dispatch)
    },
    flush: () => {
      if (rafId !== null) {
        cancelAnimationFrame(rafId)
        dispatch()
      }
    },
    abort: () => {
      if (rafId !== null) {
        cancelAnimationFrame(rafId)
        rafId = null
      }
      pending = null
    },
  }
}
