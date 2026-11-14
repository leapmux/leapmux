import type { AnchorOffsetGeometry } from './chatScrollAnchor'
import type { ChatScrollVirtualizer, ScrollContext, UseChatScrollOptions } from './useChatScroll'
import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ScrollAnchor } from '~/stores/chatTypes'
import { batch, createRoot, createSignal, untrack } from 'solid-js'
import { afterEach, beforeAll, vi } from 'vitest'
import { installControllableResizeObserver, triggerResizeObserversSync } from '~/test-support/resizeObserverStub'
import { anchorAtOffset, resolveAnchorScrollTop, resolveNearestAnchorScrollTop } from './chatScrollAnchor'
import { SCROLL_REST_QUIET_MS } from './chatScrollRest'
import { useChatScroll } from './useChatScroll'

/**
 * Shared test kit for the useChatScroll suites. The suite was split out of a
 * single 8k-line file into per-behavior spec files; these virtualizer/context/
 * DOM stubs and the environment hooks are the common prelude every split imports.
 */

/**
 * Every member of the virtualizer surface that the geometry builders below do NOT supply,
 * defaulted to no-ops: no layout shift, and scroll coordinates equal to content ones.
 * ChatScrollVirtualizer requires all of them; the stubs spread this so each builder stays
 * focused on the geometry it fakes. makeShiftVirtualizer overrides the shift members.
 *
 * Derived by SUBTRACTION from ChatScrollVirtualizer, not by re-listing the members. A hand
 * written second list drifts silently: it is why one new virtualizer member had to be typed
 * into the Pick in useChatScroll.ts AND into this file. With the Omit, a new member widens
 * this type on its own and fails to compile in exactly one place -- the literal below.
 */
export function virtualizerNoOps(): Omit<
  ChatScrollVirtualizer,
  'totalHeight' | 'geometryVersion' | 'updateViewport' | 'anchorAt' | 'scrollTopNearAnchor' | 'scrollTopForAnchor'
> {
  return {
    setFastScrollActive: () => {},
    layoutShift: () => 0,
    rebase: () => 0,
    contentTopOf: scrollTop => scrollTop,
    scrollTopOf: contentTop => contentTop,
  }
}

/**
 * Minimal virtualizer stub. `totalHeight` is a constant so the geometry
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
    ...virtualizerNoOps(),
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
    setAtBottom: () => {},
    isAtBottom: () => false,
    isFollowing: () => false,
    hasUnseenUpwardMove: () => false,
    isAnimating: () => false,
    refreshViewport: () => {},
    followTail: () => {},
    leaveFollow: () => {},
    rebase: () => {},
    placeAt: () => null,
    notePosition: () => {},
    syncAfterWrite: () => {},
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
    ...virtualizerNoOps(),
  }
  return { virt, setTotal }
}

/** The total height of rows with `heights`, with no gaps between the rows. */
function totalRowHeight(heights: readonly number[]): number {
  return heights.reduce((sum, h) => sum + h, 0)
}

/**
 * The anchor geometry of rows with `heights`, with no gaps between the rows: the one row
 * geometry of makeRowVirtualizer and makeShiftVirtualizer. Row `i` has the id
 * `${idPrefix}${i}` and the seq `i + 1`, because persisted message sequences start at one.
 * `indexOfId` accepts exactly these ids, so the ids that a stub issues and the ids that it
 * resolves cannot disagree.
 *
 * The stubs delegate the anchor math to the REAL pure functions over this geometry. Their
 * tests therefore exercise the production capture and resolve, and not a second
 * implementation that can drift from it. A zero-height row shares the offset of its
 * successor, so these tests also exercise the shared-offset tie-break of the anchor math.
 * The production virtualizer reserves a positive estimate for every row, but a caller can
 * still produce a run of zero-height rows.
 */
function rowAnchorGeometry(heights: readonly number[], idPrefix: string): AnchorOffsetGeometry {
  // Length n + 1: offsets[i] is the top of row i, and offsets[n] is the total height.
  const offsets = [0]
  for (const h of heights)
    offsets.push((offsets[offsets.length - 1] ?? 0) + h)
  return {
    list: heights.map((_, i) => ({
      id: `${idPrefix}${i}`,
      seq: BigInt(i + 1),
    })),
    // The largest index whose top offset is at most y (the row that contains y), clamped
    // to [0, n - 1].
    indexAtOffset: (y) => {
      let idx = 0
      for (let i = 0; i < heights.length; i++) {
        // The loop condition keeps `i` inside `offsets` (offsets[0] is 0); `?? 0` only
        // satisfies the type checker.
        if ((offsets[i] ?? 0) <= y)
          idx = i
        else
          break
      }
      return idx
    },
    // Compare the prefix as plain text, then require the decimal form that the ids above
    // use: digits with no leading zero, so `r07` does not resolve to row 7. A RegExp built
    // from `idPrefix` would read a metacharacter in the prefix as regex syntax.
    indexOfId: (id) => {
      if (!id.startsWith(idPrefix))
        return -1
      const digits = id.slice(idPrefix.length)
      if (!/^(?:0|[1-9]\d*)$/.test(digits))
        return -1
      const idx = Number(digits)
      return idx < heights.length
        ? idx
        : -1
    },
    offsetOfIndex: i => offsets[Math.max(0, Math.min(i, heights.length))] ?? 0,
    heightOfIndex: i => heights[i] ?? 0,
    gapAfter: () => 0,
  }
}

/**
 * Virtualizer stub backed by a mutable per-row height map, so a measurement that
 * grows or shrinks a row drives the geometry effect through a REAL offset map --
 * `anchorAt` / `scrollTopForAnchor` resolve against the current heights, exactly
 * like the production virtualizer. Row ids are `g${gen}_${index}`; `seq` mirrors
 * the index. `setRowHeight` mutates one row and bumps `geometryVersion`,
 * reproducing a DOM measurement landing after the row mounted (a freshly-mounted
 * row settling from its estimate to its real height once the pipeline measures it).
 * `replaceWindow` swaps in a fresh row set under a NEW generation
 * -- so every prior anchor id stops resolving -- modeling jumpToOldest/jumpToLatest
 * replacing the loaded window (`chatHistoryPaginator`), which is what leaves the
 * pre-jump anchor stale for a restore to recover from. It has no layout shift.
 */
export function makeRowVirtualizer(initialHeights: number[]) {
  const [heights, setHeights] = createSignal<number[]>(initialHeights)
  const [gen, setGen] = createSignal(0)
  const [geometryVersion, setGeometryVersion] = createSignal(0)
  const total = () => totalRowHeight(heights())
  // Row id encodes the window GENERATION so a replaced window's ids stop resolving (the
  // real virtualizer keys the offset map by row id; a re-fetched window has fresh rows).
  const geometry = () => rowAnchorGeometry(heights(), `g${gen()}_`)
  const virt: ChatScrollVirtualizer = {
    totalHeight: total,
    geometryVersion: () => geometryVersion(),
    updateViewport: () => {},
    anchorAt: (y: number): ScrollAnchor | null => anchorAtOffset(geometry(), y),
    scrollTopNearAnchor: (anchor: ScrollAnchor): number | null => resolveNearestAnchorScrollTop(geometry(), anchor),
    scrollTopForAnchor: (anchor: ScrollAnchor): number | null => resolveAnchorScrollTop(geometry(), anchor),
    ...virtualizerNoOps(),
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
  return {
    virt,
    setRowHeight,
    replaceWindow,
    total,
  }
}

/**
 * Virtualizer stub with rows of `heights` (ids `r0`, `r1`, ..., seq `i + 1`, no gaps) and a
 * layout shift that the test controls, over the fake element `div`. Its scroll-coordinate
 * functions, `contentTopOf`, and `scrollTopOf` apply the shift as the real virtualizer
 * does. Each change function bumps `geometryVersion` in one batch, after it changed the
 * `scrollHeight` of `div` where the change grows or shrinks the spacer:
 *
 * - `changeAbove(px)`: row 0 changes by `px`, and the shift falls by `px`, as the real rule
 *   keeps the rows below in place. The `scrollHeight` of `div` does not change. Row 0 must
 *   stay above 0, so a test with a large negative `px` gives row 0 a large height.
 * - `changeBelow(px)`: the last row changes by `px`, and the `scrollHeight` of `div`
 *   changes by `px`.
 * - `growTailPinned(px)`: the last row grows by `px`, and the shift falls by `px`, as the
 *   real rule keeps the bottom in place in follow mode. The `scrollHeight` does not change.
 * - `changeAboveKeepingTotal(px)`: row 0 grows by `px` and the last row shrinks by `px`, so
 *   the total height holds and only `geometryVersion` reports the change. The shift falls by
 *   `px`, as for `changeAbove`. The `scrollHeight` does not change.
 *
 * `rebase()` returns 0 and changes nothing for a shift of 0 or a hidden `div`. Otherwise it
 * returns the previous shift, sets the shift to 0, counts itself, and changes `div` as the
 * real rebase does: `scrollHeight` and `scrollTop` both change by `-shift`, the position
 * first for a positive shift and the layout first for a negative one.
 */
export function makeShiftVirtualizer(heights: number[], div: FakeScrollDiv) {
  const [rows, setRows] = createSignal<number[]>([...heights])
  const [shift, setShift] = createSignal(0)
  const [geometryVersion, setGeometryVersion] = createSignal(0)
  let rebases = 0
  const total = () => totalRowHeight(rows())
  // The geometry is in content coordinates. The members of `virt` below convert from and
  // to scroll coordinates with the shift.
  const geometry = () => rowAnchorGeometry(rows(), 'r')
  const toScroll = (contentTop: number | null) => contentTop === null
    ? null
    : contentTop + shift()
  const virt: ChatScrollVirtualizer = {
    ...virtualizerNoOps(),
    totalHeight: total,
    geometryVersion: () => geometryVersion(),
    updateViewport: () => {},
    layoutShift: () => shift(),
    contentTopOf: scrollTop => scrollTop - shift(),
    scrollTopOf: contentTop => contentTop + shift(),
    anchorAt: (scrollTop: number): ScrollAnchor | null => anchorAtOffset(geometry(), scrollTop - shift()),
    scrollTopForAnchor: (anchor: ScrollAnchor): number | null => toScroll(resolveAnchorScrollTop(geometry(), anchor)),
    scrollTopNearAnchor: (anchor: ScrollAnchor): number | null => toScroll(resolveNearestAnchorScrollTop(geometry(), anchor)),
    rebase: () => {
      const previous = untrack(shift)
      if (previous === 0 || div.el.clientHeight === 0)
        return 0
      const target = div.el.scrollTop - previous
      if (previous > 0) {
        div.el.scrollTop = target
        div.setScrollHeight(div.el.scrollHeight - previous)
      }
      else {
        div.setScrollHeight(div.el.scrollHeight - previous)
        div.el.scrollTop = target
      }
      setShift(0)
      rebases++
      return previous
    },
  }
  type RowChange = [index: (hs: readonly number[]) => number, px: number]
  const changeRows = (changes: readonly RowChange[], shiftBy: number) => {
    batch(() => {
      setRows((prev) => {
        const next = [...prev]
        for (const [index, px] of changes) {
          const i = index(next)
          next[i] = (next[i] ?? 0) + px
        }
        return next
      })
      if (shiftBy !== 0)
        setShift(s => s + shiftBy)
      setGeometryVersion(v => v + 1)
    })
  }
  const first = () => 0
  const last = (hs: readonly number[]) => hs.length - 1
  return {
    virt,
    changeAbove: (px: number) => changeRows([
      [first, px],
    ], -px),
    changeBelow: (px: number) => {
      div.setScrollHeight(div.el.scrollHeight + px)
      changeRows([
        [last, px],
      ], 0)
    },
    growTailPinned: (px: number) => changeRows([
      [last, px],
    ], -px),
    changeAboveKeepingTotal: (px: number) => changeRows([
      [first, px],
      [last, -px],
    ], -px),
    rebaseCount: () => rebases,
  }
}

export interface FakeScrollDiv {
  el: HTMLDivElement
  setScrollHeight: (n: number) => void
  setClientHeight: (n: number) => void
  setClientWidth: (n: number) => void
  setScrollTop: (n: number) => void
  setRawScrollTop: (n: number) => void
  getScrollTop: () => number
  /**
   * The number of writes of the code under test to the scroll position: each assignment to
   * `el.scrollTop` and each `el.scrollBy` call. The setters of this fake do not count.
   */
  scrollTopWrites: () => number
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
  let writes = 0
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
      writes++
      scrollTop = clamp(v)
    },
    configurable: true,
  })
  // jsdom's scrollBy is a no-op; apply the vertical delta so pageScroll moves.
  // Both overloads are honored -- scrollBy(x, y) reads the SECOND argument as the
  // vertical delta, like a real browser -- so a call-form refactor in production
  // can't silently turn every page jump into a no-op that trips the at-edge branch.
  el.scrollBy = ((opts?: ScrollToOptions | number, y?: number) => {
    const top = typeof opts === 'number'
      ? (y ?? 0)
      : (opts?.top ?? 0)
    writes++
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
    scrollTopWrites: () => writes,
  }
}

/** The time between two scroll events of a harness, as a steady scroll delivers them. */
export const HARNESS_EVENT_GAP_MS = 10

export interface ScrollHarnessOptions {
  /** Row heights (px). Default: 50 rows of 100px. Their sum is the `scrollHeight`. */
  heights?: number[]
  /** Whether newer messages exist beyond the window at the mount. Default: false. */
  hasNewer?: boolean
  /** The `scrollTop` of the element before the mount. */
  scrollTopBeforeMount?: number
  /** The `clientHeight` of the element at the mount. Default: 500. 0 mounts a hidden pane. */
  clientHeight?: number
  /** The `scrollHeight` of the element. Default: the sum of `heights`. */
  scrollHeight?: number
  messages?: AgentChatMessage[]
  /** More options of the hook: callbacks and accessors that a test asserts on. */
  hookOptions?: Partial<UseChatScrollOptions>
}

/**
 * The harness of the hook tests: a fake element with `clientHeight` 500, a
 * makeShiftVirtualizer over it, fake timers, and a mounted hook. The test continues
 * outside the reactive root, so a signal write runs its effects at once, as a browser
 * event does.
 *
 * - `scrollTo(x)`: the reader scrolls to `x`, 10ms after the previous event.
 * - `anchoredAt(x)`: `scrollTo(x)`, then the quiet time and one frame, so that the
 *   scroller rests.
 * - `resizeFlush()`: a ResizeObserver delivery, then one animation frame.
 * - `hideThenShow()`: `clientHeight` 0 and a resize flush, then 500 and a second one. Both
 *   flushes are necessary: the hook knows that a pane was hidden only from a flush at
 *   the height 0.
 */
export function mountScrollHarness(opts: ScrollHarnessOptions = {}) {
  vi.useFakeTimers({
    toFake: [
      'setTimeout',
      'clearTimeout',
      'setInterval',
      'clearInterval',
      'performance',
      'Date',
    ],
  })
  const heights: number[] = opts.heights ?? Array.from<number>({ length: 50 }).fill(100)
  const div = makeFakeScrollDiv()
  div.setClientHeight(opts.clientHeight ?? 500)
  div.setScrollHeight(opts.scrollHeight ?? totalRowHeight(heights))
  if (opts.scrollTopBeforeMount !== undefined)
    div.setScrollTop(opts.scrollTopBeforeMount)
  const shift = makeShiftVirtualizer(heights, div)
  const setFastScrollActive = vi.fn()
  const updateViewport = vi.fn()
  const virt: ChatScrollVirtualizer = { ...shift.virt, setFastScrollActive, updateViewport }
  const [hasNewer, setHasNewer] = createSignal(opts.hasNewer ?? false)
  const [messages, setMessages] = createSignal<AgentChatMessage[]>(opts.messages ?? [])
  const onMomentumScroll = vi.fn()
  let dispose!: () => void
  const hook = createRoot((d) => {
    dispose = d
    const mounted = useChatScroll({
      virtualizer: virt,
      messages,
      hasNewerMessages: hasNewer,
      onMomentumScroll,
      ...opts.hookOptions,
    })
    mounted.attachListRef(div.el)
    return mounted
  })
  const frame = () => new Promise<void>((resolve) => {
    requestAnimationFrame(() => resolve())
  })
  const scrollTo = (top: number) => {
    vi.advanceTimersByTime(HARNESS_EVENT_GAP_MS)
    div.setScrollTop(top)
    hook.handlers.onScroll()
  }
  const rest = async () => {
    vi.advanceTimersByTime(SCROLL_REST_QUIET_MS)
    await frame()
  }
  const anchoredAt = async (top: number) => {
    scrollTo(top)
    await rest()
  }
  const resizeFlush = async () => {
    triggerResizeObserversSync()
    await frame()
  }
  const hideThenShow = async () => {
    div.setClientHeight(0)
    await resizeFlush()
    div.setClientHeight(500)
    await resizeFlush()
  }
  return {
    ...shift,
    hook,
    div,
    virt,
    setHasNewer,
    setMessages,
    onMomentumScroll,
    setFastScrollActive,
    updateViewport,
    frame,
    scrollTo,
    rest,
    anchoredAt,
    resizeFlush,
    hideThenShow,
    dispose,
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
