/**
 * Run `cb` in the next animation frame: `requestAnimationFrame`, or a synchronous call
 * where it is absent (a test environment with no DOM). Returns the handle for
 * `cancelFrame`, and 0 after a synchronous call. `cb` receives no frame timestamp.
 */
export function requestFrame(cb: () => void): number {
  if (typeof requestAnimationFrame !== 'function') {
    cb()
    return 0
  }
  return requestAnimationFrame(() => cb())
}

/**
 * Cancel a frame that `requestFrame` requested. Does nothing where
 * `cancelAnimationFrame` is absent: there, `requestFrame` ran its callback at once.
 */
export function cancelFrame(handle: number): void {
  if (typeof cancelAnimationFrame === 'function')
    cancelAnimationFrame(handle)
}
