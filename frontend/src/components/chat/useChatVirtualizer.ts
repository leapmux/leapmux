import type { Accessor } from 'solid-js'
import type { AnchorOffsetGeometry } from './chatScrollAnchor'
import type { RowAttachMeasureStats } from './createRowMeasurer'
import type { ScrollAnchor } from '~/stores/chatTypes'
import { batch, createMemo, createSignal, onCleanup } from 'solid-js'
import { clamp } from '~/lib/clamp'
import { createLogger } from '~/lib/logger'
import { capMapInsertionOrder } from '~/lib/mapLru'
import { anchorAtOffset, resolveAnchorScrollTop, resolveNearestAnchorScrollTop } from './chatScrollAnchor'
import { monotonicNow, SCROLL_PHASE_STALL_WARN_MS } from './chatScrollGeometry'
import { createRowMeasurer } from './createRowMeasurer'

// Shares the 'chatScroll' channel with useChatScroll so a stall attributed to the offset-map
// rebuild sits alongside the refreshViewport / premeasure phase warnings.
const virtLog = createLogger('chatScroll')

/**
 * Minimal per-row descriptor the virtualizer needs. `id` keys the height
 * cache, the offset map, AND the scroll anchor — it is stable across window
 * trims and unique per row, where `seq` is NOT: every optimistic local message
 * shares seq 0n, so keying geometry by seq would collapse stacked locals onto
 * one offset. `hasSpanLines` drives the inter-row gap (span-line rows sit closer
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
 * rows -- a streaming text chunk or a command-stream delta that bumps the agent's message
 * version (and so re-walks the whole window in walkWindow) without adding/removing/
 * reordering a visible row or bumping its content version -- keeps the PRIOR array
 * reference. That stops the geom rebuild and the scroll re-pin effect from firing per
 * delta, which is the dominant per-delta churn in a hidden-heavy window (where walkWindow
 * re-classifies all raw rows, including the hidden ones, every time). Row CONTENT still
 * updates: the rendered slice reads visibleEntries directly, and a real height change is
 * still caught by the row's measurement (geometryVersion), not by this array's identity.
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

export interface ViewportUpdateStats {
  scrollTop: number
  clientHeight: number
  leadDir: ViewportLeadDir | undefined
  leadPx: number
  previousStart: number
  previousEnd: number
  nextStart: number
  nextEnd: number
  previousRows: number
  nextRows: number
  addedRows: number
  removedRows: number
  rangeChanged: boolean
  computeMs: number
  totalMs: number
  tallRow?: TallRowRangeDiagnostics
}

export interface TallRowRangeDiagnostics {
  reason: 'single-row-window' | 'tall-row-in-range'
  rowCount: number
  totalHeight: number
  maxScrollTop: number
  clampedScrollTop: number
  scrollTopWasClamped: boolean
  overscanPx: number
  overTop: number
  overBottom: number
  guardBandPx: number
  overscanTop: number
  overscanBottom: number
  rawStart: number
  rawEnd: number
  expandedForTallRow: boolean
  tallRowIndex: number
  tallRowId: string
  tallRowHeight: number
  tallRowHeightSource: 'measured' | 'estimated'
  tallRowTop: number
  tallRowBottom: number
  viewportTopOffsetInTallRow: number
  viewportBottomOffsetInTallRow: number
}

export interface TallRowMeasureStats {
  id: string
  source: 'visible' | 'premeasure'
  height: number
  previousHeight?: number
  deltaHeight?: number
  firstMeasure: boolean
  fallbackExcluded: boolean
  previousFallbackExcluded: boolean
  fallbackEstimateBefore: number
  fallbackEstimateAfter: number
  geometryVersionBefore: number
  geometryVersionAfter: number
  indexBefore: number
  indexAfter: number
  rowTopBefore: number
  rowTopAfter: number
  totalHeightBefore: number
  totalHeightAfter: number
}

interface DeferredPremeasure {
  id: string
  height: number
  heightKey: string | undefined
}

/**
 * The most recent height commit that changed the offset map -- diagnostic attribution for
 * the scroll-anchor drift WARN. The re-pin fires off `totalHeight()` and only sees THAT the
 * geometry moved, not which row moved it; this names the row and by how much, so a logged
 * drift can be traced to its cause: `firstMeasure` (an estimate->real correction, i.e. the
 * per-kind median was off for this row / it outran premeasure) vs a re-measure (a
 * premeasured-vs-visible height mismatch or a chrome/content change), and `delta`
 * (= newHeight - assumedHeight) against the drift's residualPx tells whether this single
 * commit explains it or a batch did. `commitSeq` is monotonic, so two consecutive WARNs
 * reveal how many commits fell between them.
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
   * Fired once, on a row's FIRST visible-DOM measurement with its real height. Not
   * fired on re-measures or `primeHeight` premeasure commits.
   */
  onFirstMeasure?: (id: string, measuredHeight: number) => void
  /**
   * Runtime gate for debug/perf hooks. Defaults to enabled when a hook is present;
   * ChatView wires this to the debug-logging flag so normal scrolling does not
   * allocate timing payloads.
   */
  shouldReportPerf?: () => boolean
  /**
   * Debug/perf hook for synchronous viewport range commits. It must not write
   * virtualizer-owned reactive state; ChatView uses it only for debug logging.
   */
  onViewportUpdate?: (stats: ViewportUpdateStats) => void
  /**
   * Debug/perf hook for the immediate layout read on visible row mount. It must not
   * write virtualizer-owned reactive state; ChatView uses it only for debug logging.
   */
  onRowAttachMeasure?: (stats: RowAttachMeasureStats) => void
  /**
   * Debug/perf hook for measurements whose row is taller than the normal unknown-row
   * fallback band. It is deliberately separate from `onRowAttachMeasure` because
   * hidden premeasurement can also commit the geometry delta that later affects scroll.
   */
  onTallRowMeasure?: (stats: TallRowMeasureStats) => void
}

const DEFAULT_OVERSCAN_PX = 800
/**
 * Seed/fallback row height (px) for a row that has not reached any DOM measurement
 * path yet. Once visible or hidden DOM measures rows, the per-kind median replaces it.
 */
export const DEFAULT_ESTIMATE_PX = 96
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
 * far more rows than the ceiling still bounds memory.
 *
 * Exported so the eviction test derives its over-cap loop bound from the real
 * value instead of hard-coding it (and silently breaking when the cap changes).
 */
export const HEIGHT_CACHE_MAX = 2400
function resolve(v: Accessor<number> | number | undefined, fallback: number): number {
  if (v === undefined)
    return fallback
  return typeof v === 'function' ? v() : v
}

/**
 * Whether `n` is a usable row height: finite and strictly positive. `n > 0`
 * already rejects NaN (`NaN > 0` is false); Number.isFinite additionally rejects
 * Infinity. A non-usable DOM measurement would poison the cumulative offset map
 * (every offset past that row becomes NaN/Infinity, blanking the list), so
 * measurements are ignored whenever this is false.
 */
function isUsableHeight(n: number): boolean {
  return n > 0 && Number.isFinite(n)
}

// Largest index in [0, n-1] for which `pred(mid)` holds, or 0 when none does.
// Lower-bound scan: each satisfied probe raises the floor. Module-level and pure
// (state passed in), so it isn't re-allocated per useChatVirtualizer instance and
// can be unit-tested directly.
function largestIndexWhere(n: number, pred: (mid: number) => boolean): number {
  let lo = 0
  let hi = n - 1
  let ans = 0
  while (lo <= hi) {
    const mid = (lo + hi) >>> 1
    if (pred(mid)) {
      ans = mid
      lo = mid + 1
    }
    else {
      hi = mid - 1
    }
  }
  return ans
}

// Smallest index in [0, n-1] for which `pred(mid)` holds, or `fallback` when none
// does. Upper-bound scan: each satisfied probe lowers the ceiling.
function smallestIndexWhere(n: number, pred: (mid: number) => boolean, fallback: number): number {
  let lo = 0
  let hi = n - 1
  let ans = fallback
  while (lo <= hi) {
    const mid = (lo + hi) >>> 1
    if (pred(mid)) {
      ans = mid
      hi = mid - 1
    }
    else {
      lo = mid + 1
    }
  }
  return ans
}

export interface UseChatVirtualizerResult {
  /** Reactive [start, end) slice of `items` to render. */
  range: Accessor<ChatVirtualizerRange>
  /**
   * Monotonic counter bumped on every measurement that changes a row height.
   * Lets a consumer re-pin scroll on geometry changes that leave totalHeight
   * unchanged (e.g. a row above the anchor grows while one below shrinks).
   */
  geometryVersion: Accessor<number>
  /** Total height (px) of the whole in-memory window — the scroll spacer height. */
  totalHeight: Accessor<number>
  /** Top offset (px) of a row by index. */
  offsetOfIndex: (index: number) => number
  /** Top offset (px) of the row with the given id, or undefined if absent. */
  offsetOfId: (id: string) => number | undefined
  /** Index of the row with the given id, or -1 if absent. */
  indexOfId: (id: string) => number
  /** Index of the row whose vertical span contains offset `y` (clamped). */
  indexAtOffset: (y: number) => number
  /**
   * The scroll anchor for a viewport whose top edge is at `scrollTop`: the
   * row at that offset plus the pixels from the row's top to the viewport top.
   */
  anchorAt: (scrollTop: number) => ScrollAnchor | null
  /** Resolve an anchor back to a scrollTop, or null if its row id is no longer present. */
  scrollTopForAnchor: (anchor: ScrollAnchor) => number | null
  /**
   * Resolve an anchor to a scrollTop, recovering a TRIMMED-away row by landing on the
   * nearest surviving row by seq. Null only when the row is gone and there is no seq /
   * no surviving server row to fall back to.
   */
  scrollTopNearAnchor: (anchor: ScrollAnchor) => number | null
  /** Measured-or-estimated height (px) of a row by index (excludes the gap). */
  heightOfIndex: (index: number) => number
  /**
   * Debug accessor: a row's measured DOM height when the cache holds one. The
   * generic fallback estimate is deliberately omitted from the raw-JSON surface;
   * it is not a row-specific analytical model.
   */
  heightDebugOfId: (id: string) => { measured?: number }
  /** Current global-median estimate for rows that have not reached DOM measurement yet. */
  estimateHeight: Accessor<number>
  /** Diagnostic: the most recent geometry-changing height commit, for drift-WARN attribution. */
  lastMeasurement: () => MeasurementCommitInfo | undefined
  /**
   * Compute (without committing) the visible slice for a given scroll position.
   * `lead` extends the overscan in its `dir` (the fling direction) so a fast
   * scroll renders the rows it is about to reach; omitted falls back to symmetric overscan.
   */
  computeRange: (scrollTop: number, clientHeight: number, lead?: ViewportLead) => ChatVirtualizerRange
  /** Recompute and commit the visible slice for a scroll position (see computeRange for `lead`). */
  updateViewport: (scrollTop: number, clientHeight: number, lead?: ViewportLead) => void
  /** Record a measured row height. Returns true if the offset map changed. */
  measure: (id: string, height: number) => boolean
  /**
   * Prime the measured-height cache from an offscreen DOM pre-measurement. This
   * updates geometry like `measure`, but intentionally does not fire
   * `onFirstMeasure`: hidden premeasurement is cache warm-up, not a visible mount.
   */
  primeHeight: (id: string, height: number, heightKey?: string) => boolean
  /** Whether the current item for `id` has a fresh measured/pre-measured height. */
  hasMeasuredHeight: (id: string) => boolean
  /**
   * Whether a hidden premeasurement already produced a valid height but its
   * geometry commit is queued behind the momentum-scroll deferral gate.
   */
  hasPendingPremeasuredHeight: (id: string) => boolean
  /** Gate DOM measurement commits while native momentum owns scrollTop. */
  setVisibleMeasurementDeferral: (defer: boolean) => void
  /** Whether row measurements are waiting behind the deferral gate. */
  hasDeferredMeasurements: () => boolean
  /** Commit queued row measurements in one batch. */
  flushDeferredMeasurements: () => boolean
  /** Ref callback: start observing a rendered row element. */
  attachRow: (id: string, el: HTMLElement) => void
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
 * A measured height only feeds the estimate when it is within the plausible band; a
 * pathological outlier (a giant unwindowed diff) is cached for its own row but kept out
 * of the estimate so it can't distort what an UNMEASURED row is assumed to be. The median
 * already resists outliers, but excluding them also bounds the histogram's key range and
 * keeps the `fallbackExcluded` diagnostic meaningful. Returns the contribution height, or
 * undefined when the row is an outlier (recorded on its cache entry so eviction removes
 * exactly what was added).
 */
function fallbackEstimateContribution(height: number): number | undefined {
  return height <= FALLBACK_ESTIMATE_OUTLIER_PX ? height : undefined
}

interface CachedMeasuredHeight {
  key: string | undefined
  height: number
  fallbackContribution?: number
  /**
   * The row's kind at measure time -- the estimate bucket its contribution lives in, so
   * eviction/prune removes from the right one even after the row leaves the current list.
   */
  kind: string
}

function cachedMeasuredHeightEntry(
  key: string | undefined,
  height: number,
  fallbackContribution: number | undefined,
  kind: string,
): CachedMeasuredHeight {
  return fallbackContribution === undefined
    ? { key, height, kind }
    : { key, height, fallbackContribution, kind }
}

interface ComputedRange {
  range: ChatVirtualizerRange
  tallRow?: TallRowRangeDiagnostics
}

/**
 * A multiset of measured row heights that answers the MEDIAN cheaply. Stored as a small
 * height->count histogram keyed by the exact measured px (no rounding -- an estimate that
 * feeds the offset map should not inject sub-pixel error, and `remove` must key on the same
 * value `add` used). `total` makes the median position O(1) and the sorted-key walk that
 * finds it runs lazily, only on the first `value()` after a mutation. That matters because
 * `value()` is read for every unmeasured row on every offset-map rebuild, while mutations
 * (a measurement commit / eviction) are comparatively rare -- so the hot path is a cached
 * O(1) read. add/remove/replace are paired so the count can't drift: a forgotten decrement
 * on eviction would corrupt the median for every future estimate.
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
    /** Account for a re-measured row's changed height (same row, count unchanged). */
    replace(prev: number, next: number) {
      remove(prev)
      add(next)
    },
    /** The (lower) median measured height, or undefined when nothing is recorded. */
    value(): number | undefined {
      if (total === 0)
        return undefined
      if (cachedMedian !== undefined)
        return cachedMedian
      // Lower median: the element at 0-indexed floor((n-1)/2), so an even count leans to
      // the SHORTER side -- deliberate, since over-estimating a short row is what drifts
      // the pin. Walk the sorted buckets, accumulating counts, to the bucket that contains
      // that rank.
      const rank = Math.floor((total - 1) / 2)
      const keys = [...counts.keys()].sort((a, b) => a - b)
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
    replace(kind: string, prev: number, next: number) {
      kindBucket(kind).replace(prev, next)
      global.replace(prev, next)
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
 * the cumulative offset map, and the visible-row slice. It deliberately does
 * NOT own scroll position — useChatScroll reads `offsetOfId`/`totalHeight` to
 * keep the viewport anchored, and feeds scroll positions in via `updateViewport`.
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
  let deferMeasurements = false
  const deferredPremeasures = new Map<string, DeferredPremeasure>()
  // Diagnostic-only: the most recent geometry-changing height commit, so the scroll-anchor
  // drift WARN can attribute a drift to the row that caused it (see MeasurementCommitInfo).
  // Non-reactive -- read at WARN time, never a render input.
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

  const pruneStaleKeyedHeights = (list: readonly VirtualItem[]): void => {
    for (const item of list) {
      const cached = heightCache.get(item.id)
      if (cached !== undefined && cached.key !== item.heightKey) {
        heightCache.delete(item.id)
        removeFallbackContribution(cached)
      }
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
    return cached !== undefined && cached.key === item.heightKey ? cached.height : undefined
  }
  const resolvedHeight = (item: VirtualItem): number =>
    cachedMeasuredHeight(item) ?? estimateFor(item)

  // Bumped whenever a measurement changes the geometry, so the `geom` memo and
  // every reactive getter recompute. Plain caches stay non-reactive for speed.
  const [geomVersion, setGeomVersion] = createSignal(0)

  // The gap between row i and i+1 is tightened (small) whenever the LOWER row
  // (i+1) has span lines. SpanLines gives the lower row column-specific
  // ownership of the top bridge across this gap, so ROW_GAP (--space-2) must
  // match gapSmallPx.
  const gapAfter = (list: VirtualItem[], i: number): number => {
    if (i >= list.length - 1)
      return 0
    return list[i + 1].hasSpanLines
      ? resolve(opts.gapSmallPx, DEFAULT_GAP_SMALL_PX)
      : resolve(opts.gapLargePx, DEFAULT_GAP_LARGE_PX)
  }

  // Cumulative offsets + lookup maps, recomputed when items or measurements change.
  // Keyed by row id (unique) rather than seq (0n for every optimistic local), so
  // stacked locals each get their own offset instead of collapsing onto one.
  const geom = createMemo(() => {
    const rebuildStart = monotonicNow()
    geomVersion()
    const list = opts.items()
    pruneStaleKeyedHeights(list)
    const n = list.length
    const offsets = new Float64Array(n + 1)
    const indexById = new Map<string, number>()
    for (let i = 0; i < n; i++) {
      indexById.set(list[i].id, i)
      const h = resolvedHeight(list[i])
      offsets[i + 1] = offsets[i] + h + gapAfter(list, i)
    }
    // Rebuilding the offset map is an O(n) array fill (microseconds for any real n), so this
    // should never trip -- but instrumenting it rules the offset map IN or OUT as a stall
    // source instead of leaving it a suspect. Only a slow rebuild logs.
    const rebuildMs = monotonicNow() - rebuildStart
    if (rebuildMs >= SCROLL_PHASE_STALL_WARN_MS)
      virtLog.warn('slow scroll phase', { phase: 'geomRebuild', ms: Math.round(rebuildMs), rows: n })
    return { offsets, indexById, list, n }
  })

  const totalHeight: Accessor<number> = () => {
    const g = geom()
    return g.offsets[g.n]
  }

  const offsetOfIndex = (index: number): number => {
    const g = geom()
    if (index <= 0)
      return 0
    if (index >= g.n)
      return g.offsets[g.n]
    return g.offsets[index]
  }

  const indexOfId = (id: string): number => geom().indexById.get(id) ?? -1

  const offsetOfId = (id: string): number | undefined => {
    const i = indexOfId(id)
    return i >= 0 ? offsetOfIndex(i) : undefined
  }

  const heightOfIndex = (index: number): number => {
    const g = geom()
    if (index < 0 || index >= g.n)
      return estimateHeight()
    return resolvedHeight(g.list[index])
  }

  const currentHeightKey = (id: string): string | undefined => {
    const g = geom()
    const index = g.indexById.get(id)
    return index === undefined ? undefined : g.list[index].heightKey
  }

  // The row's current estimate bucket -- used when recording its measurement so the
  // contribution lands in (and later leaves) the right per-kind median.
  const currentKind = (id: string): string => {
    const g = geom()
    const index = g.indexById.get(id)
    return index === undefined ? DEFAULT_ESTIMATE_KIND : kindOf(g.list[index])
  }

  const hasMeasuredHeight = (id: string): boolean => {
    const g = geom()
    const index = g.indexById.get(id)
    if (index === undefined)
      return false
    return cachedMeasuredHeight(g.list[index]) !== undefined
  }

  // Debug-only: surface the measured height without exposing the generic fallback
  // as though it were a row-specific estimate.
  const heightDebugOfId = (id: string): { measured?: number } => {
    const rawMeasured = heightCache.get(id)
    const i = indexOfId(id)
    if (i < 0)
      return { measured: rawMeasured?.height }
    const item = geom().list[i]
    const measured = rawMeasured !== undefined && rawMeasured.key === item.heightKey
      ? rawMeasured.height
      : undefined
    return { measured }
  }

  // Largest i in [0, n-1] with offsets[i] <= y (the row containing offset y).
  const indexAtOffset = (y: number): number => {
    const g = geom()
    if (g.n === 0)
      return 0
    return largestIndexWhere(g.n, mid => g.offsets[mid] <= y)
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

  const anchorAt = (scrollTop: number): ScrollAnchor | null =>
    anchorAtOffset(anchorGeometry(), scrollTop)
  const scrollTopForAnchor = (anchor: ScrollAnchor): number | null =>
    resolveAnchorScrollTop(anchorGeometry(), anchor)
  const scrollTopNearAnchor = (anchor: ScrollAnchor): number | null =>
    resolveNearestAnchorScrollTop(anchorGeometry(), anchor)

  const computeRangeInternal = (
    scrollTop: number,
    clientHeight: number,
    lead: ViewportLead | undefined,
    includeDiagnostics: boolean,
  ): ComputedRange => {
    const g = geom()
    const n = g.n
    if (n === 0)
      return { range: { start: 0, end: 0 } }
    const over = resolve(opts.overscanPx, DEFAULT_OVERSCAN_PX)
    // Render-ahead: extend the overscan by `lead.px` on the side the gesture is
    // heading toward, so a fast fling paints the rows it is ABOUT to reach. The
    // compositor scrolls a frame before this range update lands; without the lead a
    // jump larger than the symmetric overscan would flash an unrendered gap until
    // the next frame caught up. A non-positive lead leaves the symmetric overscan.
    const leadPx = lead && lead.px > 0 ? lead.px : 0
    const overTop = over + (lead?.dir === 'older' ? leadPx : 0)
    const overBottom = over + (lead?.dir === 'newer' ? leadPx : 0)
    // Clamp a stale-high scrollTop to the scrollable range before slicing. After a
    // trim/shrink the spacer (totalHeight = offsets[n]) drops in the same flush
    // that the DOM still reports the old, larger scrollTop; without this clamp
    // `top` would exceed every offset and the `start` search below would fall back
    // to its last index, collapsing the slice to the last row alone for one frame,
    // until the browser clamps scrollTop and the re-pin corrects. (Negative
    // scrollTop from rubber-band overscroll already floors to row 0.)
    const total = g.offsets[n]
    const maxScrollTop = Math.max(0, total - clientHeight)
    const clampedTop = clamp(scrollTop, 0, maxScrollTop)
    const top = clampedTop - overTop
    const bottom = clampedTop + clientHeight + overBottom
    // First row extending past `top` (smallest i with offsets[i+1] > top), and one
    // past the last row starting before `bottom` (largest i with offsets[i] < bottom).
    let start = smallestIndexWhere(n, mid => g.offsets[mid + 1] > top, n - 1)
    let end = largestIndexWhere(n, mid => g.offsets[mid] < bottom) + 1
    if (start < 0)
      start = 0
    if (start > n - 1)
      start = n - 1
    if (end < start + 1)
      end = start + 1
    if (end > n)
      end = n
    const rawStart = start
    const rawEnd = end
    // A row taller than the pixel overscan can legitimately contain the whole
    // viewport+overscan band, collapsing the slice to that one row. Keep one
    // neighbor on each side in that case so a slow scroll across either edge has
    // overlapping DOM instead of replacing the entire mounted set at the boundary.
    // Normal rows keep the pure pixel window; this adds at most two rows, only for
    // the oversized-row case.
    const guardBandPx = Math.max(overTop, overBottom)
    let expandedForTallRow = false
    if (guardBandPx > 0 && end === start + 1 && resolvedHeight(g.list[start]) > guardBandPx) {
      start = Math.max(0, start - 1)
      end = Math.min(n, end + 1)
      expandedForTallRow = true
    }
    const range = { start, end }
    if (!includeDiagnostics || guardBandPx <= 0)
      return { range }

    let tallRowIndex = -1
    let tallRowHeight = guardBandPx
    let reason: TallRowRangeDiagnostics['reason'] = 'tall-row-in-range'
    if (rawEnd === rawStart + 1) {
      const singleHeight = resolvedHeight(g.list[rawStart])
      if (singleHeight > guardBandPx) {
        tallRowIndex = rawStart
        tallRowHeight = singleHeight
        reason = 'single-row-window'
      }
    }
    if (tallRowIndex < 0) {
      for (let i = start; i < end; i++) {
        const h = resolvedHeight(g.list[i])
        if (h > tallRowHeight) {
          tallRowHeight = h
          tallRowIndex = i
        }
      }
    }
    if (tallRowIndex < 0)
      return { range }

    const tallRow = g.list[tallRowIndex]
    const tallRowTop = g.offsets[tallRowIndex]
    const tallRowBottom = tallRowTop + tallRowHeight
    return {
      range,
      tallRow: {
        reason,
        rowCount: n,
        totalHeight: total,
        maxScrollTop,
        clampedScrollTop: clampedTop,
        scrollTopWasClamped: clampedTop !== scrollTop,
        overscanPx: over,
        overTop,
        overBottom,
        guardBandPx,
        overscanTop: top,
        overscanBottom: bottom,
        rawStart,
        rawEnd,
        expandedForTallRow,
        tallRowIndex,
        tallRowId: tallRow.id,
        tallRowHeight,
        tallRowHeightSource: cachedMeasuredHeight(tallRow) === undefined ? 'estimated' : 'measured',
        tallRowTop,
        tallRowBottom,
        viewportTopOffsetInTallRow: clampedTop - tallRowTop,
        viewportBottomOffsetInTallRow: clampedTop + clientHeight - tallRowTop,
      },
    }
  }

  const computeRange = (
    scrollTop: number,
    clientHeight: number,
    lead?: ViewportLead,
  ): ChatVirtualizerRange => computeRangeInternal(scrollTop, clientHeight, lead, false).range

  const [range, setRange] = createSignal<ChatVirtualizerRange>({ start: 0, end: 0 })

  const updateViewport = (
    scrollTop: number,
    clientHeight: number,
    lead?: ViewportLead,
  ) => {
    const reportPerf = !!opts.onViewportUpdate && (opts.shouldReportPerf?.() ?? true)
    const startedAt = reportPerf ? monotonicNow() : 0
    const computed = computeRangeInternal(scrollTop, clientHeight, lead, reportPerf)
    const next = computed.range
    const computeMs = reportPerf ? monotonicNow() - startedAt : 0
    const cur = range()
    const rangeChanged = cur.start !== next.start || cur.end !== next.end
    if (rangeChanged)
      setRange(next)
    if (reportPerf) {
      const previousRows = Math.max(0, cur.end - cur.start)
      const nextRows = Math.max(0, next.end - next.start)
      const overlapRows = Math.max(0, Math.min(cur.end, next.end) - Math.max(cur.start, next.start))
      const stats: ViewportUpdateStats = {
        scrollTop,
        clientHeight,
        leadDir: lead?.dir,
        leadPx: lead?.px ?? 0,
        previousStart: cur.start,
        previousEnd: cur.end,
        nextStart: next.start,
        nextEnd: next.end,
        previousRows,
        nextRows,
        addedRows: rangeChanged ? nextRows - overlapRows : 0,
        removedRows: rangeChanged ? previousRows - overlapRows : 0,
        rangeChanged,
        computeMs,
        totalMs: monotonicNow() - startedAt,
      }
      if (computed.tallRow)
        stats.tallRow = computed.tallRow
      opts.onViewportUpdate?.(stats)
    }
  }

  const commitMeasuredHeight = (
    id: string,
    height: number,
    heightKey: string | undefined,
    optsForCommit: { notifyFirstMeasure: boolean, source: TallRowMeasureStats['source'] },
  ): boolean => {
    // Ignore non-positive (or non-finite) heights: a row not yet laid out -- or one
    // under a display:none ancestor (an inactive TILE/workspace; an inactive tab
    // PANE is visibility:hidden and still measures its real height) -- reports 0,
    // which would poison the height cache and drag the median estimate toward
    // zero. The finite-positive guard rejects NaN (`NaN > 0` is false, so a stray
    // NaN would otherwise flow into the median histogram and turn every fallback
    // estimate -- and thus the whole offset map -- into NaN) AND Infinity (which
    // a bare `height > 0` lets through, then poisons the estimate the same
    // way). Single chokepoint for both the immediate attachRow read and the
    // ResizeObserver flush.
    if (!isUsableHeight(height))
      return false
    const currentKey = currentHeightKey(id)
    if (heightKey !== currentKey)
      return false
    const stalePrev = heightCache.get(id)
    if (stalePrev !== undefined && stalePrev.key !== heightKey) {
      heightCache.delete(id)
      removeFallbackContribution(stalePrev)
    }
    const prevEntry = heightCache.get(id)
    const prev = prevEntry?.height
    const kind = currentKind(id)
    // The height the offset map reserved for this row BEFORE this commit -- the per-kind
    // estimate for a first measure (read now, before this row's contribution is folded in)
    // or the previously cached height for a re-measure. Its diff from the new height is
    // exactly what shifts the offset map (and, if above the anchor, drifts the pin).
    const assumedHeight = prevEntry !== undefined ? prevEntry.height : heightEstimate.value(kind, seed)
    const nextContribution = fallbackEstimateContribution(height)
    const shouldReportTallMeasure = !!opts.onTallRowMeasure
      && (opts.shouldReportPerf?.() ?? true)
      && (nextContribution === undefined || (prevEntry !== undefined && prevEntry.fallbackContribution === undefined))
    const fallbackEstimateBefore = shouldReportTallMeasure ? estimateHeight() : 0
    const geometryVersionBefore = shouldReportTallMeasure ? geomVersion() : 0
    const indexBefore = shouldReportTallMeasure ? indexOfId(id) : -1
    const rowTopBefore = shouldReportTallMeasure ? (offsetOfId(id) ?? 0) : 0
    const totalHeightBefore = shouldReportTallMeasure ? totalHeight() : 0
    // A re-measure within epsilon of the prior measured height is noise: the
    // offset map doesn't change. Still refresh the row's LRU recency (delete +
    // re-set moves it to the most-recently-used end) so a row that stays mounted
    // and re-measures at a stable height can't age to the eviction front and get
    // evicted while still visible.
    if (prev !== undefined && Math.abs(prev - height) < MEASURE_EPSILON_PX) {
      const prevContributes = prevEntry?.fallbackContribution !== undefined
      const nextContributes = nextContribution !== undefined
      if (prevContributes === nextContributes && prevEntry !== undefined) {
        heightCache.delete(id)
        heightCache.set(id, prevEntry)
        return false
      }
    }
    // A row's FIRST measurement is the first DOM height for that row; re-measures
    // (async highlight growth) are geometry updates but not first-visible callbacks.
    const isFirst = prev === undefined
    // Re-insert so this row becomes the most-recently-used entry (Map preserves
    // insertion order; a plain set on an existing key would keep its old, stale
    // position and risk evicting a freshly-measured row).
    heightCache.delete(id)
    heightCache.set(id, cachedMeasuredHeightEntry(heightKey, height, nextContribution, kind))
    if (prevEntry === undefined) {
      if (nextContribution !== undefined)
        heightEstimate.add(kind, nextContribution)
    }
    else {
      // prevEntry survived the stale-key delete above, so its heightKey == this heightKey
      // and thus its kind == this kind (a kind flip bumps heightKey) -- the same bucket.
      const prevContribution = prevEntry.fallbackContribution
      if (prevContribution === undefined && nextContribution !== undefined)
        heightEstimate.add(kind, nextContribution)
      else if (prevContribution !== undefined && nextContribution === undefined)
        heightEstimate.remove(prevEntry.kind, prevContribution)
      else if (prevContribution !== undefined && nextContribution !== undefined)
        heightEstimate.replace(kind, prevContribution, nextContribution)
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
    // Record the commit that is about to move the offset map, so a re-pin drift the
    // resulting totalHeight change triggers can name its cause (see MeasurementCommitInfo).
    measurementCommitSeq += 1
    lastMeasurement = {
      id,
      kind,
      source: optsForCommit.source,
      firstMeasure: isFirst,
      assumedHeight,
      newHeight: height,
      delta: height - assumedHeight,
      commitSeq: measurementCommitSeq,
    }
    setGeomVersion(v => v + 1)
    if (shouldReportTallMeasure) {
      const indexAfter = indexOfId(id)
      opts.onTallRowMeasure?.({
        id,
        source: optsForCommit.source,
        height,
        previousHeight: prev,
        deltaHeight: prev === undefined ? undefined : height - prev,
        firstMeasure: isFirst,
        fallbackExcluded: nextContribution === undefined,
        previousFallbackExcluded: prevEntry !== undefined && prevEntry.fallbackContribution === undefined,
        fallbackEstimateBefore,
        fallbackEstimateAfter: estimateHeight(),
        geometryVersionBefore,
        geometryVersionAfter: geomVersion(),
        indexBefore,
        indexAfter,
        rowTopBefore,
        rowTopAfter: offsetOfId(id) ?? 0,
        totalHeightBefore,
        totalHeightAfter: totalHeight(),
      })
    }
    // Outside the batch: a read-only callback for consumers that need a first
    // visible-measure notification. It must not write reactive state here.
    if (isFirst && optsForCommit.notifyFirstMeasure)
      opts.onFirstMeasure?.(id, height)
    return true
  }
  const measure = (id: string, height: number): boolean =>
    commitMeasuredHeight(id, height, currentHeightKey(id), { notifyFirstMeasure: true, source: 'visible' })
  const commitPremeasureHeight = (id: string, height: number, heightKey?: string): boolean => {
    // Once a visible row has committed a measurement, the live DOM is the
    // authoritative geometry. A hidden premeasure queued before mount can finish
    // later; don't let that hidden-layout read replace the visible height.
    if (mountedIds.has(id) && hasMeasuredHeight(id))
      return false
    return commitMeasuredHeight(id, height, heightKey, { notifyFirstMeasure: false, source: 'premeasure' })
  }
  const primeHeight = (id: string, height: number, heightKey?: string): boolean => {
    if (mountedIds.has(id) && hasMeasuredHeight(id))
      return false
    if (deferMeasurements) {
      if (!isUsableHeight(height) || heightKey !== currentHeightKey(id))
        return false
      deferredPremeasures.set(id, { id, height, heightKey })
      return false
    }
    return commitPremeasureHeight(id, height, heightKey)
  }
  const hasPendingPremeasuredHeight = (id: string): boolean => {
    const pending = deferredPremeasures.get(id)
    return pending !== undefined && pending.heightKey === currentHeightKey(id)
  }

  // The ResizeObserver / batched-microtask-flush DOM glue lives in createRowMeasurer
  // (its only coupling here is `measure` + the shared mountedIds set), keeping this
  // file the offset engine. The default scheduler/observer are the production ones; a
  // unit test injects fakes to drive the flush timing deterministically.
  const shouldReportPerf = (): boolean => opts.shouldReportPerf?.() ?? true
  const measurer = createRowMeasurer({
    measure,
    mountedIds,
    currentMeasurementKey: currentHeightKey,
    shouldDeferMeasurement: () => deferMeasurements,
    shouldReportAttachMeasure: shouldReportPerf,
    onAttachMeasure: opts.onRowAttachMeasure,
  })
  const { attachRow, detachRow } = measurer
  onCleanup(measurer.dispose)

  const hasDeferredMeasurements = (): boolean =>
    measurer.hasDeferredMeasurements() || deferredPremeasures.size > 0

  const flushDeferredMeasurements = (): boolean => {
    if (!hasDeferredMeasurements())
      return false
    let committed = false
    batch(() => {
      committed = measurer.flushDeferredMeasurements()
      for (const premeasure of deferredPremeasures.values()) {
        if (commitPremeasureHeight(premeasure.id, premeasure.height, premeasure.heightKey))
          committed = true
      }
      deferredPremeasures.clear()
    })
    return committed
  }

  return {
    range,
    geometryVersion: geomVersion,
    totalHeight,
    offsetOfIndex,
    offsetOfId,
    indexOfId,
    indexAtOffset,
    anchorAt,
    scrollTopForAnchor,
    scrollTopNearAnchor,
    heightOfIndex,
    heightDebugOfId,
    estimateHeight,
    lastMeasurement: () => lastMeasurement,
    computeRange,
    updateViewport,
    measure,
    primeHeight,
    hasMeasuredHeight,
    hasPendingPremeasuredHeight,
    setVisibleMeasurementDeferral: (defer: boolean) => {
      deferMeasurements = defer
    },
    hasDeferredMeasurements,
    flushDeferredMeasurements,
    attachRow,
    detachRow,
    mountedIds,
  }
}
