/**
 * @vitest-environment jsdom
 */
import { renderHook } from '@solidjs/testing-library'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useVisualViewportInset } from './useVisualViewportInset'

interface MockVisualViewport {
  height: number
  width: number
  offsetTop: number
  pageTop: number
  listeners: Map<string, Set<(ev: Event) => void>>
  addEventListener: (type: string, fn: (ev: Event) => void) => void
  removeEventListener: (type: string, fn: (ev: Event) => void) => void
  dispatchResize: () => void
  dispatchScroll: () => void
}

function makeMockVisualViewport(height: number, offsetTop = 0): MockVisualViewport {
  const listeners = new Map<string, Set<(ev: Event) => void>>()
  return {
    height,
    width: 390,
    offsetTop,
    pageTop: 0,
    listeners,
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
    dispatchResize() {
      const set = listeners.get('resize')
      if (!set)
        return
      const ev = new Event('resize')
      for (const fn of set) fn(ev)
    },
    dispatchScroll() {
      const set = listeners.get('scroll')
      if (!set)
        return
      const ev = new Event('scroll')
      for (const fn of set) fn(ev)
    },
  }
}

// Microtask-deferred rAF stub. A truly-synchronous rAF (run the cb
// inline before returning) misorders the hook's `rafId = rAF(apply)`
// assignment: apply() sets `rafId = null` at its start, then the outer
// assignment writes the returned ID *after*, leaving rafId non-null and
// jamming subsequent schedule() calls. Real browsers return the ID
// before invoking the callback. Mirroring that ordering by deferring
// the callback to a microtask keeps the hook's guard semantics correct
// in tests; tests just `await flush()` to drain.
function installSyncRaf() {
  const original = window.requestAnimationFrame
  const originalCancel = window.cancelAnimationFrame
  let id = 0
  const cancelled = new Set<number>()
  window.requestAnimationFrame = ((cb: FrameRequestCallback) => {
    const thisId = ++id
    queueMicrotask(() => {
      if (cancelled.has(thisId))
        return
      cb(0)
    })
    return thisId
  }) as typeof window.requestAnimationFrame
  window.cancelAnimationFrame = ((rafId: number) => {
    cancelled.add(rafId)
  }) as typeof window.cancelAnimationFrame
  return () => {
    window.requestAnimationFrame = original
    window.cancelAnimationFrame = originalCancel
  }
}

// Flush enough microtask turns to drain the focusout's nested
// queueMicrotask → schedule → rAF-as-microtask chain. Two turns suffice.
async function flush() {
  await Promise.resolve()
  await Promise.resolve()
}

// Helper: create an input, attach to body, focus it, dispatch focusin.
function focusInput(): HTMLInputElement {
  const input = document.createElement('input')
  document.body.appendChild(input)
  input.focus()
  input.dispatchEvent(new FocusEvent('focusin', { bubbles: true }))
  return input
}

describe('useVisualViewportInset', () => {
  let restoreRaf: () => void

  beforeEach(() => {
    restoreRaf = installSyncRaf()
  })

  afterEach(() => {
    restoreRaf()
    document.documentElement.style.removeProperty('--vvh')
    document.documentElement.style.removeProperty('--vv-offset')
    document.body.innerHTML = ''
    delete (window as unknown as { visualViewport?: unknown }).visualViewport
  })

  it('publishes --vv-offset when visualViewport.offsetTop > 0', () => {
    Object.defineProperty(window, 'innerHeight', { value: 800, configurable: true })
    // iOS-26 post-keyboard-dismiss: visual viewport is back to full
    // height but stays translated up by ~120px until the next layout.
    const vv = makeMockVisualViewport(800, 120)
    Object.defineProperty(window, 'visualViewport', { value: vv, configurable: true })

    const { cleanup } = renderHook(() => useVisualViewportInset())
    try {
      // Initial sync apply() inside onMount publishes immediately.
      expect(document.documentElement.style.getPropertyValue('--vv-offset')).toBe('120px')
    }
    finally {
      cleanup()
    }
  })

  it('clears --vv-offset when offsetTop returns to 0', async () => {
    Object.defineProperty(window, 'innerHeight', { value: 800, configurable: true })
    const vv = makeMockVisualViewport(800, 120)
    Object.defineProperty(window, 'visualViewport', { value: vv, configurable: true })

    const { cleanup } = renderHook(() => useVisualViewportInset())
    try {
      expect(document.documentElement.style.getPropertyValue('--vv-offset')).toBe('120px')

      vv.offsetTop = 0
      vv.dispatchScroll()
      await flush()

      expect(document.documentElement.style.getPropertyValue('--vv-offset')).toBe('')
    }
    finally {
      cleanup()
    }
  })

  it('ignores sub-pixel offsetTop jitter', () => {
    Object.defineProperty(window, 'innerHeight', { value: 800, configurable: true })
    const vv = makeMockVisualViewport(800, 0.333)
    Object.defineProperty(window, 'visualViewport', { value: vv, configurable: true })

    const { cleanup } = renderHook(() => useVisualViewportInset())
    try {
      // Below the 0.5px threshold — not published.
      expect(document.documentElement.style.getPropertyValue('--vv-offset')).toBe('')
    }
    finally {
      cleanup()
    }
  })

  it('skips --vv-offset while an editable is focused (iOS handles kbd-up translate)', async () => {
    Object.defineProperty(window, 'innerHeight', { value: 800, configurable: true })
    // Initial: no focus, offsetTop = 60 → published.
    const vv = makeMockVisualViewport(800, 60)
    Object.defineProperty(window, 'visualViewport', { value: vv, configurable: true })

    const { cleanup } = renderHook(() => useVisualViewportInset())
    try {
      expect(document.documentElement.style.getPropertyValue('--vv-offset')).toBe('60px')

      // Focus an editable (simulates user tapping the composer; iOS
      // brings up the keyboard and translates the visual viewport).
      focusInput()
      await flush()

      // While focused, the hook deliberately suppresses `--vv-offset`
      // so body's counter-translate doesn't double-shift on top of
      // iOS's own translate.
      expect(document.documentElement.style.getPropertyValue('--vv-offset')).toBe('')
    }
    finally {
      cleanup()
    }
  })

  it('publishes --vvh only while an editable is focused', async () => {
    Object.defineProperty(window, 'innerHeight', { value: 800, configurable: true })
    const vv = makeMockVisualViewport(380)
    Object.defineProperty(window, 'visualViewport', { value: vv, configurable: true })

    const { cleanup } = renderHook(() => useVisualViewportInset())
    try {
      // No focus yet → unset.
      expect(document.documentElement.style.getPropertyValue('--vvh')).toBe('')

      const input = focusInput()
      // focusin handler schedules via rAF (deferred to microtask).
      await flush()
      expect(document.documentElement.style.getPropertyValue('--vvh')).toBe('380px')

      // Blur the editable. jsdom's blur() synchronously dispatches a
      // bubbling focusout and updates document.activeElement to the
      // body. The hook's focusout handler defers the editableFocused
      // flip to a microtask so a Tab-key transition between two
      // editables doesn't flicker.
      input.blur()
      await flush()
      expect(document.documentElement.style.getPropertyValue('--vvh')).toBe('')
    }
    finally {
      cleanup()
    }
  })

  it('does not override --vvh when no visualViewport is available', async () => {
    delete (window as unknown as { visualViewport?: unknown }).visualViewport
    Object.defineProperty(window, 'innerHeight', { value: 800, configurable: true })

    const { cleanup } = renderHook(() => useVisualViewportInset())
    try {
      expect(document.documentElement.style.getPropertyValue('--vvh')).toBe('')
      expect(document.documentElement.style.getPropertyValue('--vv-offset')).toBe('')

      Object.defineProperty(window, 'innerHeight', { value: 500, configurable: true })
      window.dispatchEvent(new Event('resize'))
      await flush()

      // Still unset (no editable focused, no visualViewport).
      expect(document.documentElement.style.getPropertyValue('--vvh')).toBe('')
      expect(document.documentElement.style.getPropertyValue('--vv-offset')).toBe('')
    }
    finally {
      cleanup()
    }
  })

  it('removes listeners and clears both custom properties on cleanup', async () => {
    Object.defineProperty(window, 'innerHeight', { value: 800, configurable: true })
    // No editable focused → offsetTop is published so cleanup has
    // something to clear. Keyboard-up + offsetTop together is now a
    // skipped combo (iOS already translates the visual viewport then;
    // see the "skips --vv-offset while editable is focused" test).
    const vv = makeMockVisualViewport(800, 60)
    Object.defineProperty(window, 'visualViewport', { value: vv, configurable: true })
    const removeSpy = vi.spyOn(vv, 'removeEventListener')

    const { cleanup } = renderHook(() => useVisualViewportInset())
    await flush()
    expect(document.documentElement.style.getPropertyValue('--vv-offset')).toBe('60px')
    cleanup()

    const types = removeSpy.mock.calls.map(call => call[0])
    expect(types).toContain('resize')
    expect(types).toContain('scroll')
    expect(document.documentElement.style.getPropertyValue('--vvh')).toBe('')
    expect(document.documentElement.style.getPropertyValue('--vv-offset')).toBe('')

    // After cleanup, further events must not write to either property.
    vv.height = 100
    vv.offsetTop = 200
    vv.dispatchResize()
    await flush()
    expect(document.documentElement.style.getPropertyValue('--vvh')).toBe('')
    expect(document.documentElement.style.getPropertyValue('--vv-offset')).toBe('')
  })
})
