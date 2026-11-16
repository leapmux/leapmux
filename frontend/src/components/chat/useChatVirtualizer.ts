import type { Accessor } from 'solid-js'
import type { LayoutSnapshot } from './chatLayoutShift'
import type { RowHeightCache } from './chatRowHeightCache'
import type { ChatRowItem } from './chatRowItem'
import type { AnchorOffsetGeometry } from './chatScrollAnchor'
import type { LayoutShiftSurface } from './createLayoutShift'
import type { RowContentReadiness } from './createRowMeasurer'
import type { ScrollAnchor } from '~/stores/chatTypes'
import { createEffect, createMemo, createSignal, onCleanup, untrack } from 'solid-js'
import { largestIndexWhere, smallestIndexWhere } from '~/lib/binarySearch'
import { clamp } from '~/lib/clamp'
import { monotonicNow } from '~/lib/monotonicNow'
import { TOP_EDGE_SLACK_PX } from './chatLayoutShift'
import { BAND_BORDER_PX, messageBandKind } from './chatRowGeometry'
import { anchorAtOffset, resolveAnchorScrollTop, resolveNearestAnchorScrollTop } from './chatScrollAnchor'
import { warnSlowScrollPhase } from './chatScrollGeometry'
import { createLayoutShift } from './createLayoutShift'
import { createRowMeasurer } from './createRowMeasurer'

export interface ChatVirtualizerRange {
  /** First rendered row index (inclusive). */
  start: number
  /** One past the last rendered row index (exclusive). */
  end: number
}

/**
 * The slice that `updateViewport` committed last: its indices, and the ids of its
 * first and last row at the moment of the commit. `range` maps the two ids to their
 * current indices, so the slice follows its rows when the list changes before the
 * next commit.
 */
interface CommittedRange extends ChatVirtualizerRange {
  firstId: string | undefined
  lastId: string | undefined
}

/**
 * Map a committed slice onto the current row list through the ids of its edge rows.
 *
 * A prepend or a trim above the slice moves every index. The scroll hook commits a
 * new slice only in a later effect, so slicing the list by the old indices selected
 * other rows for one update: every mounted row was destroyed and built again. The
 * ids keep the same rows selected instead. When one id left the list, the slice
 * keeps its length from the other one. When both left, the stored indices are the
 * best guess. A non-empty commit gives a non-empty list a non-empty slice inside its
 * index range. An empty commit (none yet, or one over an empty list) gives no row until
 * the next commit: the scroll hook commits the first slice at its mount, and a row that
 * rendered before it would be built only to be removed.
 */
function followCommittedRange(
  committed: CommittedRange,
  indexById: ReadonlyMap<string, number>,
  n: number,
): ChatVirtualizerRange {
  const length = committed.end - committed.start
  if (n === 0 || length <= 0)
    return { start: 0, end: 0 }
  const first = committed.firstId === undefined
    ? undefined
    : indexById.get(committed.firstId)
  const last = committed.lastId === undefined
    ? undefined
    : indexById.get(committed.lastId)
  let start = committed.start
  let end = committed.end
  if (first !== undefined && last !== undefined) {
    start = first
    end = last + 1
  }
  else if (first !== undefined) {
    start = first
    end = first + length
  }
  else if (last !== undefined) {
    end = last + 1
    start = end - length
  }
  start = clamp(start, 0, n - 1)
  end = clamp(end, start + 1, n)
  return { start, end }
}

/**
 * Direction of a fling's render-ahead overscan: `'older'` extends the slice
 * toward index 0 (scrolling UP), `'newer'` toward the tail (scrolling DOWN). Names
 * mirror the scroll hook's `lastScrollDir` so the look-ahead follows the gesture.
 */
export type ViewportLeadDir = 'older' | 'newer'

/**
 * Render-ahead overscan extension: extend the slice by `px` extra pixels in the
 * fling direction `dir`. A single object so the px magnitude and its direction
 * can't be passed half-set (a px with no dir would be silently dropped).
 */
export interface ViewportLead {
  dir: ViewportLeadDir
  px: number
}

export interface UseChatVirtualizerOptions {
  /** Reactive list of rows to virtualize, in display order. */
  items: Accessor<ChatRowItem[]>
  /** Off-screen pixels to keep mounted on each side of the viewport. */
  overscanPx?: Accessor<number> | number
  /**
   * The cache of measured row heights. The caller owns it, and shares it with the other
   * readers of the heights: the row admission, the staging bands, and the persistence.
   * Every row of `items` has a height there in the app, because the admission passes
   * measured rows only. A development build reports a row of `items` with no height.
   */
  heightCache: RowHeightCache
  /** Inter-row gap (px) between two consecutive span-line rows (~--space-2). */
  gapSmallPx?: Accessor<number> | number
  /** Inter-row gap (px) for all other adjacent rows (~--space-5). */
  gapLargePx?: Accessor<number> | number
  /**
   * The scroll element of the list. With it, the virtualizer keeps the content under
   * the reader in place across a geometry change by a layout shift (see
   * chatLayoutShift). Without it, the shift stays 0.
   */
  scrollElement?: () => HTMLElement | undefined
  /**
   * The inset (px) of the spacer: the distance from scroll coordinate 0 to the top of the
   * spacer that holds the rows (see chatLayoutShift). ChatView measures the top padding
   * and the row gap of the list. Default 0.
   */
  spacerInsetPx?: Accessor<number> | number
  /** Keep the bottom of the content in place across a geometry change: the reader follows the tail. */
  pinBottom?: () => boolean
  /**
   * The first row of the list is the first loaded row: no loaded row waits above the list
   * for its measure (see LayoutShiftDeps.listStartsAtLoadedTop). ChatView reads the row
   * admission. Default: true, for a list that holds every loaded row.
   */
  listStartsAtLoadedTop?: Accessor<boolean>
}

/**
 * The geometry of the loaded rows: the offset map with the row list it was built
 * from. The snapshot that the layout shift compares (see chatLayoutShift).
 */
interface VirtualizerGeometry extends LayoutSnapshot {
  list: ChatRowItem[]
}

const DEFAULT_OVERSCAN_PX = 800
const DEFAULT_GAP_SMALL_PX = 8 // --space-2
const DEFAULT_GAP_LARGE_PX = 20 // --space-5

function resolve(v: Accessor<number> | number | undefined, fallback: number): number {
  if (v === undefined)
    return fallback
  return typeof v === 'function'
    ? v()
    : v
}

/** Whether two arrays hold the same numbers at the same indices. */
function sameFloats(a: Float64Array, b: Float64Array): boolean {
  if (a.length !== b.length)
    return false
  for (let i = 0; i < a.length; i++) {
    if (a[i] !== b[i])
      return false
  }
  return true
}

/**
 * The virtualizer's surface. The layout shift members (`layoutShift`, the coordinate
 * conversions, the spacer, and `rebase`) come from LayoutShiftSurface (see
 * createLayoutShift).
 */
export interface UseChatVirtualizerResult extends LayoutShiftSurface {
  /** Reactive [start, end) slice of `items` to render. */
  range: Accessor<ChatVirtualizerRange>
  /**
   * Changes with each change of the offset map of the listed rows, also one that keeps
   * totalHeight (e.g. a row above the viewport grows while one below shrinks).
   */
  geometryVersion: Accessor<number>
  /** Total height (px) of the whole in-memory window, in content coordinates. */
  totalHeight: Accessor<number>
  /** Top offset (px) of a row by index, in content coordinates. */
  offsetOfIndex: (index: number) => number
  /** Top offset (px) of the row with the given id, or undefined if absent, in content coordinates. */
  offsetOfId: (id: string) => number | undefined
  /** Index of the row with the given id, or -1 if absent. */
  indexOfId: (id: string) => number
  /** Index of the row whose vertical span contains the content offset `y` (clamped). */
  indexAtOffset: (y: number) => number
  /**
   * The scroll anchor for a viewport whose top edge is at the content coordinate
   * `contentTop`: the row at that offset plus the pixels from the row's top to the
   * viewport top. Exact: it converts nothing.
   */
  anchorAtContent: (contentTop: number) => ScrollAnchor | null
  /**
   * Resolve an anchor to a content coordinate, or null if its row id is no longer present.
   * Exact: it converts nothing.
   */
  contentTopForAnchor: (anchor: ScrollAnchor) => number | null
  /** `anchorAtContent` for the scroll coordinate `scrollTop` (see contentTopOf). */
  anchorAt: (scrollTop: number) => ScrollAnchor | null
  /** `contentTopForAnchor` as the scroll coordinate of a placement (see placementScrollTopOf). */
  scrollTopForAnchor: (anchor: ScrollAnchor) => number | null
  /**
   * Resolve an anchor to the scroll coordinate of a placement (see placementScrollTopOf),
   * recovering a TRIMMED-away row by landing on the nearest surviving row by seq. Null only
   * when the row is gone and there is no seq / no surviving server row to fall back to.
   */
  scrollTopNearAnchor: (anchor: ScrollAnchor) => number | null
  /** The measured height (px) of a row by index (excludes the gap). 0 for an index outside the list. */
  heightOfIndex: (index: number) => number
  /**
   * The measured height (px) of a row by id (excludes the gap); 0 for an absent row.
   * Composes indexOfId + heightOfIndex so a
   * caller never has to thread a possibly-stale index between the two.
   */
  heightOfId: (id: string) => number
  /**
   * The gap (px) the offset map left ABOVE a row: `gapAfter` of the row before
   * it, and 0 for the first row. Negative where two adjacent bands overlap.
   *
   * The gap-bridge overlay sizes its segments from this rather than from a token
   * of its own, so the bridge and the offsets cannot disagree: at a merged band
   * seam there is no gap, and the segment collapses instead of being drawn for
   * one that does not exist.
   */
  gapAboveOf: (id: string) => number
  /**
   * Debug accessor: a row's measured DOM height when the cache holds one under the
   * current key of the row, for the raw-JSON surface.
   */
  heightDebugOfId: (id: string) => { measured?: number }
  /**
   * Compute (without committing) the visible slice for a given scroll coordinate.
   * `lead` extends the overscan in its `dir` (the fling direction) so a fast
   * scroll renders the rows it is about to reach; omitted falls back to symmetric overscan.
   */
  computeRange: (scrollTop: number, clientHeight: number, lead?: ViewportLead) => ChatVirtualizerRange
  /** Recompute and commit the visible slice for a scroll coordinate (see computeRange for `lead`). */
  updateViewport: (scrollTop: number, clientHeight: number, lead?: ViewportLead) => void
  /**
   * Record that the scroll hook placed the view at the scroll coordinate `scrollTop`: a
   * seek, a restore, or a jump to the top, and not a move of the reader along the content.
   * Call it after the write of the position.
   *
   * The virtualizer records each mounted row that lay above the viewport top when it
   * mounted: the reader reaches such a row from below, and the layout shift can then hold
   * its bottom edge inside the view (see LayoutShiftInput.enteredFromAbove). A placement
   * moves the view with no such move of the reader, so this call makes the record again
   * from the new position: a row above the new viewport top is in it, and every other row
   * is not.
   */
  notePlacement: (scrollTop: number) => void
  /**
   * Record a height that a read of the mounted row gave. Returns true if the offset map
   * changed. The first read since the row mounted corrects a height that the reader did not
   * see, so the layout shift then holds the bottom edge of the row (see nextLayoutShift).
   */
  measure: (id: string, height: number) => boolean
  /**
   * Whether the mounted row `id` shows the height that the offset map holds for it: a read
   * of the row gave or confirmed that height since the row mounted. False for a row that is
   * not mounted, and for a mounted row that no read measured yet. A row whose last element
   * detached counts as mounted until the microtask of that detach, because a new element
   * can still take it over (see RowHeightCache.noteDetached). The reader did not see
   * the height of such a row: a hidden copy, the storage, or an earlier mount measured it,
   * and a fling skeleton or a copy that waits for its content can stand in for the row. The
   * first read of the row can change it, and the layout shift then holds the bottom edge
   * of the row (see nextLayoutShift). Not reactive: read it when the answer is needed.
   */
  showsHeight: (id: string) => boolean
  /**
   * Reactive "fast user scroll in flight" flag — a momentum fling OR a fast
   * scrollbar/touch drag. Set by the scroll hook; rows entering the window
   * read it to decide skeleton-vs-full mount (ChatView's fling skeletons), and
   * no pivot element is recorded while it is set and the scroll position still
   * moves (see createLayoutPivot).
   */
  fastScrollActive: Accessor<boolean>
  setFastScrollActive: (active: boolean) => void
  /**
   * Ref callback: start observing a rendered row element. `readiness` holds the
   * measurement while the row copy is not measurable (it waits for content, or
   * replaces it), and measures the copy again when it turns measurable; see
   * RowMeasurer.attachRow. Call it within the row's reactive owner when it passes
   * `readiness`.
   */
  attachRow: (id: string, el: HTMLElement, readiness?: RowContentReadiness) => void
  /** Cleanup callback: stop observing a row element (keeps its cached height). */
  detachRow: (el: HTMLElement) => void
}

/** The rows of the list, with the index of each row by its id. */
interface IndexedRows {
  list: readonly ChatRowItem[]
  indexById: ReadonlyMap<string, number>
}

/** The item of row `id` in `rows`, or `undefined` for a row that is not in the list. */
function itemOf(rows: IndexedRows, id: string): ChatRowItem | undefined {
  const index = rows.indexById.get(id)
  return index === undefined
    ? undefined
    : rows.list[index]
}

/**
 * Geometry engine for the virtualized chat list. Reads the height of each row from the
 * RowHeightCache that the caller passes, and owns
 * the cumulative offset map, the visible-row slice, and, through createLayoutShift, the
 * layout shift that keeps the content under the reader in place across a geometry change
 * (see chatLayoutShift). It does not decide the scroll position: useChatScroll feeds
 * positions in via `updateViewport`, and asks for a `rebase` when the shift may go.
 */
export function useChatVirtualizer(opts: UseChatVirtualizerOptions): UseChatVirtualizerResult {
  // The measured heights by row id (see createRowHeightCache). ChatView owns the cache,
  // which the admission and the premeasure queue also read.
  const cache = opts.heightCache
  // The ids of the rows currently MOUNTED in the DOM (see RowHeightCache.mountedIds). The
  // cache keeps the set from the mount notices of the row measurer.
  const mountedIds = cache.mountedIds
  // Reactive "fast user scroll in flight" flag, set by the scroll hook for ANY
  // genuine user scroll at fling velocity — momentum flings AND direct drags
  // (scrollbar thumb, touch). Rows entering the window read it to decide
  // skeleton-vs-full mount (ChatView's fling skeletons).
  const [fastScrollActive, setFastScrollActive] = createSignal(false)
  // The key for which the virtualizer asked the measurer to read a mounted row again, by
  // row id, so that it asks once for each key of a row (see the effect below the
  // measurer). An entry leaves when the row unmounts, and when the cache holds a height of
  // the current key again.
  const rereadKeyById = new Map<string, string | undefined>()

  // The ids of the listed rows with no height that this virtualizer reported, so that it
  // reports each id once (see resolvedHeight).
  const reportedUnmeasuredIds = new Set<string>()

  // A row's height: the height that the cache holds for it. Every row of the list has one,
  // because the admission passes measured rows only. A row with no height breaks that
  // invariant: a development build reports its id once, and the row takes 0px, so the rows
  // below it overlap its content.
  const resolvedHeight = (item: ChatRowItem): number => {
    const height = cache.heightOf(item)
    if (height !== undefined)
      return height
    if (import.meta.env.DEV && !reportedUnmeasuredIds.has(item.id)) {
      reportedUnmeasuredIds.add(item.id)
      console.error(`useChatVirtualizer: the row ${item.id} is in the list with no measured height. The admission must pass measured rows only.`)
    }
    return 0
  }

  // The number of reads of each row that reached `measure` since the row mounted. The
  // first read of a mounted row replaces a height that the reader did not see on that
  // row: a height from a hidden copy or from storage, or the height of an
  // earlier mount, under a fling skeleton or a copy that waited for its content. Each
  // later read changes a height that the reader saw (see sawHeight). A row that stays
  // mounted while a new element takes it over keeps its count: the reader saw it. That
  // holds for both orders of the takeover (see noteRowUnmounted).
  const readsSinceMount = new Map<string, number>()
  // See UseChatVirtualizerResult.showsHeight.
  const showsHeight = (id: string): boolean => (readsSinceMount.get(id) ?? 0) > 0
  // Whether the reader saw the row at the height that the offset map held for it before
  // the last read of the row: an earlier read gave or confirmed that height. The layout
  // shift asks after a read changed the height (see LayoutShiftInput.sawHeight).
  const sawHeight = (id: string): boolean => (readsSinceMount.get(id) ?? 0) > 1

  // The mounted rows that lay above the viewport top when they mounted, or at the last
  // placement of the view (see notePlacement). The reader reaches such a row from below: it
  // comes into the view over the top edge (see LayoutShiftInput.enteredFromAbove).
  const rowsAboveView = new Set<string>()
  const enteredFromAbove = (id: string): boolean => rowsAboveView.has(id)

  // The last element of the row detached.
  //
  // The row is not unmounted yet. A row whose entry changes (new content under a new key)
  // is replaced in one update of the row list: its old element detaches before its new
  // element attaches. The cache counts the row as mounted for one microtask (see
  // RowHeightCache.noteDetached), and between the two steps the row must still count as
  // measured, in two ways:
  //
  // - The cache keeps its answer. A reader of the heights in that moment, such as
  //   `attachRow` when it asks whether the row lies above the view, would otherwise
  //   compute the offset map with a height of 0 for the row. Nothing would correct that
  //   map: the row is mounted again, so no version changes.
  // - The cache changes no version. The admission would otherwise take the row, and each
  //   row on its far side, out of the list until a hidden copy measured it again: the
  //   rows under the reader would vanish and return.
  //
  // The records of the mounted row stay for the same microtask: the reader saw the row
  // that a new element takes over, whatever the order of the detach and the attach. That
  // holds also when the new element waits for its content: the row then keeps the slot
  // that the reader saw, and the read of the content changes a height that the reader
  // saw. Only a row that no element took over drops its records: its next mount shows the
  // reader no height yet.
  const noteRowUnmounted = (id: string): void => {
    cache.noteDetached(id, () => {
      readsSinceMount.delete(id)
      rowsAboveView.delete(id)
      rereadKeyById.delete(id)
    })
  }

  // The gap between row i and i+1 is tightened (small) whenever the LOWER row
  // (i+1) has span lines. SpanLines gives the lower row column-specific
  // ownership of the top bridge across this gap, and that bridge sizes itself
  // from `gapAboveOf` below rather than from a token of its own -- so this
  // function is the single decider for both the offset and the rail across it.
  //
  // Adjacent bands share one border. Overlap their edges by one border width
  // when both heights support it. Reduce the overlap for an empty or short band
  // so offsets never decrease. Premeasure still measures each row separately.
  const gapAfter = (list: ChatRowItem[], i: number): number => {
    if (i >= list.length - 1)
      return 0
    const next = list[i + 1]
    if (next === undefined)
      return 0
    const cur = list[i]
    if (cur !== undefined && messageBandKind(cur.kind ?? '') && messageBandKind(next.kind ?? ''))
      return -Math.min(BAND_BORDER_PX, resolvedHeight(cur), resolvedHeight(next))
    return next.hasSpanLines
      ? resolve(opts.gapSmallPx, DEFAULT_GAP_SMALL_PX)
      : resolve(opts.gapLargePx, DEFAULT_GAP_LARGE_PX)
  }

  // Row identity map, rebuilt only when the item LIST changes (append, trim,
  // heightKey change) -- NOT on every measurement commit. The row ID remains
  // stable across sequence changes. A height commit changes only the version of the
  // cache, which rebuilds the offsets below; re-running the n string-keyed
  // Map.set calls per commit rebuilt a map that could not have changed (dozens of
  // commits per fling through unmeasured history made that the dominant rebuild
  // cost).
  const rowIndex = createMemo(() => {
    const list = opts.items()
    const n = list.length
    const indexById = new Map<string, number>()
    for (let i = 0; i < n; i++) {
      const item = list[i]
      if (item !== undefined)
        indexById.set(item.id, i)
    }
    return { list, indexById, n }
  })

  // Cumulative offsets, recomputed when items or measurements change, with the height of
  // each row: the snapshot that the layout shift compares. The identity map rides along
  // from rowIndex so every consumer keeps its one-stop read.
  //
  // The cache changes its version for commits that move no listed row: a hidden copy that
  // measures a staged row, and a read that confirms a height under a new key. The memo then
  // keeps its previous value, so the layout shift, the scroll hook, and the rail do not run
  // for a geometry that did not change. `indexById` and `n` follow from `list`.
  const geom = createMemo((): VirtualizerGeometry => {
    const rebuildStart = monotonicNow()
    cache.version()
    const { list, indexById, n } = rowIndex()
    const offsets = new Float64Array(n + 1)
    const heights = new Float64Array(n)
    for (let i = 0; i < n; i++) {
      const item = list[i]
      if (item === undefined)
        continue
      const height = resolvedHeight(item)
      heights[i] = height
      offsets[i + 1] = (offsets[i] ?? 0) + height + gapAfter(list, i)
    }
    // Rebuilding the offset map is an O(n) array fill (microseconds for any real n), so this
    // should never trip -- but instrumenting it rules the offset map IN or OUT as a stall
    // source instead of leaving it a suspect. Only a slow rebuild logs.
    warnSlowScrollPhase('geomRebuild', monotonicNow() - rebuildStart, { rows: n })
    return {
      offsets,
      heights,
      indexById,
      list,
      n,
    }
  }, undefined, {
    equals: (a, b) => a.list === b.list && sameFloats(a.offsets, b.offsets) && sameFloats(a.heights, b.heights),
  })
  // See UseChatVirtualizerResult.geometryVersion.
  const geometryVersion = createMemo<number>((version) => {
    geom()
    return version + 1
  }, 0)

  const totalHeight: Accessor<number> = () => {
    const g = geom()
    return g.offsets[g.n] ?? 0
  }

  const offsetOfIndex = (index: number): number => {
    const g = geom()
    if (index <= 0)
      return 0
    if (index >= g.n)
      return g.offsets[g.n] ?? 0
    return g.offsets[index] ?? 0
  }

  const indexOfId = (id: string): number => geom().indexById.get(id) ?? -1

  const offsetOfId = (id: string): number | undefined => {
    const i = indexOfId(id)
    return i >= 0
      ? offsetOfIndex(i)
      : undefined
  }

  const heightOfIndex = (index: number): number => {
    const g = geom()
    const height = index >= 0 && index < g.n
      ? g.heights[index]
      : undefined
    return height ?? 0
  }

  const heightOfId = (id: string): number => heightOfIndex(indexOfId(id))

  const gapAboveOf = (id: string): number => {
    const i = indexOfId(id)
    return i > 0
      ? gapAfter(geom().list, i - 1)
      : 0
  }

  // The current item of row `id`, or `undefined` for a row that left the list. It reads
  // `rowIndex`, not `geom`: the measurer asks for each read that it commits, and inside
  // the batch of one flush a read of `geom` after a commit would rebuild the whole offset
  // map once per commit.
  const currentItem = (id: string): ChatRowItem | undefined => itemOf(rowIndex(), id)

  // Debug-only: the measured height of a row, under its current key when the row is in
  // the list. It reads the version of the cache, and not `geom`: a commit that confirms a
  // height under a new key changes the answer, and no geometry.
  const heightDebugOfId = (id: string): { measured?: number } => {
    cache.version()
    const item = currentItem(id)
    const measured = item === undefined
      ? cache.debugEntry(id)?.height
      : cache.heightUnderCurrentKey(item)
    return measured !== undefined
      ? { measured }
      : {}
  }

  // Largest i in [0, n-1] with offsets[i] <= y (the row containing offset y).
  const indexAtOffset = (y: number): number => {
    const g = geom()
    if (g.n === 0)
      return 0
    return largestIndexWhere(g.n, mid => (g.offsets[mid] ?? 0) <= y)
  }

  // The offset-engine surface the (extracted, pure) anchor math reads, over the
  // CURRENT row list. Built per call -- geom() is memoized, so the repeated reads are
  // cheap and stay consistent within one anchor operation.
  const anchorGeometry = (): AnchorOffsetGeometry => ({
    list: geom().list,
    indexAtOffset,
    indexOfId,
    offsetOfIndex,
    heightOfIndex,
    gapAfter: i => gapAfter(geom().list, i),
  })
  // The anchor math works in content coordinates, so these two resolvers convert nothing.
  // A caller that holds a content coordinate uses them, and no round trip through the
  // scroll coordinates can lose a fraction of a pixel.
  const anchorAtContent = (contentTop: number): ScrollAnchor | null =>
    anchorAtOffset(anchorGeometry(), contentTop)
  const contentTopForAnchor = (anchor: ScrollAnchor): number | null =>
    resolveAnchorScrollTop(anchorGeometry(), anchor)

  // The slice for a viewport whose top edge is at the content coordinate `contentTop`.
  const computeContentRange = (
    contentTop: number,
    clientHeight: number,
    lead?: ViewportLead,
  ): ChatVirtualizerRange => {
    const g = geom()
    const n = g.n
    if (n === 0)
      return { start: 0, end: 0 }
    const over = resolve(opts.overscanPx, DEFAULT_OVERSCAN_PX)
    // Render-ahead: extend the overscan by `lead.px` on the side the gesture is
    // heading toward, so a fast fling paints the rows it is ABOUT to reach. The
    // compositor scrolls a frame before this range update lands; without the lead a
    // jump larger than the symmetric overscan would flash an unrendered gap until
    // the next frame caught up. A non-positive lead leaves the symmetric overscan.
    const leadPx = lead && lead.px > 0
      ? lead.px
      : 0
    const overTop = over + (lead?.dir === 'older'
      ? leadPx
      : 0)
    const overBottom = over + (lead?.dir === 'newer'
      ? leadPx
      : 0)
    // Clamp a stale-high position to the content before slicing. After a trim/shrink
    // the spacer drops in the same flush that the DOM still reports the old, larger
    // scrollTop; without this clamp `top` would exceed every offset and the `start`
    // search below would fall back to its last index, collapsing the slice to the last
    // row alone for one frame. (A negative position -- rubber-band overscroll, the inset of
    // the spacer, or the blank band above the first row under a positive layout shift --
    // floors to row 0.)
    const total = g.offsets[n] ?? 0
    const maxContentTop = Math.max(0, total - clientHeight)
    const clampedTop = clamp(contentTop, 0, maxContentTop)
    const top = clampedTop - overTop
    const bottom = clampedTop + clientHeight + overBottom
    // First row extending past `top` (smallest i with offsets[i+1] > top), and one
    // past the last row starting before `bottom` (largest i with offsets[i] < bottom).
    let start = smallestIndexWhere(n, mid => (g.offsets[mid + 1] ?? 0) > top, n - 1)
    let end = largestIndexWhere(n, mid => (g.offsets[mid] ?? 0) < bottom) + 1
    if (start < 0)
      start = 0
    if (start > n - 1)
      start = n - 1
    if (end < start + 1)
      end = start + 1
    if (end > n)
      end = n
    // A row taller than the pixel overscan can legitimately contain the whole
    // viewport+overscan band, collapsing the slice to that one row. Keep one
    // neighbor on each side in that case so a slow scroll across either edge has
    // overlapping DOM instead of replacing the entire mounted set at the boundary.
    // Normal rows keep the pure pixel window; this adds at most two rows, only for
    // the oversized-row case.
    const guardBandPx = Math.max(overTop, overBottom)
    if (guardBandPx > 0 && end === start + 1 && (g.heights[start] ?? 0) > guardBandPx) {
      start = Math.max(0, start - 1)
      end = Math.min(n, end + 1)
    }
    return { start, end }
  }

  const [committedRange, setCommittedRange] = createSignal<CommittedRange>({
    start: 0,
    end: 0,
    firstId: undefined,
    lastId: undefined,
  })
  // The committed slice, mapped onto the current row list by the ids of its edge rows
  // (see followCommittedRange). It keeps its previous object while the indices hold,
  // so a commit that changes only the stored ids notifies no reader.
  const range = createMemo<ChatVirtualizerRange>(
    () => {
      const { indexById, n } = rowIndex()
      return followCommittedRange(committedRange(), indexById, n)
    },
    { start: 0, end: 0 },
    {
      equals: (a, b) => a.start === b.start && a.end === b.end,
    },
  )

  const measure = (id: string, height: number): boolean => {
    // Count the read before its commit: the layout shift of the commit asks sawHeight.
    readsSinceMount.set(id, (readsSinceMount.get(id) ?? 0) + 1)
    // A row that left the list commits nothing.
    const item = currentItem(id)
    return item !== undefined && cache.commit(item, height, item.heightKey, 'visible') === 'moved'
  }
  // The ResizeObserver / batched-microtask-flush DOM glue lives in createRowMeasurer
  // (its coupling here is `measure`, the height key, the height under that key, and the
  // mount notices for the cache), keeping this file the offset engine. The default
  // scheduler/observer are the production ones; a unit test injects fakes to drive
  // the flush timing deterministically.
  const measurer = createRowMeasurer({
    measure,
    currentMeasurementKey: id => currentItem(id)?.heightKey,
    committedHeight: (id) => {
      const item = currentItem(id)
      return item === undefined
        ? undefined
        : cache.heightUnderCurrentKey(item)
    },
    onRowMounted: cache.noteMounted,
    onRowUnmounted: noteRowUnmounted,
  })
  const { detachRow } = measurer
  onCleanup(measurer.dispose)
  // Read each mounted row whose key changed again, at the measurer's next flush. The cache
  // answers by key, so nothing drops a height at a key change: a mounted row keeps the
  // height of its old key provisionally (see RowHeightCache.heightOf). A row whose new
  // content keeps its height never resizes, so only a read of its element confirms the
  // height under the new key.
  //
  // The search follows the row list alone: a cached height goes out of date only when the
  // heightKey of its item changes, which changes the identity of the list. The cache
  // lookups below read no signal.
  createEffect(() => {
    const rows = rowIndex()
    for (const id of mountedIds) {
      const item = itemOf(rows, id)
      // No read for a row that left the list.
      if (item === undefined)
        continue
      // No read for a row whose height is of its current key, and none for a row with no
      // height: the measurer reads that row at its attach.
      if (!cache.hasProvisionalHeight(item)) {
        rereadKeyById.delete(id)
        continue
      }
      // One read for each key of a row: a list change that keeps the key asks for nothing.
      if (rereadKeyById.has(id) && rereadKeyById.get(id) === item.heightKey)
        continue
      rereadKeyById.set(id, item.heightKey)
      measurer.measureAgain(id)
    }
  })

  // The layout shift on top of the offset map (see createLayoutShift). It needs the row
  // measurer, which holds the element of each mounted row.
  const { schedulePivotRecord, ...layoutShiftSurface } = createLayoutShift({
    geometry: geom,
    totalHeight,
    scrollElement: opts.scrollElement,
    spacerInsetPx: () => resolve(opts.spacerInsetPx, 0),
    pinBottom: () => opts.pinBottom?.() ?? false,
    listStartsAtLoadedTop: () => opts.listStartsAtLoadedTop?.() ?? true,
    isSuspended: fastScrollActive,
    rows: measurer,
    sawHeight,
    enteredFromAbove,
    lastMeasurement: cache.lastMeasurement,
  })
  const { contentTopOf, placementScrollTopOf } = layoutShiftSurface

  // The scroll coordinate of the viewport top at the last updateViewport or notePlacement,
  // or `undefined` before the first one. A rebase moves `scrollTop` and the shift by the
  // same distance, so `rebase` below moves this coordinate with them: it then still
  // converts to the content coordinate of the viewport top.
  let viewportScrollTop: number | undefined
  const rebase = (): number => {
    const removed = layoutShiftSurface.rebase()
    if (viewportScrollTop !== undefined)
      viewportScrollTop -= removed
    return removed
  }
  // Whether the top edge of the row lies above the viewport top at `scrollTop`, by more
  // than the slack within which the viewport top counts as that edge (see
  // TOP_EDGE_SLACK_PX). Untracked: the callers record a fact of this moment.
  const liesAboveView = (id: string, scrollTop: number): boolean => untrack(() => {
    const top = offsetOfId(id)
    return top !== undefined && top < contentTopOf(scrollTop) - TOP_EDGE_SLACK_PX
  })
  const attachRow = (id: string, el: HTMLElement, readiness?: RowContentReadiness): void => {
    // The record comes before the attach: the measurer reads the row at once, and the
    // layout shift of that read asks enteredFromAbove. A row that a new element takes over
    // stays mounted, and keeps its record (see noteRowUnmounted).
    if (!cache.isMounted(id) && viewportScrollTop !== undefined && liesAboveView(id, viewportScrollTop))
      rowsAboveView.add(id)
    measurer.attachRow(id, el, readiness)
  }
  const notePlacement = (scrollTop: number): void => {
    viewportScrollTop = scrollTop
    rowsAboveView.clear()
    for (const id of mountedIds) {
      if (liesAboveView(id, scrollTop))
        rowsAboveView.add(id)
    }
  }

  // These resolvers take and return scroll coordinates, so a caller that holds a scroll
  // position needs no conversion of its own. A resolved anchor is a placement, so the top
  // of the first loaded row resolves to the top of the scroller (see placementScrollTop).
  const anchorAt = (scrollTop: number): ScrollAnchor | null =>
    anchorAtContent(contentTopOf(scrollTop))
  const scrollTopForAnchor = (anchor: ScrollAnchor): number | null => {
    const contentTop = contentTopForAnchor(anchor)
    return contentTop === null
      ? null
      : placementScrollTopOf(contentTop)
  }
  const scrollTopNearAnchor = (anchor: ScrollAnchor): number | null => {
    const contentTop = resolveNearestAnchorScrollTop(anchorGeometry(), anchor)
    return contentTop === null
      ? null
      : placementScrollTopOf(contentTop)
  }

  const computeRange = (
    scrollTop: number,
    clientHeight: number,
    lead?: ViewportLead,
  ): ChatVirtualizerRange => computeContentRange(contentTopOf(scrollTop), clientHeight, lead)

  const updateViewport = (
    scrollTop: number,
    clientHeight: number,
    lead?: ViewportLead,
  ) => {
    // Before the range commits: a row that the new range mounts compares its top edge with
    // this position (see attachRow).
    viewportScrollTop = scrollTop
    const next = computeRange(scrollTop, clientHeight, lead)
    const cur = range()
    const { list } = rowIndex()
    const firstId = list[next.start]?.id
    const lastId = list[next.end - 1]?.id
    // Commit new ids also when the indices hold: ids that left the list would
    // otherwise stay stored, and the next prepend would slice by the stale indices.
    const stored = committedRange()
    if (cur.start !== next.start || cur.end !== next.end || stored.firstId !== firstId || stored.lastId !== lastId)
      setCommittedRange({ ...next, firstId, lastId })
    // A new position needs a new record of the element at the viewport top.
    schedulePivotRecord()
  }

  return {
    range,
    geometryVersion,
    totalHeight,
    ...layoutShiftSurface,
    rebase,
    offsetOfIndex,
    offsetOfId,
    indexOfId,
    indexAtOffset,
    anchorAtContent,
    contentTopForAnchor,
    anchorAt,
    scrollTopForAnchor,
    scrollTopNearAnchor,
    heightOfIndex,
    heightOfId,
    gapAboveOf,
    heightDebugOfId,
    computeRange,
    updateViewport,
    notePlacement,
    measure,
    showsHeight,
    fastScrollActive,
    setFastScrollActive,
    attachRow,
    detachRow,
  }
}
