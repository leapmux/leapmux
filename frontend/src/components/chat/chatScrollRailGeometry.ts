import type { VirtualItem } from './useChatVirtualizer'
import { largestIndexWhere } from '~/lib/binarySearch'
import { clamp } from '~/lib/clamp'
import { EDGE_INTENT_TOLERANCE_PX } from './chatScrollGeometry'

// ---------------------------------------------------------------------------
// Scroll-rail geometry
//
// Pure math mapping the chat scroll state into SEQ SPACE -- the position within the
// WHOLE conversation [minSeq..maxSeq], not the loaded window. This is why the rail's
// thumb is accurate where the native scrollbar (which only knows the ~150-message
// loaded window) is not. The rail DECISIONS built on this math (which scrollbar owns the
// viewport, whether a thumb is worth drawing, how marks cluster into jump dots) live in
// chatRailPolicy, so this file stays pure seq<->pixel coordinate math.
//
// Three coordinate spaces:
//   1. content-Y: pixels within the virtual spacer, [0, totalHeight].
//   2. seq: bigint message seqs. Each row occupies one seq unit; seq gaps (deletes,
//      hidden rows excluded from the virtual list) absorb linearly into the pixel
//      span between adjacent visible rows.
//   3. fraction: [0,1] over [minSeq..maxSeq], what the rail renders against.
//
// bigint -> number conversions cross an explicit exact-integer boundary. Per-agent seqs are
// expected to stay far below 2^53, but the backend stores int64s; when a future/imported DB
// violates that assumption, rail geometry fails closed instead of silently rounding jumps.
// ---------------------------------------------------------------------------

// EDGE_INTENT_TOLERANCE_PX (the 1px "treated as fully at the edge" slack the rail's edge snaps
// use) is shared with the rest of the scroll pipeline -- imported from chatScrollGeometry so
// the rail can't drift from the edge slack every other scroll module already agrees on.
const MAX_SAFE_SEQ_BIGINT = BigInt(Number.MAX_SAFE_INTEGER)

/** Convert an absolute seq to a number only when JS can represent it exactly. */
export function safeSeqNumber(seq: bigint): number | null {
  if (seq < 0n || seq > MAX_SAFE_SEQ_BIGINT)
    return null
  return Number(seq)
}

/** Convert a seq delta/span to a number only when JS can represent it exactly. */
export function safeSeqDeltaNumber(delta: bigint): number | null {
  if (delta < 0n || delta > MAX_SAFE_SEQ_BIGINT)
    return null
  return Number(delta)
}

export interface SeqSpaceGeometry {
  /** Visible rows, ascending by seq; trailing optimistic locals carry seq 0n. */
  items: readonly VirtualItem[]
  /** Content-Y of the top of row `i` (i in [0, items.length]; == totalHeight at the end). */
  offsetOfIndex: (index: number) => number
  /** Total virtual content height in px. */
  totalHeight: number
}

export interface RailRange {
  minSeq: bigint
  maxSeq: bigint
}

export interface SeqThumb {
  /** Top of the thumb as a fraction of the rail [0,1]. */
  topFraction: number
  /**
   * Viewport span as a fraction of the whole seq range. The rendered thumb height is fixed;
   * this span is still needed to project the thumb top over the scrollable travel so a bottom
   * viewport lands with the fixed thumb flush to the rail bottom.
   */
  visibleFraction: number
}

export interface SeqThumbInputs {
  /** Logical (clamped) scrollTop of the scroll container. */
  scrollTop: number
  clientHeight: number
  minSeq: bigint
  maxSeq: bigint
  hasMoreOlder: boolean
  hasMoreNewer: boolean
  /** distFromBottom(el): scrollHeight - scrollTop - clientHeight, for the bottom edge snap. */
  distFromBottomPx: number
}

/**
 * The absolute seq (as a number) at the START of each row's coverage, plus a terminal
 * boundary. `out[i]` is where row i begins in seq space; `out[n]` = `out[n-1] + 1` so
 * the last row occupies one seq unit. Trailing optimistic locals (seq 0n) are assigned
 * one unit above the running server max, so they map ABOVE the last server row (where
 * they render) rather than scanning as the oldest. Returns null when the window holds no
 * server row (all-locals / empty), since no seq anchor exists to place a thumb against.
 *
 * Derived purely from `items`, and the internal primitive of {@link prepareGeometry} --
 * callers hold a {@link PreparedGeometry} (which memoizes this once) rather than calling
 * seqAtContentY / contentYForSeq / computeSeqThumb with a raw geometry, so the (n+1)-length
 * array is scanned a single time per geometry, not re-derived on every scroll frame.
 */
export function rowStartSeqs(items: readonly VirtualItem[]): number[] | null {
  const n = items.length
  if (n === 0)
    return null
  let hasServer = false
  let running = 0
  const out = Array.from<number>({ length: n + 1 })
  for (let i = 0; i < n; i++) {
    const s = items[i].seq
    if (s !== undefined && s !== 0n) {
      const safe = safeSeqNumber(s)
      if (safe === null)
        return null
      hasServer = true
      running = safe
      out[i] = running
    }
    else {
      // Optimistic local: one unit above the running max (pinned at the tail).
      if (running >= Number.MAX_SAFE_INTEGER)
        return null
      running += 1
      out[i] = running
    }
  }
  if (!hasServer)
    return null
  if (out[n - 1] >= Number.MAX_SAFE_INTEGER)
    return null
  out[n] = out[n - 1] + 1
  return out
}

/**
 * A {@link SeqSpaceGeometry} paired with its `rowStartSeqs` computed ONCE, handed to
 * seqAtContentY / contentYForSeq / computeSeqThumb so the (n+1)-length row map is scanned a
 * single time per geometry rather than re-derived on every call (the rail recomputes the
 * thumb every scroll frame). `rowSeqs` is null when the window holds no server row
 * (all-locals / empty), so a single guard on it covers the "no seq anchor" case for every
 * consumer.
 *
 * The rail component builds this INLINE from two separate memos (an `items`-keyed rowSeqs
 * memo plus a per-commit `geo` wrapper) so a streaming turn -- which bumps totalHeight every
 * commit while keeping the item reference -- reruns the O(n) row-seq scan only when the item
 * list actually changes. {@link prepareGeometry} is the one-shot constructor for callers
 * without that streaming-frequency concern (tests, one-off computations).
 */
export interface PreparedGeometry {
  geo: SeqSpaceGeometry
  rowSeqs: number[] | null
}

/**
 * Pair a geometry with its precomputed row-start-seqs map. The one-shot convenience form of
 * the "compute once" seam; the rail builds {@link PreparedGeometry} inline instead (see that
 * type's note) to keep the row-seq scan off the streaming-commit path.
 */
export function prepareGeometry(geo: SeqSpaceGeometry): PreparedGeometry {
  return { geo, rowSeqs: rowStartSeqs(geo.items) }
}

/**
 * The inclusive seq-range metrics shared by every range->fraction consumer: `min` is the range
 * floor as a number and `span` is the count of seqs the band covers (maxSeq - minSeq + 1). Returns
 * null when either crosses the exact-integer boundary (an unsafe seq), the range is inverted, or the
 * span is non-positive -- so all consumers fail CLOSED on a degenerate/overflowing range through one
 * shared guard rather than re-deriving the `+ 1n` band width and its null/zero check at each site.
 */
export function seqSpan(range: RailRange): { min: number, span: number } | null {
  const min = safeSeqNumber(range.minSeq)
  const span = safeSeqDeltaNumber(range.maxSeq - range.minSeq + 1n)
  if (min === null || span === null || span <= 0)
    return null
  return { min, span }
}

/**
 * The fractional seq (absolute, as a number) under content-Y `y`. Interpolates within
 * the row+gap span so the result is continuous across the whole content height. Returns
 * null when the window has no server row.
 */
export function seqAtContentY(prep: PreparedGeometry, y: number): number | null {
  const { geo, rowSeqs } = prep
  if (!rowSeqs)
    return null
  const n = geo.items.length
  const cy = clamp(y, 0, geo.totalHeight)
  const offAt = (i: number) => (i >= n ? geo.totalHeight : geo.offsetOfIndex(i))
  const i = largestIndexWhere(n, k => offAt(k) <= cy)
  const top = offAt(i)
  const bot = offAt(i + 1)
  const span = bot - top
  const t = span > 0 ? clamp((cy - top) / span, 0, 1) : 0
  return rowSeqs[i] + t * (rowSeqs[i + 1] - rowSeqs[i])
}

/**
 * Inverse of seqAtContentY: the content-Y that places fractional seq `seqF` at the
 * viewport top. Used for in-window thumb-drag live-scrolling. Returns null when the
 * window has no server row, or `seqF` falls outside the loaded window's seq span.
 */
export function contentYForSeq(prep: PreparedGeometry, seqF: number): number | null {
  const { geo, rowSeqs } = prep
  if (!rowSeqs)
    return null
  const n = geo.items.length
  if (seqF < rowSeqs[0] || seqF > rowSeqs[n])
    return null
  const i = largestIndexWhere(n, k => rowSeqs[k] <= seqF)
  // `rowSpan`, not `seqSpan`: the module exports a `seqSpan()` function, and a local of
  // that name would shadow it -- a future `seqSpan(range)` call added here would resolve
  // to this number and throw "seqSpan is not a function".
  const rowSpan = rowSeqs[i + 1] - rowSeqs[i]
  const t = rowSpan > 0 ? clamp((seqF - rowSeqs[i]) / rowSpan, 0, 1) : 0
  const top = geo.offsetOfIndex(i)
  const bot = i + 1 >= n ? geo.totalHeight : geo.offsetOfIndex(i + 1)
  return top + t * (bot - top)
}

/**
 * The thumb rect in seq-space fractions, or null when the rail can't/shouldn't render
 * (empty conversation, or the window has no server row). Handles the case the native
 * scrollbar gets wrong -- clientHeight >= totalHeight while remote history exists -- by
 * computing the loaded window's SHARE of [minSeq..maxSeq], positioned by seq.
 */
export function computeSeqThumb(prep: PreparedGeometry, inp: SeqThumbInputs): SeqThumb | null {
  if (inp.maxSeq <= 0n)
    return null
  // Guard an inverted range (maxSeq < minSeq): a stale minSeq stranded above a
  // delete-lowered maxSeq would make `span` (below) <= 0 and every fraction NaN, which
  // renders the thumb/track at `NaNpx`. Hide the thumb instead until the range heals.
  if (inp.maxSeq < inp.minSeq)
    return null
  const topSeqF = seqAtContentY(prep, inp.scrollTop)
  if (topSeqF === null)
    return null
  const botSeqF = seqAtContentY(prep, inp.scrollTop + inp.clientHeight)
  if (botSeqF === null)
    return null
  const metrics = seqSpan({ minSeq: inp.minSeq, maxSeq: inp.maxSeq })
  if (metrics === null)
    return null
  const { min, span } = metrics
  let topFraction = clamp((topSeqF - min) / span, 0, 1)
  let bottomFraction = clamp((botSeqF - min) / span, topFraction, 1)
  // Edge snaps: kill sub-pixel / container-padding bias exactly where users notice it.
  if (!inp.hasMoreOlder && inp.scrollTop <= EDGE_INTENT_TOLERANCE_PX)
    topFraction = 0
  if (!inp.hasMoreNewer && inp.distFromBottomPx <= EDGE_INTENT_TOLERANCE_PX)
    bottomFraction = 1
  return { topFraction, visibleFraction: Math.max(bottomFraction - topFraction, 0) }
}

/**
 * Rail-Y of the thumb's CENTRE for position fraction `p` in [0,1]. The thumb centre can only
 * travel [thumbHalf, railHeight - thumbHalf] (its top/bottom EDGES hit the rail ends at the
 * extremes), so dots, the track, and track-clicks all map onto THIS inset axis -- then a dot
 * always lines up with the thumb centre, never falling in the dead zones past the centre's
 * reach. Degenerates to the rail midpoint when the thumb fills the rail (no travel).
 */
export function centerAxisY(p: number, railHeightPx: number, thumbHeightPx: number): number {
  return thumbHeightPx / 2 + clamp(p, 0, 1) * Math.max(railHeightPx - thumbHeightPx, 0)
}

/** Inverse of {@link centerAxisY}: the position fraction [0,1] for a rail-Y on the centre axis. */
export function centerAxisFraction(y: number, railHeightPx: number, thumbHeightPx: number): number {
  const travel = railHeightPx - thumbHeightPx
  if (travel <= 0)
    return 0
  return clamp((y - thumbHeightPx / 2) / travel, 0, 1)
}

/**
 * The absolute (fractional) seq NUMBER at rail fraction `f`, over [minSeq, maxSeq], or null when
 * the range is inverted (maxSeq < minSeq) or crosses the exact-integer boundary (an unsafe seq).
 * The shared fail-closed core of the fraction->seq mapping: {@link fractionToSeq} rounds it to a
 * bigint, and the thumb-drag maps its pointer fraction to an in-window content-Y through it -- so
 * the safe bigint->number conversion and the `min + f*span` travel math live in ONE tested place
 * rather than hand-rolled at both sites. A NaN `f` (a 0/0 from a degenerate rail/rect height at a
 * mid-collapse layout frame) is treated as 0 so it can never reach a BigInt(NaN) downstream.
 */
export function seqNumberAtFraction(f: number, minSeq: bigint, maxSeq: bigint): number | null {
  if (maxSeq < minSeq)
    return null
  const min = safeSeqNumber(minSeq)
  const span = safeSeqDeltaNumber(maxSeq - minSeq)
  if (min === null || span === null)
    return null
  const frac = Number.isFinite(f) ? clamp(f, 0, 1) : 0
  return min + frac * span
}

/** The nearest integer seq at rail fraction `f`, clamped to [minSeq, maxSeq]. */
export function fractionToSeq(f: number, minSeq: bigint, maxSeq: bigint): bigint | null {
  const seqF = seqNumberAtFraction(f, minSeq, maxSeq)
  if (seqF === null)
    return null
  // seqF - min == frac*span exactly (same float multiply), and minSeq is safe (seqNumberAtFraction
  // succeeded), so Number(minSeq) is exact -- this rounds the same delta the inline math did.
  return minSeq + BigInt(Math.round(seqF - Number(minSeq)))
}

/**
 * The seq at rail-relative pixel `y` (track click / drag), mapped through the thumb-centre
 * axis so `y` picks the seq whose thumb centre would sit there -- consistent with where dots
 * are drawn ({@link centerAxisY}).
 */
export function railYToSeq(y: number, railHeightPx: number, thumbHeightPx: number, range: RailRange): bigint | null {
  return fractionToSeq(centerAxisFraction(y, railHeightPx, thumbHeightPx), range.minSeq, range.maxSeq)
}

export interface ThumbPx {
  topPx: number
  heightPx: number
}

/**
 * The thumb's pixel height: fixed at `fixedThumbPx`, capped at the rail height so a collapsed
 * rail cannot render the thumb outside its own bounds. The single home for the fixed-size rule
 * shared by `projectThumbPx` (the resting thumb) and the rail's drag-preview branch.
 */
export function fixedThumbHeightPx(railHeightPx: number, fixedThumbPx: number): number {
  return clamp(fixedThumbPx, 0, railHeightPx)
}

/**
 * Project a seq-space thumb onto rail pixels with a fixed thumb height. The top is projected
 * through the classic scrollbar progress formula so a fixed-height thumb still spans the full
 * rail travel (top at fraction 0, bottom at fraction 1) rather than overrunning the rail.
 */
export function projectThumbPx(thumb: SeqThumb, railHeightPx: number, fixedThumbPx: number): ThumbPx {
  const heightPx = fixedThumbHeightPx(railHeightPx, fixedThumbPx)
  const travel = railHeightPx - heightPx
  const denom = 1 - thumb.visibleFraction
  const progress = denom > 0 ? clamp(thumb.topFraction / denom, 0, 1) : 0
  return { topPx: travel * progress, heightPx }
}

/**
 * The drag-PREVIEW thumb rect: the resting thumb height `thumbHeightPx`, positioned so its
 * CENTRE sits on the same {@link centerAxisY} axis the dots + track are drawn on, at drag
 * position `fraction`. `topPx = centerAxisY(fraction) - thumbHeightPx/2` reduces to
 * `travel * fraction`, but routing through {@link centerAxisY} keeps the "drag thumb lines
 * up with the dots" invariant structural -- one home for the centre-axis math -- and carries
 * its `clamp(fraction)` and non-negative-travel guard rather than re-deriving them inline.
 */
export function dragThumbPx(fraction: number, railHeightPx: number, thumbHeightPx: number): ThumbPx {
  return { topPx: centerAxisY(fraction, railHeightPx, thumbHeightPx) - thumbHeightPx / 2, heightPx: thumbHeightPx }
}
