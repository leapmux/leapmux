import { largestIndexWhere } from '~/lib/binarySearch'
import { clamp } from '~/lib/clamp'

// ---------------------------------------------------------------------------
// The layout shift of the chat list
//
// The rows of the chat list sit at content coordinates: `offsets[i]` is the distance
// from the top of the first loaded row to the top of row `i`. The scroll element has
// its own coordinates, the `scrollTop` space. Two terms convert between the two:
// `scroll = content + shift + inset`.
//
// - The inset is the distance from scroll coordinate 0 to the top of the spacer that
//   holds the rows: the top padding of the list and the gap above the spacer, which
//   ChatView measures. A list shorter than the view also grows the flex spacer above the
//   rows, and the inset does not count that height. Such a list cannot scroll, so every
//   placement lands at 0, and its viewport top lies above the first row with or without
//   that height.
// - The virtualizer moves the rows by the layout shift, so a geometry change can keep the
//   content under the reader in place with no `scrollTop` write. A write during a gesture
//   can end the momentum of the browser, and WebKit can then deliver a scroll event in the
//   old coordinates.
//
// The viewport top is the top edge of the visible area of the scroll element. Its content
// coordinate is `scrollTop - shift - inset`.
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
  /** Length `n`: the measured height of each row, without the gap below it. */
  heights: Float64Array
  n: number
}

export interface LayoutShiftInput {
  /** The snapshot that the current shift applies to, or `undefined` before the first one. */
  previous: LayoutSnapshot | undefined
  next: LayoutSnapshot
  previousShift: number
  /**
   * The `scrollTop` of the scroll element, clamped to its scroll range, minus the inset
   * (see the module comment). So `scrollTop - previousShift` is the content coordinate of
   * the viewport top.
   */
  scrollTop: number
  /** The height (px) of the visible area of the scroll element. The view starts at the viewport top. */
  viewHeight: number
  /** Keep the bottom of the content in place: the reader follows the live tail. */
  pinBottom: boolean
  /**
   * How far the pivot element moved inside the row `rowId` since the last read (see
   * `createLayoutPivot`). The rule calls it once, and only when the viewport top cuts
   * the body of a row whose previous height the reader saw.
   */
  intraRowShift: (rowId: string) => number
  /**
   * Whether the reader saw the row `rowId` at the height that `previous` holds for it: a
   * read of the mounted copy of the row gave or confirmed that height before the read that
   * changed it. False when the first read of the copy changed it. The copy then showed the
   * reader no content at that height: a hidden copy or an earlier session measured it, or
   * an earlier copy of the row did, and a placeholder stood in for the row since.
   *
   * The rule calls it only for a row whose height differs in `next`: at most once
   * for the row whose body the viewport top cuts, and at most once for each row that
   * `enteredFromAbove` and whose top edge is in the view.
   */
  sawHeight: (rowId: string) => boolean
  /**
   * Whether the row `rowId` came into the view over the top edge of the view: it lay above
   * the viewport top when it mounted, or at the last placement of the view. The reader then
   * reached the row from below, by a move of the view up the content. False for a row that
   * mounted inside the view or below it, as after a seek, a restore, or a scroll down.
   *
   * The rule calls it only for a row whose top edge is in the view and whose height differs
   * in `next`.
   */
  enteredFromAbove: (rowId: string) => boolean
}

/**
 * The slack (px) within which the viewport top counts as the top edge of its row. A
 * placement can align the top edge of a row with the viewport top, as the restore of an
 * anchor at the top of its row does, and a sub-pixel layout can leave the viewport top a
 * fraction of a pixel inside the row. Home places the view at scroll coordinate 0, which
 * lies the inset above the first row (see the module comment), so it needs no slack.
 * `rowCutAt` applies it for the shift rule and for the row in which the virtualizer
 * records a pivot element, so the two agree on which case applies.
 */
export const TOP_EDGE_SLACK_PX = 1

/**
 * How far below the viewport top a seek places the top edge of the row that it lands on
 * (see chatSeek), so a little of the row above stays in view. The seek converts the row
 * top with the inset (see the module comment), so the distance holds on screen.
 * `rowCutAt` gives the top edge of a row whose top lies at most this far, plus
 * TOP_EDGE_SLACK_PX, below the viewport top. So the rule holds the landed row while the
 * rows around it measure, also when the viewport top lies inside the row above it, as it
 * does between two bands.
 */
export const ROW_ALIGN_OFFSET_PX = 8

/**
 * Where the viewport top falls in the row that holds it, by its content coordinate (see
 * the module comment). The kind is the point of the row that the shift rule holds (see
 * nextLayoutShift):
 *
 * - `top-edge`: at most TOP_EDGE_SLACK_PX below the top edge of the row, above the first
 *   row, or at most ROW_ALIGN_OFFSET_PX + TOP_EDGE_SLACK_PX above the top edge of the
 *   next row (where a seek leaves the viewport top). The last case gives the next row.
 * - `bottom-edge`: in the gap below the row.
 * - `body`: inside the body of the row. Only this kind reads a pivot element. The
 *   shift rule holds the bottom edge for it too when the row changes a height that the
 *   reader did not see (see nextLayoutShift).
 *
 * The shift rule holds no point of this row when a late row inside the view holds instead
 * (see lateRowAboveLargestBlock).
 */
export interface RowCut {
  kind: 'top-edge' | 'bottom-edge' | 'body'
  /**
   * The index of the row: the last row whose top is at or above the viewport top (0 when
   * none is), or the row after it when its top lies within the alignment distance below.
   */
  index: number
  /** The content top of the row. */
  top: number
  /** The content bottom of the row, without the gap below it. */
  bottom: number
}

/**
 * Classify the content coordinate `contentTop` of the viewport top in `s`, which must hold
 * a row. The caller subtracts the shift and the inset from `scrollTop` to get it.
 */
export function rowCutAt(s: LayoutSnapshot, contentTop: number): RowCut {
  const holding = largestIndexWhere(s.n, i => rowTop(s, i) <= contentTop)
  const following = holding + 1
  const index = following < s.n && rowTop(s, following) - contentTop <= ROW_ALIGN_OFFSET_PX + TOP_EDGE_SLACK_PX
    ? following
    : holding
  const top = rowTop(s, index)
  const bottom = rowBottom(s, index)
  const kind = contentTop - top <= TOP_EDGE_SLACK_PX
    ? 'top-edge'
    : contentTop >= bottom
      ? 'bottom-edge'
      : 'body'
  return { kind, index, top, bottom }
}

/** The content top of row `i` of `s`. */
function rowTop(s: LayoutSnapshot, i: number): number {
  return s.offsets[i] ?? 0
}

/** The content bottom of row `i` of `s`, without the gap below it. */
function rowBottom(s: LayoutSnapshot, i: number): number {
  return rowTop(s, i) + (s.heights[i] ?? 0)
}

/**
 * The shift for the `next` snapshot that keeps one point of the content at its scroll
 * coordinate.
 *
 * - No previous snapshot, or no row in the next one: 0.
 * - `pinBottom`: the bottom of the content keeps its scroll coordinate.
 * - A late row can hold in place of the viewport top (see lateRowAboveLargestBlock): the
 *   rule then holds the bottom edge of that row, so the row corrects its height above
 *   itself, and the larger part of the view, which lies below it, stays in place.
 * - Otherwise the rule finds the row `R` that holds the viewport top (see rowCutAt), and
 *   it holds:
 *   - the top edge of `R` when the viewport top is at that edge, or a seek placed the top
 *     edge of the next row just below the viewport top (then `R` is that next row);
 *   - the bottom edge of `R` when the viewport top is in the gap below `R`, or the reader
 *     did not see `R` at its previous height: a height that the mounted copy of `R` never
 *     showed and now changes (`sawHeight`), as when a fling skeleton or a copy that waited
 *     for its content gives way to the row. A hidden copy, the storage, or an earlier
 *     mount measured that height. A read that corrects it then moves no row below `R`, which
 *     keeps an upward scroll through history stable: the reader came into `R` from below,
 *     so the correction goes above the viewport top;
 *   - otherwise the same distance below the top edge of `R`, plus the distance that the
 *     content at the viewport top moved inside `R` (`intraRowShift`), limited to the
 *     new height of `R`. This is correct for a row that grows at its bottom, as a
 *     streamed answer does.
 *
 *   The rule holds the bottom edge of such a row whatever the direction of the reader. A
 *   reader who scrolls down past the top edge of such a row before its first read then
 *   keeps the distance to its bottom edge, so a row that grows hides its growth above the
 *   viewport top. That case is rare: a row below the view mounts and measures while the
 *   reader crosses the view and the overscan, and the hidden copy, which is as wide as the
 *   row, measures the height that the row takes.
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
  const cut = rowCutAt(previous, c)
  const late = lateRowAboveLargestBlock(previous, next, c, cut, input)
  // A held bottom edge moves the point at the viewport top by as much as the edge moved.
  const heldContentTop = late === undefined
    ? heldPoint(previous, next, c, cut, input)
    : c + rowBottom(next, late.nextIndex) - rowBottom(previous, late.index)
  return heldContentTop === undefined
    ? 0
    : limitSpacer(scrollTop - heldContentTop)
}

/** A row of both snapshots, by its index in each. */
interface RowInBoth {
  index: number
  nextIndex: number
}

/**
 * The late row whose bottom edge holds in place of the viewport top, or `undefined` when
 * the viewport top holds. `previous` must hold a row, and `cut` is `rowCutAt(previous, c)`.
 *
 * A late row is a row that fits all of these:
 *
 * - Its top edge is in the view: at the viewport top (the `top-edge` kind of `cut`) or
 *   below it, and above the bottom of the view.
 * - It came into the view over the top edge (`enteredFromAbove`), so the reader reached
 *   it from below.
 * - This step corrects its height, and the reader did not see that height (`sawHeight`).
 *
 * Such a row waited for its content behind a placeholder of the height that a hidden copy
 * or the storage gave, while the reader scrolled up to it, as a long report does while
 * its worker renders it. The first read of the row can differ from that height by more
 * than a view. Wherever the row grows, a part of the view moves: the part above it
 * or the part below it.
 *
 * The late rows divide the view into blocks that no late row touches: from the viewport
 * top to the first late row, between two late rows, and from the last late row to the
 * bottom of the view. The rule keeps the largest block in place, so the smaller part of
 * what the reader sees moves:
 *
 * - The block at the viewport top is the largest, or as large as the largest: the
 *   viewport top holds, as with no late row. Each late row then grows at its bottom.
 * - Another block is the largest: the bottom edge of the late row above that block holds.
 *   That row and each row above it move up, and each late row below the block grows at
 *   its bottom.
 *
 * A reader who scrolls up meets the placeholder high in the view, with the rows that the
 * reader looked at below it. The row then grows above itself, and those rows stay in
 * place. A row that mounted inside the view or below it is never late, so a seek, a
 * restore, and a scroll down keep the viewport top.
 */
function lateRowAboveLargestBlock(
  previous: LayoutSnapshot,
  next: LayoutSnapshot,
  c: number,
  cut: RowCut,
  { viewHeight, sawHeight, enteredFromAbove }: Pick<LayoutShiftInput, 'viewHeight' | 'sawHeight' | 'enteredFromAbove'>,
): RowInBoth | undefined {
  const viewBottom = c + viewHeight
  // The index in `next` of the row `i` when the row is late, or `undefined`.
  const lateNextIndex = (i: number): number | undefined => {
    const id = previous.list[i]?.id
    const nextIndex = id === undefined
      ? undefined
      : next.indexById.get(id)
    if (id === undefined || nextIndex === undefined)
      return undefined
    if ((next.heights[nextIndex] ?? 0) === (previous.heights[i] ?? 0) || !enteredFromAbove(id))
      return undefined
    return !sawHeight(id)
      ? nextIndex
      : undefined
  }
  // The largest block so far, with the late row above it: `undefined` for the block at the
  // viewport top. A later block replaces it only when it is larger.
  let largest: RowInBoth | undefined
  let largestSize = -1
  // The block that is open now: its start, and the late row above it.
  let blockStart = c
  let above: RowInBoth | undefined
  const closeBlockAt = (end: number): void => {
    const size = Math.max(0, Math.min(end, viewBottom) - blockStart)
    if (size > largestSize) {
      largestSize = size
      largest = above
    }
  }
  // The top edge of the row at the viewport top is in the view only for the `top-edge` kind.
  const first = cut.kind === 'top-edge'
    ? cut.index
    : cut.index + 1
  for (let i = first; i < previous.n && rowTop(previous, i) < viewBottom; i++) {
    const nextIndex = lateNextIndex(i)
    if (nextIndex === undefined)
      continue
    closeBlockAt(rowTop(previous, i))
    above = { index: i, nextIndex }
    blockStart = rowBottom(previous, i)
  }
  closeBlockAt(viewBottom)
  return largest
}

/**
 * The content coordinate in `next` of the point at `c` in `previous`, or `undefined` when
 * no row is in both. `previous` must hold a row, and `cut` is `rowCutAt(previous, c)`.
 */
function heldPoint(
  previous: LayoutSnapshot,
  next: LayoutSnapshot,
  c: number,
  cut: RowCut,
  { intraRowShift, sawHeight }: Pick<LayoutShiftInput, 'intraRowShift' | 'sawHeight'>,
): number | undefined {
  const r = cut.index
  const rowId = previous.list[r]?.id
  const nextIndex = rowId === undefined
    ? undefined
    : next.indexById.get(rowId)
  if (rowId !== undefined && nextIndex !== undefined) {
    const nextTop = rowTop(next, nextIndex)
    if (cut.kind === 'top-edge')
      return nextTop + (c - cut.top)
    const nextHeight = next.heights[nextIndex] ?? 0
    // A row that keeps its height gives the same point for each edge, so only a row that
    // changes its height needs the answer. A mounted row changes its height only through a
    // read of its copy, so the answer is about that read.
    const holdsBottomEdge = cut.kind === 'bottom-edge'
      || (nextHeight !== (previous.heights[r] ?? 0) && !sawHeight(rowId))
    if (holdsBottomEdge)
      return nextTop + nextHeight + (c - cut.bottom)
    return nextTop + clamp(c - cut.top + intraRowShift(rowId), 0, nextHeight)
  }
  for (let i = r + 1; i < previous.n; i++) {
    const below = surviving(previous, next, i)
    if (below !== undefined)
      return rowTop(next, below) + (c - rowTop(previous, i))
  }
  for (let i = r - 1; i >= 0; i--) {
    const above = surviving(previous, next, i)
    if (above !== undefined)
      return rowBottom(next, above) + (c - rowBottom(previous, i))
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
