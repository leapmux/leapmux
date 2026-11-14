import type { Accessor } from 'solid-js'
import type { LayoutPivot, PivotRow } from './chatLayoutPivot'
import type { LayoutSnapshot } from './chatLayoutShift'
import type { AnchorOffsetGeometry } from './chatScrollAnchor'
import type { RowContentReadiness } from './createRowMeasurer'
import type { ScrollAnchor } from '~/stores/chatTypes'
import { createComputed, createEffect, createMemo, createSignal, on, onCleanup, untrack } from 'solid-js'
import { largestIndexWhere, smallestIndexWhere } from '~/lib/binarySearch'
import { clamp } from '~/lib/clamp'
import { capMapInsertionOrder } from '~/lib/mapLru'
import { monotonicNow } from '~/lib/monotonicNow'
import { createLayoutPivot } from './chatLayoutPivot'
import { nextLayoutShift, TOP_EDGE_SLACK_PX } from './chatLayoutShift'
import { BAND_BORDER_PX, messageBandKind } from './chatRowGeometry'
import { anchorAtOffset, resolveAnchorScrollTop, resolveNearestAnchorScrollTop } from './chatScrollAnchor'
import { createContentMovedReporter } from './chatScrollDiagnostics'
import { clampScrollTop, warnSlowScrollPhase } from './chatScrollGeometry'
import { createRowMeasurer } from './createRowMeasurer'

/**
 * Minimal per-row descriptor the virtualizer needs. `id` keys the height
 * cache, the offset map, and the scroll anchor. It is stable across window
 * trims and unique per row. A sequence can change when the worker consolidates
 * a notification. `hasSpanLines` drives the inter-row gap (span-line rows sit closer
 * together so their vertical rails bridge — see SpanLines.css.ts).
 */
export interface VirtualItem {
  id: string
  hasSpanLines: boolean
  /**
   * The row's message seq, recorded onto a captured ScrollAnchor so a trimmed-away
   * anchor can be ordered against the surviving rows for the nearest-survivor restore
   * (scrollTopNearAnchor). Optional: a caller that doesn't supply it just can't do
   * that recovery (the offset map and slicing never read it -- ordering is positional).
   */
  seq?: bigint
  /**
   * Per-row geometry version for cached DOM heights.
   * A stable id can change content, width-sensitive layout, or UI state in place; this
   * key changes in those cases so stale measured/premeasured heights are ignored.
   * Absent -> id-only caching.
   */
  heightKey?: string
  /**
   * The message's rendering kind (category.kind: 'user_text', 'tool_result', ...), used
   * to bucket the height ESTIMATE for an unmeasured row. A single global mean over the
   * whole chat over-estimated short rows (user text) whenever the mean was inflated by
   * tall code/diff rows, so scrolling such a row in above the anchor shrank content above
   * it and drifted the pin; a per-kind median draws each row's estimate from its own
   * height distribution. Reuses the entry's EXISTING classification (ChatView passes
   * `category.kind`), never re-classifying. Absent -> a shared default bucket (falls back
   * to the global median, i.e. the pre-per-kind behavior).
   */
  kind?: string
}

/**
 * Classifies EVERY VirtualItem field as geometry-affecting (true -> compared by
 * sameVirtualItems / feeds the offset map, gap, and measured-height cache) or not. The
 * `Record<keyof Required<VirtualItem>, boolean>` annotation forces a COMPILE error when a
 * field is added to VirtualItem without classifying it here, closing the "forgot to update
 * the equality" hazard. `seq` (anchor label) is NOT geometry -- it does not feed the offset
 * map.
 */
const GEOMETRY_RELEVANCE: Record<keyof Required<VirtualItem>, boolean> = {
  id: true,
  hasSpanLines: true,
  heightKey: true,
  seq: false,
  // Feeds the per-kind estimate bucket, so a kind change re-estimates an unmeasured row
  // and shifts the offset map. In practice a kind flip also bumps heightKey (both track
  // the same reclassification), so this rarely forces an extra rebuild on its own -- but
  // classifying it as geometry keeps the offset map correct even if that coupling ever
  // loosens.
  kind: true,
}

/** The geometry-affecting VirtualItem keys, derived once (module load) from the above. */
const GEOMETRY_KEYS = (Object.keys(GEOMETRY_RELEVANCE) as (keyof VirtualItem)[])
  .filter(k => GEOMETRY_RELEVANCE[k])

/**
 * Whether two virtual-item arrays are GEOMETRY-EQUIVALENT: same length and the same
 * geometry fields (GEOMETRY_KEYS = id, hasSpanLines, heightKey, kind) at every index. The
 * virtualizer keys its offset map, inter-row gap, measured-height cache, and per-kind
 * estimate bucket on exactly those fields, so a recompute that preserves them produces an
 * identical offset map. Used as the
 * `virtualItems` memo's `equals` so a recompute that DOESN'T change the visible window's
 * rows. A recompute that does not add, remove, reorder, or revise a visible row
 * keeps the prior array
 * reference. That stops an unnecessary geometry rebuild and layout-shift pass. Row
 * content still updates through visibleEntries. Row measurement catches a real
 * height change.
 *
 * MAINTENANCE: the compared fields are DERIVED from GEOMETRY_RELEVANCE above (compile-
 * enforced exhaustive over VirtualItem's keys), so a new geometry-affecting field can't be
 * silently omitted from the equality and leave the offset map stale.
 */
export function sameVirtualItems(a: VirtualItem[], b: VirtualItem[]): boolean {
  if (a === b)
    return true
  if (a.length !== b.length)
    return false
  for (let i = 0; i < a.length; i++) {
    const x = a[i]
    const y = b[i]
    if (x === undefined || y === undefined)
      return false
    for (const k of GEOMETRY_KEYS) {
      if (x[k] !== y[k])
        return false
    }
  }
  return true
}

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
 * bounds. An empty commit (none yet, or one over an empty list) gives no row until the
 * next commit: the scroll hook commits the first slice at its mount, and a row that
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

/**
 * One measured row height in transportable form — what `snapshotHeights`
 * exports and `primeHeights` re-imports (the reload warm-start round trip,
 * see chatRowHeightPersistence).
 */
export interface PersistableRowHeight {
  id: string
  heightKey: string | undefined
  height: number
}

/**
 * The most recent height commit that changed the offset map -- diagnostic attribution for
 * the `content moved without a scroll` WARN (see chatScrollDiagnostics). A move of the
 * content at the viewport top that no scroll caused follows a geometry change; this
 * identifies the row and the change, so a logged move can be traced to its cause:
 * `firstMeasure` (an estimate->real correction, i.e. the per-kind median was off for this
 * row / it outran premeasure) vs a re-measure (a premeasured-vs-visible height mismatch or
 * a chrome/content change), and `delta` (= newHeight - assumedHeight). `commitSeq` is
 * monotonic, so two consecutive WARNs reveal how many commits fell between them.
 */
export interface MeasurementCommitInfo {
  id: string
  kind: string
  source: 'visible' | 'premeasure'
  firstMeasure: boolean
  /**
   * The height the offset map assumed before this commit: the estimate (first measure) or
   * the previously cached height (re-measure).
   */
  assumedHeight: number
  newHeight: number
  /** newHeight - assumedHeight: how far this commit shifted the row's reserved height. */
  delta: number
  commitSeq: number
}

export interface UseChatVirtualizerOptions {
  /** Reactive list of rows to virtualize, in display order. */
  items: Accessor<VirtualItem[]>
  /** Off-screen pixels to keep mounted on each side of the viewport. */
  overscanPx?: Accessor<number> | number
  /** Seed height (px) for rows that have never been measured. */
  estimateHeight?: number
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
  /** Keep the bottom of the content in place across a geometry change: the reader follows the tail. */
  pinBottom?: () => boolean
}

/**
 * The geometry of the loaded rows: the offset map with the row list it was built
 * from. The snapshot that the layout shift compares (see chatLayoutShift).
 */
interface VirtualizerGeometry extends LayoutSnapshot {
  list: VirtualItem[]
}

const DEFAULT_OVERSCAN_PX = 800
/**
 * Seed/fallback row height (px) for a row that has not reached any DOM measurement
 * path yet. Once visible or hidden DOM measures rows, the per-kind median replaces it.
 */
const DEFAULT_ESTIMATE_PX = 96
const DEFAULT_GAP_SMALL_PX = 8 // --space-2
const DEFAULT_GAP_LARGE_PX = 20 // --space-5
/** Ignore sub-pixel measurement jitter below this threshold. */
const MEASURE_EPSILON_PX = 0.5
/**
 * A real DOM measurement above this stays authoritative for that row, but is excluded
 * from the global fallback estimate for rows that have not measured yet. The fallback
 * represents a typical unknown row; letting one huge command output/diff dominate it
 * stretches every still-unmeasured normal row, then collapses the offset map as those
 * rows measure, which is the visible slow-scroll jump around oversized rows.
 */
const FALLBACK_ESTIMATE_OUTLIER_PX = 1200
/**
 * Max measured-height cache entries. Must stay comfortably ABOVE the largest
 * in-memory window the consumer can pass -- the chat window floats up to its
 * ceiling (MAX_LOADED_CHAT_MESSAGES_CEILING = 8 * 150 = 1200) on a deep scroll
 * through a hidden-message-heavy history, well past the base of 150. Sizing the
 * cache above that ceiling keeps the whole active window cached (mountedIds are
 * additionally protected from eviction), so a measured row is never re-measured
 * while still in the window, while a scroll-through-everything session that churns
 * far more rows than the ceiling still limits memory.
 *
 * Exported so the eviction test derives its loop limit from the real
 * value instead of hard-coding it (and silently breaking when the cap changes).
 */
export const HEIGHT_CACHE_MAX = 2400
/** `requestAnimationFrame`, or a synchronous call where it is absent (SSR, some test environments). */
function requestFrame(cb: () => void): number {
  if (typeof requestAnimationFrame !== 'function') {
    cb()
    return 0
  }
  return requestAnimationFrame(() => cb())
}

function cancelFrame(handle: number): void {
  if (typeof cancelAnimationFrame === 'function')
    cancelAnimationFrame(handle)
}

function resolve(v: Accessor<number> | number | undefined, fallback: number): number {
  if (v === undefined)
    return fallback
  return typeof v === 'function'
    ? v()
    : v
}

/** Reject a height that cannot enter the cumulative offset map. */
function isUsableHeight(n: number): boolean {
  return n >= 0 && Number.isFinite(n)
}

export interface UseChatVirtualizerResult {
  /** Reactive [start, end) slice of `items` to render. */
  range: Accessor<ChatVirtualizerRange>
  /**
   * Monotonic counter bumped on every measurement that changes a row height.
   * Lets a consumer react to geometry changes that leave totalHeight unchanged
   * (e.g. a row above the viewport grows while one below shrinks).
   */
  geometryVersion: Accessor<number>
  /** Total height (px) of the whole in-memory window, in content coordinates. */
  totalHeight: Accessor<number>
  /**
   * The layout shift (px): `scroll = content + layoutShift`. The rows move by it, so a
   * geometry change keeps the content under the reader in place with no `scrollTop`
   * write (see chatLayoutShift). 0 without the `scrollElement` option.
   */
  layoutShift: Accessor<number>
  /** The height of the scroll spacer: `totalHeight() + layoutShift()`, never below 0. */
  spacerHeight: Accessor<number>
  /** Convert a scroll coordinate to a content coordinate: `scrollTop - layoutShift()`. */
  contentTopOf: (scrollTop: number) => number
  /** Convert a content coordinate to a scroll coordinate: `contentTop + layoutShift()`. */
  scrollTopOf: (contentTop: number) => number
  /** The spacer and the layer of rows that `rebase` writes. Pass `undefined` on unmount. */
  attachLayout: (spacer: HTMLElement | undefined, layer: HTMLElement | undefined) => void
  /**
   * Remove the layout shift with no visible change: the layer returns to 0, the spacer
   * to `totalHeight()`, and `scrollTop` changes by `-shift`. It writes the DOM itself,
   * synchronously, in the order that the clamp of `scrollTop` allows, so a caller in an
   * effect and one in an event handler get the same result. Returns the shift that it
   * removed: 0, with no write, when the shift is 0 or the scroll element is absent or
   * hidden.
   */
  rebase: () => number
  /** Top offset (px) of a row by index, in content coordinates. */
  offsetOfIndex: (index: number) => number
  /** Top offset (px) of the row with the given id, or undefined if absent, in content coordinates. */
  offsetOfId: (id: string) => number | undefined
  /** Index of the row with the given id, or -1 if absent. */
  indexOfId: (id: string) => number
  /** Index of the row whose vertical span contains the content offset `y` (clamped). */
  indexAtOffset: (y: number) => number
  /**
   * The scroll anchor for a viewport whose top edge is at the scroll coordinate
   * `scrollTop`: the row at that offset plus the pixels from the row's top to the
   * viewport top.
   */
  anchorAt: (scrollTop: number) => ScrollAnchor | null
  /** Resolve an anchor back to a scroll coordinate, or null if its row id is no longer present. */
  scrollTopForAnchor: (anchor: ScrollAnchor) => number | null
  /**
   * Resolve an anchor to a scroll coordinate, recovering a TRIMMED-away row by landing on
   * the nearest surviving row by seq. Null only when the row is gone and there is no seq /
   * no surviving server row to fall back to.
   */
  scrollTopNearAnchor: (anchor: ScrollAnchor) => number | null
  /** Measured-or-estimated height (px) of a row by index (excludes the gap). */
  heightOfIndex: (index: number) => number
  /**
   * Measured-or-estimated height (px) of a row by id (excludes the gap); the
   * global estimate for an absent row. Composes indexOfId + heightOfIndex so a
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
   * Debug accessor: a row's measured DOM height when the cache holds one. The
   * generic fallback estimate is deliberately omitted from the raw-JSON surface;
   * it is not a row-specific analytical model.
   */
  heightDebugOfId: (id: string) => { measured?: number }
  /** Current global-median estimate for rows that have not reached DOM measurement yet. */
  estimateHeight: Accessor<number>
  /** Diagnostic: the most recent geometry-changing height commit, for WARN attribution. */
  lastMeasurement: () => MeasurementCommitInfo | undefined
  /**
   * Compute (without committing) the visible slice for a given scroll coordinate.
   * `lead` extends the overscan in its `dir` (the fling direction) so a fast
   * scroll renders the rows it is about to reach; omitted falls back to symmetric overscan.
   */
  computeRange: (scrollTop: number, clientHeight: number, lead?: ViewportLead) => ChatVirtualizerRange
  /** Recompute and commit the visible slice for a scroll coordinate (see computeRange for `lead`). */
  updateViewport: (scrollTop: number, clientHeight: number, lead?: ViewportLead) => void
  /** Record a measured row height. Returns true if the offset map changed. */
  measure: (id: string, height: number) => boolean
  /**
   * Prime the measured-height cache from an offscreen DOM pre-measurement. This
   * updates geometry like `measure`, but is recorded with source 'premeasure':
   * hidden premeasurement is cache warm-up, not a visible mount.
   */
  primeHeight: (id: string, height: number, heightKey?: string) => boolean
  /**
   * Bulk variant of `primeHeight` for reload warm-start hydration: commits
   * every valid entry with the same guards, but bumps geometryVersion ONCE
   * for the whole batch — one offset-map rebuild instead of one per row.
   * Returns the number of entries adopted.
   */
  primeHeights: (entries: readonly PersistableRowHeight[]) => number
  /**
   * The measured-height cache as persistable entries, in LRU order (least
   * recently measured first) — a consumer that caps the persisted set
   * should keep the TAIL, the most recently measured rows. A mounted row's
   * provisional height, which no measure under its key produced, is left out.
   */
  snapshotHeights: () => PersistableRowHeight[]
  /**
   * Whether the current item for `id` has a fresh measured/pre-measured height,
   * or is a mounted row that keeps the height that it measured under an earlier
   * key until a read of its element under the new key (see pruneStaleKeyedHeights).
   */
  hasMeasuredHeight: (id: string) => boolean
  /**
   * Whether a measure under the current key of row `id` produced its height: as
   * `hasMeasuredHeight`, but false for a height that a mounted row keeps from an
   * earlier key. A toggled row is confirmed at the first read of its element under
   * the new key, which waits while the row replaces its content (see
   * RowMeasurer.attachRow). Reactive: a confirmation bumps `geometryVersion`.
   */
  hasConfirmedHeight: (id: string) => boolean
  /**
   * Reactive "fast user scroll in flight" flag — a momentum fling OR a fast
   * scrollbar/touch drag. Set by the scroll hook; rows entering the window
   * read it to decide skeleton-vs-full mount (ChatView's fling skeletons), and
   * no pivot element is recorded while it is set.
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
  /**
   * The ids of the rows currently MOUNTED in the DOM (the precise rendered set,
   * maintained synchronously by attach/detach). A live, read-only view -- consumers
   * read it at use time, never mutate it. The height-cache eviction protects these;
   * exposed so a sibling cache (the per-message UI state) can protect the same set
   * and never evict a visible row's choice.
   */
  mountedIds: ReadonlySet<string>
}

/**
 * Only a positive height within the estimate limit contributes to the median.
 * Cache an empty row or a large outlier for that row without changing the estimate.
 * Record each contribution on its cache entry. Eviction removes that exact value.
 */
function fallbackEstimateContribution(height: number): number | undefined {
  return height > 0 && height <= FALLBACK_ESTIMATE_OUTLIER_PX
    ? height
    : undefined
}

interface CachedMeasuredHeight {
  /** The height key of the row that the entry serves. */
  key: string | undefined
  /**
   * The height key under which a measure produced `height`. It differs from `key` while a
   * MOUNTED row keeps the height that it measured under an earlier key (see
   * pruneStaleKeyedHeights): its element still shows this height, and the next read of the
   * element confirms or replaces it. Such an entry is provisional: it leaves the cache when
   * the row unmounts unconfirmed, and no snapshot saves it.
   */
  measuredKey: string | undefined
  height: number
  fallbackContribution?: number
  /**
   * The row's kind at measure time -- the estimate bucket its contribution lives in, so
   * eviction/prune removes from the right one even after the row leaves the current list.
   */
  kind: string
}

/** Whether no measure under its key produced the height of `entry` (see CachedMeasuredHeight.measuredKey). */
function isProvisional(entry: CachedMeasuredHeight): boolean {
  return entry.key !== entry.measuredKey
}

/**
 * What a height commit changed: `moved` when it changed the offset map, `confirmed` when
 * it only confirmed a provisional height under its key, and `none` otherwise. Each change
 * owes a geometryVersion bump, because `hasConfirmedHeight` reads through geom().
 */
type HeightCommitResult = 'moved' | 'confirmed' | 'none'

/**
 * A multiset of measured row heights that answers the MEDIAN cheaply. Stored as a small
 * height->count histogram keyed by the exact measured px (no rounding -- an estimate that
 * feeds the offset map should not inject sub-pixel error, and `remove` must key on the same
 * value `add` used). `total` makes the median position O(1) and the sorted-key walk that
 * finds it runs lazily, only on the first `value()` after a mutation. That matters because
 * `value()` is read for every unmeasured row on every offset-map rebuild, while mutations
 * (a measurement commit / eviction) are comparatively rare -- so the hot path is a cached
 * O(1) read. add/remove are paired (`remove` decrements `total` in lockstep and deletes a
 * bucket that hits zero) so the count can't drift: a forgotten decrement on eviction would
 * corrupt the median for every future estimate.
 */
function createHeightMedian() {
  const counts = new Map<number, number>()
  let total = 0
  let cachedMedian: number | undefined
  const add = (height: number) => {
    counts.set(height, (counts.get(height) ?? 0) + 1)
    total += 1
    cachedMedian = undefined
  }
  const remove = (height: number) => {
    const c = counts.get(height)
    if (c === undefined)
      return
    if (c <= 1)
      counts.delete(height)
    else
      counts.set(height, c - 1)
    total -= 1
    cachedMedian = undefined
  }
  return {
    add,
    remove,
    /** The (lower) median measured height, or undefined when nothing is recorded. */
    value(): number | undefined {
      if (total === 0)
        return undefined
      if (cachedMedian !== undefined)
        return cachedMedian
      // Lower median: the element at 0-indexed floor((n-1)/2), so an even count leans to
      // the SHORTER side -- deliberate, since an over-estimated short row reserves space
      // that its measure takes back. Walk the sorted buckets, accumulating counts, to the bucket that contains
      // that rank.
      const rank = Math.floor((total - 1) / 2)
      const keys = [
        ...counts.keys(),
      ].sort((a, b) => a - b)
      let acc = 0
      let result = keys[keys.length - 1]
      for (const k of keys) {
        acc += counts.get(k) as number
        if (acc > rank) {
          result = k
          break
        }
      }
      cachedMedian = result
      return result
    },
  }
}

/**
 * The unmeasured-row height estimate, a per-kind median of measured heights. Keying by
 * message kind (user_text vs tool_result, ...) gives each row an estimate from its own
 * height distribution instead of one global mean that a few tall code/diff rows pull up --
 * the over-estimate that shrank content above the anchor and drifted the scroll pin. Every
 * measurement also feeds a kind-agnostic GLOBAL median, the fallback for a kind not yet
 * seen (and the value the public `estimateHeight` exposes for out-of-range rows).
 */
function createPerKindHeightEstimate() {
  const byKind = new Map<string, ReturnType<typeof createHeightMedian>>()
  const global = createHeightMedian()
  const kindBucket = (kind: string) => {
    let bucket = byKind.get(kind)
    if (bucket === undefined) {
      bucket = createHeightMedian()
      byKind.set(kind, bucket)
    }
    return bucket
  }
  return {
    add(kind: string, height: number) {
      kindBucket(kind).add(height)
      global.add(height)
    },
    remove(kind: string, height: number) {
      // Never auto-creates: you only remove what a prior add recorded under this kind.
      byKind.get(kind)?.remove(height)
      global.remove(height)
    },
    /** Estimate for a kind: its own median, else the global median, else `seed`. */
    value(kind: string, seed: number): number {
      return byKind.get(kind)?.value() ?? global.value() ?? seed
    },
    /** Kind-agnostic estimate (global median, else `seed`) for out-of-range/unknown rows. */
    globalValue(seed: number): number {
      return global.value() ?? seed
    },
  }
}

/** Estimate bucket for a row whose kind is absent (tests / non-classifying callers). */
const DEFAULT_ESTIMATE_KIND = ''

/**
 * Geometry engine for the virtualized chat list. Owns the per-row height cache,
 * the cumulative offset map, the visible-row slice, and the layout shift that keeps
 * the content under the reader in place across a geometry change (see
 * chatLayoutShift). It does not decide the scroll position: useChatScroll feeds
 * positions in via `updateViewport`, and asks for a `rebase` when the shift may go.
 */
export function useChatVirtualizer(opts: UseChatVirtualizerOptions): UseChatVirtualizerResult {
  const seed = opts.estimateHeight ?? DEFAULT_ESTIMATE_PX

  // Measured heights by row id. Survives row unmount so re-entry is flash-free.
  // Bounded by an LRU cap so a long session that scrolls through tens of
  // thousands of distinct messages doesn't grow it without limit (which would
  // defeat the windowing's memory goal). `measure` refreshes a row's recency,
  // and rows mount/measure as they enter the viewport, so the active window
  // (far smaller than the cap) is never the eviction target.
  const heightCache = new Map<string, CachedMeasuredHeight>()
  // Ids of the rows currently MOUNTED in the DOM (a keyed <For> attaches one el
  // per id and detaches it on unmount). This is the precise rendered set the
  // height-cache eviction must protect -- maintained synchronously by attach/
  // detach rather than derived from `range()`, which commits on a deferred rAF and
  // could momentarily omit a row that just scrolled in, letting it be evicted the
  // same frame it measured.
  const mountedIds = new Set<string>()
  // Reactive "fast user scroll in flight" flag, set by the scroll hook for ANY
  // genuine user scroll at fling velocity — momentum flings AND direct drags
  // (scrollbar thumb, touch). Rows entering the window read it to decide
  // skeleton-vs-full mount (ChatView's fling skeletons).
  const [fastScrollActive, setFastScrollActive] = createSignal(false)
  // Diagnostic-only: the most recent geometry-changing height commit, so the
  // `content moved without a scroll` WARN can name the row that likely caused a move
  // (see MeasurementCommitInfo). Non-reactive -- read at WARN time, never a render input.
  let lastMeasurement: MeasurementCommitInfo | undefined
  let measurementCommitSeq = 0
  // Per-kind median of measured heights. An unmeasured row uses the median for its OWN
  // kind (falling back to the global median, then the seed) until visible or hidden DOM
  // measurement commits its real height -- see createPerKindHeightEstimate.
  const heightEstimate = createPerKindHeightEstimate()
  const kindOf = (item: VirtualItem): string => item.kind ?? DEFAULT_ESTIMATE_KIND
  // A single row's estimate, keyed by its kind. Read for every unmeasured row inside geom.
  const estimateFor = (item: VirtualItem): number => heightEstimate.value(kindOf(item), seed)
  // Kind-agnostic estimate: the generic fallback for out-of-range rows and the value the
  // public accessor exposes. Not reactive on its own (the medians are plain state); reads
  // inside geom() recompute via geomVersion when a measurement commits.
  const estimateHeight: Accessor<number> = () => heightEstimate.globalValue(seed)

  const removeFallbackContribution = (entry: CachedMeasuredHeight | undefined): void => {
    if (entry?.fallbackContribution !== undefined)
      heightEstimate.remove(entry.kind, entry.fallbackContribution)
  }

  // The mounted rows whose height the last prune kept under a new key, provisionally.
  // An effect below asks the measurer to read each of them again.
  const rekeyedMountedIds: string[] = []

  // A height measured under an earlier key no longer describes a row that is not
  // mounted, so the row falls back to the estimate until a measure. A MOUNTED row keeps
  // its height, provisionally, under the new key: its element still shows that height,
  // and the measurer reads the element again at its next flush. That read commits the
  // new height, or confirms the kept one when the row keeps its height under the new
  // key, which no resize would report. So the row keeps its slot, and the rows below it
  // move once, when the row's content really changes. A diff view that mounts its new
  // layout in idle slices behind the old one holds that read until it swaps the layouts
  // (see RenderReadiness.trackReplacing), and its row stays visible until then. Hiding
  // the row until a measure under the new key, as an unmeasured row does, would hide it
  // for the whole mount. A key that changes back to the one that measured the height
  // confirms it again.
  const pruneStaleKeyedHeights = (list: readonly VirtualItem[]): void => {
    for (const item of list) {
      const cached = heightCache.get(item.id)
      if (cached === undefined || cached.key === item.heightKey)
        continue
      if (mountedIds.has(item.id)) {
        const kept = { ...cached, key: item.heightKey }
        heightCache.set(item.id, kept)
        if (isProvisional(kept))
          rekeyedMountedIds.push(item.id)
        continue
      }
      heightCache.delete(item.id)
      removeFallbackContribution(cached)
    }
  }

  // A row's resolved height: its measured height when the height cache holds one, else the
  // per-kind median estimate. An UNMEASURED row reserves its estimate whether it is
  // in the rendered window or not, so scrolling a row into the window causes no offset
  // change -- only its later measurement shifts geometry (a small estimate->measured
  // correction), instead of the estimate->0->measured bounce a collapse-to-0 caused. An
  // in-window unmeasured row is rendered `visibility:hidden` (ChatView's hideUntilMeasured)
  // until its height commits, so reserving space here can't paint over the next row.
  const cachedMeasuredHeight = (item: VirtualItem): number | undefined => {
    const cached = heightCache.get(item.id)
    return cached !== undefined && cached.key === item.heightKey
      ? cached.height
      : undefined
  }
  const resolvedHeight = (item: VirtualItem): number =>
    cachedMeasuredHeight(item) ?? estimateFor(item)

  // Bumped whenever a measurement changes the geometry, so the `geom` memo and
  // every reactive getter recompute. Plain caches stay non-reactive for speed.
  const [geomVersion, setGeomVersion] = createSignal(0)

  // A provisional height describes only the element that still shows it. When the
  // row's last element unmounts, the row is unmeasured under its key again.
  const dropProvisionalHeight = (id: string): void => {
    const cached = heightCache.get(id)
    if (cached === undefined || !isProvisional(cached))
      return
    heightCache.delete(id)
    removeFallbackContribution(cached)
    setGeomVersion(v => v + 1)
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
  const gapAfter = (list: VirtualItem[], i: number): number => {
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
  // stable across sequence changes. A height commit bumps only
  // geomVersion, which rebuilds the offsets below; re-running the n string-keyed
  // Map.set calls per commit rebuilt a map that could not have changed (dozens of
  // commits per fling through unmeasured history made that the dominant rebuild
  // cost). Stale-keyed height pruning lives here for the same reason: a cached
  // height goes stale only when its item's heightKey changes, which changes the
  // list identity -- and resolvedHeight key-checks anyway, so a commit-time skip
  // of the prune can never serve a stale height. The prune keeps a mounted row's
  // height under the new key, provisionally (see pruneStaleKeyedHeights).
  const rowIndex = createMemo(() => {
    const list = opts.items()
    pruneStaleKeyedHeights(list)
    const n = list.length
    const indexById = new Map<string, number>()
    for (let i = 0; i < n; i++) {
      const item = list[i]
      if (item !== undefined)
        indexById.set(item.id, i)
    }
    return { list, indexById, n }
  })

  // Cumulative offsets, recomputed when items or measurements change, with the resolved
  // height of each row and whether a measure produced it: the snapshot that the layout
  // shift compares. The identity map rides along from rowIndex so every consumer keeps
  // its one-stop read.
  const geom = createMemo((): VirtualizerGeometry => {
    const rebuildStart = monotonicNow()
    geomVersion()
    const { list, indexById, n } = rowIndex()
    const offsets = new Float64Array(n + 1)
    const heights = new Float64Array(n)
    const measured = new Uint8Array(n)
    for (let i = 0; i < n; i++) {
      const item = list[i]
      if (item === undefined)
        continue
      const cached = cachedMeasuredHeight(item)
      const height = cached ?? estimateFor(item)
      heights[i] = height
      measured[i] = cached === undefined
        ? 0
        : 1
      offsets[i + 1] = (offsets[i] ?? 0) + height + gapAfter(list, i)
    }
    // Rebuilding the offset map is an O(n) array fill (microseconds for any real n), so this
    // should never trip -- but instrumenting it rules the offset map IN or OUT as a stall
    // source instead of leaving it a suspect. Only a slow rebuild logs.
    warnSlowScrollPhase('geomRebuild', monotonicNow() - rebuildStart, { rows: n })
    return {
      offsets,
      heights,
      measured,
      indexById,
      list,
      n,
    }
  })

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
    if (index < 0 || index >= g.n)
      return estimateHeight()
    const item = g.list[index]
    return item !== undefined
      ? resolvedHeight(item)
      : estimateHeight()
  }

  const heightOfId = (id: string): number => heightOfIndex(indexOfId(id))

  const gapAboveOf = (id: string): number => {
    const i = indexOfId(id)
    return i > 0
      ? gapAfter(geom().list, i - 1)
      : 0
  }

  const currentHeightKey = (id: string): string | undefined => {
    const g = geom()
    const index = g.indexById.get(id)
    const item = index === undefined
      ? undefined
      : g.list[index]
    return item !== undefined
      ? item.heightKey
      : undefined
  }

  // The cache entry of row `id` under its current key, read through geom() so that a
  // reactive caller re-reads it when a measurement commits.
  const currentHeightEntry = (id: string): CachedMeasuredHeight | undefined => {
    const g = geom()
    const index = g.indexById.get(id)
    const item = index === undefined
      ? undefined
      : g.list[index]
    const cached = heightCache.get(id)
    return item !== undefined && cached !== undefined && cached.key === item.heightKey
      ? cached
      : undefined
  }
  const hasMeasuredHeight = (id: string): boolean => currentHeightEntry(id) !== undefined
  const hasConfirmedHeight = (id: string): boolean => {
    const entry = currentHeightEntry(id)
    return entry !== undefined && !isProvisional(entry)
  }

  // Debug-only: surface the measured height without exposing the generic fallback
  // as though it were a row-specific estimate.
  const heightDebugOfId = (id: string): { measured?: number } => {
    const rawMeasured = heightCache.get(id)
    const i = indexOfId(id)
    if (i < 0) {
      return rawMeasured !== undefined
        ? { measured: rawMeasured.height }
        : {}
    }
    const item = geom().list[i]
    const measured = item !== undefined && rawMeasured !== undefined && rawMeasured.key === item.heightKey
      ? rawMeasured.height
      : undefined
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

  // The layout shift: `scroll = content + layoutShift`. A computation over `geom` below
  // sets it, before the render effects of the same update run.
  const [layoutShift, setLayoutShift] = createSignal(0)
  // The pivot element that the shift rule measures (see chatLayoutPivot). It exists only
  // with a scroll element, and it needs the row measurer, so it is created below.
  let layoutPivot: LayoutPivot | undefined
  const contentTopOf = (scrollTop: number): number => scrollTop - layoutShift()
  const scrollTopOf = (contentTop: number): number => contentTop + layoutShift()
  const spacerHeight = (): number => Math.max(0, totalHeight() + layoutShift())

  // The anchor math works in content coordinates. These functions take and return
  // scroll coordinates, so every caller outside the virtualizer stays in one space.
  const anchorAt = (scrollTop: number): ScrollAnchor | null =>
    anchorAtOffset(anchorGeometry(), contentTopOf(scrollTop))
  const scrollTopForAnchor = (anchor: ScrollAnchor): number | null => {
    const contentTop = resolveAnchorScrollTop(anchorGeometry(), anchor)
    return contentTop === null
      ? null
      : scrollTopOf(contentTop)
  }
  const scrollTopNearAnchor = (anchor: ScrollAnchor): number | null => {
    const contentTop = resolveNearestAnchorScrollTop(anchorGeometry(), anchor)
    return contentTop === null
      ? null
      : scrollTopOf(contentTop)
  }

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
    // row alone for one frame. (A negative position -- rubber-band overscroll, or the
    // blank band above the first row under a positive layout shift -- floors to row 0.)
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
    const startItem = g.list[start]
    if (guardBandPx > 0 && end === start + 1 && startItem !== undefined && resolvedHeight(startItem) > guardBandPx) {
      start = Math.max(0, start - 1)
      end = Math.min(n, end + 1)
    }
    return { start, end }
  }

  const computeRange = (
    scrollTop: number,
    clientHeight: number,
    lead?: ViewportLead,
  ): ChatVirtualizerRange => computeContentRange(contentTopOf(scrollTop), clientHeight, lead)

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

  const updateViewport = (
    scrollTop: number,
    clientHeight: number,
    lead?: ViewportLead,
  ) => {
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
    layoutPivot?.scheduleRecord()
  }

  /**
   * The commit core shared by the single-row paths (which bump
   * geometryVersion per commit) and the bulk hydration path (which bumps it
   * once per batch): validates, updates the cache + estimate medians +
   * eviction, and records the commit diagnostic. `currentKey`/`kind` are
   * passed in so a bulk caller can resolve them from ONE geom() snapshot
   * instead of re-reading geometry per row. Returns what the commit changed
   * (the caller owes a geometryVersion bump for any change).
   */
  const applyHeightCommit = (
    id: string,
    height: number,
    heightKey: string | undefined,
    source: MeasurementCommitInfo['source'],
    currentKey: string | undefined,
    kind: string,
  ): HeightCommitResult => {
    // DOM callers verify layout before this numeric commit. A valid empty row
    // has a zero height. It completes measurement without changing the estimate.
    if (!isUsableHeight(height))
      return 'none'
    if (heightKey !== currentKey)
      return 'none'
    const stalePrev = heightCache.get(id)
    if (stalePrev !== undefined && stalePrev.key !== heightKey) {
      heightCache.delete(id)
      removeFallbackContribution(stalePrev)
    }
    const prevEntry = heightCache.get(id)
    const prev = prevEntry?.height
    // The height the offset map reserved for this row BEFORE this commit -- the per-kind
    // estimate for a first measure (read now, before this row's contribution is folded in)
    // or the previously cached height for a re-measure. Its diff from the new height is
    // exactly what shifts the offset map (the layout shift then holds the view).
    const assumedHeight = prevEntry !== undefined
      ? prevEntry.height
      : heightEstimate.value(kind, seed)
    const nextContribution = fallbackEstimateContribution(height)
    // A re-measure within epsilon of the prior measured height is noise: the
    // offset map doesn't change. Still refresh the row's LRU recency (delete +
    // re-set moves it to the most-recently-used end) so a row that stays mounted
    // and re-measures at a stable height can't age to the eviction front and get
    // evicted while still visible. The measure confirms a provisional height
    // under its key.
    if (prev !== undefined && Math.abs(prev - height) < MEASURE_EPSILON_PX) {
      const prevContributes = prevEntry?.fallbackContribution !== undefined
      const nextContributes = nextContribution !== undefined
      if (prevContributes === nextContributes && prevEntry !== undefined) {
        heightCache.delete(id)
        heightCache.set(id, { ...prevEntry, measuredKey: heightKey })
        return isProvisional(prevEntry)
          ? 'confirmed'
          : 'none'
      }
    }
    // A row's FIRST measurement is the first DOM height for that row; re-measures
    // (async highlight growth) are geometry updates, distinguished in the commit info.
    const isFirst = prev === undefined
    // Re-insert so this row becomes the most-recently-used entry (Map preserves
    // insertion order; a plain set on an existing key would keep its old, stale
    // position and risk evicting a freshly-measured row).
    heightCache.delete(id)
    heightCache.set(id, {
      key: heightKey,
      measuredKey: heightKey,
      height,
      kind,
      ...(nextContribution !== undefined
        ? { fallbackContribution: nextContribution }
        : {}),
    })
    if (prevEntry === undefined) {
      if (nextContribution !== undefined)
        heightEstimate.add(kind, nextContribution)
    }
    else {
      // A re-measure. Route each contribution to its OWN bucket: the previous one lives in
      // prevEntry.kind (where a prior add recorded it), the new one belongs to THIS commit's
      // kind. These are normally the same bucket -- a reclassification that changes kind also
      // changes a heightKey input, so prevEntry would not have survived the stale-key delete
      // above -- but keying `remove` on prevEntry.kind and `add` on the current kind keeps the
      // per-kind medians correct even if that coupling ever loosens, instead of stranding a
      // phantom count in the old bucket. (The global median nets out to the same remove+add.)
      const prevContribution = prevEntry.fallbackContribution
      if (prevContribution !== undefined)
        heightEstimate.remove(prevEntry.kind, prevContribution)
      if (nextContribution !== undefined)
        heightEstimate.add(kind, nextContribution)
    }
    // Evict the least-recently-measured rows once over the cap, keeping the
    // per-kind median estimate consistent (remove each evicted height from its
    // bucket first) and never dropping a currently-MOUNTED row (the live mountedIds
    // set, so a row still on screen keeps its measured height instead of falling
    // back to the estimate).
    if (heightCache.size > HEIGHT_CACHE_MAX) {
      capMapInsertionOrder(heightCache, HEIGHT_CACHE_MAX, {
        protect: mountedIds,
        onEvict: (oldest) => {
          removeFallbackContribution(heightCache.get(oldest))
        },
      })
    }
    // Record the commit that is about to move the offset map, so a WARN about a move of
    // the content at the viewport top can name its likely cause (see MeasurementCommitInfo).
    measurementCommitSeq += 1
    lastMeasurement = {
      id,
      kind,
      source,
      firstMeasure: isFirst,
      assumedHeight,
      newHeight: height,
      delta: height - assumedHeight,
      commitSeq: measurementCommitSeq,
    }
    return 'moved'
  }
  const commitMeasuredHeight = (
    id: string,
    height: number,
    heightKey: string | undefined,
    source: MeasurementCommitInfo['source'],
  ): boolean => {
    const { list, indexById } = rowIndex()
    const index = indexById.get(id)
    const item = index === undefined
      ? undefined
      : list[index]
    if (item === undefined)
      return false
    const result = applyHeightCommit(id, height, heightKey, source, item.heightKey, kindOf(item))
    if (result !== 'none')
      setGeomVersion(v => v + 1)
    return result === 'moved'
  }
  const measure = (id: string, height: number): boolean =>
    commitMeasuredHeight(id, height, currentHeightKey(id), 'visible')
  const commitPremeasureHeight = (id: string, height: number, heightKey?: string): boolean => {
    // Once a visible row has committed a measurement, the live DOM is the
    // authoritative geometry. A hidden premeasure queued before mount can finish
    // later; don't let that hidden-layout read replace the visible height.
    if (mountedIds.has(id) && hasMeasuredHeight(id))
      return false
    return commitMeasuredHeight(id, height, heightKey, 'premeasure')
  }
  const primeHeight = (id: string, height: number, heightKey?: string): boolean => {
    if (!rowIndex().indexById.has(id))
      return false
    if (mountedIds.has(id) && hasMeasuredHeight(id))
      return false
    return commitPremeasureHeight(id, height, heightKey)
  }
  const primeHeights = (entries: readonly PersistableRowHeight[]): number => {
    if (entries.length === 0)
      return 0
    // ONE geometry snapshot for the whole batch. applyHeightCommit never
    // bumps geomVersion itself, so `g` stays valid (and cheap) across the
    // loop; per-entry commitMeasuredHeight calls would instead re-derive
    // geometry per row and rebuild the offset map once per entry.
    const g = geom()
    let adopted = 0
    let changed = false
    for (const entry of entries) {
      const index = g.indexById.get(entry.id)
      if (index === undefined)
        continue
      const item = g.list[index]
      if (item === undefined)
        continue
      // Mirror primeHeight's guard: a live visible measurement is authoritative.
      if (mountedIds.has(entry.id) && cachedMeasuredHeight(item) !== undefined)
        continue
      const result = applyHeightCommit(entry.id, entry.height, entry.heightKey, 'premeasure', item.heightKey, kindOf(item))
      if (result === 'moved')
        adopted += 1
      if (result !== 'none')
        changed = true
    }
    if (changed)
      setGeomVersion(v => v + 1)
    return adopted
  }
  const snapshotHeights = (): PersistableRowHeight[] => {
    const out: PersistableRowHeight[] = []
    for (const [id, cached] of heightCache) {
      // No measure under its key produced a provisional height.
      if (!isProvisional(cached))
        out.push({ id, heightKey: cached.key, height: cached.height })
    }
    return out
  }

  // The ResizeObserver / batched-microtask-flush DOM glue lives in createRowMeasurer
  // (its coupling here is `measure`, the height key and the shared mountedIds set),
  // keeping this file the offset engine. The default
  // scheduler/observer are the production ones; a unit test injects fakes to drive
  // the flush timing deterministically.
  const measurer = createRowMeasurer({
    measure,
    mountedIds,
    onRowUnmounted: dropProvisionalHeight,
    currentMeasurementKey: currentHeightKey,
    // The DOM read and the offset map ignore the same jitter, by one constant.
    measureEpsilonPx: MEASURE_EPSILON_PX,
  })
  const { attachRow, detachRow } = measurer
  onCleanup(measurer.dispose)
  // Read each mounted row that the last prune re-keyed again, at the measurer's next
  // flush: a row whose new content keeps its height never resizes, and only a read of
  // its element confirms the height under the new key.
  createEffect(() => {
    rowIndex()
    for (const id of rekeyedMountedIds.splice(0))
      measurer.measureAgain(id)
  })

  // ---- Layout shift (see chatLayoutShift) ----

  // The row whose measured body the viewport top cuts by more than the top-edge slack,
  // with its mounted element: the row in which the pivot element is recorded.
  const pivotRow = (scrollTop: number): PivotRow | undefined => {
    const g = geom()
    if (g.n === 0)
      return undefined
    const shift = layoutShift()
    const contentTop = scrollTop - shift
    const index = indexAtOffset(contentTop)
    const item = g.list[index]
    if (item === undefined || g.measured[index] !== 1)
      return undefined
    const top = g.offsets[index] ?? 0
    const bottom = top + (g.heights[index] ?? 0)
    if (contentTop - top <= TOP_EDGE_SLACK_PX || contentTop >= bottom)
      return undefined
    const element = measurer.elementOf(item.id)
    return element === undefined
      ? undefined
      : { id: item.id, element, top: top + shift }
  }

  const scrollElement = opts.scrollElement
  if (scrollElement !== undefined) {
    const pivot = createLayoutPivot({
      scrollElement,
      pivotRow,
      hasUncommittedHeight: measurer.hasUncommittedHeight,
      isSuspended: fastScrollActive,
      requestFrame,
      cancelFrame,
      onContentMoved: createContentMovedReporter({
        layoutShift: () => untrack(layoutShift),
        lastMeasurement: () => lastMeasurement,
      }),
    })
    layoutPivot = pivot
    onCleanup(pivot.dispose)

    // The snapshot that the current shift applies to. The computation keeps its own
    // copy: `geom` can recompute several times inside one batch, and the shift must
    // compare the last snapshot that it saw with the one that reaches the DOM.
    let shiftedSnapshot: LayoutSnapshot | undefined
    // A computation, not an effect: the rows and the layer must reach the DOM in the
    // same flush, so the shift must be set before the render effects run. It tracks
    // only `geom`.
    createComputed(() => {
      const next = geom()
      untrack(() => {
        const previous = shiftedSnapshot
        shiftedSnapshot = next
        const el = scrollElement()
        if (el === undefined || el.clientHeight === 0) {
          setLayoutShift(0)
          return
        }
        setLayoutShift(nextLayoutShift({
          previous,
          next,
          previousShift: layoutShift(),
          scrollTop: clampScrollTop(el, el.scrollTop),
          pinBottom: opts.pinBottom?.() ?? false,
          intraRowShift: pivot.takeIntraRowShift,
        }))
      })
    })
    // A geometry change or a new shift needs a new record of the pivot element. So does
    // the end of a fast scroll: each frame during it dropped the record, and a scroll
    // that stopped schedules no further frame.
    createEffect(on([geom, layoutShift, fastScrollActive], () => pivot.scheduleRecord()))
  }

  // The two elements that `rebase` writes: the spacer and the layer that holds the rows.
  let layoutSpacer: HTMLElement | undefined
  let layoutLayer: HTMLElement | undefined
  const attachLayout = (spacer: HTMLElement | undefined, layer: HTMLElement | undefined): void => {
    layoutSpacer = spacer
    layoutLayer = layer
  }

  const rebase = (): number => {
    const shift = untrack(layoutShift)
    const el = scrollElement?.()
    if (shift === 0 || el === undefined || el.clientHeight === 0)
      return 0
    const target = el.scrollTop - shift
    const unshiftLayout = () => {
      if (layoutSpacer !== undefined)
        layoutSpacer.style.height = `${Math.max(0, untrack(totalHeight))}px`
      if (layoutLayer !== undefined)
        layoutLayer.style.transform = 'translateY(0px)'
    }
    // The browser clamps `scrollTop` to the scroll range, so the range must hold the
    // target when `scrollTop` is written: a positive shift shrinks the spacer, so the
    // position moves first; a negative shift grows it, so the layout moves first.
    if (shift > 0) {
      el.scrollTop = target
      unshiftLayout()
    }
    else {
      unshiftLayout()
      el.scrollTop = target
    }
    // The style bindings write the same two values again on the next flush.
    setLayoutShift(0)
    return shift
  }

  return {
    range,
    geometryVersion: geomVersion,
    totalHeight,
    layoutShift,
    spacerHeight,
    contentTopOf,
    scrollTopOf,
    attachLayout,
    rebase,
    offsetOfIndex,
    offsetOfId,
    indexOfId,
    indexAtOffset,
    anchorAt,
    scrollTopForAnchor,
    scrollTopNearAnchor,
    heightOfIndex,
    heightOfId,
    gapAboveOf,
    heightDebugOfId,
    estimateHeight,
    lastMeasurement: () => lastMeasurement,
    computeRange,
    updateViewport,
    measure,
    primeHeight,
    primeHeights,
    snapshotHeights,
    hasMeasuredHeight,
    hasConfirmedHeight,
    fastScrollActive,
    setFastScrollActive,
    attachRow,
    detachRow,
    mountedIds,
  }
}
