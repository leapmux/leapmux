import { cancelFrame as defaultCancelFrame, requestFrame as defaultRequestFrame } from '~/lib/animationFrame'

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
 * - `isPending()`: whether a frame is scheduled and holds an event to dispatch.
 *
 * The coalescer clears its pending frame before it calls the handler. So a frame that
 * runs synchronously inside `requestFrame` leaves nothing pending, and the next `push`
 * schedules again. A push from inside the handler also schedules a new frame.
 *
 * A frame that runs after an `abort` or a `flush` cancelled it dispatches nothing, even
 * when a later `push` scheduled a new frame. So a `cancelFrame` that cannot stop a frame
 * does no harm.
 */
export interface RafCoalescer<E> {
  push: (e: E) => void
  flush: () => void
  abort: () => void
  isPending: () => boolean
}

/** The frame functions of a coalescer. */
export interface RafCoalescerFrames {
  /**
   * Run the callback in a later frame and return a handle for `cancelFrame`.
   * Default: `requestFrame` of `~/lib/animationFrame`, which calls the callback at once
   * where `requestAnimationFrame` is absent.
   */
  requestFrame?: ((cb: () => void) => number) | undefined
  /** Cancel a frame that `requestFrame` requested. Default: `cancelFrame` of `~/lib/animationFrame`. */
  cancelFrame?: ((handle: number) => void) | undefined
}

/** A scheduled frame and the latest event that it dispatches. */
interface PendingFrame<E> {
  handle: number
  event: E
}

export function createRafCoalescer<E>(handler: (e: E) => void, frames: RafCoalescerFrames = {}): RafCoalescer<E> {
  const requestFrame = frames.requestFrame ?? defaultRequestFrame
  const cancelFrame = frames.cancelFrame ?? defaultCancelFrame
  // An object, not a bare handle: a frame that runs synchronously inside `requestFrame`
  // clears it before `requestFrame` returns, and its callback can tell whether it is still
  // the pending frame.
  let pending: PendingFrame<E> | undefined

  /** Cancel the pending frame, and return it. */
  const cancelPending = (): PendingFrame<E> | undefined => {
    const frame = pending
    if (frame !== undefined) {
      cancelFrame(frame.handle)
      pending = undefined
    }
    return frame
  }

  return {
    push: (e) => {
      if (pending !== undefined) {
        pending.event = e
        return
      }
      const frame: PendingFrame<E> = { handle: 0, event: e }
      pending = frame
      frame.handle = requestFrame(() => {
        if (pending !== frame)
          return
        pending = undefined
        handler(frame.event)
      })
    },
    flush: () => {
      const frame = cancelPending()
      if (frame !== undefined)
        handler(frame.event)
    },
    abort: () => {
      cancelPending()
    },
    isPending: () => pending !== undefined,
  }
}
