/** A trailing-edge-debounced function with cancel/flush controls. */
export type TrailingDebounced = (() => void) & {
  cancel: () => void
  flush: () => void
}

/**
 * Trailing-edge debounce. Coalesces rapid-fire calls into a single
 * trailing invocation after `ms` of quiet. Use `.cancel()` to drop a
 * pending invocation (e.g. on dispose) and `.flush()` to fire it now
 * (e.g. on unload).
 */
export function trailingDebounce(fn: () => void, ms: number): TrailingDebounced {
  let timer: ReturnType<typeof setTimeout> | null = null
  const debounced = () => {
    if (timer !== null)
      clearTimeout(timer)
    timer = setTimeout(() => {
      timer = null
      fn()
    }, ms)
  }
  debounced.cancel = () => {
    if (timer !== null) {
      clearTimeout(timer)
      timer = null
    }
  }
  debounced.flush = () => {
    if (timer !== null) {
      clearTimeout(timer)
      timer = null
      fn()
    }
  }
  return debounced
}
