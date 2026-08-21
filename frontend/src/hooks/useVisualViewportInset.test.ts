/**
 * @vitest-environment jsdom
 */
import { renderHook } from '@solidjs/testing-library'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { isSoftKeyboardVisible } from '~/lib/softKeyboard'
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

/**
 * Raise the on-screen keyboard: shrink the visual viewport past the hook's
 * threshold and tell it. Callers focus FIRST, which is the order a real device
 * produces — and focus alone is deliberately not enough, because the composer
 * takes focus whenever it is enabled and no keyboard follows that.
 */
async function openKeyboard(vv: MockVisualViewport, height = 380) {
  vv.height = height
  vv.dispatchResize()
  await flush()
}

// Helper: create an input, attach to body, focus it, dispatch focusin.
function focusInput(): HTMLInputElement {
  const input = document.createElement('input')
  document.body.appendChild(input)
  input.focus()
  input.dispatchEvent(new FocusEvent('focusin', { bubbles: true }))
  return input
}

// The same, for an editing host. The chat composer is one of these, not an
// <input>, so this is the element the keyboard actually comes up for.
function focusEditable(spelling: string): HTMLElement {
  const el = document.createElement('div')
  el.setAttribute('contenteditable', spelling)
  document.body.appendChild(el)
  el.focus()
  el.dispatchEvent(new FocusEvent('focusin', { bubbles: true }))
  return el
}

/**
 * Report one pointer type to `window.matchMedia`, and return a restore.
 *
 * The shared `~/test-support/matchMediaStub` cannot serve here: it answers
 * every query with `matches: false`, and the soft-keyboard branch needs the
 * matching one. Stating the device in each test also keeps these assertions
 * off jsdom's feature set — jsdom implements no `matchMedia` today, which the
 * hook reads as "correct anyway", so a future jsdom that adds one would
 * silently flip every shift expectation below.
 */
function stubPointer(kind: 'coarse' | 'fine'): () => void {
  const original = window.matchMedia
  window.matchMedia = ((query: string) => ({
    matches: query.includes(`pointer: ${kind}`),
    media: query,
    addEventListener: () => {},
    removeEventListener: () => {},
  })) as unknown as typeof window.matchMedia
  return () => {
    if (original)
      window.matchMedia = original
    else
      delete (window as unknown as { matchMedia?: unknown }).matchMedia
  }
}

describe('useVisualViewportInset', () => {
  let restoreRaf: () => void
  let restorePointer: () => void

  beforeEach(() => {
    restoreRaf = installSyncRaf()
    // A phone unless a test says otherwise: every displacement this hook
    // corrects is one a soft keyboard causes.
    restorePointer = stubPointer('coarse')
  })

  afterEach(() => {
    restoreRaf()
    // Restores whatever the test left installed, because it puts back the
    // value captured before `beforeEach` stubbed anything.
    restorePointer()
    document.documentElement.style.removeProperty('--vvh')
    document.documentElement.style.removeProperty('--vv-shift')
    document.body.innerHTML = ''
    delete (window as unknown as { visualViewport?: unknown }).visualViewport
  })

  it('publishes a negative --vv-shift when the keyboard is down and offsetTop > 0', () => {
    Object.defineProperty(window, 'innerHeight', { value: 800, configurable: true })
    // iOS-26 post-keyboard-dismiss: visual viewport is back to full
    // height but stays translated up by ~120px until the next layout.
    const vv = makeMockVisualViewport(800, 120)
    Object.defineProperty(window, 'visualViewport', { value: vv, configurable: true })

    const { cleanup } = renderHook(() => useVisualViewportInset())
    try {
      // Initial sync apply() inside onMount publishes immediately.
      expect(document.documentElement.style.getPropertyValue('--vv-shift')).toBe('-120px')
    }
    finally {
      cleanup()
    }
  })

  it('clears --vv-shift when offsetTop returns to 0', async () => {
    Object.defineProperty(window, 'innerHeight', { value: 800, configurable: true })
    const vv = makeMockVisualViewport(800, 120)
    Object.defineProperty(window, 'visualViewport', { value: vv, configurable: true })

    const { cleanup } = renderHook(() => useVisualViewportInset())
    try {
      expect(document.documentElement.style.getPropertyValue('--vv-shift')).toBe('-120px')

      vv.offsetTop = 0
      vv.dispatchScroll()
      await flush()

      expect(document.documentElement.style.getPropertyValue('--vv-shift')).toBe('')
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
      expect(document.documentElement.style.getPropertyValue('--vv-shift')).toBe('')
    }
    finally {
      cleanup()
    }
  })

  // The keyboard-up pair. iOS does not resize the layout viewport for the
  // keyboard, so `dvh` still reports the full height and the app runs on under
  // it; iOS then scrolls the visual viewport down to reveal the focused
  // composer, and `body` is `position: fixed`, so the app reads as shifted UP.
  // The height and the shift answer those two halves, and only both together
  // leave the body covering the visible region.
  it('publishes the keyboard-up height and a positive shift once the keyboard opens', async () => {
    Object.defineProperty(window, 'innerHeight', { value: 800, configurable: true })
    // Keyboard down, at the full height, with a residual offsetTop → the
    // keyboard-down correction. This height is also the shrink baseline.
    const vv = makeMockVisualViewport(800, 60)
    Object.defineProperty(window, 'visualViewport', { value: vv, configurable: true })

    const { cleanup } = renderHook(() => useVisualViewportInset())
    try {
      expect(document.documentElement.style.getPropertyValue('--vv-shift')).toBe('-60px')
      expect(document.documentElement.style.getPropertyValue('--vvh')).toBe('')

      // The user taps the composer. Focus lands FIRST and the keyboard has not
      // opened yet, which is also what a programmatic focus looks like -- so
      // nothing may change until the viewport actually loses height.
      const input = focusInput()
      await flush()
      expect(document.documentElement.style.getPropertyValue('--vvh')).toBe('')
      expect(document.documentElement.style.getPropertyValue('--vv-shift')).toBe('-60px')

      // Now the keyboard opens: the viewport shrinks and iOS scrolls it down.
      vv.height = 380
      vv.dispatchResize()
      await flush()

      // Both halves, or the shift alone would push the composer out of view.
      expect(document.documentElement.style.getPropertyValue('--vvh')).toBe('380px')
      expect(document.documentElement.style.getPropertyValue('--vv-shift')).toBe('60px')

      // ...and back, so a dismissed keyboard keeps neither the keyboard-up
      // sign nor a height that no longer describes the visible region.
      input.blur()
      vv.height = 800
      vv.dispatchResize()
      await flush()

      expect(document.documentElement.style.getPropertyValue('--vvh')).toBe('')
      expect(document.documentElement.style.getPropertyValue('--vv-shift')).toBe('-60px')
    }
    finally {
      cleanup()
    }
  })

  // The regression that hid the mobile tab bar the moment a chat opened:
  // `MarkdownEditor` focuses the composer whenever it is enabled, and iOS
  // raises no keyboard for a programmatic focus.
  it('ignores a focus that no keyboard follows', async () => {
    Object.defineProperty(window, 'innerHeight', { value: 800, configurable: true })
    const vv = makeMockVisualViewport(800)
    Object.defineProperty(window, 'visualViewport', { value: vv, configurable: true })

    const { result, cleanup } = renderHook(() => useVisualViewportInset())
    try {
      focusInput()
      await flush()

      expect(result()).toBe(false)
      expect(document.documentElement.style.getPropertyValue('--vvh')).toBe('')
    }
    finally {
      cleanup()
    }
  })

  // Browser chrome resizes the visual viewport too -- iOS Safari collapsing
  // its toolbar moves ~50-100px -- and taking that for a keyboard would hide
  // the tab bar mid-scroll.
  it('ignores a shrink too small to be a keyboard', async () => {
    Object.defineProperty(window, 'innerHeight', { value: 800, configurable: true })
    const vv = makeMockVisualViewport(800)
    Object.defineProperty(window, 'visualViewport', { value: vv, configurable: true })

    const { result, cleanup } = renderHook(() => useVisualViewportInset())
    try {
      focusInput()
      vv.height = 700 // 100px: a toolbar, not a keyboard
      vv.dispatchResize()
      await flush()
      expect(result()).toBe(false)

      vv.height = 680 // 120px: at the threshold
      vv.dispatchResize()
      await flush()
      expect(result()).toBe(true)
    }
    finally {
      cleanup()
    }
  })

  // On a desktop these values track a pinch-zoom, where resizing or
  // translating the body fights the user's own gesture.
  it('publishes neither property on a fine-pointer device', async () => {
    stubPointer('fine') // afterEach restores; see the beforeEach note
    Object.defineProperty(window, 'innerHeight', { value: 800, configurable: true })
    const vv = makeMockVisualViewport(380, 120)
    Object.defineProperty(window, 'visualViewport', { value: vv, configurable: true })

    const { cleanup } = renderHook(() => useVisualViewportInset())
    try {
      expect(document.documentElement.style.getPropertyValue('--vv-shift')).toBe('')

      focusInput()
      await flush()

      expect(document.documentElement.style.getPropertyValue('--vv-shift')).toBe('')
      expect(document.documentElement.style.getPropertyValue('--vvh')).toBe('')
    }
    finally {
      cleanup()
    }
  })

  // The signal `AppShell` hides the mobile tab bar on.
  it('reports the soft keyboard as up while it takes screen space, and not after', async () => {
    Object.defineProperty(window, 'innerHeight', { value: 800, configurable: true })
    const vv = makeMockVisualViewport(800)
    Object.defineProperty(window, 'visualViewport', { value: vv, configurable: true })

    const { result, cleanup } = renderHook(() => useVisualViewportInset())
    try {
      expect(result()).toBe(false)

      const input = focusInput()
      await openKeyboard(vv)
      expect(result()).toBe(true)

      input.blur()
      await openKeyboard(vv, 800)
      expect(result()).toBe(false)
    }
    finally {
      cleanup()
    }
  })

  // The same fact reaches the callers outside the reactive graph, which is
  // what decides whether a completed send releases the composer.
  it('publishes the measured keyboard state to the softKeyboard module', async () => {
    Object.defineProperty(window, 'innerHeight', { value: 800, configurable: true })
    const vv = makeMockVisualViewport(800)
    Object.defineProperty(window, 'visualViewport', { value: vv, configurable: true })

    const { cleanup } = renderHook(() => useVisualViewportInset())
    try {
      expect(isSoftKeyboardVisible()).toBe(false)

      const input = focusInput()
      await flush()
      // Focus alone is not a keyboard -- a hardware keyboard raises none.
      expect(isSoftKeyboardVisible()).toBe(false)

      await openKeyboard(vv)
      expect(isSoftKeyboardVisible()).toBe(true)

      input.blur()
      await flush()
      expect(isSoftKeyboardVisible()).toBe(false)
    }
    finally {
      cleanup()
    }
  })

  it('clears the published keyboard state on cleanup', async () => {
    Object.defineProperty(window, 'innerHeight', { value: 800, configurable: true })
    const vv = makeMockVisualViewport(800)
    Object.defineProperty(window, 'visualViewport', { value: vv, configurable: true })

    const { cleanup } = renderHook(() => useVisualViewportInset())
    focusInput()
    await openKeyboard(vv)
    expect(isSoftKeyboardVisible()).toBe(true)

    cleanup()

    expect(isSoftKeyboardVisible()).toBe(false)
  })

  // A fine pointer has no keyboard to take screen space, so neither focus nor
  // a resize may hide the bar there.
  it('never reports the soft keyboard as up on a fine-pointer device', async () => {
    stubPointer('fine')
    Object.defineProperty(window, 'innerHeight', { value: 800, configurable: true })
    const vv = makeMockVisualViewport(800)
    Object.defineProperty(window, 'visualViewport', { value: vv, configurable: true })

    const { result, cleanup } = renderHook(() => useVisualViewportInset())
    try {
      focusInput()
      await openKeyboard(vv)
      expect(result()).toBe(false)
    }
    finally {
      cleanup()
    }
  })

  // A host with no media queries at all still gets the correction: it fires
  // only on a non-zero offsetTop, and refusing it outright would drop the iOS
  // mitigation wherever `matchMedia` is missing.
  it('publishes --vv-shift when the host implements no matchMedia', () => {
    delete (window as unknown as { matchMedia?: unknown }).matchMedia
    Object.defineProperty(window, 'innerHeight', { value: 800, configurable: true })
    const vv = makeMockVisualViewport(800, 120)
    Object.defineProperty(window, 'visualViewport', { value: vv, configurable: true })

    const { cleanup } = renderHook(() => useVisualViewportInset())
    try {
      expect(document.documentElement.style.getPropertyValue('--vv-shift')).toBe('-120px')
    }
    finally {
      cleanup()
    }
  })

  it('publishes --vvh only while an editable is focused', async () => {
    Object.defineProperty(window, 'innerHeight', { value: 800, configurable: true })
    const vv = makeMockVisualViewport(800)
    Object.defineProperty(window, 'visualViewport', { value: vv, configurable: true })

    const { cleanup } = renderHook(() => useVisualViewportInset())
    try {
      // No focus yet → unset.
      expect(document.documentElement.style.getPropertyValue('--vvh')).toBe('')

      const input = focusInput()
      await openKeyboard(vv)
      expect(document.documentElement.style.getPropertyValue('--vvh')).toBe('380px')

      // Blur the editable. jsdom's blur() synchronously dispatches a
      // bubbling focusout and updates document.activeElement to the
      // body. The hook's focusout handler defers the editableFocused
      // flip to a microtask so a Tab-key transition between two
      // editables doesn't flicker. The property clears on the blur alone,
      // before the keyboard has finished animating away.
      input.blur()
      await flush()
      expect(document.documentElement.style.getPropertyValue('--vvh')).toBe('')
    }
    finally {
      cleanup()
    }
  })

  // jsdom implements neither `isContentEditable` nor `contentEditable`, so
  // before the predicate gained its attribute fallback this branch answered
  // false here for every spelling and the whole case was untestable.
  it('publishes --vvh for a focused editing host, whichever spelling it carries', async () => {
    for (const spelling of ['true', '', 'plaintext-only']) {
      Object.defineProperty(window, 'innerHeight', { value: 800, configurable: true })
      const vv = makeMockVisualViewport(800)
      Object.defineProperty(window, 'visualViewport', { value: vv, configurable: true })

      const { cleanup } = renderHook(() => useVisualViewportInset())
      try {
        expect(document.documentElement.style.getPropertyValue('--vvh')).toBe('')

        const editor = focusEditable(spelling)
        await openKeyboard(vv)
        expect(document.documentElement.style.getPropertyValue('--vvh'), `contenteditable="${spelling}"`).toBe('380px')

        editor.blur()
        await flush()
        expect(document.documentElement.style.getPropertyValue('--vvh')).toBe('')
      }
      finally {
        cleanup()
        document.body.innerHTML = ''
      }
    }
  })

  // The negative half of the same branch: `false` turns editing off, so no
  // keyboard comes up and nothing is published.
  it('publishes no --vvh for a focused contenteditable="false" element', async () => {
    Object.defineProperty(window, 'innerHeight', { value: 800, configurable: true })
    const vv = makeMockVisualViewport(800)
    Object.defineProperty(window, 'visualViewport', { value: vv, configurable: true })

    const { cleanup } = renderHook(() => useVisualViewportInset())
    try {
      focusEditable('false')
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
      expect(document.documentElement.style.getPropertyValue('--vv-shift')).toBe('')

      Object.defineProperty(window, 'innerHeight', { value: 500, configurable: true })
      window.dispatchEvent(new Event('resize'))
      await flush()

      // Still unset (no editable focused, no visualViewport).
      expect(document.documentElement.style.getPropertyValue('--vvh')).toBe('')
      expect(document.documentElement.style.getPropertyValue('--vv-shift')).toBe('')
    }
    finally {
      cleanup()
    }
  })

  it('removes listeners and clears both custom properties on cleanup', async () => {
    Object.defineProperty(window, 'innerHeight', { value: 800, configurable: true })
    // No editable focused → the keyboard-down correction is published, so
    // cleanup has something to clear.
    const vv = makeMockVisualViewport(800, 60)
    Object.defineProperty(window, 'visualViewport', { value: vv, configurable: true })
    const removeSpy = vi.spyOn(vv, 'removeEventListener')

    const { cleanup } = renderHook(() => useVisualViewportInset())
    await flush()
    expect(document.documentElement.style.getPropertyValue('--vv-shift')).toBe('-60px')
    cleanup()

    const types = removeSpy.mock.calls.map(call => call[0])
    expect(types).toContain('resize')
    expect(types).toContain('scroll')
    expect(document.documentElement.style.getPropertyValue('--vvh')).toBe('')
    expect(document.documentElement.style.getPropertyValue('--vv-shift')).toBe('')

    // After cleanup, further events must not write to either property.
    vv.height = 100
    vv.offsetTop = 200
    vv.dispatchResize()
    await flush()
    expect(document.documentElement.style.getPropertyValue('--vvh')).toBe('')
    expect(document.documentElement.style.getPropertyValue('--vv-shift')).toBe('')
  })
})
