import type { Accessor } from 'solid-js'
import type { HeightInput } from './chatHeightEstimator'
import type { AnchorOffsetGeometry } from './chatScrollAnchor'
import type { ScrollAnchor } from '~/stores/chatTypes'
import { batch, createMemo, createSignal, onCleanup } from 'solid-js'
import { clamp } from '~/lib/clamp'
import { capMapInsertionOrder } from '~/lib/mapLru'
import { anchorAtOffset, resolveAnchorScrollTop, resolveNearestAnchorScrollTop } from './chatScrollAnchor'
import { createRowMeasurer } from './createRowMeasurer'

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
   * LAZY opaque per-row descriptor for the injected height estimator
   * (`opts.estimate`). The virtualizer never inspects it — it only forwards it to
   * the estimate callback for an unmeasured row. A thunk (not an eager value) so
   * the per-row build (a content parse + diff-geometry extraction) runs ONLY for a
   * row that actually needs an estimate (never-measured / estimate-cache-miss),
   * not for the measured-and-cached majority of a large window on every recompute.
   * ChatView builds it (kind + content metrics + interactive state) so a
   * never-measured row is sized analytically from how it renders, not from a
   * per-kind average. Absent → the global-mean fallback.
   */
  features?: () => HeightInput
  /**
   * Per-row content version for the analytical-estimate cache (memoizing path
   * only). The cache is keyed by `id`, invalidated wholesale on an epoch change
   * (width / global prefs) and per-id on measure — but NOT when a row's OWN
   * content changes in place under a stable id (a reseq / notification
   * consolidation of an off-screen, still-unmeasured row). Without a content
   * token the cache would hand back that row's pre-change estimate, drifting the
   * offset map (and the scroll anchor) until the row scrolls into view and
   * measures. ChatView builds this via `buildEstimateKey` as a composite of the
   * fields that change a row's estimated height under a stable id (seq,
   * tool-use-sibling presence + the opener's content version, the row's UI-toggle
   * version, and its own content version) — so any of those changes busts the
   * row's cached estimate while a measurement-only recompute keeps it. Absent →
   * id-only caching (the prior behavior).
   */
  estimateKey?: string
}

/**
 * Classifies EVERY VirtualItem field as geometry-affecting (true -> compared by
 * sameVirtualItems / feeds the offset map, gap, and estimate cache) or not. The
 * `Record<keyof Required<VirtualItem>, boolean>` annotation forces a COMPILE error when a
 * field is added to VirtualItem without classifying it here, closing the "forgot to update
 * the equality" hazard. `seq` (anchor label) and `features` (lazy estimate thunk) are NOT
 * geometry -- neither feeds the offset map.
 */
const GEOMETRY_RELEVANCE: Record<keyof Required<VirtualItem>, boolean> = {
  id: true,
  hasSpanLines: true,
  estimateKey: true,
  seq: false,
  features: false,
}

/** The geometry-affecting VirtualItem keys, derived once (module load) from the above. */
const GEOMETRY_KEYS = (Object.keys(GEOMETRY_RELEVANCE) as (keyof VirtualItem)[])
  .filter(k => GEOMETRY_RELEVANCE[k])

/**
 * Whether two virtual-item arrays are GEOMETRY-EQUIVALENT: same length and the same
 * geometry fields (GEOMETRY_KEYS = id, hasSpanLines, estimateKey) at every index. The
 * virtualizer keys its offset map, inter-row gap, and estimate cache on exactly those
 * fields, so a recompute that preserves them produces an identical offset map. Used as the
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
   * Estimate the height (px) of an UNMEASURED row. Called from the geom scan
   * for cache-miss rows only. Reads the live content width reactively (ChatView
   * closes over a width signal), so geom recomputes when the width changes.
   * Omitted → the global running-mean fallback is used instead.
   */
  estimate?: (item: VirtualItem) => number
  /**
   * Reactive "estimate epoch" -- a value that changes whenever `estimate`'s
   * output could change for the SAME item (in practice the bucketed content
   * width). When provided alongside `estimate`, the geom scan memoizes each row's
   * analytical estimate keyed by id and invalidates the whole cache when this
   * changes, so a measurement-only geom recompute (which bumps every frame during
   * a window fill) doesn't re-run the estimator for every still-unmeasured row.
   * geom subscribes to it so a width change still recomputes the offset map.
   * Omitted → estimates are recomputed each scan (the prior, un-memoized path).
   */
  estimateEpoch?: () => unknown
  /**
   * Debug-only: the full analytical estimate BREAKDOWN for a row (kind / total /
   * terms / metrics), surfaced verbatim by `heightDebugOfId` for the raw-JSON debug
   * surface. Returns the consumer's breakdown object (opaque here) or null. Not used
   * for the offset map -- only `estimate` drives geometry.
   */
  estimateBreakdown?: (item: VirtualItem) => unknown
  /**
   * Fired once, on a row's FIRST measurement, with its real height. Lets the
   * consumer compare its analytical estimate against the actual (the
   * estimate-vs-actual divergence WARN). Not fired on re-measures.
   */
  onFirstMeasure?: (id: string, measuredHeight: number) => void
}

const DEFAULT_OVERSCAN_PX = 800
/**
 * Seed/fallback row height (px) for a row that has never been measured and has no
 * usable analytical estimate. Exported so ChatView's no-features fallback shares
 * the one seed instead of duplicating the literal.
 */
export const DEFAULT_ESTIMATE_PX = 96
const DEFAULT_GAP_SMALL_PX = 8 // --space-2
const DEFAULT_GAP_LARGE_PX = 20 // --space-5
/** Ignore sub-pixel measurement jitter below this threshold. */
const MEASURE_EPSILON_PX = 0.5
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
/**
 * Backstop bound on the memoized per-row estimate map (only used when estimateEpoch
 * is wired). The cache is normally bounded by WINDOW LIVENESS -- geom's `retain`
 * drops entries whose row left the loaded window, so the map tracks the item set
 * and an in-window row is never evicted. This cap only guards a runaway the retain
 * path should never let happen, so it sits well above the window ceiling
 * (MAX_LOADED_CHAT_MESSAGES_CEILING = 8 * 150 = 1200); a test pins that margin so a
 * ceiling raised past the cap can't turn the backstop into a hot eviction path.
 */
export const ESTIMATE_CACHE_MAX = 2400

function resolve(v: Accessor<number> | number | undefined, fallback: number): number {
  if (v === undefined)
    return fallback
  return typeof v === 'function' ? v() : v
}

/**
 * Whether `n` is a usable row height: finite and strictly positive. `n > 0`
 * already rejects NaN (`NaN > 0` is false); Number.isFinite additionally rejects
 * Infinity. A non-usable estimate would poison the cumulative offset map (every
 * offset past that row becomes NaN/Infinity, blanking the list), so estimateOf
 * falls back to the running mean whenever this is false.
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
   * Debug accessor: a row's raw analytical estimate (NOT the running-mean
   * fallback), its measured height when the cache holds one, and the full estimate
   * BREAKDOWN (kind / total / terms / metrics, when an `estimateBreakdown` is
   * wired), side by side (heightOfIndex collapses estimate/measured into one
   * resolved value). Fields are undefined when unavailable -- unknown id, no
   * estimator, or not yet measured. Feeds the raw-JSON debug surface; it does not
   * drive the offset map.
   */
  heightDebugOfId: (id: string) => { estimated?: number, measured?: number, breakdown?: unknown }
  /** Current global running-mean estimate (the fallback when no estimator is injected). */
  estimateHeight: Accessor<number>
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
 * Per-row analytical-estimate cache, keyed by row id. Only populated when
 * estimateEpoch is wired (memoizingEstimates): the cache is invalidated wholesale
 * whenever the epoch changes (see geom), so it only ever holds current-epoch
 * values and needs no per-entry epoch tag. Holds either a usable analytical
 * estimate (`est: number`) or a poison marker (`est: null`) for a row whose
 * estimator returned an unusable value (NaN/Infinity/threw) -- never the
 * running-mean fallback itself, which shifts per measurement and must not be
 * frozen. The poison marker is load-bearing: without it a row with a malformed
 * payload re-parses on EVERY geom pass (its key never changes, it never measures
 * while off-screen), defeating the memo precisely for the row that costs the most.
 * A poison hit returns the live running mean, not a frozen value.
 *
 * BOUNDED BY WINDOW LIVENESS, not by recency. The cache holds at most one entry
 * per row currently in the loaded window: `retain` (called from geom when the
 * items array changes) drops every entry whose id has left the window, so the
 * cache tracks the window exactly and an entry can never go stale-by-eviction
 * while its row is still on screen. Because the loaded window is bounded well
 * below ESTIMATE_CACHE_MAX, the cap is only a defensive backstop (a runaway it
 * should never hit); the delete+set in `put` keeps that backstop an LRU drop
 * rather than a FIFO one. Also cleared wholesale on an epoch change (stale width)
 * and dropped per-id on measure (a measured row uses its height, not an estimate).
 *
 * Each entry stores the row's `estimateKey` (its content version) so a row whose
 * CONTENT changed in place under a stable id -- a reseq / consolidation of an
 * off-screen, still-unmeasured row -- recomputes instead of returning the
 * pre-change estimate. The epoch (width + global prefs) can't catch that: the
 * change is per-row, not global. A measurement-only recompute leaves the key
 * untouched, so it still reuses the cache (the memoization win is preserved).
 *
 * Encapsulated so the freshness rule (`getFresh` returns a hit only when the
 * content version is unchanged) lives next to its bounding (`retain` prunes dead
 * rows, `put` backstop-caps), in one place a reader can verify.
 */
function createEstimateCache() {
  const map = new Map<string, { key: string | undefined, est: number | null }>()
  return {
    /**
     * The cache entry for `id` iff its content version (`key`) is unchanged:
     * a usable estimate (number), a poison marker (null), or undefined on a miss.
     * The tri-state lets the caller distinguish "known unusable, use the running
     * mean" (null) from "never computed, run the estimator" (undefined).
     */
    getFresh(id: string, key: string | undefined): number | null | undefined {
      const cached = map.get(id)
      return cached !== undefined && cached.key === key ? cached.est : undefined
    },
    /** Cache a usable estimate (or a null poison marker); the cap is a backstop. */
    put(id: string, key: string | undefined, est: number | null) {
      // delete + set so a re-put (a row re-estimated after its content version
      // changed) moves the entry to the most-recent end -- without the delete a
      // Map keeps an existing key at its original position, making the backstop cap
      // a FIFO drop rather than an LRU one. Mirrors the height cache's measure().
      map.delete(id)
      map.set(id, { key, est })
      capMapInsertionOrder(map, ESTIMATE_CACHE_MAX)
    },
    /** Drop a single row's estimate (it just measured, so it no longer estimates). */
    drop(id: string) {
      map.delete(id)
    },
    /**
     * Drop every entry whose row has left the loaded window (`isLive(id)` false),
     * so the cache tracks the current item set. Called from geom on an items
     * change; bounds the cache to the window without leaning on the cap.
     */
    retain(isLive: (id: string) => boolean) {
      for (const id of map.keys()) {
        if (!isLive(id))
          map.delete(id)
      }
    },
    /** Invalidate every estimate (the epoch changed, so all are stale-width). */
    clear() {
      map.clear()
    },
  }
}

/**
 * Running mean of measured row heights -- the fallback estimate used when no
 * analytical estimator is injected, and for out-of-range indices. Bundles the
 * sum/count pair behind add/replace/remove so the two can't drift: a forgotten
 * count decrement on eviction would poison every fallback estimate (and thus the
 * whole offset map). `value(seed)` returns the mean, or the seed when no row has
 * been measured yet.
 */
function createRunningMean() {
  let sum = 0
  let count = 0
  return {
    /** Record a newly measured row's height. */
    add(height: number) {
      sum += height
      count += 1
    },
    /** Account for a re-measured row's changed height (count unchanged -- same row). */
    replace(prev: number, height: number) {
      sum += height - prev
    },
    /** Drop an evicted row's measured height from the mean. */
    remove(height: number) {
      sum -= height
      count -= 1
    },
    /** The mean of measured heights, or `seed` when nothing has been measured. */
    value(seed: number): number {
      return count > 0 ? sum / count : seed
    },
  }
}

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
  const heightCache = new Map<string, number>()
  // Ids of the rows currently MOUNTED in the DOM (a keyed <For> attaches one el
  // per id and detaches it on unmount). This is the precise rendered set the
  // height-cache eviction must protect -- maintained synchronously by attach/
  // detach rather than derived from `range()`, which commits on a deferred rAF and
  // could momentarily omit a row that just scrolled in, letting it be evicted the
  // same frame it measured.
  const mountedIds = new Set<string>()
  // Global running mean of measured heights — the fallback estimate used when no
  // analytical estimator is injected (`opts.estimate`), and for out-of-range
  // indices. With the estimator wired, unmeasured rows are estimated per item.
  const measuredMean = createRunningMean()
  const [estimateHeight, setEstimateHeight] = createSignal(seed)

  // Per-row analytical-estimate cache (see createEstimateCache for the freshness
  // and eviction rules).
  const estimateCache = createEstimateCache()
  const memoizingEstimates = !!opts.estimate && !!opts.estimateEpoch
  // Memoization needs BOTH an estimator and its epoch. Wiring `estimate` without
  // `estimateEpoch` silently disables it -- every unmeasured row re-runs the
  // (per-row parse + diff geometry) estimator each geom pass. The only consumer
  // passes both; make the misconfiguration loud in dev rather than a silent
  // O(window)-per-frame regression.
  if (import.meta.env.DEV && opts.estimate && !opts.estimateEpoch)
    console.warn('useChatVirtualizer: `estimate` was provided without `estimateEpoch`; per-row estimate memoization is disabled (every unmeasured row re-estimates each geom pass)')

  // Estimated height for an unmeasured row: the injected per-item analytical
  // estimate when available, else the global running mean. The injected
  // estimate reads the live content width reactively, so geom recomputes on a
  // width change for the unmeasured (off-screen) rows that still rely on it.
  //
  // Run the injected estimator defensively, returning a USABLE height or null. The
  // estimator parses a per-row wire payload (a provider's heightMetrics hook) and CAN
  // throw on a malformed one -- and it runs inside geom's createMemo, which has no error
  // boundary, so an uncaught throw would propagate out and blank the ENTIRE virtualized
  // list, not just mis-size one row. A NaN/Infinity RETURN is just as poisonous: it would
  // corrupt the cumulative offset map (every offset past that row becomes NaN/Infinity,
  // blanking the list and stranding scroll, since every comparison against NaN is false).
  // Fold BOTH failure modes into one discriminated result -- null -- so each caller
  // branches once instead of re-deriving the isUsableHeight check (and risking forgetting
  // it). `est > 0` already rejects NaN (`NaN > 0` is false); Number.isFinite additionally
  // rejects Infinity. The DOM measure() path keeps its own isUsableHeight guard for real
  // layout reads (an independent 0/Infinity source).
  const runEstimate = (item: VirtualItem): number | null => {
    try {
      const est = opts.estimate!(item)
      return isUsableHeight(est) ? est : null
    }
    catch {
      return null
    }
  }
  const estimateOf = (item: VirtualItem): number => {
    if (!opts.estimate)
      return estimateHeight()
    if (!memoizingEstimates)
      return runEstimate(item) ?? estimateHeight()
    // Memoized path: reuse the cached estimate (geom already cleared the cache if
    // the epoch changed) UNLESS the row's content version changed under the same
    // id. A cache hit skips the estimator -- the geom-level epoch read keeps the
    // offset map recomputing on a width change.
    const hit = estimateCache.getFresh(item.id, item.estimateKey)
    if (hit !== undefined)
      // A usable cached estimate returns as-is; a poison marker (null) returns the
      // live running mean -- the row is known-unusable, but the fallback must stay
      // current, so we read it fresh rather than freezing it into the cache.
      return hit ?? estimateHeight()
    const est = runEstimate(item)
    if (est === null) {
      // Unusable (or threw): fall back to the shifting mean, but record a poison
      // marker so the malformed payload isn't re-parsed on every geom pass until
      // its key or the epoch changes (or it measures, which drops the marker).
      estimateCache.put(item.id, item.estimateKey, null)
      return estimateHeight()
    }
    estimateCache.put(item.id, item.estimateKey, est)
    return est
  }

  // A row's resolved height: its measured height when the height cache holds one,
  // else the analytical/running-mean estimate. The single home for the "measured
  // wins, else estimate" rule the offset map (geom) and heightOfIndex both apply,
  // so a future change to the fallback can't drift between them.
  const resolvedHeight = (item: VirtualItem): number => heightCache.get(item.id) ?? estimateOf(item)

  // Bumped whenever a measurement changes the geometry, so the `geom` memo and
  // every reactive getter recompute. Plain caches stay non-reactive for speed.
  const [geomVersion, setGeomVersion] = createSignal(0)

  // The gap between row i and i+1 is tightened (small) whenever the LOWER row
  // (i+1) has span lines, so the vertical rails bridge across the gap. The
  // SpanLines pseudo-elements over-extend by exactly this gap (ROW_GAP =
  // --space-2 in SpanLines.css.ts), so the gap token must match gapSmallPx.
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
  let cachedEstimateEpoch: unknown
  let lastItemsRef: VirtualItem[] | undefined
  const geom = createMemo(() => {
    geomVersion()
    // Subscribe to the estimate epoch (the content width) so a width change
    // recomputes the offset map even when every row is an estimate-cache hit, and
    // invalidate the cache wholesale when it changes so stale-width estimates
    // can't survive (cache hits below then can't return a wrong-width value).
    if (memoizingEstimates) {
      const epoch = opts.estimateEpoch!()
      if (epoch !== cachedEstimateEpoch) {
        cachedEstimateEpoch = epoch
        estimateCache.clear()
      }
    }
    const list = opts.items()
    const n = list.length
    const offsets = new Float64Array(n + 1)
    const indexById = new Map<string, number>()
    for (let i = 0; i < n; i++) {
      indexById.set(list[i].id, i)
      const h = resolvedHeight(list[i])
      offsets[i + 1] = offsets[i] + h + gapAfter(list, i)
    }
    // When the item set itself changes (a window trim / merge / page load, not a
    // measurement-only recompute), drop estimate-cache entries for rows that left
    // the window so the cache tracks the loaded set instead of drifting toward the
    // backstop cap. Gated on the array identity so a measurement-only geom pass
    // (same items) skips the scan; gated on memoizingEstimates since the cache is
    // otherwise unused.
    if (memoizingEstimates && list !== lastItemsRef) {
      lastItemsRef = list
      estimateCache.retain(id => indexById.has(id))
    }
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

  // Debug-only: the raw analytical estimate and the measured height side by side
  // (heightOfIndex collapses them into one resolved value). runEstimate is
  // side-effect-free -- it never writes estimateCache -- so reading this for a
  // copy-to-clipboard never perturbs the offset map. `estimated` is undefined when
  // there is no estimator or it returned an unusable value; `measured` is undefined
  // until the row has been measured.
  const heightDebugOfId = (id: string): { estimated?: number, measured?: number, breakdown?: unknown } => {
    const measured = heightCache.get(id)
    const i = indexOfId(id)
    if (i < 0)
      return { measured }
    const item = geom().list[i]
    const est = runEstimate(item)
    // A non-null result proves the FIRST item.features() call didn't throw, but
    // estimateBreakdown RE-EVALUATES features() -- if the payload/state changed between
    // the two calls and the second throws, the exception must not escape into the
    // raw-JSON debug/copy surface. Guard it like runEstimate guards its own evaluation.
    let breakdown: unknown
    if (est !== null) {
      try {
        breakdown = opts.estimateBreakdown?.(item)
      }
      catch {
        breakdown = undefined
      }
    }
    return { estimated: est ?? undefined, measured, breakdown }
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

  const computeRange = (
    scrollTop: number,
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
    const maxScrollTop = Math.max(0, g.offsets[n] - clientHeight)
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
    return { start, end }
  }

  const [range, setRange] = createSignal<ChatVirtualizerRange>({ start: 0, end: 0 })

  const updateViewport = (
    scrollTop: number,
    clientHeight: number,
    lead?: ViewportLead,
  ) => {
    const next = computeRange(scrollTop, clientHeight, lead)
    const cur = range()
    if (cur.start !== next.start || cur.end !== next.end)
      setRange(next)
  }

  const measure = (id: string, height: number): boolean => {
    // Ignore non-positive (or non-finite) heights: a row not yet laid out -- or one
    // under a display:none ancestor (an inactive TILE/workspace; an inactive tab
    // PANE is visibility:hidden and still measures its real height) -- reports 0,
    // which would poison the height cache and drag the global-mean fallback toward
    // zero. isUsableHeight is the SAME finite-positive guard estimateOf applies to
    // analytical estimates: it rejects NaN (`NaN > 0` is false, so a stray NaN would
    // otherwise flow into the running mean and turn every fallback estimate -- and thus
    // the whole offset map -- into NaN) AND Infinity (which a bare `height > 0` lets
    // through, then poisons the running mean the same way). Single chokepoint for both the
    // immediate attachRow read and the ResizeObserver flush.
    if (!isUsableHeight(height))
      return false
    const prev = heightCache.get(id)
    // A re-measure within epsilon of the prior measured height is noise: the
    // offset map doesn't change. Still refresh the row's LRU recency (delete +
    // re-set moves it to the most-recently-used end) so a row that stays mounted
    // and re-measures at a stable height can't age to the eviction front and get
    // evicted while still visible.
    if (prev !== undefined && Math.abs(prev - height) < MEASURE_EPSILON_PX) {
      heightCache.delete(id)
      heightCache.set(id, prev)
      return false
    }
    // A measured row uses heightCache, never the analytical estimate, so drop any
    // memoized estimate for it (keeps the estimate cache to genuinely-unmeasured
    // rows).
    estimateCache.drop(id)
    // A row's FIRST measurement is the estimate->actual round-trip the consumer
    // logs against; re-measures (async highlight growth) carry no fresh estimate.
    const isFirst = prev === undefined
    // Re-insert so this row becomes the most-recently-used entry (Map preserves
    // insertion order; a plain set on an existing key would keep its old, stale
    // position and risk evicting a freshly-measured row).
    heightCache.delete(id)
    heightCache.set(id, height)
    if (prev === undefined)
      measuredMean.add(height)
    else
      measuredMean.replace(prev, height)
    // Evict the least-recently-measured rows once over the cap, keeping the
    // global-mean fallback consistent (subtract each evicted height first) and
    // never dropping a currently-MOUNTED row (the live mountedIds set, so a row
    // still on screen keeps its measured height instead of being re-estimated).
    if (heightCache.size > HEIGHT_CACHE_MAX) {
      capMapInsertionOrder(heightCache, HEIGHT_CACHE_MAX, {
        protect: mountedIds,
        onEvict: oldest => measuredMean.remove(heightCache.get(oldest)!),
      })
    }
    batch(() => {
      setEstimateHeight(measuredMean.value(seed))
      setGeomVersion(v => v + 1)
    })
    // Outside the batch: a read-only/logging callback. The consumer compares its
    // analytical estimate for this row against `height` and may WARN — it must
    // not write reactive state here (it only reads width/state and logs).
    if (isFirst)
      opts.onFirstMeasure?.(id, height)
    return true
  }

  // The ResizeObserver / batched-microtask-flush DOM glue lives in createRowMeasurer
  // (its only coupling here is `measure` + the shared mountedIds set), keeping this
  // file the offset engine. The default scheduler/observer are the production ones; a
  // unit test injects fakes to drive the flush timing deterministically.
  const measurer = createRowMeasurer({ measure, mountedIds })
  const { attachRow, detachRow } = measurer
  onCleanup(measurer.dispose)

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
    computeRange,
    updateViewport,
    measure,
    attachRow,
    detachRow,
    mountedIds,
  }
}
