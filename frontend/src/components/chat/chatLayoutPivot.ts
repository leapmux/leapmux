import { smallestIndexWhere } from '~/lib/binarySearch'
import { clampScrollTop } from './chatScrollGeometry'

// ---------------------------------------------------------------------------
// The pivot element of the chat list
//
// The layout shift (chatLayoutShift.ts) holds the content at the viewport top in
// place. When the viewport top cuts the body of a row, the offset map alone cannot
// say where that content went: a row can rewrap at a new width, or grow above the
// viewport top inside itself. This module records one DOM element at the viewport
// top, inside its row, and measures how far that element moved inside the row
// since the record. The distance inside the row does not depend on `scrollTop`, on
// a row transform, or on the layout shift, so a flush that moves the row on purpose
// does not change it.
// ---------------------------------------------------------------------------

/** The deepest level below the row element that `selectPivotElement` descends to. */
const PIVOT_MAX_DEPTH = 8
/** Above this many children, `selectPivotElement` finds the child by a binary search. */
const PIVOT_LINEAR_SCAN_MAX_CHILDREN = 16
/** The most side-by-side columns that `selectPivotElement` steps across at one level. */
const PIVOT_MAX_SIDE_BY_SIDE = 4
/** An element counts as cut by the line only when it extends this far below it. */
const LINE_SLACK_PX = 1
/** The most nested wrappers with no box that `selectPivotElement` looks through at one level. */
const PIVOT_MAX_BOXLESS_DEPTH = 4

/**
 * Select the element of `rowElement` whose position the reader sees at the line
 * `lineY`: the first element that starts at or below the line, or, inside an element
 * that the line cuts, the deepest such element. `lineY` and `viewportBottomY` are
 * client coordinates.
 *
 * A wrapper with no box (`display: contents`) reports an empty rectangle, so the search
 * looks through it at its children, as layout does. A wrapper ends the descent
 * otherwise: no child of it extends below the line, and the container above it, which
 * starts at the row top, would be the record.
 *
 * A wrong pick is not an error: any connected element of the row is a valid record.
 * It only makes the measured displacement less precise.
 */
export function selectPivotElement(rowElement: Element, lineY: number, viewportBottomY: number): Element {
  let current = rowElement
  for (let level = 0; level < PIVOT_MAX_DEPTH; level++) {
    const children = boxChildren(current)
    let index = firstChildBelowLine(children, lineY)
    if (index < 0)
      return current
    let selected = children[index]!
    let selectedRect = selected.getBoundingClientRect()
    // Columns side by side, such as a split diff, all start above the line. Take the last
    // one that the line cuts.
    for (let step = 0; step < PIVOT_MAX_SIDE_BY_SIDE; step++) {
      const sibling = children[index + 1]
      if (sibling === undefined)
        break
      const siblingRect = sibling.getBoundingClientRect()
      if (!(siblingRect.top < lineY && siblingRect.bottom > lineY + LINE_SLACK_PX))
        break
      index += 1
      selected = sibling
      selectedRect = siblingRect
    }
    if (selectedRect.top >= lineY)
      return selected
    const next = children[index + 1]
    if (next !== undefined) {
      const nextTop = next.getBoundingClientRect().top
      if (lineY <= nextTop && nextTop < viewportBottomY)
        return next
    }
    if (selected.childElementCount === 0)
      return selected
    current = selected
  }
  return current
}

/**
 * The children of `element` that have a box, in document order: a child with no box and
 * with children of its own (`display: contents`) is replaced by its own box children, at
 * most PIVOT_MAX_BOXLESS_DEPTH wrappers deep. A long list (a code block, many paragraphs)
 * keeps its plain children, so the binary search reads a few rectangles, not all of them.
 */
function boxChildren(element: Element, depth = 0): Element[] {
  const children = [...element.children]
  if (children.length > PIVOT_LINEAR_SCAN_MAX_CHILDREN)
    return children
  const out: Element[] = []
  for (const child of children) {
    if (depth < PIVOT_MAX_BOXLESS_DEPTH && child.childElementCount > 0 && hasNoBox(child))
      out.push(...boxChildren(child, depth + 1))
    else
      out.push(child)
  }
  return out
}

/** Whether layout gave `element` no box: an empty rectangle at the origin. */
function hasNoBox(element: Element): boolean {
  const rect = element.getBoundingClientRect()
  return rect.width === 0 && rect.height === 0 && rect.top === 0 && rect.left === 0
}

/** The index of the first child that extends below the line, or -1 when no child does. */
function firstChildBelowLine(children: readonly Element[], lineY: number): number {
  const count = children.length
  const extendsBelow = (i: number) => children[i]!.getBoundingClientRect().bottom > lineY + LINE_SLACK_PX
  if (count > PIVOT_LINEAR_SCAN_MAX_CHILDREN)
    return smallestIndexWhere(count, extendsBelow, -1)
  for (let i = 0; i < count; i++) {
    if (extendsBelow(i))
      return i
  }
  return -1
}

/**
 * The distance (px) at which an element that moved on screen with no scroll and no
 * move of its row is worth a report. A smaller move is sub-pixel layout.
 */
export const CONTENT_MOVED_WARN_PX = 2

/** A move of the content at the viewport top that no scroll and no layout shift caused. */
export interface ContentMovedInfo {
  rowId: string
  /** How far the pivot element moved on screen, positive downward. */
  movedPx: number
  scrollTop: number
}

/** The row that the viewport top cuts, as `createLayoutPivot` needs it. */
export interface PivotRow {
  id: string
  element: Element
  /** The scroll-coordinate top of the row. */
  top: number
}

export interface LayoutPivotDeps {
  scrollElement: () => HTMLElement | undefined
  /**
   * The row whose measured body the viewport top cuts at `scrollTop`, with its mounted
   * element, or `undefined`.
   */
  pivotRow: (scrollTop: number) => PivotRow | undefined
  /** Whether the measurer would commit a new height for the row now. */
  hasUncommittedHeight: (rowId: string) => boolean
  /** True while no record must exist, such as during a fast scroll. */
  isSuspended: () => boolean
  requestFrame: (cb: () => void) => number
  cancelFrame: (handle: number) => void
  /** Report an element at the viewport top that moved on screen with no scroll and no layout move. */
  onContentMoved?: (info: ContentMovedInfo) => void
}

export interface LayoutPivot {
  /** Make a new record in the next frame. Several calls before the frame make one record. */
  scheduleRecord: () => void
  /**
   * How far the recorded element moved inside the row `rowId` since the last read, positive
   * downward. The record then holds the new distance, so a second read before the DOM
   * changes again returns 0. Returns 0 when no usable record exists for the row.
   */
  takeIntraRowShift: (rowId: string) => number
  /** Cancel the pending frame and drop the record. */
  dispose: () => void
}

interface PivotRecord {
  rowId: string
  rowElement: Element
  element: Element
  /** The distance from the top edge of the row element to the top edge of the element. */
  inRowTop: number
  /** The distance from the top edge of the scroll element to the top edge of the element. */
  screenTop: number
  /** The scroll-coordinate top of the row. */
  rowTop: number
  scrollTop: number
}

/**
 * Record the pivot element after each scroll and each layout flush, and measure its
 * displacement inside its row on demand.
 *
 * The frame keeps the previous record while the row has a height change that the
 * measurer did not commit yet. A browser runs the animation frame callbacks before it
 * delivers a ResizeObserver notification, so without this rule a frame during a
 * continuous resize records the new layout first, and the commit that follows reads a
 * displacement of 0.
 */
export function createLayoutPivot(deps: LayoutPivotDeps): LayoutPivot {
  let record: PivotRecord | undefined
  // The pending frame. An object, so that a frame that runs synchronously inside
  // `requestFrame` cannot leave a handle that marks it as still pending.
  let pendingFrame: { handle: number } | undefined

  const recordNow = () => {
    const el = deps.scrollElement()
    if (el === undefined || el.clientHeight === 0 || deps.isSuspended()) {
      record = undefined
      return
    }
    const scrollTop = clampScrollTop(el, el.scrollTop)
    const row = deps.pivotRow(scrollTop)
    if (row === undefined) {
      record = undefined
      return
    }
    if (deps.hasUncommittedHeight(row.id))
      return
    const scrollRect = el.getBoundingClientRect()
    const lineY = scrollRect.top + el.clientTop
    const element = selectPivotElement(row.element, lineY, lineY + el.clientHeight)
    const elementTop = element.getBoundingClientRect().top
    const inRowTop = elementTop - row.element.getBoundingClientRect().top
    const screenTop = elementTop - scrollRect.top
    if (!Number.isFinite(inRowTop) || !Number.isFinite(screenTop)) {
      record = undefined
      return
    }
    const previous = record
    if (deps.onContentMoved !== undefined
      && previous !== undefined
      && previous.element === element
      && previous.rowId === row.id
      && Math.abs(previous.scrollTop - scrollTop) < 1
      && Math.abs(previous.rowTop - row.top) < 0.5
      && Math.abs(screenTop - previous.screenTop) >= CONTENT_MOVED_WARN_PX) {
      deps.onContentMoved({ rowId: row.id, movedPx: screenTop - previous.screenTop, scrollTop })
    }
    record = {
      rowId: row.id,
      rowElement: row.element,
      element,
      inRowTop,
      screenTop,
      rowTop: row.top,
      scrollTop,
    }
  }

  const scheduleRecord = () => {
    if (pendingFrame !== undefined)
      return
    const frame = { handle: 0 }
    pendingFrame = frame
    frame.handle = deps.requestFrame(() => {
      if (pendingFrame !== frame)
        return
      pendingFrame = undefined
      recordNow()
    })
  }

  const takeIntraRowShift = (rowId: string): number => {
    const current = record
    if (current === undefined || current.rowId !== rowId || !current.element.isConnected || !current.rowElement.isConnected)
      return 0
    const inRowTop = current.element.getBoundingClientRect().top - current.rowElement.getBoundingClientRect().top
    if (!Number.isFinite(inRowTop))
      return 0
    const displacement = inRowTop - current.inRowTop
    current.inRowTop = inRowTop
    return displacement
  }

  const dispose = () => {
    if (pendingFrame !== undefined)
      deps.cancelFrame(pendingFrame.handle)
    pendingFrame = undefined
    record = undefined
  }

  return { scheduleRecord, takeIntraRowShift, dispose }
}
