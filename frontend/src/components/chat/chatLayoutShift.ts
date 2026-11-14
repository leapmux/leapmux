import { largestIndexWhere } from '~/lib/binarySearch'
import { clamp } from '~/lib/clamp'

// ---------------------------------------------------------------------------
// The layout shift of the chat list
//
// The rows of the chat list sit at content coordinates: `offsets[i]` is the distance
// from the top of the first loaded row to the top of row `i`. The scroll element has
// its own coordinates, the `scrollTop` space. The layout shift converts between the
// two: `scroll = content + shift`. The virtualizer moves the rows by the shift, so a
// geometry change can keep the content under the reader in place with no `scrollTop`
// write. A write during a gesture can end the momentum of the browser, and WebKit can
// then deliver a scroll event in the old coordinates.
//
// This module holds the rule that selects the next shift. It is pure: no reactive
// owner and no DOM. The caller measures the one value that needs the DOM, how far an
// element moved inside its row, and hands it in as `intraRowShift`.
// ---------------------------------------------------------------------------

/** The geometry of the loaded rows at one moment, as the virtualizer computed it. */
export interface LayoutSnapshot {
  list: readonly { id: string }[]
  indexById: ReadonlyMap<string, number>
  /** Length `n + 1`: the content top of each row, and the total height at index `n`. */
  offsets: Float64Array
  /** Length `n`: the resolved height of each row, without the gap below it. */
  heights: Float64Array
  /**
   * Length `n`: 1 when the row had a cached height under its current key, a
   * provisional one included, and 0 when its height was an estimate.
   */
  measured: Uint8Array
  n: number
}

export interface LayoutShiftInput {
  /** The snapshot that the current shift applies to, or `undefined` before the first one. */
  previous: LayoutSnapshot | undefined
  next: LayoutSnapshot
  previousShift: number
  /** The `scrollTop` of the scroll element, clamped to its scroll range. */
  scrollTop: number
  /** Keep the bottom of the content in place: the reader follows the live tail. */
  pinBottom: boolean
  /**
   * How far the pivot element moved inside the row `rowId` since the last read (see
   * `createLayoutPivot`). The rule calls it once, and only when the viewport top cuts
   * the body of a measured row.
   */
  intraRowShift: (rowId: string) => number
}

/**
 * The slack (px) within which the viewport top counts as the top edge of its row. A
 * seek or Home aligns a row to the viewport top, and a sub-pixel layout can leave the
 * top a fraction of a pixel inside the row. The virtualizer records a pivot element only
 * below this slack, so the two agree on which case applies.
 */
export const TOP_EDGE_SLACK_PX = 1

/**
 * The shift for the `next` snapshot that keeps one point of the content at its scroll
 * coordinate.
 *
 * - No previous snapshot, or no row in the next one: 0.
 * - `pinBottom`: the bottom of the content keeps its scroll coordinate.
 * - Otherwise the rule finds the row `R` that holds the viewport top, and it holds:
 *   - the top edge of `R` when the viewport top is at that edge;
 *   - the bottom edge of `R` when the viewport top is in the gap below `R`, or `R` had
 *     an estimated height. An estimate that a measure replaces then moves no row below
 *     it, which keeps an upward scroll through unmeasured history stable;
 *   - otherwise the same distance below the top edge of `R`, plus the distance that the
 *     content at the viewport top moved inside `R` (`intraRowShift`), limited to the
 *     new height of `R`. This is correct for a row that grows at its bottom, as a
 *     streamed answer does.
 * - When `R` left the list, the nearest row of both snapshots holds: first the nearest
 *   row below `R`, by its top edge, then the nearest row above `R`, by its bottom edge.
 *   When no row is in both snapshots, the shift is 0.
 *
 * The result keeps the spacer height, `total + shift`, at 0 or more.
 */
export function nextLayoutShift(input: LayoutShiftInput): number {
  const { previous, next, previousShift, scrollTop } = input
  if (previous === undefined || next.n === 0)
    return 0
  const nextTotal = next.offsets[next.n] ?? 0
  const limitSpacer = (shift: number) => Math.max(shift, -nextTotal)
  if (input.pinBottom)
    return limitSpacer(previousShift + (previous.offsets[previous.n] ?? 0) - nextTotal)
  if (previous.n === 0)
    return 0
  // The viewport top in the content coordinates of the previous snapshot. Before the
  // first row (a positive shift above the reachable top), the row 0 holds by its top edge.
  const c = scrollTop - previousShift
  const r = largestIndexWhere(previous.n, i => (previous.offsets[i] ?? 0) <= c)
  const heldContentTop = heldPoint(input, c, r)
  return heldContentTop === undefined
    ? 0
    : limitSpacer(scrollTop - heldContentTop)
}

/** The content coordinate in `next` of the point at `c` in `previous`, or `undefined` when no row is in both. */
function heldPoint(input: LayoutShiftInput, c: number, r: number): number | undefined {
  const previous = input.previous!
  const { next } = input
  const rowId = previous.list[r]?.id
  const nextIndex = rowId === undefined
    ? undefined
    : next.indexById.get(rowId)
  if (rowId !== undefined && nextIndex !== undefined) {
    const top = previous.offsets[r] ?? 0
    const bottom = top + (previous.heights[r] ?? 0)
    const nextTop = next.offsets[nextIndex] ?? 0
    const nextHeight = next.heights[nextIndex] ?? 0
    if (c - top <= TOP_EDGE_SLACK_PX)
      return nextTop + (c - top)
    if (c >= bottom || previous.measured[r] !== 1)
      return nextTop + nextHeight + (c - bottom)
    return nextTop + clamp(c - top + input.intraRowShift(rowId), 0, nextHeight)
  }
  for (let i = r + 1; i < previous.n; i++) {
    const below = surviving(previous, next, i)
    if (below !== undefined)
      return (next.offsets[below] ?? 0) + (c - (previous.offsets[i] ?? 0))
  }
  for (let i = r - 1; i >= 0; i--) {
    const above = surviving(previous, next, i)
    if (above !== undefined) {
      const bottom = (previous.offsets[i] ?? 0) + (previous.heights[i] ?? 0)
      const nextBottom = (next.offsets[above] ?? 0) + (next.heights[above] ?? 0)
      return nextBottom + (c - bottom)
    }
  }
  return undefined
}

/** The index in `next` of the row at `index` in `previous`, or `undefined` when it left the list. */
function surviving(previous: LayoutSnapshot, next: LayoutSnapshot, index: number): number | undefined {
  const id = previous.list[index]?.id
  return id === undefined
    ? undefined
    : next.indexById.get(id)
}
