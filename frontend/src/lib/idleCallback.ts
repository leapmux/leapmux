import { monotonicNow } from './monotonicNow'
import { WORK_SLICE_MS } from './workSlices'

/**
 * `requestIdleCallback` with a `setTimeout` fallback for environments that lack it
 * (older Safari, jsdom). Returns an opaque handle for {@link cancelIdle}.
 *
 * Used to defer non-interactive post-mount chrome (e.g. injecting copy buttons into a
 * chat row's code blocks) off the synchronous mount path: a row that flings past
 * unmounts and cancels its handle before the callback fires, so the work is skipped
 * entirely for rows that scroll by and runs only for rows that settle visible.
 */
/**
 * True only when BOTH `requestIdleCallback` and `cancelIdleCallback` exist, so
 * {@link requestIdle} and {@link cancelIdle} always pick the SAME mechanism. A
 * partial polyfill (one present, the other absent) would otherwise let a handle
 * scheduled by `requestIdleCallback` be "cancelled" by `clearTimeout` -- a silent
 * no-op that leaks the deferred work past the row's unmount. Checked per-call (not
 * cached) so a test stubbing the globals after import is still honored.
 */
function hasIdleCallback(): boolean {
  return typeof requestIdleCallback === 'function' && typeof cancelIdleCallback === 'function'
}

/**
 * The idle time that the fallback grants its callback, in milliseconds. The
 * fallback cannot know whether the browser is idle, so it grants one work slice,
 * not the 50 ms that a real idle period can hold.
 */
export const FALLBACK_IDLE_BUDGET_MS = WORK_SLICE_MS

/** A deadline that runs out FALLBACK_IDLE_BUDGET_MS after now. */
function fallbackDeadline(): IdleDeadline {
  const end = monotonicNow() + FALLBACK_IDLE_BUDGET_MS
  return {
    didTimeout: false,
    timeRemaining: () => Math.max(0, end - monotonicNow()),
  }
}

/**
 * `options.timeout` caps how long the callback waits for an idle window, for a
 * caller whose work must happen even on a page that never goes idle. The
 * fallback ignores it, because a timer already runs the callback immediately.
 *
 * The callback receives the deadline of its idle period. A caller that splits
 * its work reads `deadline.timeRemaining()` between steps.
 */
export function requestIdle(callback: (deadline: IdleDeadline) => void, options?: IdleRequestOptions): number {
  // `options` is forwarded only when a caller gave one, so the ordinary call
  // stays a one-argument call rather than passing an explicit `undefined`.
  if (hasIdleCallback()) {
    return options === undefined
      ? requestIdleCallback(callback)
      : requestIdleCallback(callback, options)
  }
  return setTimeout(() => callback(fallbackDeadline()), 1) as unknown as number
}

/** Cancel a callback scheduled by {@link requestIdle}. */
export function cancelIdle(handle: number): void {
  if (hasIdleCallback())
    cancelIdleCallback(handle)
  else
    clearTimeout(handle)
}
