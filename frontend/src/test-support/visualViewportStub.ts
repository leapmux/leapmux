/**
 * Minimal jsdom test double for `window.visualViewport` with listener tracking
 * and event dispatch for `resize` and `scroll`.
 */
export interface MockVisualViewport {
  width: number
  height: number
  offsetTop: number
  offsetLeft: number
  pageTop: number
  scale: number
  addEventListener: (type: string, fn: (ev: Event) => void) => void
  removeEventListener: (type: string, fn: (ev: Event) => void) => void
  dispatch: (type: 'resize' | 'scroll') => void
  listenerCount: (type: 'resize' | 'scroll') => number
}

export function createMockVisualViewport(
  height: number,
  offsetTop = 0,
  scale = 1,
): MockVisualViewport {
  const listeners = new Map<string, Set<(ev: Event) => void>>()
  return {
    width: 390,
    height,
    offsetTop,
    offsetLeft: 0,
    pageTop: 0,
    scale,
    addEventListener(type, fn) {
      let set = listeners.get(type)
      if (!set) {
        set = new Set()
        listeners.set(type, set)
      }
      set.add(fn)
    },
    removeEventListener(type, fn) {
      listeners.get(type)?.delete(fn)
    },
    dispatch(type) {
      const set = listeners.get(type)
      if (!set)
        return
      const ev = new Event(type)
      for (const fn of [
        ...set,
      ]) {
        fn(ev)
      }
    },
    listenerCount(type) {
      return listeners.get(type)?.size ?? 0
    },
  }
}
