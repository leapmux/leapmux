import { vi } from 'vitest'

/**
 * A manual animation frame queue. A frame runs only when the test calls `runFrames`.
 *
 * `requestFrame` and `cancelFrame` are spies with the signatures of
 * `requestAnimationFrame` and `cancelAnimationFrame`. Pass them to a unit that takes them
 * as dependencies, or install them with `vi.stubGlobal` (and `vi.unstubAllGlobals` after
 * the test).
 */
export function createManualFrames() {
  const frames = new Map<number, FrameRequestCallback>()
  let nextHandle = 1
  const requestFrame = vi.fn((cb: FrameRequestCallback): number => {
    const handle = nextHandle++
    frames.set(handle, cb)
    return handle
  })
  const cancelFrame = vi.fn((handle: number): void => {
    frames.delete(handle)
  })
  return {
    requestFrame,
    cancelFrame,
    /** Run the frames requested so far. A frame that one of them requests waits for the next call. */
    runFrames: (): void => {
      const pending = [
        ...frames.values(),
      ]
      frames.clear()
      for (const cb of pending)
        cb(performance.now())
    },
    /** The number of frames requested and not yet run or cancelled. */
    pendingFrames: (): number => frames.size,
  }
}
