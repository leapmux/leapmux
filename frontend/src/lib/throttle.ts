/** A leading-edge-throttled function with a cancel control. */
export type LeadingThrottled = (() => void) & {
  cancel: () => void
}

/**
 * Leading-edge throttle with a trailing edge. The first call runs `fn`
 * IMMEDIATELY and opens a window of `ms`. Every call inside that window shares
 * one more invocation at its end, which then opens the next window.
 *
 * This is the opposite end from {@link trailingDebounce} in `~/lib/debounce.ts`,
 * and the two do not substitute for each other. A debounce delays the first
 * call, which suits an input that keeps arriving while the user types. A
 * throttle answers the first call at once, which suits a burst whose FIRST
 * member the user waits for and whose remainder only repeats the same work.
 *
 * `cancel` drops a pending trailing call and closes the open window. `fn` may
 * call it to say "this invocation did nothing", which lets the next call lead
 * again instead of waiting out a window that bought nothing.
 *
 * There is no `flush`. A caller that cancels does so because its owner goes
 * away, and firing `fn` into a torn-down owner is the failure the cancel
 * exists to prevent.
 */
export function leadingThrottle(fn: () => void, ms: number): LeadingThrottled {
  let timer: ReturnType<typeof setTimeout> | null = null
  let pending = false

  const fire = () => {
    pending = false
    // The window opens BEFORE `fn` runs, for two reasons. A call `fn` makes
    // back into the throttle then coalesces instead of running a second
    // invocation inside the same window. And `fn` can call `cancel()` to close
    // the window it is in -- for a caller whose invocation turned out to have
    // nothing to do, so the next call leads again rather than waiting out a
    // window that spent nothing.
    timer = setTimeout(() => {
      timer = null
      if (pending)
        fire()
    }, ms)
    fn()
  }

  const throttled = () => {
    if (timer !== null) {
      pending = true
      return
    }
    fire()
  }
  throttled.cancel = () => {
    if (timer !== null)
      clearTimeout(timer)
    timer = null
    pending = false
  }
  return throttled
}
