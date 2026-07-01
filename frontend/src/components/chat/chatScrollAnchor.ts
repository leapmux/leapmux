import type { ScrollAnchor } from '~/stores/chatTypes'
import { clamp } from '~/lib/clamp'

// ---------------------------------------------------------------------------
// Scroll-anchor math
//
// The pure capture/resolve of a viewport's scroll position as a ROW ANCHOR (a row
// id + a fractional position into it), independent of the virtualizer's offset
// engine and ResizeObserver glue. Extracted from useChatVirtualizer so the
// proportional within-row resolve, the in-gap fraction, and the trimmed-row
// nearest-survivor recovery are unit-testable against a fake geometry -- mirroring
// the createRowMeasurer extraction. The virtualizer supplies an AnchorOffsetGeometry
// (its index/offset accessors over the current row list); these functions read no
// reactive state of their own.
// ---------------------------------------------------------------------------

/** The minimum a row must expose for the anchor math: id + (server) seq for ordering. */
export interface AnchorRow {
  id: string
  seq?: bigint
}

/** The offset-engine surface the anchor math reads, over the CURRENT row list. */
export interface AnchorOffsetGeometry {
  /** The rows in display order (for id/seq lookups + the nearest-survivor scan). */
  list: readonly AnchorRow[]
  /** Index of the row whose vertical span contains offset `y` (clamped to [0, n-1]). */
  indexAtOffset: (y: number) => number
  /** Index of the row with `id`, or -1 when it is not in the current list. */
  indexOfId: (id: string) => number
  /** Cumulative offset (top, px) of the row at `index`. */
  offsetOfIndex: (index: number) => number
  /** Measured-or-estimated height (px) of the row at `index` (excludes the gap). */
  heightOfIndex: (index: number) => number
  /** Inter-row gap (px) below the row at `index` (0 for the last row). */
  gapAfter: (index: number) => number
}

/**
 * The scroll anchor for a viewport whose top edge sits at `scrollTop`: the row at that
 * offset, the pixels from the row's top to the viewport top (clamped to the row body),
 * the row's height as the proportional basis, and how far into the gap below the row
 * the top sat (as a [0,1] fraction). Null for an empty list.
 */
export function anchorAtOffset(geo: AnchorOffsetGeometry, scrollTop: number): ScrollAnchor | null {
  const n = geo.list.length
  if (n === 0)
    return null
  let idx = geo.indexAtOffset(scrollTop)
  // Prefer the FIRST row of any run that shares this cumulative offset. Rows whose reserved
  // height + gap is 0 collapse onto a single offset, and indexAtOffset returns the LAST of
  // them (the first row with real height below). Anchoring to that lower row would let the
  // zero-height rows ABOVE it, once they grow, push it down and drag the viewport with it.
  // Walking back to the topmost row sharing this offset pins that one instead, so the growth
  // lands BELOW the anchor and the viewport stays put. This is a defensive invariant of the
  // pure anchor math: the live virtualizer now reserves a strictly-positive estimate for
  // every unmeasured row (no zero-height runs reach here), so in production the loop finds
  // distinct offsets and never steps -- but it keeps the tie-break correct for any caller
  // (and the unit tests' synthetic geometry) that can produce a shared-offset run.
  //
  // Only when scrollTop sits AT the shared offset, though: with the viewport top strictly
  // INSIDE the terminal row's body (scrollTop > the run's offset), walking back to a
  // zero-height run-top would clamp `within` against that row's 0 basisHeight and DISCARD
  // the within-row offset -- the anchor would resolve to the run's offset, yanking the
  // capture->resolve round trip up by the discarded pixels with no geometry change at all.
  // At the exact offset `within` is 0 either way, so the tie-break costs nothing there.
  const anchorOffset = geo.offsetOfIndex(idx)
  if (scrollTop <= anchorOffset) {
    while (idx > 0 && geo.offsetOfIndex(idx - 1) === anchorOffset)
      idx--
  }
  // Floor at 0 for a transient NEGATIVE scrollTop -- some browsers report one
  // during elastic/rubber-band overscroll at the top, which indexAtOffset floors
  // to row 0, yielding a negative `within` that would store a negative offset and
  // re-pin above the top.
  const within = Math.max(0, scrollTop - geo.offsetOfIndex(idx))
  // Clamp `within` to the row's height (measured or estimated) and record that
  // height as the basis. Clamping bounds the stored fraction (within / basisHeight)
  // to [0, 1], so scrollTopForAnchor's proportional resolve can never overshoot the
  // row body.
  const basisHeight = geo.heightOfIndex(idx)
  const offsetWithinRow = Math.min(within, basisHeight)
  // When scrollTop landed PAST the row body, it sat in the inter-row gap below the
  // row. Record how far into the gap as a fraction so the restore can reproduce a
  // viewport parked mid-gap (instead of snapping to the row bottom), re-applied
  // against the CURRENT gap so it stays gap-size-independent. 0 within the body.
  const gap = geo.gapAfter(idx)
  const overflow = within - basisHeight
  const gapFraction = overflow > 0 && gap > 0 ? Math.min(1, overflow / gap) : 0
  return { id: geo.list[idx].id, offsetWithinRow, basisHeight, gapFraction, seq: geo.list[idx].seq }
}

/**
 * Resolve an anchor back to a scrollTop, or null when its row id is no longer in the
 * list. offsetWithinRow is resolved PROPORTIONALLY against the row's CURRENT height so
 * a height change between capture and resolve scales the offset with the row instead of
 * truncating (yanking up) or overshooting it; the in-gap fraction is re-added against
 * the current gap.
 */
export function resolveAnchorScrollTop(geo: AnchorOffsetGeometry, anchor: ScrollAnchor): number | null {
  const idx = geo.indexOfId(anchor.id)
  if (idx < 0)
    return null
  // The fraction is bounded to [0, 1] by anchorAt's clamp, so the scaled offset stays
  // inside the row. An anchor from old persistence carries no basisHeight; fall back to
  // absolute clamping against the current height.
  const rowHeight = geo.heightOfIndex(idx)
  const within = anchor.basisHeight && anchor.basisHeight > 0
    ? anchor.offsetWithinRow * (rowHeight / anchor.basisHeight)
    : anchor.offsetWithinRow
  const top = geo.offsetOfIndex(idx) + clamp(within, 0, rowHeight)
  // Re-add the in-gap fraction (the viewport top sat in the gap below the row at
  // capture), scaled to the CURRENT gap below the row so a gap-size change scales with
  // it. The within-body clamp lands `top` at the row bottom for such an anchor
  // (offsetWithinRow == basisHeight), so adding the gap offset places it back in the
  // gap. 0 / a now-absent gap (the row became the tail) cleanly falls back to the row
  // bottom. Absent on a pre-gapFraction anchor -> the prior pin-to-bottom.
  if (anchor.gapFraction)
    return top + anchor.gapFraction * geo.gapAfter(idx)
  return top
}

/**
 * Resolve an anchor to a scrollTop, recovering when its row was TRIMMED away (id gone)
 * by landing on the NEAREST surviving row by seq. Returns the exact scrollTopForAnchor
 * when the row still resolves; otherwise, if the anchor carries a seq and the list has a
 * server row, the offset (top) of the surviving server row whose seq is closest to the
 * anchor's. Null when the row is gone AND there is no seq / no surviving server row.
 */
export function resolveNearestAnchorScrollTop(geo: AnchorOffsetGeometry, anchor: ScrollAnchor): number | null {
  const exact = resolveAnchorScrollTop(geo, anchor)
  if (exact != null)
    return exact
  // Bail when the anchor has no orderable seq. `== null` covers a pre-seq persisted
  // anchor; `=== 0n` covers an anchor captured on an optimistic LOCAL (ChatView stamps
  // VirtualItem.seq from the message seq, which is 0n until the server echo arrives).
  // A 0n anchor has no ordering against server rows -- the delta scan below would treat
  // every survivor's delta as its own seq and pick the OLDEST (smallest-seq) row, yanking
  // a reader parked on a tail-pinned local to the top of history once the local
  // reconciles (its id changes, so the exact resolve above fails). Falling back to null
  // lets the caller snap to the tail, which is where a local lived anyway.
  if (anchor.seq == null || anchor.seq === 0n)
    return null
  // Scan the surviving SERVER rows (skip trailing optimistic locals, seq 0n, which pin
  // to the tail out of seq order) for the one whose seq is closest to the gone anchor's.
  // A trim/replacement drops a contiguous run, so the nearest survivor is normally the
  // list's oldest (anchor older) or newest (anchor newer) row; the general nearest also
  // handles a mid-list drop. Linear over a bounded list and only on a rare restore.
  const { list } = geo
  let bestIdx = -1
  let bestDelta = 0n
  for (let i = 0; i < list.length; i++) {
    const s = list[i].seq
    if (s == null || s === 0n)
      continue
    const delta = s > anchor.seq ? s - anchor.seq : anchor.seq - s
    if (bestIdx < 0 || delta < bestDelta) {
      bestIdx = i
      bestDelta = delta
    }
  }
  return bestIdx < 0 ? null : geo.offsetOfIndex(bestIdx)
}
