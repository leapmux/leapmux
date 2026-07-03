import { monotonicNow } from './monotonicNow'

export interface RafResizeObserver {
  observe: (target: Element, options?: ResizeObserverOptions) => void
  unobserve: (target: Element) => void
  disconnect: () => void
}

function requestFrame(cb: FrameRequestCallback): number {
  if (typeof requestAnimationFrame === 'function')
    return requestAnimationFrame(cb)
  return setTimeout(() => cb(monotonicNow()), 0) as unknown as number
}

function cancelFrame(id: number): void {
  if (typeof cancelAnimationFrame === 'function')
    cancelAnimationFrame(id)
  else
    clearTimeout(id)
}

/**
 * Create a ResizeObserver whose callback runs in the next animation frame.
 *
 * ResizeObserver callbacks run during the browser's resize-observation delivery
 * phase. DOM writes or reactive commits made there can resize another observed
 * element before the current delivery is complete, producing
 * "ResizeObserver loop completed with undelivered notifications." Deferring
 * non-critical resize work to rAF breaks that feedback loop while still applying
 * the latest measurement before the following paint.
 */
export function createRafResizeObserver(
  callback: ResizeObserverCallback,
): RafResizeObserver | undefined {
  if (typeof ResizeObserver === 'undefined')
    return undefined

  const pendingEntries = new Map<Element, ResizeObserverEntry>()
  let frameId: number | null = null
  let scheduled = false
  let disconnected = false
  let observer: ResizeObserver

  const flush = () => {
    frameId = null
    scheduled = false
    if (disconnected || pendingEntries.size === 0)
      return

    const entries = [...pendingEntries.values()]
    pendingEntries.clear()
    callback(entries, observer)
  }

  const scheduleFlush = () => {
    if (scheduled)
      return
    scheduled = true

    let firedSynchronously = false
    const id = requestFrame(() => {
      firedSynchronously = true
      flush()
    })

    // Some tests intentionally stub rAF as synchronous. Avoid recording a stale
    // handle after `flush` already ran.
    if (scheduled && !firedSynchronously)
      frameId = id
  }

  const cancelPendingFrame = () => {
    if (!scheduled)
      return
    scheduled = false
    if (frameId !== null)
      cancelFrame(frameId)
    frameId = null
  }

  observer = new ResizeObserver((entries) => {
    if (disconnected)
      return
    for (const entry of entries)
      pendingEntries.set(entry.target, entry)
    scheduleFlush()
  })

  return {
    observe: (target, options) => observer.observe(target, options),
    unobserve: (target) => {
      observer.unobserve(target)
      pendingEntries.delete(target)
    },
    disconnect: () => {
      disconnected = true
      observer.disconnect()
      pendingEntries.clear()
      cancelPendingFrame()
    },
  }
}
