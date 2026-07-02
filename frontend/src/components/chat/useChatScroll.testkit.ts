import type { AnchorOffsetGeometry } from './chatScrollAnchor'
import type { ChatScrollVirtualizer, ScrollContext } from './useChatScroll'
import type { ScrollAnchor } from '~/stores/chatTypes'
import { createSignal } from 'solid-js'
import { afterEach, beforeAll, vi } from 'vitest'
import { installControllableResizeObserver } from '../../../tests/unit/helpers/resizeObserverStub'
import { anchorAtOffset, resolveAnchorScrollTop, resolveNearestAnchorScrollTop } from './chatScrollAnchor'

/**
 * Shared test kit for the useChatScroll suites. The suite was split out of a
 * single 8k-line file into per-behavior spec files; these virtualizer/context/
 * DOM stubs and the environment hooks are the common prelude every split imports.
 */

/**
 * The measurement-deferral half of the virtualizer surface, defaulted to no-ops.
 * ChatScrollVirtualizer requires these members (an optional surface let a caller
 * silently disable the fling deferral machinery); the stubs spread this so each
 * builder stays focused on the geometry it fakes. Tests that exercise the deferral
 * or the row-top hold release override the members they assert on.
 */
export function measurementDeferralNoOps(): Pick<
  ChatScrollVirtualizer,
  'setVisibleMeasurementDeferral' | 'hasDeferredMeasurements' | 'flushDeferredMeasurements' | 'lastMeasurement' | 'hasMeasuredHeight'
  | 'setFastScrollActive'
> {
  return {
    setVisibleMeasurementDeferral: () => {},
    setFastScrollActive: () => {},
    hasDeferredMeasurements: () => false,
    flushDeferredMeasurements: () => false,
    lastMeasurement: () => undefined,
    hasMeasuredHeight: () => false,
  }
}

/**
 * Minimal virtualizer stub. `totalHeight` is a constant so the geometry re-pin
 * effect never fires — these tests exercise the sticky-bottom / resize / scroll
 * logic, which is independent of the offset map. Tests that need anchoring
 * behavior drive the math in useChatVirtualizer.test.ts instead.
 */
export function makeStubVirtualizer(): ChatScrollVirtualizer {
  return {
    totalHeight: () => 0,
    geometryVersion: () => 0,
    updateViewport: () => {},
    anchorAt: () => null,
    scrollTopNearAnchor: () => null,
    scrollTopForAnchor: () => null,
    ...measurementDeferralNoOps(),
  }
}

/**
 * A fully-stubbed ScrollContext (the shared scroll primitives the helpers reach back
 * into useChatScroll for). Pass overrides for the fields a test wires to its element
 * or asserts on; every other primitive defaults to a no-op.
 */
export function makeScrollContext(overrides: Partial<ScrollContext> = {}): ScrollContext {
  return {
    getEl: () => undefined,
    virt: makeStubVirtualizer(),
    atBottom: () => false,
    setAtBottom: () => {},
    isAtBottom: () => false,
    isFollowing: () => false,
    isAnimating: () => false,
    followTail: () => {},
    refreshViewport: () => {},
    writeScrollTop: () => {},
    syncVelocityToProgrammatic: () => {},
    setAnchor: () => {},
    ...overrides,
  }
}

/**
 * Virtualizer stub whose total height is a controllable signal. Bumping it
 * drives the geometry effect (the path that replaced the content-element
 * ResizeObserver for detecting content growth).
 */
export function makeGrowableVirtualizer() {
  const [total, setTotal] = createSignal(0)
  const virt: ChatScrollVirtualizer = {
    totalHeight: () => total(),
    geometryVersion: () => 0,
    updateViewport: () => {},
    anchorAt: () => null,
    scrollTopNearAnchor: () => null,
    scrollTopForAnchor: () => null,
    ...measurementDeferralNoOps(),
  }
  return { virt, setTotal }
}

/**
 * Virtualizer stub backed by a mutable per-row height map, so a measurement that
 * grows or shrinks a row drives the geometry re-pin through a REAL offset map --
 * `anchorAt` / `scrollTopForAnchor` resolve against the current heights, exactly
 * like the production virtualizer. Row ids are `g${gen}_${index}`; `seq` mirrors
 * the index. `setRowHeight` mutates one row and bumps `geometryVersion`,
 * reproducing a DOM measurement landing after the row mounted (a freshly-mounted
 * row settling from its estimate to its real height once the pipeline measures it).
 * `replaceWindow` swaps in a fresh row set under a NEW generation
 * -- so every prior anchor id stops resolving -- modeling jumpToOldest/jumpToLatest
 * replacing the loaded window (`chatHistoryPaginator`), which is what leaves the
 * pre-jump anchor stale for the re-pin to recover from.
 */
export function makeRowVirtualizer(initialHeights: number[]) {
  const [heights, setHeights] = createSignal<number[]>(initialHeights)
  const [gen, setGen] = createSignal(0)
  const [geometryVersion, setGeometryVersion] = createSignal(0)
  // Cumulative offsets, length n+1: cumOffsets[i] is the top of row i; cumOffsets[n] the
  // total. Zero-height rows share the offset of their successor, so these tests exercise the
  // pure anchor math's shared-offset tie-break (a caller can produce zero-height runs even
  // though the production virtualizer now reserves a positive estimate for every row).
  const cumOffsets = () => {
    const out = [0]
    for (const h of heights())
      out.push(out[out.length - 1] + h)
    return out
  }
  const total = () => {
    const o = cumOffsets()
    return o[o.length - 1]
  }
  // Row id encodes the window GENERATION so a replaced window's ids stop resolving (the
  // real virtualizer keys the offset map by row id; a re-fetched window has fresh rows).
  const indexOfId = (id: string): number => {
    const m = new RegExp(`^g${gen()}_(\\d+)$`).exec(id)
    if (!m)
      return -1
    const idx = Number(m[1])
    return idx >= 0 && idx < heights().length ? idx : -1
  }
  // Delegate the anchor math to the REAL pure functions over this geometry, so these
  // tests exercise the production capture/resolve (including the zero-height-run
  // tie-break) rather than a re-implementation that could drift from it.
  const geometry = (): AnchorOffsetGeometry => {
    const hs = heights()
    const offs = cumOffsets()
    return {
      // seq is 1-based: 0n is the OPTIMISTIC-LOCAL sentinel, which the nearest-anchor
      // recovery deliberately treats as unorderable (resolveNearestAnchorScrollTop bails
      // on an anchor with seq 0n and skips 0n survivors). Stamping row 0 with 0n would
      // silently route every head-of-list recovery test through the local-fallback path
      // instead of the nearest-survivor path it means to exercise -- and models a state
      // production can't produce (server rows never carry seq 0n).
      list: hs.map((_, i) => ({ id: `g${gen()}_${i}`, seq: BigInt(i + 1) })),
      // Largest index whose top offset <= y (the row containing y), clamped to [0, n-1].
      indexAtOffset: (y) => {
        let idx = 0
        for (let i = 0; i < hs.length; i++) {
          if (offs[i] <= y)
            idx = i
          else
            break
        }
        return idx
      },
      indexOfId,
      offsetOfIndex: i => offs[Math.max(0, Math.min(i, hs.length))],
      heightOfIndex: i => hs[i] ?? 0,
      gapAfter: () => 0,
    }
  }
  const virt: ChatScrollVirtualizer = {
    totalHeight: total,
    geometryVersion: () => geometryVersion(),
    updateViewport: () => {},
    anchorAt: (y: number): ScrollAnchor | null => anchorAtOffset(geometry(), y),
    scrollTopNearAnchor: (anchor: ScrollAnchor): number | null => resolveNearestAnchorScrollTop(geometry(), anchor),
    scrollTopForAnchor: (anchor: ScrollAnchor): number | null => resolveAnchorScrollTop(geometry(), anchor),
    ...measurementDeferralNoOps(),
  }
  const setRowHeight = (idx: number, h: number) => {
    setHeights((prev) => {
      const next = [...prev]
      next[idx] = h
      return next
    })
    setGeometryVersion(v => v + 1)
  }
  const replaceWindow = (newHeights: number[]) => {
    setGen(g => g + 1)
    setHeights(newHeights)
    setGeometryVersion(v => v + 1)
  }
  return { virt, setRowHeight, replaceWindow, total }
}

export interface FakeScrollDiv {
  el: HTMLDivElement
  setScrollHeight: (n: number) => void
  setClientHeight: (n: number) => void
  setClientWidth: (n: number) => void
  setScrollTop: (n: number) => void
  setRawScrollTop: (n: number) => void
  getScrollTop: () => number
}

/**
 * Build a real <div> with stubbed scroll/layout properties so the hook can
 * read scrollHeight / clientHeight / scrollTop and we can observe writes to
 * scrollTop. jsdom doesn't compute layout, so these have to be patched.
 *
 * `scrollTop` is clamped to [0, scrollHeight - clientHeight] on write to
 * match real browser behavior — the hook frequently uses
 * `scrollTop = scrollHeight` as a "scroll to bottom" idiom that relies on
 * that clamping to land at the actual visual bottom.
 */
export function makeFakeScrollDiv(): FakeScrollDiv {
  const el = document.createElement('div')
  let scrollHeight = 0
  let clientHeight = 0
  let clientWidth = 0
  let scrollTop = 0
  const clamp = (v: number) => Math.max(0, Math.min(v, scrollHeight - clientHeight))
  Object.defineProperty(el, 'scrollHeight', {
    get: () => scrollHeight,
    configurable: true,
  })
  Object.defineProperty(el, 'clientHeight', {
    get: () => clientHeight,
    configurable: true,
  })
  Object.defineProperty(el, 'clientWidth', {
    get: () => clientWidth,
    configurable: true,
  })
  Object.defineProperty(el, 'scrollTop', {
    get: () => scrollTop,
    set: (v: number) => {
      scrollTop = clamp(v)
    },
    configurable: true,
  })
  // jsdom's scrollBy is a no-op; apply the vertical delta so pageScroll moves.
  // Both overloads are honored -- scrollBy(x, y) reads the SECOND argument as the
  // vertical delta, like a real browser -- so a call-form refactor in production
  // can't silently turn every page jump into a no-op that trips the at-edge branch.
  el.scrollBy = ((opts?: ScrollToOptions | number, y?: number) => {
    const top = typeof opts === 'number' ? (y ?? 0) : (opts?.top ?? 0)
    scrollTop = clamp(scrollTop + top)
  }) as typeof el.scrollBy
  return {
    el,
    setScrollHeight: (n) => {
      scrollHeight = n
      scrollTop = clamp(scrollTop)
    },
    setClientHeight: (n) => {
      clientHeight = n
      scrollTop = clamp(scrollTop)
    },
    setClientWidth: (n) => {
      clientWidth = n
    },
    setScrollTop: (n) => {
      scrollTop = clamp(n)
    },
    // Safari/WebKit rubber-band overscroll can report negative scrollTop on read
    // even though normal assignments clamp. Tests use this to exercise the hook's
    // logical scroll-position normalization.
    setRawScrollTop: (n) => {
      scrollTop = n
    },
    getScrollTop: () => scrollTop,
  }
}

/**
 * Register the shared scroll-test environment on the calling suite: a
 * synchronous-on-microtask requestAnimationFrame, the controllable
 * ResizeObserver, and an unconditional timer/mock reset after each test.
 * Every useChatScroll spec file calls this once at module top level (the
 * former top-level beforeAll/afterEach of the pre-split monolith).
 */
export function installScrollTestEnv(): void {
  beforeAll(() => {
    installControllableResizeObserver()
    // Run rAF synchronously on a microtask so tests can `await Promise.resolve()`
    // to flush scheduled scroll writes from the resize handler. Handles are real and
    // CANCELABLE: cancelAnimationFrame before the microtask fires drops the callback,
    // matching browser semantics -- with the old no-op cancel, every cancel path in the
    // hook (cancelScrollAnimation, the coalesced viewport refresh, onCleanup) was
    // unobservable, so a regression that dropped a cancel call still passed the suite.
    let nextRafHandle = 1
    const pendingRafCallbacks = new Map<number, FrameRequestCallback>()
    globalThis.requestAnimationFrame = ((cb: FrameRequestCallback) => {
      const handle = nextRafHandle++
      pendingRafCallbacks.set(handle, cb)
      queueMicrotask(() => {
        const pending = pendingRafCallbacks.get(handle)
        if (pending) {
          pendingRafCallbacks.delete(handle)
          pending(performance.now())
        }
      })
      return handle
    }) as typeof requestAnimationFrame
    globalThis.cancelAnimationFrame = ((handle: number) => {
      pendingRafCallbacks.delete(handle)
    }) as typeof cancelAnimationFrame
  })

  // Backstop: many tests install fake timers and restore them only inside their own
  // try/catch arms. If an assertion rejects in a microtask outside that wrapper, the
  // faked clock would strand into the next test and mis-measure velocity. Restore
  // timers (and any spies, defensively) unconditionally after every test so a single
  // leak can't cascade. (The beforeAll rAF/ResizeObserver globals are direct
  // assignments, not vi spies, so restoreAllMocks leaves them intact.)
  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
  })
}
