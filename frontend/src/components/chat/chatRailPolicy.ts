import type { RailRange } from './chatScrollRailGeometry'
import type { MarkType } from '~/generated/leapmux/v1/agent_pb'
import type { SeqMark } from '~/stores/chatMessageMarks'
import { lowerBoundBySeq, smallestIndexWhere } from '~/lib/binarySearch'
import { clamp } from '~/lib/clamp'
import { SCROLLBAR_OVERFLOW_TOLERANCE_PX } from './chatScrollGeometry'
import { centerAxisY, safeSeqDeltaNumber, seqSpan } from './chatScrollRailGeometry'

// ---------------------------------------------------------------------------
// Scroll-rail policy
//
// The rail DECISIONS layered on top of the pure coordinate math in chatScrollRailGeometry:
// which scrollbar owns the viewport, whether a seq-space thumb is worth drawing, and how the
// marked seqs cluster into jump dots (plus the scrub hit-test). Kept apart from the geometry
// module so that file stays pure seq<->pixel math; everything here reads that math and adds a
// rail-behaviour judgement on top. The dependency is one-way (policy -> geometry, never back).
// ---------------------------------------------------------------------------

/**
 * Whether the rail can render a seq-space thumb, over a PRECOMPUTED rowStartSeqs. The caller
 * memoizes rowStartSeqs (the scroll-owner resolution in ChatView) and reuses it here instead of
 * re-deriving the O(n) row-seq map on every evaluation -- so the owner decision stays O(1) on a
 * scroll frame or a streaming-height commit, which leave the item list (and thus rowStartSeqs)
 * unchanged. `rowSeqs === null` (no server anchor) is the only structural gate beyond the range
 * guards; single-seq (maxSeq === minSeq) still reports drawable here -- the "can't scrub a point"
 * rule is resolveScrollbarOwner's, not the geometry's (see its `maxSeq > minSeq` term).
 */
export function canRenderSeqRailThumb(rowSeqs: number[] | null, range: RailRange): boolean {
  if (range.maxSeq <= 0n)
    return false
  if (rowSeqs === null)
    return false
  // seqSpan folds in the inverted-range (maxSeq < minSeq) and unsafe-seq guards.
  return seqSpan(range) !== null
}

/**
 * Which scrollbar owns the viewport: the native one, the seq-space rail, or neither (content
 * fits). The SINGLE decision behind both the rail's self-hide (ChatScrollRail.hidden) and the
 * native-scrollbar hide (ChatView.hideNativeScrollbar). To make the "never zero or two
 * scrollbars" invariant STRUCTURAL rather than a coincidence, ChatView resolves this ONCE and
 * hands the rail its result (see ChatView.railOwner): a shared pure function is not enough if the
 * two consumers feed it inputs from two reactive sources -- notably viewport height, where the
 * native-hide once read the content-box height and the rail read its own padding-box
 * clientHeight, so a whole-history conversation overflowing by up to the container's vertical
 * padding could hide BOTH bars. Rules:
 *   - not loaded -> native (the rail isn't ready to take over).
 *   - whole history loaded AND content fits -> none (nothing to scroll; both hide).
 *   - the rail can scroll every pixel -> rail; otherwise -> native.
 * "The rail can scroll every pixel" requires a drawable thumb AND a whole-history seq span of
 * MORE than one seq: a single distinct seq (maxSeq === minSeq) collapses the thumb-drag to a
 * point, so a lone message taller than the viewport must keep the native scrollbar rather than
 * strand behind a frozen rail.
 */
export type ScrollbarOwner = 'native' | 'rail' | 'none'

export interface ScrollbarOwnerInputs {
  /** True once the marks RPC has seeded the rail (ChatRailData.loaded). */
  loaded: boolean
  /** Count of visible virtual rows; 0 = empty window (nothing to scroll, so neither bar shows). */
  itemCount: number
  /**
   * rowStartSeqs of the visible rows (null when the window holds no server row). Passed in
   * PRECOMPUTED -- the caller memoizes it on the item list -- so this O(1) resolution never
   * triggers the O(n) row-seq scan on a scroll frame or a streaming-height commit, both of which
   * leave the item list (and thus rowStartSeqs) unchanged.
   */
  rowSeqs: number[] | null
  /** The whole-history seq range the rail spans. */
  range: RailRange
  /** Whether older/newer messages remain unfetched beyond the loaded window. */
  hasMoreOlder: boolean
  hasMoreNewer: boolean
  /** Total virtual content height (px). */
  totalHeight: number
  /**
   * The viewport CONTENT-box height (px) -- the same coordinate space totalHeight is measured in,
   * NOT the padding-box `clientHeight`. Content overflows iff totalHeight > content-box (the
   * scroll container's vertical padding cancels out of `scrollHeight > clientHeight`), so feeding
   * clientHeight here would over-report the fit by that padding and hide the rail over overflowing
   * content. ChatView passes its ResizeObserver's contentRect.height (see viewportHeight).
   */
  viewportHeight: number
}

export function resolveScrollbarOwner(inp: ScrollbarOwnerInputs): ScrollbarOwner {
  if (!inp.loaded)
    return 'native'
  // No rows loaded: nothing to scroll, so neither scrollbar is needed. Structural (independent of a
  // possibly-unmeasured height) so an empty window never flickers a native bar over no content.
  if (inp.itemCount === 0)
    return 'none'
  const contentFits = inp.totalHeight <= inp.viewportHeight + SCROLLBAR_OVERFLOW_TOLERANCE_PX
  const railCanScroll = inp.range.maxSeq > inp.range.minSeq && canRenderSeqRailThumb(inp.rowSeqs, inp.range)
  if (railCanScroll) {
    // The rail scrubs the whole seq-space, so it owns scrolling whenever there is any -- it
    // self-hides only when the whole history is loaded AND fits (nothing to scroll anywhere).
    const wholeHistoryLoaded = !inp.hasMoreOlder && !inp.hasMoreNewer
    return wholeHistoryLoaded && contentFits ? 'none' : 'rail'
  }
  // The rail can't scrub (no server row, or a single-seq history): the native bar owns scrolling,
  // needed only when content actually overflows; otherwise nothing scrolls.
  return contentFits ? 'none' : 'native'
}

/**
 * The rail fraction [0,1] at which to draw the dot for message `seq` (centered on its band), or
 * null when the range is degenerate / overflows an exact int. Fails CLOSED like the geometry
 * siblings (safeSeqNumber, computeSeqThumb, fractionToSeq) -- honoring the "fail closed instead of
 * silently rounding jumps" contract -- so an unsafe range DROPS the dot in clusterMarks rather than
 * pinning it to the rail top (fraction 0, a valid in-band position that would mis-place the jump).
 */
export function dotFraction(seq: bigint, range: RailRange): number | null {
  const metrics = seqSpan(range)
  if (metrics === null)
    return null
  return dotFractionForSpan(seq, range.minSeq, metrics.span)
}

// The span-carrying core of dotFraction, so clusterMarks can hoist the range-invariant
// seqSpan(range) out of its per-mark loop instead of recomputing it (a bigint subtraction +
// object alloc) once per mark.
function dotFractionForSpan(seq: bigint, minSeq: bigint, span: number): number | null {
  const offset = seq >= minSeq ? safeSeqDeltaNumber(seq - minSeq) : null
  if (offset === null)
    return null
  return clamp((offset + 0.5) / span, 0, 1)
}

/** One rail dot: a pixel that stands for `count` marks (>1 = a cluster). */
export interface DotCluster {
  /** The member nearest the pixel centre -- the jump + preview target. */
  seq: bigint
  /** Rail-Y (px) of the dot, on the thumb-centre axis. */
  topPx: number
  /** The representative member's mark type (drives the label/colour). */
  type: MarkType
  /** How many marks collapsed to this pixel. */
  count: number
}

/**
 * Cluster the in-range marks into one dot per rounded rail pixel. Many marks in a tall
 * history collapse to the same pixel; rather than DROP the collisions (which would make
 * ~all-but-one message per pixel unreachable on a long history), each pixel becomes ONE
 * dot that stands for its `count` marks, jumps to the member nearest the pixel centre, and
 * previews with an aggregate header. This caps the DOM node count at ~rail height while
 * keeping every marked message reachable.
 *
 * Dots are placed on the thumb-CENTRE axis (via {@link centerAxisY}) so a dot lines up with
 * the thumb centre, not the rail edge. Returns [] for a zero-height rail (nothing to place).
 * Marks must be ascending by seq (the store keeps them so); since {@link dotFraction} is
 * monotonic, buckets are inserted in ascending pixel order and Map iteration preserves it,
 * so the result needs no sort.
 */
export function clusterMarks(
  marks: readonly SeqMark[],
  range: RailRange,
  railHeightPx: number,
  thumbHeightPx: number,
): DotCluster[] {
  if (railHeightPx <= 0)
    return []
  // seqSpan(range) is invariant across the loop, so resolve it once here rather than per mark
  // (dotFraction would recompute it M times). A degenerate/overflowing range yields null,
  // which would drop EVERY mark, so bail to an empty cluster set immediately.
  const metrics = seqSpan(range)
  if (metrics === null)
    return []
  // Bucket in-range marks by rounded rail pixel; the representative is the member whose
  // exact (pre-round) position is nearest the pixel centre -- the "jump-to-nearest" target.
  const byPx = new Map<number, { rep: SeqMark, repDist: number, count: number }>()
  const start = lowerBoundBySeq(marks, range.minSeq)
  for (let i = start; i < marks.length; i++) {
    const mark = marks[i]
    if (mark.seq > range.maxSeq)
      break
    const frac = dotFractionForSpan(mark.seq, range.minSeq, metrics.span)
    if (frac === null)
      continue
    const exact = centerAxisY(frac, railHeightPx, thumbHeightPx)
    const px = Math.round(exact)
    const dist = Math.abs(exact - px)
    const cur = byPx.get(px)
    if (!cur) {
      byPx.set(px, { rep: mark, repDist: dist, count: 1 })
    }
    else {
      cur.count += 1
      if (dist < cur.repDist) {
        cur.rep = mark
        cur.repDist = dist
      }
    }
  }
  const out: DotCluster[] = []
  for (const [px, c] of byPx)
    out.push({ seq: c.rep.seq, topPx: px, type: c.rep.type, count: c.count })
  return out
}

/**
 * Whether two dot clusters are identical across EVERY field -- the single "same dot" rule shared by
 * the `dots` memo's array `equals` ({@link dotClustersEqual}) and the rail's hover-cleanup membership
 * check, so adding a {@link DotCluster} field can't leave one comparison stale while the other updates.
 */
export function dotClusterEqual(a: DotCluster, b: DotCluster): boolean {
  return a.seq === b.seq && a.topPx === b.topPx && a.type === b.type && a.count === b.count
}

/**
 * Content equality for two {@link clusterMarks} results, for a memo's `equals`: an unchanged
 * layout keeps the SAME array reference so `<For>` doesn't tear down + rebuild every dot's
 * tooltip. maxSeq ticks up on every persisted row during a streaming turn, but on a long
 * conversation a +1 seq shift rounds to the same clusters, so recomputing would otherwise
 * hand `<For>` a fresh array each frame for no visual change.
 */
export function dotClustersEqual(a: readonly DotCluster[], b: readonly DotCluster[]): boolean {
  return a.length === b.length && a.every((d, i) => dotClusterEqual(d, b[i]))
}

/**
 * The dot nearest rail-Y `y` within `rangePx`, or null when none is that close -- the
 * scrub-hover hit-test the rail draws its single preview popover from as the dragging
 * thumb passes each dot. `dots` are ascending by {@link DotCluster.topPx} (clusterMarks
 * emits them in pixel order), so a lower-bound search finds the first dot at/after `y` and
 * the nearer of it and its predecessor wins. On a tie the dot at/after `y` wins (it is
 * checked second under the inclusive `<=`). Pure + testable here rather than inline in the
 * component, alongside clusterMarks / resolveScrollbarOwner.
 */
export function nearestDotWithin(dots: readonly DotCluster[], y: number, rangePx: number): DotCluster | null {
  const idx = smallestIndexWhere(dots.length, i => dots[i].topPx >= y, dots.length)
  let best: DotCluster | null = null
  let bestDist = rangePx
  // Check the predecessor first, then the dot at/after y, WITHOUT allocating a candidate array
  // each call (this runs per rAF frame during a scrub). The at/after dot is checked second so a
  // tie resolves to it, under the inclusive `<=`.
  const consider = (d: DotCluster | undefined) => {
    if (!d)
      return
    const dist = Math.abs(d.topPx - y)
    if (dist <= bestDist) {
      best = d
      bestDist = dist
    }
  }
  consider(dots[idx - 1])
  consider(dots[idx])
  return best
}
