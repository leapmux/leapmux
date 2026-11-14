import type { ChatScrollState, ChatScrollVirtualizer } from './useChatScroll'
import type { ScrollAnchor } from '~/stores/chatTypes'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createViewportRestore } from './chatScrollViewportRestore'
import { makeScrollContext, virtualizerNoOps } from './useChatScroll.testkit'

// Manual requestAnimationFrame queue so a test can step frames deterministically.
// handleResize schedules its flush on a rAF and re-schedules it while an animation
// runs; flush() runs exactly the callbacks queued so far (a re-scheduled one lands in
// the NEXT flush, modelling a real frame boundary).
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
      const batch = [
        ...queue.values(),
      ]
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

/** A virtualizer stub whose anchors resolve to the given scroll coordinates. */
function stubVirt(opts: { totalHeight: number, forAnchor: number | null, nearAnchor: number | null }): ChatScrollVirtualizer {
  return {
    ...virtualizerNoOps(),
    totalHeight: () => opts.totalHeight,
    geometryVersion: () => 0,
    updateViewport: vi.fn(),
    anchorAt: vi.fn(() => null),
    scrollTopForAnchor: vi.fn(() => opts.forAnchor),
    scrollTopNearAnchor: vi.fn(() => opts.nearAnchor),
  }
}

/**
 * A viewport restore over stubbed deps. `placed` records what each `placeAt` resolver
 * returned, in order: a `null` is a strategy whose anchor did not resolve.
 */
function harness(opts: {
  saved?: ChatScrollState | undefined
  totalHeight?: number
  forAnchor?: number | null
  nearAnchor?: number | null
  followRequested?: boolean
  lastAnchor?: () => ScrollAnchor | null
} = {}) {
  const el = { scrollTop: 0, scrollHeight: 5000, clientHeight: 500 } as HTMLDivElement
  const state = { animating: false }
  const placed: Array<number | null> = []
  const placeAt = vi.fn((resolve: () => number | null): boolean | null => {
    const top = resolve()
    placed.push(top)
    return top === null
      ? null
      : true
  })
  const syncAfterWrite = vi.fn()
  const extras = {
    stickToBottom: vi.fn(() => true),
    forceScrollToBottom: vi.fn(),
    clearSavedViewportScroll: vi.fn(),
    savedViewportScroll: () => opts.saved,
    restoreAnchor: vi.fn(),
    lastAnchor: vi.fn(opts.lastAnchor ?? (() => null)),
    followRequested: () => opts.followRequested ?? false,
    onGeometrySettled: vi.fn(),
  }
  const restore = createViewportRestore(
    makeScrollContext({
      getEl: () => el,
      virt: stubVirt({
        totalHeight: opts.totalHeight ?? 5000,
        forAnchor: opts.forAnchor === undefined
          ? 1200
          : opts.forAnchor,
        nearAnchor: opts.nearAnchor === undefined
          ? 1200
          : opts.nearAnchor,
      }),
      isAnimating: () => state.animating,
      placeAt,
      syncAfterWrite,
    }),
    extras,
  )
  // prevClientHeight defaults to 0 (hidden); el.clientHeight is 500 -> wasHidden.
  return {
    restore,
    state,
    placed,
    placeAt,
    syncAfterWrite,
    extras,
    el,
  }
}

const SAVED_ANCHOR: ChatScrollState = {
  anchor: { id: 'r1', offsetWithinRow: 10 },
  atBottom: false,
  hasMoreNewer: false,
}

describe('chatScrollViewportRestore handleResize', () => {
  it('defers a hidden->visible restore while an animation runs, then restores once it ends', () => {
    const raf = installRaf()
    try {
      const { restore, state, placed } = harness({ saved: SAVED_ANCHOR })
      state.animating = true

      // A hidden->visible resize fires while a scroll animation is in flight.
      restore.handleResize()
      raf.flush() // the flush sees isAnimating() -> re-schedules instead of restoring
      expect(placed).toEqual([])

      // The animation is still running across another frame: still deferred, NOT dropped.
      raf.flush()
      expect(placed).toEqual([])

      // Animation ends; the re-scheduled flush now performs the restore.
      state.animating = false
      raf.flush()
      expect(placed).toEqual([1200])
    }
    finally {
      raf.restore()
    }
  })

  it('restores immediately when no animation is running', () => {
    const raf = installRaf()
    try {
      const { restore, placed, extras } = harness({ saved: SAVED_ANCHOR })
      restore.handleResize()
      raf.flush()
      expect(placed).toEqual([1200])
      expect(extras.clearSavedViewportScroll).toHaveBeenCalledOnce()
    }
    finally {
      raf.restore()
    }
  })

  it('passes the anchor of the ResizeObserver callback to the restore', () => {
    const raf = installRaf()
    try {
      const anchorA: ScrollAnchor = { id: 'a', offsetWithinRow: 1 }
      const anchorB: ScrollAnchor = { id: 'b', offsetWithinRow: 2 }
      let current = anchorA
      // No saved state and no request: the show takes the recheck path. The test does
      // not call initClientHeight, so the pane counts as hidden before the resize.
      const { restore, extras, syncAfterWrite } = harness({
        saved: undefined,
        lastAnchor: () => current,
      })
      restore.handleResize()
      // A scroll event of the browser replaces the anchor of the hook before the flush.
      current = anchorB
      raf.flush()
      expect(extras.restoreAnchor).toHaveBeenCalledWith(anchorA)
      expect(syncAfterWrite).toHaveBeenCalledOnce()
    }
    finally {
      raf.restore()
    }
  })

  it('sticks on a resize while the reader requests the tail', () => {
    const raf = installRaf()
    try {
      const { restore, extras } = harness({ saved: undefined, followRequested: true })
      restore.initClientHeight()
      restore.handleResize()
      raf.flush()
      expect(extras.stickToBottom).toHaveBeenCalledOnce()
      expect(extras.restoreAnchor).not.toHaveBeenCalled()
    }
    finally {
      raf.restore()
    }
  })

  it('records the position on a resize of a visible reader with no request', () => {
    const raf = installRaf()
    try {
      const { restore, extras, syncAfterWrite } = harness({ saved: undefined })
      restore.initClientHeight()
      restore.handleResize()
      raf.flush()
      expect(extras.stickToBottom).not.toHaveBeenCalled()
      // Only a show restores the anchor: the pane was visible before this resize.
      expect(extras.restoreAnchor).not.toHaveBeenCalled()
      expect(syncAfterWrite).toHaveBeenCalledOnce()
    }
    finally {
      raf.restore()
    }
  })
})

/**
 * A hidden->visible restore of a NON-bottom save whose anchor doesn't resolve. The
 * priority is: anchor -> raw-top (while still all-hidden) -> nearest survivor ->
 * hasMoreNewer tail-snap -> top. The key invariant: an all-hidden reader who scrolled
 * UP while windowed away (hasMoreNewer) must be RESTORED to their raw offset, not
 * yanked to the live tail by the tail-snap.
 */
describe('chatScrollViewportRestore restore priority', () => {
  function priorityHarness(opts: {
    totalHeight: number
    rawScrollTop?: number
    hasMoreNewer: boolean
    // A saved anchor whose exact row no longer resolves (trimmed); `nearAnchorTop` is
    // what scrollTopNearAnchor recovers for it (null = no surviving row to land on).
    anchor?: ChatScrollState['anchor']
    nearAnchorTop?: number | null
  }) {
    const saved: ChatScrollState = {
      ...(opts.anchor !== undefined
        ? { anchor: opts.anchor }
        : {}),
      atBottom: false,
      hasMoreNewer: opts.hasMoreNewer,
      ...(opts.rawScrollTop !== undefined
        ? { rawScrollTop: opts.rawScrollTop }
        : {}),
    }
    return harness({
      saved,
      totalHeight: opts.totalHeight,
      forAnchor: null,
      nearAnchor: opts.nearAnchorTop ?? null,
    })
  }

  it('restores the raw offset of an all-hidden scrolled-up reader instead of snapping to the live tail', () => {
    const raf = installRaf()
    try {
      // All-hidden window (totalHeight 0), a saved raw offset, and hasMoreNewer set.
      const { restore, placed, extras } = priorityHarness({
        totalHeight: 0,
        rawScrollTop: 800,
        hasMoreNewer: true,
      })
      restore.handleResize()
      raf.flush()
      // raw-top restore wins over the hasMoreNewer tail-snap.
      expect(placed).toEqual([800])
      expect(extras.forceScrollToBottom).not.toHaveBeenCalled()
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
      const { restore, placed, extras } = priorityHarness({
        totalHeight: 3000,
        rawScrollTop: 800,
        hasMoreNewer: true,
      })
      restore.handleResize()
      raf.flush()
      expect(extras.forceScrollToBottom).toHaveBeenCalled()
      expect(placed).toEqual([])
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
      const { restore, placed, extras } = priorityHarness({
        totalHeight: 3000,
        hasMoreNewer: true,
        anchor: { id: 'gone', offsetWithinRow: 10, seq: 42n },
        nearAnchorTop: 250,
      })
      restore.handleResize()
      raf.flush()
      expect(placed).toEqual([null, 250])
      expect(extras.forceScrollToBottom).not.toHaveBeenCalled()
    }
    finally {
      raf.restore()
    }
  })

  it('falls back to the live-tail snap when no surviving row resolves for the trimmed anchor', () => {
    const raf = installRaf()
    try {
      const { restore, placed, extras } = priorityHarness({
        totalHeight: 3000,
        hasMoreNewer: true,
        anchor: { id: 'gone', offsetWithinRow: 10, seq: 42n },
        nearAnchorTop: null, // e.g. an empty window -- nothing to land on
      })
      restore.handleResize()
      raf.flush()
      expect(extras.forceScrollToBottom).toHaveBeenCalled()
      expect(placed).toEqual([null, null])
    }
    finally {
      raf.restore()
    }
  })

  it('places the view at the top when nothing resolves and no newer page exists', () => {
    const raf = installRaf()
    try {
      const { restore, placed, extras } = priorityHarness({ totalHeight: 3000, hasMoreNewer: false })
      restore.handleResize()
      raf.flush()
      expect(placed).toEqual([0])
      expect(extras.forceScrollToBottom).not.toHaveBeenCalled()
    }
    finally {
      raf.restore()
    }
  })
})

describe('chatScrollViewportRestore geometry settle', () => {
  it('re-arms the geometry-derived memos after a resize settles (no scroll event fires)', () => {
    const raf = installRaf()
    try {
      const { restore, extras } = harness({ saved: undefined })
      restore.initClientHeight() // prevClientHeight = 500 -> the next resize is non-hidden
      restore.handleResize()
      raf.flush()
      // Geometry-derived memos read only when this ticks; a resize that clamped
      // scrollTop to an edge fired no scroll event, so the resize must tick it.
      expect(extras.onGeometrySettled).toHaveBeenCalled()
    }
    finally {
      raf.restore()
    }
  })
})
