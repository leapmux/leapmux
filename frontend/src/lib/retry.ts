/**
 * Per-key exponential-backoff scheduler.
 *
 * Pattern: each key (a tab id, a worker id, …) tracks at most one
 * pending timer plus its last-used delay. On failure, the caller asks
 * `schedule(key, fire)` to arm the next retry; on success, the caller
 * calls `reset(key)` so the next failure restarts at `initialMs`.
 *
 * Delay sequence (per key): `initialMs`, `initialMs × multiplier`,
 * `initialMs × multiplier²`, …, clamped at `maxMs`. Reset returns the
 * sequence to the start.
 *
 * Cancellation: SolidJS callers must call `cancelAll()` from
 * `onCleanup` so timers don't outlive the component / hook. There is
 * no per-key cleanup token — `reset(key)` is the per-key cancel.
 *
 * Giving up: `maxAttempts` caps the retries per key. It is opt-in because some
 * sites genuinely are "keep trying until something else changes" loops (the
 * candidate set shrinks, the user navigates away, the parent unmounts). But a
 * site whose work can never succeed — an RPC to a worker that has been
 * deregistered — has nothing else to change, and without a cap it holds a timer
 * per key for the life of the page. Those sites pass one.
 *
 * This helper deliberately does NOT wrap the operation being retried. Callers
 * own the `fire` callback (and any in-flight guard / async cancellation around
 * it); mixing the two concerns leaks SolidJS reactivity assumptions into the
 * helper.
 */
export interface ExponentialBackoffOpts {
  /** First scheduled delay, in ms (before jitter). */
  initialMs: number
  /** Upper bound on the un-jittered base delay. */
  maxMs: number
  /** Per-retry growth factor. Defaults to 2. */
  multiplier?: number
  /**
   * Symmetric jitter fraction. The actual scheduled delay is
   * `base * (1 + rand)` where `rand ∈ [-jitterFactor, +jitterFactor)`,
   * so e.g. `jitterFactor: 0.2` gives a [0.8×, 1.2×] window around the
   * base delay. Defaults to 0.2 (±20%) to match the backend's
   * `symmetricJitter` helper. Set to 0 to disable.
   *
   * The doubling sequence advances on the *base* delay, not the
   * jittered one — jitter only fuzzes the timer arm-time. This keeps
   * the maximum scheduled delay bounded near `maxMs * (1 + jitter)`
   * instead of letting jittered values compound.
   */
  jitterFactor?: number
  /**
   * Maximum number of retries per key before `schedule` gives up and returns
   * `null` without arming a timer. Unlimited when omitted.
   *
   * `maxMs` bounds how OFTEN a key retries, not how LONG: without a cap, a key
   * whose work can never succeed — an RPC to a deregistered worker, a resource
   * the caller will never be granted — keeps a timer armed for the life of the
   * page, one per key. A cap turns "retry until it works" into "retry until it
   * clearly won't", which is what every current caller actually wants.
   *
   * `reset(key)` clears the count along with the timer, so a key that succeeds
   * (or whose inputs change) starts over with a full budget.
   */
  maxAttempts?: number
}

export interface ExponentialBackoff<K> {
  /**
   * Arm a retry timer for `key`. No-op (returns `null`) if a timer is
   * already pending, or if `maxAttempts` is set and `key` has exhausted it.
   * Otherwise returns the delay that was scheduled.
   *
   * The pending-timer slot for `key` is cleared *before* `fire` runs,
   * so `fire` may re-call `schedule(key, …)` from inside itself.
   */
  schedule: (key: K, fire: () => void) => number | null
  /**
   * Whether `key` has used up its `maxAttempts` budget. Always false when no
   * cap is configured. Lets a caller distinguish "still retrying" from "gave
   * up" without racing the timer.
   */
  isExhausted: (key: K) => boolean
  /**
   * Cancel `key`'s pending timer (if any) and forget its last delay so
   * the next `schedule(key, …)` restarts at `initialMs`. Idempotent.
   */
  reset: (key: K) => void
  /** Cancel every pending timer and forget every per-key delay. */
  cancelAll: () => void
  /** Number of pending timers — test helper. */
  size: () => number
  /**
   * Inspect the un-jittered *base* delay `schedule(key, …)` would use
   * next, without arming a timer. Returns `null` when `key` already
   * has a pending timer (since the call would no-op). Used by tests
   * and by code that reasons about the backoff sequence without
   * caring about jitter; the actual arm-time is fuzzed by
   * `jitterFactor`.
   */
  peekNextDelay: (key: K) => number | null
}

export function createExponentialBackoff<K>(opts: ExponentialBackoffOpts): ExponentialBackoff<K> {
  const { initialMs, maxMs, maxAttempts } = opts
  const multiplier = opts.multiplier ?? 2
  const jitterFactor = opts.jitterFactor ?? 0.2
  if (initialMs <= 0)
    throw new Error(`createExponentialBackoff: initialMs must be > 0 (got ${initialMs})`)
  if (maxMs < initialMs)
    throw new Error(`createExponentialBackoff: maxMs (${maxMs}) must be >= initialMs (${initialMs})`)
  if (multiplier <= 1)
    throw new Error(`createExponentialBackoff: multiplier must be > 1 (got ${multiplier})`)
  if (jitterFactor < 0 || jitterFactor >= 1)
    throw new Error(`createExponentialBackoff: jitterFactor must be in [0, 1) (got ${jitterFactor})`)
  if (maxAttempts !== undefined && maxAttempts < 1)
    throw new Error(`createExponentialBackoff: maxAttempts must be >= 1 (got ${maxAttempts})`)

  const timers = new Map<K, ReturnType<typeof setTimeout>>()
  const lastBaseDelays = new Map<K, number>()
  const attempts = new Map<K, number>()

  const isExhausted = (key: K): boolean =>
    maxAttempts !== undefined && (attempts.get(key) ?? 0) >= maxAttempts

  const nextBaseDelayFor = (key: K): number => {
    const prev = lastBaseDelays.get(key)
    return prev === undefined ? initialMs : Math.min(prev * multiplier, maxMs)
  }

  // Symmetric jitter: result ∈ [base * (1 - jitterFactor), base * (1 + jitterFactor)).
  // Floored at 0 in case a caller passes a value approaching 1 — we
  // never want a negative setTimeout.
  const applyJitter = (base: number): number => {
    if (jitterFactor === 0)
      return base
    const offset = base * jitterFactor * (Math.random() * 2 - 1)
    return Math.max(0, base + offset)
  }

  const reset = (key: K): void => {
    const t = timers.get(key)
    if (t !== undefined) {
      clearTimeout(t)
      timers.delete(key)
    }
    lastBaseDelays.delete(key)
    attempts.delete(key)
  }

  const schedule = (key: K, fire: () => void): number | null => {
    if (timers.has(key))
      return null
    if (isExhausted(key))
      return null
    attempts.set(key, (attempts.get(key) ?? 0) + 1)
    const base = nextBaseDelayFor(key)
    lastBaseDelays.set(key, base)
    const delay = applyJitter(base)
    const t = setTimeout(() => {
      // Clear the slot before firing so `fire` can re-schedule from
      // inside itself without tripping the "already pending" guard.
      timers.delete(key)
      fire()
    }, delay)
    timers.set(key, t)
    return delay
  }

  return {
    schedule,
    reset,
    cancelAll() {
      for (const t of timers.values())
        clearTimeout(t)
      timers.clear()
      lastBaseDelays.clear()
      attempts.clear()
    },
    size: () => timers.size,
    peekNextDelay: key => (timers.has(key) ? null : nextBaseDelayFor(key)),
    isExhausted,
  }
}
