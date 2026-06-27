import type { ChatScrollState, ChatScrollVirtualizer, ScrollContext } from './useChatScroll'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createViewportRestore } from './chatScrollViewportRestore'

/**
 * A fully-stubbed ScrollContext (the shared scroll primitives the helpers reach back
 * into useChatScroll for). Pass overrides for the fields a test wires to its element
 * or asserts on; every other primitive defaults to a no-op vi.fn().
 */
function makeScrollContext(overrides: Partial<ScrollContext> = {}): ScrollContext {
  const virt = {
    totalHeight: () => 0,
    updateViewport: vi.fn(),
    anchorAt: vi.fn(() => null),
    scrollTopForAnchor: vi.fn(() => null),
    scrollTopNearAnchor: vi.fn(() => null),
    geometryVersion: () => 0,
  } as unknown as ChatScrollVirtualizer
  return {
    getEl: () => undefined,
    virt,
    atBottom: () => false,
    setAtBottom: vi.fn(),
    isAtBottom: () => false,
    isFollowing: () => false,
    isAnimating: () => false,
    followTail: vi.fn(),
    refreshViewport: vi.fn(),
    writeScrollTop: vi.fn(),
    markProgrammaticScroll: vi.fn(),
    syncVelocityToProgrammatic: vi.fn(),
    setAnchor: vi.fn(),
    ...overrides,
  }
}

// Manual requestAnimationFrame queue so a test can step frames deterministically.
// handleResize schedules its flush on a rAF and re-schedules it while an animation
// runs; flushRaf() runs exactly the callbacks queued so far (a re-scheduled one
// lands in the NEXT flush, modelling a real frame boundary).
function installRaf() {
  const queue = new Map<number, FrameRequestCallback>()
  let id = 0
  const origRaf = globalThis.requestAnimationFrame
  const origCancel = globalThis.cancelAnimationFrame
  globalThis.requestAnimationFrame = (cb: FrameRequestCallback): number => {
    const next = ++id
    queue.set(next, cb)
    return next
  }
  globalThis.cancelAnimationFrame = (handle: number) => {
    queue.delete(handle)
  }
  return {
    flush() {
      const batch = [...queue.values()]
      queue.clear()
      for (const cb of batch)
        cb(0)
    },
    restore() {
      globalThis.requestAnimationFrame = origRaf
      globalThis.cancelAnimationFrame = origCancel
    },
  }
}

afterEach(() => vi.restoreAllMocks())

/** A viewport-restore over fully stubbed deps; `state.animating` is mutable mid-test. */
function harness() {
  const el = { scrollTop: 0, scrollHeight: 5000, clientHeight: 500 } as HTMLDivElement
  const saved: ChatScrollState = {
    anchor: { id: 'r1', offsetWithinRow: 10 },
    atBottom: false,
    hasMoreNewer: false,
  }
  const state = { animating: false }
  const writeScrollTop = vi.fn()
  const virt = {
    totalHeight: () => 5000,
    updateViewport: vi.fn(),
    anchorAt: vi.fn(() => null),
    scrollTopForAnchor: vi.fn(() => 1200), // a resolvable anchor -> restore writes
    scrollTopNearAnchor: vi.fn(() => 1200),
    geometryVersion: () => 0,
  } as unknown as ChatScrollVirtualizer

  const setSuppressOlder = vi.fn()
  const onGeometrySettled = vi.fn()
  const restore = createViewportRestore(
    makeScrollContext({ getEl: () => el, virt, writeScrollTop, isAnimating: () => state.animating }),
    {
      checkAtBottom: vi.fn(),
      repinToAnchor: vi.fn(),
      stickToBottom: vi.fn(() => false),
      forceScrollToBottom: vi.fn(),
      clearSavedViewportScroll: vi.fn(),
      savedViewportScroll: () => saved,
      setSuppressOlder,
      setLastScrollTopForDir: vi.fn(),
      onGeometrySettled,
    },
  )
  // prevClientHeight defaults to 0 (hidden); el.clientHeight is 500 -> wasHidden.
  return { restore, state, writeScrollTop, setSuppressOlder, onGeometrySettled, el }
}

describe('chatscrollviewportrestore handleResize', () => {
  it('defers a hidden->visible restore while an animation runs, then restores once it ends', () => {
    const raf = installRaf()
    try {
      const { restore, state, writeScrollTop } = harness()
      state.animating = true

      // A hidden->visible resize fires while a scroll animation is in flight.
      restore.handleResize()
      raf.flush() // the flush sees isAnimating() -> re-schedules instead of restoring
      expect(writeScrollTop).not.toHaveBeenCalled()

      // The animation is still running across another frame: still deferred, NOT dropped.
      raf.flush()
      expect(writeScrollTop).not.toHaveBeenCalled()

      // Animation ends; the re-scheduled flush now performs the restore.
      state.animating = false
      raf.flush()
      expect(writeScrollTop).toHaveBeenCalledWith(1200, 'viewport-restore-anchor')
    }
    finally {
      raf.restore()
    }
  })

  it('restores immediately when no animation is running', () => {
    const raf = installRaf()
    try {
      const { restore, writeScrollTop } = harness() // state.animating stays false
      restore.handleResize()
      raf.flush()
      expect(writeScrollTop).toHaveBeenCalledWith(1200, 'viewport-restore-anchor')
    }
    finally {
      raf.restore()
    }
  })
})

/**
 * A hidden->visible restore of a NON-bottom save whose anchor doesn't resolve. The
 * priority is: anchor -> raw-top (while still all-hidden) -> hasMoreNewer tail-snap
 * -> clamp-to-top. The key invariant: an all-hidden reader who scrolled UP while
 * windowed away (hasMoreNewer) must be RESTORED to their raw offset, not yanked to
 * the live tail by the tail-snap.
 */
describe('chatscrollviewportrestore restore priority', () => {
  function priorityHarness(opts: {
    totalHeight: number
    rawScrollTop?: number
    hasMoreNewer: boolean
    // A saved anchor whose exact row no longer resolves (trimmed); `nearAnchorTop` is
    // what scrollTopNearAnchor recovers for it (null = no surviving row to land on).
    anchor?: ChatScrollState['anchor']
    nearAnchorTop?: number | null
  }) {
    const el = { scrollTop: 0, scrollHeight: 5000, clientHeight: 500 } as HTMLDivElement
    // The exact anchor never resolves (scrollTopForAnchor returns null); a provided
    // `anchor` exercises the nearest-survivor recovery via scrollTopNearAnchor.
    const saved: ChatScrollState = {
      anchor: opts.anchor,
      atBottom: false,
      hasMoreNewer: opts.hasMoreNewer,
      rawScrollTop: opts.rawScrollTop,
    }
    const writeScrollTop = vi.fn()
    const forceScrollToBottom = vi.fn()
    const virt = {
      totalHeight: () => opts.totalHeight,
      updateViewport: vi.fn(),
      anchorAt: vi.fn(() => null),
      scrollTopForAnchor: vi.fn(() => null),
      scrollTopNearAnchor: vi.fn(() => opts.nearAnchorTop ?? null),
      geometryVersion: () => 0,
    } as unknown as ChatScrollVirtualizer
    const restore = createViewportRestore(
      makeScrollContext({ getEl: () => el, virt, writeScrollTop }),
      {
        checkAtBottom: vi.fn(),
        repinToAnchor: vi.fn(),
        stickToBottom: vi.fn(() => false),
        forceScrollToBottom,
        clearSavedViewportScroll: vi.fn(),
        savedViewportScroll: () => saved,
        setSuppressOlder: vi.fn(),
        setLastScrollTopForDir: vi.fn(),
        onGeometrySettled: vi.fn(),
      },
    )
    return { restore, writeScrollTop, forceScrollToBottom }
  }

  it('restores the raw offset of an all-hidden scrolled-up reader instead of snapping to the live tail', () => {
    const raf = installRaf()
    try {
      // All-hidden window (totalHeight 0), a saved raw offset, and hasMoreNewer set.
      const { restore, writeScrollTop, forceScrollToBottom } = priorityHarness({
        totalHeight: 0,
        rawScrollTop: 800,
        hasMoreNewer: true,
      })
      restore.handleResize()
      raf.flush()
      // raw-top restore wins over the hasMoreNewer tail-snap (800 is within [0, 4500]).
      expect(writeScrollTop).toHaveBeenCalledWith(800, 'viewport-restore-raw-top')
      expect(forceScrollToBottom).not.toHaveBeenCalled()
    }
    finally {
      raf.restore()
    }
  })

  it('snaps to the live tail when the window is no longer all-hidden and no anchor recovers', () => {
    const raf = installRaf()
    try {
      // Virtual rows appeared (totalHeight > 0) and no saved anchor to recover, so the
      // raw offset is stale (restoreFromRawTop declines) and the tail-snap stands.
      const { restore, writeScrollTop, forceScrollToBottom } = priorityHarness({
        totalHeight: 3000,
        rawScrollTop: 800,
        hasMoreNewer: true,
      })
      restore.handleResize()
      raf.flush()
      expect(forceScrollToBottom).toHaveBeenCalled()
      expect(writeScrollTop).not.toHaveBeenCalled()
    }
    finally {
      raf.restore()
    }
  })

  it('recovers to the nearest surviving row when the anchored row was trimmed away', () => {
    const raf = installRaf()
    try {
      // A scrolled-up reader whose anchor row was trimmed as the window advanced: the
      // exact anchor no longer resolves, the window isn't all-hidden, and newer messages
      // exist -- but instead of the live-tail snap, restore lands on the nearest survivor.
      const { restore, writeScrollTop, forceScrollToBottom } = priorityHarness({
        totalHeight: 3000,
        hasMoreNewer: true,
        anchor: { id: 'gone', offsetWithinRow: 10, seq: 42n },
        nearAnchorTop: 250,
      })
      restore.handleResize()
      raf.flush()
      expect(writeScrollTop).toHaveBeenCalledWith(250, 'viewport-restore-near-anchor')
      expect(forceScrollToBottom).not.toHaveBeenCalled()
    }
    finally {
      raf.restore()
    }
  })

  it('falls back to the live-tail snap when no surviving row resolves for the trimmed anchor', () => {
    const raf = installRaf()
    try {
      const { restore, writeScrollTop, forceScrollToBottom } = priorityHarness({
        totalHeight: 3000,
        hasMoreNewer: true,
        anchor: { id: 'gone', offsetWithinRow: 10, seq: 42n },
        nearAnchorTop: null, // e.g. an empty window -- nothing to land on
      })
      restore.handleResize()
      raf.flush()
      expect(forceScrollToBottom).toHaveBeenCalled()
      expect(writeScrollTop).not.toHaveBeenCalled()
    }
    finally {
      raf.restore()
    }
  })
})

describe('chatscrollviewportrestore geometry-settle + suppression', () => {
  // A viewport-restore over a single element with no saved scroll, so handleResize
  // routes through recheckOnResize (the already-visible-resize path) once
  // initClientHeight has seeded prevClientHeight (making wasHidden false).
  function settleHarness(el: HTMLDivElement) {
    const setSuppressOlder = vi.fn()
    const onGeometrySettled = vi.fn()
    const restore = createViewportRestore(
      makeScrollContext({ getEl: () => el, isAnimating: () => false }),
      {
        checkAtBottom: vi.fn(),
        repinToAnchor: vi.fn(),
        stickToBottom: vi.fn(() => false),
        forceScrollToBottom: vi.fn(),
        clearSavedViewportScroll: vi.fn(),
        savedViewportScroll: () => undefined,
        setSuppressOlder,
        setLastScrollTopForDir: vi.fn(),
        onGeometrySettled,
      },
    )
    return { restore, setSuppressOlder, onGeometrySettled }
  }

  it('re-arms the geometry-derived memos after a resize settles (no scroll event fires)', () => {
    const raf = installRaf()
    try {
      const el = { scrollTop: 0, scrollHeight: 5000, clientHeight: 500 } as HTMLDivElement
      const { restore, onGeometrySettled } = settleHarness(el)
      restore.initClientHeight() // prevClientHeight = 500 -> the next resize is non-hidden
      restore.handleResize()
      raf.flush()
      // Geometry-derived memos read only when this ticks; a resize that clamped
      // scrollTop to an edge fired no scroll event, so the resize must tick it.
      expect(onGeometrySettled).toHaveBeenCalled()
    }
    finally {
      raf.restore()
    }
  })

  it('clears the older-load suppression when a resize leaves the viewport non-scrollable', () => {
    const raf = installRaf()
    try {
      // Content (400) now fits the viewport (500): maxScrollTopOf is 0, so no scroll
      // event can ever reach the top band to clear the suppression -- the resize must.
      const el = { scrollTop: 0, scrollHeight: 400, clientHeight: 500 } as HTMLDivElement
      const { restore, setSuppressOlder } = settleHarness(el)
      restore.initClientHeight()
      restore.handleResize()
      raf.flush()
      expect(setSuppressOlder).toHaveBeenLastCalledWith(false)
    }
    finally {
      raf.restore()
    }
  })

  it('leaves the older-load suppression untouched on a still-scrollable resize', () => {
    const raf = installRaf()
    try {
      const el = { scrollTop: 1000, scrollHeight: 5000, clientHeight: 500 } as HTMLDivElement
      const { restore, setSuppressOlder } = settleHarness(el)
      restore.initClientHeight()
      restore.handleResize()
      raf.flush()
      // recheckOnResize never touches the flag, and a scrollable viewport can still
      // clear it via a scroll-to-top, so the non-scrollable clear must NOT fire.
      expect(setSuppressOlder).not.toHaveBeenCalled()
    }
    finally {
      raf.restore()
    }
  })
})
