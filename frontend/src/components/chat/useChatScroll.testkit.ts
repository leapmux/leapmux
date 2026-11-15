import type { LayoutSnapshot } from './chatLayoutShift'
import type { AnchorOffsetGeometry } from './chatScrollAnchor'
import type { ChatScrollVirtualizer, ScrollContext, UseChatScrollOptions } from './useChatScroll'
import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ScrollAnchor } from '~/stores/chatTypes'
import { batch, createRoot, createSignal, untrack } from 'solid-js'
import { afterEach, beforeAll, onTestFinished, vi } from 'vitest'
import { largestIndexWhere } from '~/lib/binarySearch'
import { deferred, flush } from '~/test-support/async'
import { installControllableResizeObserver, triggerResizeObserversSync } from '~/test-support/resizeObserverStub'
import { nextLayoutShift } from './chatLayoutShift'
import { anchorAtOffset, resolveAnchorScrollTop, resolveNearestAnchorScrollTop } from './chatScrollAnchor'
import { readLogicalScrollTop } from './chatScrollGeometry'
import { SCROLL_REST_QUIET_MS } from './chatScrollRest'
import { applyRebaseWrite, placementScrollTop } from './createLayoutShift'
import { useChatScroll } from './useChatScroll'

/**
 * Shared test kit for the useChatScroll suites. The suite was split out of a
 * single 8k-line file into per-behavior spec files; these virtualizer/context/
 * DOM stubs and the environment hooks are the common prelude every split imports.
 */

/**
 * The members of the virtualizer surface that withContentAnchors derives from the others:
 * the anchor resolvers in content coordinates.
 */
type ContentAnchorResolvers = 'anchorAtContent' | 'contentTopForAnchor'

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
  | ContentAnchorResolvers
> {
  return {
    setFastScrollActive: () => {},
    // A stub has no height that the reader did not see: every row shows its height.
    showsHeight: () => true,
    notePlacement: () => {},
    layoutShift: () => 0,
    rebase: () => 0,
    contentTopOf: scrollTop => scrollTop,
    scrollTopOf: contentTop => contentTop,
    placementScrollTopOf: contentTop => placementScrollTop(contentTop, top => top),
  }
}

/**
 * Complete a virtualizer stub with the anchor resolvers in content coordinates, derived
 * from its scroll resolvers through its own `contentTopOf` and `scrollTopOf`, as the real
 * virtualizer relates them. A stub that sets `anchorAt` or `scrollTopForAnchor` then
 * answers the content resolvers consistently without restating them.
 */
export function withContentAnchors(stub: Omit<ChatScrollVirtualizer, ContentAnchorResolvers>): ChatScrollVirtualizer {
  return {
    ...stub,
    anchorAtContent: contentTop => stub.anchorAt(stub.scrollTopOf(contentTop)),
    contentTopForAnchor: (anchor) => {
      const scrollTop = stub.scrollTopForAnchor(anchor)
      return scrollTop === null
        ? null
        : stub.contentTopOf(scrollTop)
    },
  }
}

/**
 * Minimal virtualizer stub. `totalHeight` is a constant so the geometry
 * effect never fires — these tests exercise the sticky-bottom / resize / scroll
 * logic, which is independent of the offset map. Tests that need anchoring
 * behavior use makeRowVirtualizer or makeShiftVirtualizer, and the anchor math has its
 * own tests in chatScrollAnchor.test.ts and useChatVirtualizer.geometry.test.ts.
 */
export function makeStubVirtualizer(): ChatScrollVirtualizer {
  return withContentAnchors({
    totalHeight: () => 0,
    geometryVersion: () => 0,
    updateViewport: () => {},
    anchorAt: () => null,
    scrollTopNearAnchor: () => null,
    scrollTopForAnchor: () => null,
    ...virtualizerNoOps(),
  })
}

/**
 * A fully-stubbed ScrollContext (the scroll primitives of useChatScroll and the hook
 * actions that its helpers share). Pass overrides for the fields a test wires to its
 * element or asserts on. Every other member defaults to a no-op, or to false for a
 * predicate, except three that act as the hook's do: `getVisibleEl`, `placeAtAnchor`, and
 * `writeOwnScrollTop`.
 */
export function makeScrollContext(overrides: Partial<ScrollContext> = {}): ScrollContext {
  const ctx: ScrollContext = {
    getEl: () => undefined,
    // The element of this context's `getEl` while it has a height, as the hook defines it.
    getVisibleEl: () => {
      const el = ctx.getEl()
      return el !== undefined && el.clientHeight > 0
        ? el
        : undefined
    },
    virt: makeStubVirtualizer(),
    isFollowing: () => false,
    followRequested: () => false,
    hasNewerMessages: () => false,
    noteScrollRange: () => {},
    isAnimating: () => false,
    followTail: () => {},
    leaveFollow: () => {},
    checkPosition: () => {},
    cancelScrollAnimation: () => {},
    retakeScrollControl: () => {},
    rearmScrollBufferFiller: () => {},
    rebase: () => {},
    placeAt: () => null,
    // Places through the `placeAt` and the virtualizer of this context, as the hook does,
    // without the hold of an anchor whose row has an estimated height.
    placeAtAnchor: (anchor, nearest) => ctx.placeAt(() => nearest
      ? ctx.virt.scrollTopNearAnchor(anchor)
      : ctx.virt.scrollTopForAnchor(anchor)),
    // Writes to the element of this context's `getVisibleEl`, as the hook does, with no
    // record and no refresh.
    writeOwnScrollTop: (top) => {
      const el = ctx.getVisibleEl()
      if (el)
        el.scrollTop = top
    },
    recordPosition: () => {},
    ...overrides,
  }
  return ctx
}

/**
 * Virtualizer stub whose total height is a controllable signal. Bumping it
 * drives the geometry effect (the path that replaced the content-element
 * ResizeObserver for detecting content growth).
 */
export function makeGrowableVirtualizer() {
  const [total, setTotal] = createSignal(0)
  const virt: ChatScrollVirtualizer = {
    ...makeStubVirtualizer(),
    totalHeight: () => total(),
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
    // to [0, n - 1]. The search keeps `i` inside `offsets`; `?? 0` only satisfies the type
    // checker.
    indexAtOffset: y => largestIndexWhere(heights.length, i => (offsets[i] ?? 0) <= y),
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
 * like the production virtualizer. Row ids are `g${gen}_${index}`; `seq` is the
 * index plus one. `setRowHeight` mutates one row and bumps `geometryVersion`,
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
  const anchorAtContent = (y: number): ScrollAnchor | null => anchorAtOffset(geometry(), y)
  const contentTopForAnchor = (anchor: ScrollAnchor): number | null => resolveAnchorScrollTop(geometry(), anchor)
  // With no layout shift, a scroll coordinate is a content coordinate, so the scroll
  // resolvers are the content ones.
  const virt: ChatScrollVirtualizer = {
    totalHeight: total,
    geometryVersion: () => geometryVersion(),
    updateViewport: () => {},
    anchorAtContent,
    contentTopForAnchor,
    anchorAt: anchorAtContent,
    scrollTopNearAnchor: (anchor: ScrollAnchor): number | null => resolveNearestAnchorScrollTop(geometry(), anchor),
    scrollTopForAnchor: contentTopForAnchor,
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
 * The layout snapshot of the anchor geometry `g`, as the shift rule reads it. Every row
 * counts as measured, except the rows at the indexes in `estimated`.
 */
function layoutSnapshotOf(g: AnchorOffsetGeometry, estimated: ReadonlySet<number>): LayoutSnapshot {
  const n = g.list.length
  return {
    list: g.list,
    indexById: new Map(g.list.map((row, i) => [row.id, i])),
    offsets: Float64Array.from({ length: n + 1 }, (_, i) => g.offsetOfIndex(i)),
    heights: Float64Array.from({ length: n }, (_, i) => g.heightOfIndex(i)),
    measured: Uint8Array.from({ length: n }, (_, i) => estimated.has(i)
      ? 0
      : 1),
    n,
  }
}

export interface ShiftVirtualizerDeps {
  /**
   * Whether the shift rule holds the bottom of the content, as the `pinBottom` option of the
   * virtualizer. mountScrollHarness reads the `isFollowing` of the mounted hook, as ChatView
   * does.
   */
  pinBottom: () => boolean
  /**
   * The indexes of the rows whose height is an estimate, as a row that has not measured yet.
   * `showsHeight` reports such a row as not shown, and a change of such a row measures it.
   * Default: none, every row is measured.
   */
  estimatedRows?: readonly number[]
  /**
   * The indexes of the rows with a measured height that the reader did not see, as a row
   * that a hidden copy measured and whose mounted copy no read measured yet. `showsHeight`
   * reports such a row as not shown. A change of such a row is the first read of its copy:
   * the shift rule holds its bottom edge (`sawHeight` false), and the row then shows its
   * height. Default: none, the reader saw each measured height.
   */
  unseenRows?: readonly number[]
}

/**
 * Virtualizer stub with rows of `heights` (ids `r0`, `r1`, ..., seq `i + 1`, no gaps) over the
 * fake element `div`, whose layout shift follows the REAL rule (nextLayoutShift). Its
 * scroll-coordinate functions, `contentTopOf`, and `scrollTopOf` apply the shift as the real
 * virtualizer does, with no inset, and a placement converts through the real rule
 * (placementScrollTop). Its content-coordinate resolvers convert nothing.
 *
 * Each change function computes the next shift as the computation of the real layout shift
 * does: nextLayoutShift over a snapshot of the rows before and after the change, with every
 * row measured except `deps.estimatedRows` (a change of such a row measures it), each
 * measured height one that the reader saw except those of `deps.unseenRows` (a change of
 * such a row is the first read of its copy), no move inside a row (`intraRowShift` 0), no
 * row that came into the view over the top edge (`enteredFromAbove` false), and
 * `deps.pinBottom()` read before
 * the change reaches the element. For a hidden `div`, the shift becomes 0. Then the
 * `scrollHeight` of `div` changes by the change of the spacer height, `max(0, total +
 * shift)`, as the style binding of ChatView writes it, and the browser clamps `scrollTop`
 * to the new range. Last, the rows, the shift, and `geometryVersion` change in one batch. A
 * test that calls `div.setScrollHeight` itself changes the content outside the rows, such as
 * a tail sibling of the spacer.
 *
 * - `changeAbove(px)`: row 0 changes by `px`. Row 0 must stay above 0, so a test with a
 *   large negative `px` gives row 0 a large height.
 * - `changeBelow(px)`: the last row changes by `px`.
 * - `changeAboveKeepingTotal(px)`: row 0 grows by `px` and the last row shrinks by `px`, so
 *   the total height holds and only `geometryVersion` reports the change.
 *
 * `rebase()` writes through applyRebaseWrite, as the real rebase does: `scrollTop` changes by
 * `-shift`, and the spacer part of the `scrollHeight` of `div` follows the shift. It keeps a
 * remainder as the shift, and it changes nothing for a shift below SCROLL_ROUNDING_MAX_PX or a
 * hidden `div`. It returns the part of the shift that it removed, and counts each rebase that
 * writes.
 */
export function makeShiftVirtualizer(heights: number[], div: FakeScrollDiv, deps: ShiftVirtualizerDeps) {
  const [rows, setRows] = createSignal<number[]>([...heights])
  const [shift, setShift] = createSignal(0)
  const [geometryVersion, setGeometryVersion] = createSignal(0)
  // The rows whose height is still an estimate.
  const estimated = new Set(deps.estimatedRows ?? [])
  // The rows whose measured height the reader did not see yet.
  const unseen = new Set(deps.unseenRows ?? [])
  // The snapshot that the current shift applies to.
  let shiftedSnapshot = layoutSnapshotOf(rowAnchorGeometry(heights, 'r'), estimated)
  let rebases = 0
  const total = () => totalRowHeight(rows())
  const spacerHeightFor = (totalHeight: number, layoutShift: number) => Math.max(0, totalHeight + layoutShift)
  // The geometry is in content coordinates. The members of `virt` below convert from and
  // to scroll coordinates with the shift.
  const geometry = () => rowAnchorGeometry(rows(), 'r')
  const contentTopOf = (scrollTop: number) => scrollTop - shift()
  const scrollTopOf = (contentTop: number) => contentTop + shift()
  const placementScrollTopOf = (contentTop: number) => placementScrollTop(contentTop, scrollTopOf)
  const toScroll = (contentTop: number | null) => contentTop === null
    ? null
    : placementScrollTopOf(contentTop)
  const anchorAtContent = (contentTop: number): ScrollAnchor | null => anchorAtOffset(geometry(), contentTop)
  const contentTopForAnchor = (anchor: ScrollAnchor): number | null => resolveAnchorScrollTop(geometry(), anchor)
  const virt: ChatScrollVirtualizer = {
    ...virtualizerNoOps(),
    // As the real virtualizer: false for a row with an estimated height, for a row whose
    // measured height the reader did not see, and for an id that is not in the list.
    showsHeight: (id: string) => {
      const index = geometry().indexOfId(id)
      return index >= 0 && !estimated.has(index) && !unseen.has(index)
    },
    totalHeight: total,
    geometryVersion: () => geometryVersion(),
    updateViewport: () => {},
    layoutShift: () => shift(),
    contentTopOf,
    scrollTopOf,
    placementScrollTopOf,
    anchorAtContent,
    contentTopForAnchor,
    anchorAt: (scrollTop: number): ScrollAnchor | null => anchorAtContent(contentTopOf(scrollTop)),
    scrollTopForAnchor: (anchor: ScrollAnchor): number | null => toScroll(contentTopForAnchor(anchor)),
    scrollTopNearAnchor: (anchor: ScrollAnchor): number | null => toScroll(resolveNearestAnchorScrollTop(geometry(), anchor)),
    rebase: () => {
      const previous = untrack(shift)
      if (div.el.clientHeight === 0)
        return 0
      const totalHeight = untrack(total)
      const outsideRows = div.el.scrollHeight - spacerHeightFor(totalHeight, previous)
      const kept = applyRebaseWrite(div.el, previous, s => div.setScrollHeight(outsideRows + spacerHeightFor(totalHeight, s)))
      const removed = previous - kept
      if (removed !== 0) {
        setShift(kept)
        rebases++
      }
      return removed
    },
  }
  type RowChange = [index: (hs: readonly number[]) => number, px: number]
  const changeRows = (changes: readonly RowChange[]) => {
    const previousRows = untrack(rows)
    const nextRows = [...previousRows]
    // The rows whose change is the first read of their copy.
    const firstReads = new Set<number>()
    for (const [index, px] of changes) {
      const i = index(nextRows)
      nextRows[i] = (nextRows[i] ?? 0) + px
      // The change is the measure of a row with an estimated height.
      estimated.delete(i)
      if (unseen.delete(i))
        firstReads.add(i)
    }
    const nextGeometry = rowAnchorGeometry(nextRows, 'r')
    const next = layoutSnapshotOf(nextGeometry, estimated)
    const previousShift = untrack(shift)
    const nextShift = div.el.clientHeight === 0
      ? 0
      : nextLayoutShift({
          previous: shiftedSnapshot,
          next,
          previousShift,
          scrollTop: readLogicalScrollTop(div.el),
          viewHeight: div.el.clientHeight,
          pinBottom: deps.pinBottom(),
          intraRowShift: () => 0,
          sawHeight: id => !firstReads.has(nextGeometry.indexOfId(id)),
          // The stub mounts no row, so no row came into the view over the top edge.
          enteredFromAbove: () => false,
        })
    shiftedSnapshot = next
    const spacerChange = spacerHeightFor(totalRowHeight(nextRows), nextShift)
      - spacerHeightFor(totalRowHeight(previousRows), previousShift)
    div.setScrollHeight(div.el.scrollHeight + spacerChange)
    batch(() => {
      setRows(nextRows)
      setShift(nextShift)
      setGeometryVersion(v => v + 1)
    })
  }
  const first = () => 0
  const last = (hs: readonly number[]) => hs.length - 1
  return {
    virt,
    changeAbove: (px: number) => changeRows([
      [first, px],
    ]),
    changeBelow: (px: number) => changeRows([
      [last, px],
    ]),
    changeAboveKeepingTotal: (px: number) => changeRows([
      [first, px],
      [last, -px],
    ]),
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

/**
 * A scroller with `clientHeight` 300, `scrollHeight` 1000, and the given `scrollTop` and
 * `overflow-y` (default `auto`), inside `parent`. The document holds `parent`, because a
 * detached element has no computed style; the caller removes it after the test. Returns a
 * child of the scroller: the target of an input inside it.
 */
export function innerScrollerTarget(parent: HTMLElement, opts: { scrollTop: number, overflowY?: string }): HTMLElement {
  const inner = makeFakeScrollDiv()
  inner.el.style.overflowY = opts.overflowY ?? 'auto'
  inner.setClientHeight(300)
  inner.setScrollHeight(1000)
  inner.setScrollTop(opts.scrollTop)
  const target = document.createElement('span')
  inner.el.appendChild(target)
  parent.appendChild(inner.el)
  if (!parent.isConnected)
    document.body.appendChild(parent)
  return target
}

/** A latest-page jump that the test settles: `onJumpToLatest` returns its promise. */
export function deferredJump() {
  const jump = deferred<void>()
  return {
    ...jump,
    onJumpToLatest: vi.fn(() => jump.promise),
  }
}

/** Let the branches of a settled jump run: its `then` or its `catch`, and what they start. */
export async function settleJump(promise: Promise<void>): Promise<void> {
  await promise.catch(() => undefined)
  await flush()
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
  /** The indexes of the rows whose height is an estimate (see ShiftVirtualizerDeps). */
  estimatedRows?: readonly number[]
  /** The indexes of the rows with a measured height that the reader did not see (see ShiftVirtualizerDeps). */
  unseenRows?: readonly number[]
  messages?: AgentChatMessage[]
  /** More options of the hook: callbacks and accessors that a test asserts on. */
  hookOptions?: Partial<UseChatScrollOptions>
}

/**
 * The harness of the hook tests: a fake element with `clientHeight` 500, a
 * makeShiftVirtualizer over it whose rule holds the bottom while the hook follows the tail,
 * fake timers, and a mounted hook. The test continues outside the reactive root, so a
 * signal write runs its effects at once, as a browser event does.
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
  // Bound to the mounted hook below, as ChatView binds the bottom pin of its virtualizer.
  // A fresh list follows until then.
  let isFollowingTail: () => boolean = () => true
  const shift = makeShiftVirtualizer(heights, div, {
    pinBottom: () => isFollowingTail(),
    ...(opts.estimatedRows === undefined
      ? {}
      : { estimatedRows: opts.estimatedRows }),
    ...(opts.unseenRows === undefined
      ? {}
      : { unseenRows: opts.unseenRows }),
  })
  const setFastScrollActive = vi.fn()
  const updateViewport = vi.fn()
  const notePlacement = vi.fn()
  const virt: ChatScrollVirtualizer = {
    ...shift.virt,
    setFastScrollActive,
    updateViewport,
    notePlacement,
  }
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
    isFollowingTail = mounted.isFollowing
    mounted.attachListRef(div.el)
    return mounted
  })
  // A test that fails before its own `dispose()` must not leave the hook mounted: its window
  // listeners and timers would act in the next test. A second dispose does nothing.
  onTestFinished(() => dispose())
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
    notePlacement,
    frame,
    scrollTo,
    rest,
    anchoredAt,
    resizeFlush,
    hideThenShow,
    dispose,
  }
}

/** The harness of mountScrollHarness. */
export type ScrollHarness = ReturnType<typeof mountScrollHarness>

/**
 * A reader at 2000 in a window paged away from the live tail, who then requested the tail
 * (forceScrollToBottom): the latest-page jump stays pending until the test settles `jump`.
 * `mount` is the harness of the suite: mountScrollHarness, or a wrapper that returns it
 * with more members.
 */
export async function mountWithPendingJump<H extends ScrollHarness>(mount: (opts: ScrollHarnessOptions) => H) {
  const jump = deferredJump()
  const h = mount({
    hasNewer: true,
    hookOptions: { onJumpToLatest: jump.onJumpToLatest },
  })
  await h.anchoredAt(2000)
  h.hook.forceScrollToBottom()
  return { h, jump }
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
