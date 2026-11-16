import type { Accessor } from 'solid-js'
import type { RowHeightCache } from './chatRowHeightCache'
import type { VirtualItem } from './useChatVirtualizer'
import type { ScrollAnchor } from '~/stores/chatTypes'
import { createMemo, createSignal, untrack } from 'solid-js'
import { shallowEqualArrays } from '~/lib/shallowEqual'
import { anchorOffsetInRow } from './chatScrollAnchor'

// ---------------------------------------------------------------------------
// Row admission
//
// The virtualized chat list holds measured rows only. A loaded row joins the list
// after a hidden copy measured it, so the list never lays a row out at a height that
// it guessed, and the first measure of a row never moves a row that the reader can
// see or scroll to.
//
// This unit decides which loaded rows the virtualizer gets:
//
//  - Loaded rows: the rows of the loaded window that are not hidden.
//  - Measured row: a loaded row for which the height cache answers (see
//    RowHeightCache.isMeasured). A measure produced its height under the current height
//    key of the row, or the row is mounted and still shows the height of another key.
//  - Admitted rows: one contiguous range of the loaded rows, each of them measured.
//    The virtualizer lays out exactly these rows.
//  - Staged rows: the loaded rows outside the admitted range. Only hidden copies
//    render them.
//  - Target: a place that the view must show next: the tail, the top, or one row.
//
// The rules:
//
//  1. The admitted range is one contiguous range of loaded rows, each measured.
//  2. The range grows at an end as soon as the next row at that end is measured.
//  3. A row that stops being measured leaves the range, and so does every row on the
//     far side of it. The range keeps the part that holds the view.
//  4. The view changes to another place only through a target. A target is reached
//     when its row is measured and the measured rows around it fill one view height.
//     Until then the admitted range stays as it is.
//  5. Every target comes from a request of the scroll hook, which asks for the place
//     that a command of the reader goes to. This unit forms no target of its own: it
//     would admit a range that no placement puts the view on.
//  6. The admitted rows hold the tail of the loaded rows when their last row is the last
//     loaded row, and also while only new messages are staged below them (see
//     `holdsTail`).
//
// A reader who scrolls faster than the measurement reaches the end of the admitted
// rows and waits there, as a reader waits for a page of older history.
// ---------------------------------------------------------------------------

/** A place that the view must show next. */
export type AdmissionTarget
  = | { kind: 'tail' }
    | { kind: 'top' }
    | {
      kind: 'row'
      /**
       * The row, and the place inside it that the viewport top goes to. The placement
       * resolves it against the measured height of the row (see resolveAnchorScrollTop).
       */
      anchor: ScrollAnchor
    }

/** The row that the range keeps when a row inside it stops being measured. */
export type AdmissionViewAnchor
  = | { kind: 'tail' }
    | { kind: 'row', id: string }

/**
 * The state of a target:
 *
 * - `none`: no target is pending.
 * - `waiting`: the row of the target is not measured, or the measured rows around it
 *   do not fill the view yet.
 * - `reached`: the admitted rows are the rows of the target, and a placement can put
 *   the view on it.
 * - `gone`: the row of the target left the loaded rows.
 */
export type AdmissionTargetState = 'none' | 'waiting' | 'reached' | 'gone'

/** The admitted rows, as indexes into the loaded rows. `end` is excluded. */
export interface AdmittedRange {
  start: number
  end: number
}

/**
 * The most rows that a waiting target stages in hidden copies at one time. One shared
 * premeasure frame measures them together, so the number limits the work of one frame.
 */
export const TARGET_STAGING_ROWS = 12

export interface AdmittedRangeInput {
  /** The loaded rows, in order. */
  rows: readonly VirtualItem[]
  /**
   * The height that the height cache answers for the row, or `undefined` for a row that
   * is not measured (see RowHeightCache.heightOf).
   */
  heightOf: (row: VirtualItem) => number | undefined
  /** The ids of the rows of the previous range, or `undefined` for an empty one. */
  previous: ReadonlySet<string> | undefined
  /**
   * What the view shows: the tail for a reader who follows it, else the row of the
   * last anchor that the scroll hook recorded for the reader, or `undefined`.
   */
  viewAnchor: AdmissionViewAnchor | undefined
  /** Whether the row is mounted. */
  isMounted: (id: string) => boolean
  /** The pending target, or `undefined`. */
  target: AdmissionTarget | undefined
  /** The height of the viewport in pixels. 0 for a pane with no layout box. */
  viewHeight: number
}

export interface WantedRowsInput {
  rows: readonly VirtualItem[]
  isMeasured: (row: VirtualItem) => boolean
  /** The pending target, and its state from `nextAdmittedRange`. Only a waiting target wants rows. */
  target: AdmissionTarget | undefined
  targetState: AdmissionTargetState
}

const EMPTY_RANGE: AdmittedRange = { start: 0, end: 0 }

/** The largest run of measured rows that contains the measured row at `index`. */
function measuredRunAround(rows: readonly VirtualItem[], isMeasured: (row: VirtualItem) => boolean, index: number): AdmittedRange {
  let start = index
  while (start > 0 && isMeasured(rows[start - 1]!))
    start--
  let end = index + 1
  while (end < rows.length && isMeasured(rows[end]!))
    end++
  return { start, end }
}

/** The index of the row with `id` in `rows`, or -1. */
function indexOfRow(rows: readonly VirtualItem[], id: string): number {
  return rows.findIndex(row => row.id === id)
}

/**
 * The span of the previous range in the current rows, as inclusive indexes: from the
 * first to the last row that is left of the range. `undefined` when no row of it is left.
 * A row of the range that left the rows, or that moved, cannot move the span past the
 * rows that are left: a row that arrived outside them is no row of the range.
 */
function previousSpan(rows: readonly VirtualItem[], previous: AdmittedRangeInput['previous']): { first: number, last: number } | undefined {
  if (previous === undefined)
    return undefined
  let first = -1
  let last = -1
  for (let index = 0; index < rows.length; index++) {
    if (!previous.has(rows[index]!.id))
      continue
    if (first < 0)
      first = index
    last = index
  }
  return first < 0
    ? undefined
    : { first, last }
}

/**
 * The range that continues the previous one: the largest run of measured rows around
 * the row that holds the view. Empty when no row of the previous range is measured.
 */
function continuedRange(input: AdmittedRangeInput, isMeasured: (row: VirtualItem) => boolean): AdmittedRange {
  const { rows, viewAnchor } = input
  const span = previousSpan(rows, input.previous)
  if (span === undefined)
    return EMPTY_RANGE
  const inSpan = (index: number): boolean => index >= span.first && index <= span.last
  const measuredAt = (index: number): boolean => isMeasured(rows[index]!)
  /** The first index of the span that `accept` takes, or -1. */
  const firstInSpan = (accept: (index: number) => boolean): number => {
    for (let index = span.first; index <= span.last; index++) {
      if (accept(index))
        return index
    }
    return -1
  }
  // The holder: the row that the range must keep, the first of these that exists.
  let holder = -1
  // 1. A follower sees the last row of the range.
  if (viewAnchor?.kind === 'tail' && measuredAt(span.last))
    holder = span.last
  // 2. Any other reader sees the row of the last recorded anchor.
  if (holder < 0 && viewAnchor?.kind === 'row') {
    const anchored = indexOfRow(rows, viewAnchor.id)
    if (anchored >= 0 && inSpan(anchored) && measuredAt(anchored))
      holder = anchored
  }
  // 3. A mounted row is in or near the view.
  if (holder < 0)
    holder = firstInSpan(index => input.isMounted(rows[index]!.id) && measuredAt(index))
  // 4. Any measured row of the range.
  if (holder < 0)
    holder = firstInSpan(measuredAt)
  return holder < 0
    ? EMPTY_RANGE
    : measuredRunAround(rows, isMeasured, holder)
}

/**
 * The height of the row at `index`. The fill rule reads the rows of a measured run only,
 * so a row with no height there is a defect of the rule.
 */
function measuredHeight(input: AdmittedRangeInput, index: number): number {
  const row = input.rows[index]!
  const height = input.heightOf(row)
  if (height === undefined)
    throw new Error(`The fill rule read the row ${row.id}, which has no measured height.`)
  return height
}

/** Whether the heights of the rows from `start` to `end` (excluded) sum to `need` or more. */
function heightReaches(input: AdmittedRangeInput, start: number, end: number, need: number): boolean {
  let sum = 0
  for (let index = start; index < end && sum < need; index++)
    sum += measuredHeight(input, index)
  return sum >= need
}

/**
 * The fill rule: whether the reader will see no empty space that a staged row still has
 * to fill, with the view on the target row `t` inside the measured run `run`.
 *
 * Heights count without the gaps between rows. The gap is positive between most rows, so
 * the rule then asks for a little more than the view needs. Two adjacent bands overlap by
 * the width of one border (see BAND_BORDER_PX), so for a run of bands the rule asks for
 * that width less at each pair.
 */
function targetFills(input: AdmittedRangeInput, target: AdmissionTarget, t: number, run: AdmittedRange): boolean {
  const { viewHeight } = input
  // A pane with no layout box has no view to fill. A range that changed to the rows of
  // the target there would have to wait again at the height of the shown pane, with the
  // rows of the old view gone.
  if (viewHeight <= 0)
    return false
  const atFirstRow = run.start === 0
  const atLastRow = run.end === input.rows.length
  // A function, so that a run at the first or the last row sums nothing.
  const runFillsView = (): boolean => heightReaches(input, run.start, run.end, viewHeight)
  switch (target.kind) {
    case 'tail':
      // The view shows the rows up from the tail.
      return atFirstRow || runFillsView()
    case 'top':
      // The view shows the rows down from the top.
      return atLastRow || runFillsView()
    case 'row': {
      // The view shows the rows down from the target row, from the place of the anchor
      // inside it. The placement resolves that place against the measured height of the
      // row, so this rule does the same.
      const need = viewHeight + anchorOffsetInRow(target.anchor, measuredHeight(input, t))
      if (heightReaches(input, t, run.end, need))
        return true
      // The rows below the target end before the view is full: the browser then shows
      // the bottom of the content, and the rows above the target fill the view.
      return atLastRow && (atFirstRow || runFillsView())
    }
  }
}

/** The index of the row of `target`, or -1 for a row target whose row is not loaded. */
function targetRowIndex(rows: readonly VirtualItem[], target: AdmissionTarget): number {
  switch (target.kind) {
    case 'tail': return rows.length - 1
    case 'top': return 0
    case 'row': return indexOfRow(rows, target.anchor.id)
  }
}

/**
 * The range rule: the admitted range for the loaded rows and their measured heights,
 * and the state of the pending target. Pure: it reads no clock and no DOM.
 */
export function nextAdmittedRange(input: AdmittedRangeInput): AdmittedRange & { targetState: AdmissionTargetState } {
  const { rows, target } = input
  const isMeasured = (row: VirtualItem): boolean => input.heightOf(row) !== undefined
  const continued = continuedRange(input, isMeasured)
  if (target === undefined)
    return { ...continued, targetState: 'none' }
  if (rows.length === 0) {
    // No row has to be measured for the tail or the top. The content outside the rows (a
    // banner, a streaming block) can still scroll, so End and Home must still place the
    // view there.
    return {
      ...EMPTY_RANGE,
      targetState: target.kind === 'row'
        ? 'gone'
        : 'reached',
    }
  }
  const t = targetRowIndex(rows, target)
  if (t < 0)
    return { ...continued, targetState: 'gone' }
  if (!isMeasured(rows[t]!))
    return { ...continued, targetState: 'waiting' }
  const run = measuredRunAround(rows, isMeasured, t)
  return targetFills(input, target, t, run)
    ? { ...run, targetState: 'reached' }
    : { ...continued, targetState: 'waiting' }
}

/**
 * The target band of the staging: the indexes of the first TARGET_STAGING_ROWS unmeasured
 * rows along the scan of the fill rule, from the target row toward the side that the view
 * shows (up from the tail, down from the top, down and then up from a row). The scan has
 * no row limit: a limit could leave a waiting target with no row to measure.
 */
export function rowsWantedForTarget(input: WantedRowsInput): number[] {
  const { rows, isMeasured, target } = input
  if (target === undefined || input.targetState !== 'waiting')
    return []
  const t = targetRowIndex(rows, target)
  if (t < 0)
    return []
  const wanted: number[] = []
  const want = (index: number): void => {
    if (wanted.length < TARGET_STAGING_ROWS && !isMeasured(rows[index]!))
      wanted.push(index)
  }
  if (target.kind !== 'tail') {
    for (let index = t; index < rows.length && wanted.length < TARGET_STAGING_ROWS; index++)
      want(index)
  }
  if (target.kind !== 'top') {
    // A row target scanned its own row above, so the scan up starts at the row before it.
    const from = target.kind === 'row'
      ? t - 1
      : t
    for (let index = from; index >= 0 && wanted.length < TARGET_STAGING_ROWS; index--)
      want(index)
  }
  return wanted
}

/** The result of one run of the admission, with the state that the next run continues. */
interface AdmissionComputation {
  /** The loaded rows of the run. */
  rows: readonly VirtualItem[]
  start: number
  end: number
  targetState: AdmissionTargetState
  holdsTail: boolean
  wanted: number[]
  /** The ids of the admitted rows (see AdmittedRangeInput.previous). */
  admittedIds: ReadonlySet<string> | undefined
  /** The kept row (see RowAdmission.holdsTail). */
  keptRowId: string | undefined
}

/** The ids of the rows of `range`, or `undefined` for an empty range. */
function admittedIdsOf(rows: readonly VirtualItem[], range: AdmittedRange): ReadonlySet<string> | undefined {
  if (range.end <= range.start)
    return undefined
  const ids = new Set<string>()
  for (let index = range.start; index < range.end; index++)
    ids.add(rows[index]!.id)
  return ids
}

/**
 * Whether the admitted rows of `range` hold the tail (see RowAdmission.holdsTail).
 * `keptRowId` is the kept row, or `undefined`.
 */
function rangeHoldsTail(rows: readonly VirtualItem[], range: AdmittedRange, keptRowId: string | undefined): boolean {
  if (rows.length === 0)
    return true
  if (range.end <= range.start)
    return false
  if (range.end === rows.length)
    return true
  if (keptRowId === undefined)
    return false
  const lastAdmitted = range.end - 1
  if (rows[lastAdmitted]!.id === keptRowId)
    return true
  // A kept row that left the loaded rows counts for nothing.
  const kept = indexOfRow(rows, keptRowId)
  return kept >= 0 && lastAdmitted > kept
}

/** One request of the scroll hook for a target. */
export interface AdmissionRequest {
  /** The state of the target. `none` once another request replaced this one, or it ended. */
  state: Accessor<AdmissionTargetState>
  /** End the request. A reached request releases its target; a waiting one is cancelled. */
  end: () => void
}

export interface RowAdmission {
  /** The admitted rows, as indexes into the loaded rows. */
  range: Accessor<AdmittedRange>
  /**
   * Loaded rows wait above the admitted range for their measure. True also while loaded
   * rows exist and none is admitted: every loaded row then waits, on both sides.
   */
  stagedOlder: Accessor<boolean>
  /** The same for the loaded rows below the admitted range. */
  stagedNewer: Accessor<boolean>
  /** Loaded rows exist and none is admitted. */
  awaitingFirstView: Accessor<boolean>
  /**
   * The admitted rows hold the tail of the loaded rows: a reader at their bottom has no
   * loaded row below that the reader did not see yet, except a new message. That bottom
   * is the end of the chat only while the loaded window holds the live tail, which the
   * caller tests. True with no loaded row: no row lies below such a reader. False while
   * loaded rows exist and none is admitted.
   *
   * They hold it when their last row is the last loaded row. They hold it also while only
   * new messages are staged below: the unit keeps the last admitted row of each state in
   * which the admitted rows hold the tail (the kept row), and the admitted rows hold the
   * tail while their last row is the kept row or a row after it. So a burst of new
   * messages that measures row by row never ends the hold.
   *
   * The unit keeps a row only while the loaded window holds the live tail, and forgets it
   * otherwise: the rows of a newer history page are no new messages, and a reader who
   * waits for that page at the loaded bottom must not start to follow when it arrives.
   * After a width change, the rows below the admitted rows were admitted before: the last
   * admitted row then lies before the kept row, so the admitted rows do not hold the tail
   * until those rows are measured again.
   */
  holdsTail: Accessor<boolean>
  /** The indexes of the loaded rows that the waiting target needs measured next. */
  wantedRows: Accessor<readonly number[]>
  /** Ask for a target. It replaces the request before it, whose state turns `'none'`. */
  request: (target: AdmissionTarget) => AdmissionRequest
}

/**
 * Create the row admission. Call within a reactive owner: it creates memos.
 */
export function createRowAdmission(deps: {
  /** The loaded rows. */
  rows: Accessor<readonly VirtualItem[]>
  heights: Pick<RowHeightCache, 'heightOf' | 'isMeasured' | 'isMounted' | 'version'>
  /** The height of the viewport in pixels. */
  viewHeight: Accessor<number>
  /**
   * What the view shows. A plain getter: the range computation calls it untracked at each
   * run. It reads state of the scroll hook, which is created after this unit and after the
   * virtualizer, and a tracked read would make the range depend on a unit that depends on
   * the range.
   */
  viewAnchor: () => AdmissionViewAnchor | undefined
  /** The loaded window holds the live tail: the store has no newer page. */
  tailLoaded: Accessor<boolean>
}): RowAdmission {
  // The one request, or `undefined`. Its identity tells a request that was replaced from
  // the current one.
  const [currentRequest, setCurrentRequest] = createSignal<{ target: AdmissionTarget }>()
  const { isMeasured } = deps.heights

  // The one computation over the loaded rows, their heights, the target, and the view
  // height. It carries the admitted rows and the kept row from one run to the next in its
  // own previous value, so a run commits them together with its result. A second run with
  // the same inputs gives the same result: the range that it kept is a largest run of
  // measured rows, and continuing it gives that run again.
  //
  // The memo must take no `equals` option: a skipped commit would keep the carried state
  // of the run before.
  const computation = createMemo<AdmissionComputation>((before) => {
    const rows = deps.rows()
    // The mounted set is a plain Set, so the cache changes its version when the unmount of
    // a row changes an answer of `heightOf`.
    deps.heights.version()
    const tailLoaded = deps.tailLoaded()
    const target = currentRequest()?.target
    const result = nextAdmittedRange({
      rows,
      heightOf: deps.heights.heightOf,
      previous: before?.admittedIds,
      viewAnchor: untrack(deps.viewAnchor),
      isMounted: deps.heights.isMounted,
      target,
      viewHeight: deps.viewHeight(),
    })
    const admitsRows = result.end > result.start
    // The unit forgets the kept row while the loaded window does not hold the live tail.
    const kept = tailLoaded
      ? before?.keptRowId
      : undefined
    const holdsTail = rangeHoldsTail(rows, result, kept)
    // The ids change only with the rows or the range, so most runs build no set.
    const sameAdmittedRows = before !== undefined
      && before.rows === rows
      && before.start === result.start
      && before.end === result.end
    return {
      rows,
      start: result.start,
      end: result.end,
      targetState: result.targetState,
      holdsTail,
      wanted: rowsWantedForTarget({
        rows,
        isMeasured,
        target,
        targetState: result.targetState,
      }),
      admittedIds: sameAdmittedRows
        ? before.admittedIds
        : admittedIdsOf(rows, result),
      keptRowId: tailLoaded && holdsTail && admitsRows
        ? rows[result.end - 1]!.id
        : kept,
    }
  })

  const range = createMemo<AdmittedRange>(
    () => {
      const { start, end } = computation()
      return { start, end }
    },
    EMPTY_RANGE,
    {
      equals: (a, b) => a.start === b.start && a.end === b.end,
    },
  )
  const targetState = createMemo(() => computation().targetState)
  const holdsTail = createMemo(() => computation().holdsTail)
  const wantedRows = createMemo<readonly number[]>(() => computation().wanted, [], { equals: shallowEqualArrays })
  const isEmpty = (): boolean => {
    const { start, end } = range()
    return end <= start
  }
  const rowCount = createMemo(() => computation().rows.length)
  const awaitingFirstView = createMemo(() => rowCount() > 0 && isEmpty())

  return {
    range,
    stagedOlder: createMemo(() => awaitingFirstView() || range().start > 0),
    stagedNewer: createMemo(() => awaitingFirstView() || range().end < rowCount()),
    awaitingFirstView,
    holdsTail,
    wantedRows,
    request: (target) => {
      const own = { target }
      setCurrentRequest(own)
      return {
        state: () => (currentRequest() === own
          ? targetState()
          : 'none'),
        end: () => {
          // A request that a newer one replaced changes nothing when it ends.
          if (untrack(currentRequest) === own)
            setCurrentRequest(undefined)
        },
      }
    },
  }
}
