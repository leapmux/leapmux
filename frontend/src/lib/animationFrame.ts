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

/**
 * Schedule a layout read for the next frame. Without the browser frame APIs, defer it
 * with a timer. The returned function cancels the same scheduler that placed the read.
 */
export function scheduleDeferredFrame(callback: () => void): () => void {
  const request = globalThis.requestAnimationFrame
  const cancel = globalThis.cancelAnimationFrame
  if (typeof request === 'function' && typeof cancel === 'function') {
    const handle = request.call(globalThis, () => callback())
    return () => cancel.call(globalThis, handle)
  }
  const timer = setTimeout(callback, 0)
  return () => clearTimeout(timer)
}
