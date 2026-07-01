/**
 * High-res monotonic clock: `performance.now()`, falling back to `Date.now()` where
 * `performance` is absent or incomplete (SSR / some test envs). Never a constant, so
 * interval math built on it can't divide by a frozen clock.
 *
 * The single home for the fallback-clock idiom — the chat scroll/measure pipeline
 * (`chatScrollGeometry` and friends), the rAF-batched ResizeObserver's setTimeout
 * frame fallback, and the deadline scheduler's default clock all read this one
 * definition instead of hand-rolling near-identical ternaries.
 */
export function monotonicNow(): number {
  return typeof performance !== 'undefined' && typeof performance.now === 'function'
    ? performance.now()
    : Date.now()
}
