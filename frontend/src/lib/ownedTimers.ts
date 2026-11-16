import { onCleanup } from 'solid-js'

/** Timers that end when the reactive owner that holds them is disposed. */
export interface OwnedTimers {
  /**
   * Run `callback` once, after `delayMs`. Returns the function that cancels this timer.
   * A cancel after the timer ran, or after `clear`, does nothing.
   *
   * After the owner is disposed, `after` starts no timer, and `callback` never runs: its
   * owner no longer exists.
   */
  after: (delayMs: number, callback: () => void) => () => void
  /** Cancel each timer that has not run yet. A later call of `after` starts a timer again. */
  clear: () => void
}

/**
 * Create a set of timers that belongs to the reactive owner of the caller. The cleanup of
 * that owner cancels each timer that has not run, so no timer runs its callback for an
 * owner that is gone.
 *
 * Call it within a reactive owner, such as a component or a root, while the owner sets up.
 */
export function createOwnedTimers(): OwnedTimers {
  const timers = new Set<ReturnType<typeof setTimeout>>()
  let disposed = false
  const clear = (): void => {
    for (const timer of timers)
      clearTimeout(timer)
    timers.clear()
  }
  onCleanup(() => {
    disposed = true
    clear()
  })
  return {
    after(delayMs, callback) {
      if (disposed)
        return () => {}
      const timer = setTimeout(() => {
        timers.delete(timer)
        callback()
      }, delayMs)
      timers.add(timer)
      return () => {
        clearTimeout(timer)
        timers.delete(timer)
      }
    },
    clear,
  }
}
