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
 * `options.timeout` caps how long the callback waits for an idle window, for a
 * caller whose work must happen even on a page that never goes idle. The
 * fallback ignores it, because a timer already runs the callback immediately.
 */
export function requestIdle(callback: () => void, options?: IdleRequestOptions): number {
  // `options` is forwarded only when a caller gave one, so the ordinary call
  // stays a one-argument call rather than passing an explicit `undefined`.
  if (hasIdleCallback())
    return options === undefined ? requestIdleCallback(callback) : requestIdleCallback(callback, options)
  return setTimeout(callback, 1) as unknown as number
}

/** Cancel a callback scheduled by {@link requestIdle}. */
export function cancelIdle(handle: number): void {
  if (hasIdleCallback())
    cancelIdleCallback(handle)
  else
    clearTimeout(handle)
}
