import type { Accessor } from 'solid-js'
import type { LayoutPivot, PivotRow } from './chatLayoutPivot'
import type { LayoutSnapshot } from './chatLayoutShift'
import type { ContentMovedReporterDeps } from './chatScrollDiagnostics'
import type { RowMeasurer } from './createRowMeasurer'
import { createComputed, createEffect, createSignal, on, onCleanup, untrack } from 'solid-js'
import { cancelFrame, requestFrame } from '~/lib/animationFrame'
import { createLayoutPivot } from './chatLayoutPivot'
import { nextLayoutShift, rowCutAt } from './chatLayoutShift'
import { createContentMovedReporter } from './chatScrollDiagnostics'
import { readLogicalScrollTop } from './chatScrollGeometry'

// ---------------------------------------------------------------------------
// The layout shift controller of the chat list
//
// The virtualizer owns the offset map of the loaded rows. This module owns the layout
// shift on top of it (see chatLayoutShift.ts): the shift signal, the conversions between
// scroll and content coordinates, the spacer height, the pivot element that the shift
// rule measures (see chatLayoutPivot.ts), and the rebase that moves the shift into
// `scrollTop`. The virtualizer creates it once, after its row measurer, and exposes its
// members in its result.
// ---------------------------------------------------------------------------

/** The members of the layout shift that the virtualizer exposes in its result. */
export interface LayoutShiftSurface {
  /**
   * The layout shift (px): `scroll = content + layoutShift + inset` (see chatLayoutShift
   * for the inset). The rows move by it, so a geometry change keeps the content under the
   * reader in place with no `scrollTop` write. 0 without a scroll element.
   */
  layoutShift: Accessor<number>
  /** The height of the scroll spacer: `totalHeight() + layoutShift()`, never below 0. */
  spacerHeight: Accessor<number>
  /**
   * Convert a scroll coordinate to a content coordinate: `scrollTop - layoutShift() - inset`.
   * The inset is the distance from scroll coordinate 0 to the top of the spacer. It does
   * not count the flex spacer above the rows of a list shorter than the view (see
   * chatLayoutShift).
   */
  contentTopOf: (scrollTop: number) => number
  /**
   * Convert a content coordinate to a scroll coordinate: `contentTop + layoutShift() + inset`
   * (see contentTopOf).
   */
  scrollTopOf: (contentTop: number) => number
  /**
   * The scroll coordinate of a placement at the content coordinate `contentTop` (see
   * placementScrollTop): the top of the scroller for a target at or above the content top,
   * while the first row of the list is the first loaded row.
   */
  placementScrollTopOf: (contentTop: number) => number
  /** The spacer that `rebase` writes. Pass `undefined` on unmount. */
  attachSpacer: (spacer: HTMLElement | undefined) => void
  /** The layer of rows inside the spacer, which `rebase` also writes. Pass `undefined` on unmount. */
  attachLayer: (layer: HTMLElement | undefined) => void
  /**
   * Remove the layout shift with no visible change: `scrollTop` changes by `-shift`, and
   * the layer and the spacer follow (see applyRebaseWrite). The browser rounds the written
   * `scrollTop`, so the shift keeps that remainder, which is below SCROLL_ROUNDING_MAX_PX,
   * and the content stays in place. A shift below SCROLL_ROUNDING_MAX_PX stays as it is. A
   * clamp of `scrollTop` drops its remainder, and the clamp then moves the content. It
   * writes the DOM itself, synchronously, so a caller in an effect and one in an event
   * handler get the same result. Returns the part of the shift that it removed: 0, with no
   * write, when the shift stays or the scroll element is absent or hidden.
   */
  rebase: () => number
}

/** The layout shift, with the one member that only the virtualizer calls. */
export interface LayoutShiftController extends LayoutShiftSurface {
  /**
   * Make a new record of the pivot element in the next frame: the viewport moved. Does
   * nothing without a scroll element.
   */
  schedulePivotRecord: () => void
}

export interface LayoutShiftDeps {
  /** The geometry of the loaded rows. Each new snapshot moves the shift (see nextLayoutShift). */
  geometry: Accessor<LayoutSnapshot>
  /** The total height (px) of the loaded rows, in content coordinates. */
  totalHeight: Accessor<number>
  /** The scroll element of the list. Without it, the shift stays 0. */
  scrollElement: (() => HTMLElement | undefined) | undefined
  /**
   * The inset (px) of the spacer: the distance from scroll coordinate 0 to the top of the
   * spacer (see chatLayoutShift). The conversions and the shift rule apply it. A rebase
   * changes `scrollTop` by the shift alone, so it needs no inset.
   */
  spacerInsetPx: Accessor<number>
  /** Keep the bottom of the content in place: the reader follows the tail. */
  pinBottom: () => boolean
  /**
   * The first row of the list is the first loaded row: no loaded row waits above the list
   * for its measure. A placement at the content top then goes to the top of the scroller
   * (see placementScrollTop).
   */
  listStartsAtLoadedTop: Accessor<boolean>
  /** True during a fast scroll (see LayoutPivotDeps.isSuspended). */
  isSuspended: Accessor<boolean>
  /** The mounted rows, in which the pivot element is recorded. */
  rows: Pick<RowMeasurer, 'elementOf' | 'hasUncommittedHeight'>
  /**
   * Whether the reader saw the row at the height that the last snapshot holds for it (see
   * LayoutShiftInput.sawHeight). The shift rule holds the bottom edge of a row that changes
   * a height that the reader did not see.
   */
  sawHeight: (rowId: string) => boolean
  /**
   * Whether the row came into the view over the top edge of the view, so the reader reached
   * it from below (see LayoutShiftInput.enteredFromAbove). The shift rule can hold the
   * bottom edge of such a row inside the view, when the row corrects a height that the
   * reader did not see.
   */
  enteredFromAbove: (rowId: string) => boolean
  /** The last commit that changed the height of a loaded row, for the report of a content move. */
  lastMeasurement: ContentMovedReporterDeps['lastMeasurement']
}

/**
 * The largest move (px) of a written `scrollTop` that is not a clamp. A browser stores the
 * position on its grid of device pixels, and at a device pixel ratio of 1 or more a device
 * pixel is at most one CSS pixel. A rebase keeps a smaller remainder of its write as the
 * shift, so the rounding errors of many rebases cannot add up to a visible move. A shift
 * below it gets no rebase: the browser could round the write back to where it was.
 */
export const SCROLL_ROUNDING_MAX_PX = 1

/**
 * Write a rebase of `shift` to the DOM, and return the shift that remains. `writeLayout`
 * writes the spacer and the layer for the shift that it receives.
 *
 * - A shift below SCROLL_ROUNDING_MAX_PX stays: nothing is written, and the result is
 *   `shift`.
 * - Otherwise `scrollTop` changes by `-shift`. The browser clamps `scrollTop` to the
 *   scroll range, so the range must hold the target when `scrollTop` is written: a
 *   positive shift shrinks the spacer, so the position moves first; a negative shift
 *   grows it, so the layout moves first, to the shift 0.
 * - The browser rounds the written `scrollTop`. The remainder, `scrollTop - target`, is the
 *   shift that keeps the content in place, and the layout ends at it. A remainder of
 *   SCROLL_ROUNDING_MAX_PX or more comes from a clamp, which moves the content anyway: the
 *   result is then 0.
 */
export function applyRebaseWrite(el: HTMLElement, shift: number, writeLayout: (shift: number) => void): number {
  if (Math.abs(shift) < SCROLL_ROUNDING_MAX_PX)
    return shift
  const target = el.scrollTop - shift
  if (shift < 0)
    writeLayout(0)
  el.scrollTop = target
  const remainder = el.scrollTop - target
  const kept = Math.abs(remainder) < SCROLL_ROUNDING_MAX_PX
    ? remainder
    : 0
  // A negative shift wrote the layout at 0 above, which a remainder of 0 keeps.
  if (shift > 0 || kept !== 0)
    writeLayout(kept)
  return kept
}

/**
 * The scroll coordinate of a placement at the content coordinate `contentTop`:
 * `scrollTopOf(contentTop)`, or 0 for a target at or above the content top while the first
 * row of the list is the first loaded row (`listStartsAtLoadedTop`).
 *
 * The inset of the spacer (the list's top padding and row gap) lies above content 0, so no
 * content coordinate places the view inside it, and anchorAtOffset floors a position inside
 * it to the top of the first row. A placement at the content top therefore goes to the top
 * of the scroller and shows the inset, as the browser's clamp shows the space below the
 * last row. A reader at the top of the history comes back to it, and a seek or a scrub to
 * the first row reaches the top of the scroller.
 *
 * While loaded rows wait above the list for their measure, content 0 is the top of the
 * first row of the list, and not the top of the loaded rows. A placement there converts
 * exactly, as for every other row: the row stays in its place when the rows above it join,
 * because the layout shift holds its top edge.
 */
export function placementScrollTop(
  contentTop: number,
  scrollTopOf: (contentTop: number) => number,
  listStartsAtLoadedTop: boolean,
): number {
  return contentTop <= 0 && listStartsAtLoadedTop
    ? 0
    : scrollTopOf(contentTop)
}

/** Create the layout shift of the virtualized chat list (see the module comment). */
export function createLayoutShift(deps: LayoutShiftDeps): LayoutShiftController {
  // The layout shift: `scroll = content + layoutShift + inset`. A computation over the
  // geometry below sets it, before the render effects of the same update run.
  const [layoutShift, setLayoutShift] = createSignal(0)
  const contentTopOf = (scrollTop: number): number => scrollTop - layoutShift() - deps.spacerInsetPx()
  const scrollTopOf = (contentTop: number): number => contentTop + layoutShift() + deps.spacerInsetPx()
  const placementScrollTopOf = (contentTop: number): number =>
    placementScrollTop(contentTop, scrollTopOf, deps.listStartsAtLoadedTop())
  // The spacer height for a shift. The style binding of ChatView and `rebase` both use it.
  const spacerHeightFor = (shift: number): number => Math.max(0, deps.totalHeight() + shift)
  const spacerHeight = (): number => spacerHeightFor(layoutShift())

  // The row whose measured body the viewport top cuts (the `body` kind of rowCutAt, the
  // one kind in which the shift rule reads a pivot element), with its mounted element: the
  // row in which the pivot element is recorded.
  const pivotRow = (scrollTop: number): PivotRow | undefined => {
    const g = deps.geometry()
    if (g.n === 0)
      return undefined
    const cut = rowCutAt(g, contentTopOf(scrollTop))
    const id = g.list[cut.index]?.id
    if (cut.kind !== 'body' || id === undefined)
      return undefined
    const element = deps.rows.elementOf(id)
    return element === undefined
      ? undefined
      : {
          id,
          element,
          top: scrollTopOf(cut.top),
        }
  }

  /**
   * Create the pivot element that the shift rule measures, and follow each geometry
   * snapshot with a new shift. Only a list with a scroll element has either.
   */
  function followGeometry(scrollElement: () => HTMLElement | undefined): LayoutPivot {
    const pivot = createLayoutPivot({
      scrollElement,
      pivotRow,
      hasUncommittedHeight: deps.rows.hasUncommittedHeight,
      isSuspended: deps.isSuspended,
      pinBottom: deps.pinBottom,
      requestFrame,
      cancelFrame,
      onContentMoved: createContentMovedReporter({
        layoutShift: () => untrack(layoutShift),
        lastMeasurement: deps.lastMeasurement,
      }),
    })
    onCleanup(pivot.dispose)

    // The snapshot that the current shift applies to. The computation keeps its own
    // copy: the geometry can recompute several times inside one batch, and the shift must
    // compare the last snapshot that it saw with the one that reaches the DOM.
    let shiftedSnapshot: LayoutSnapshot | undefined
    // A computation, not an effect: the rows and the layer must reach the DOM in the
    // same flush, so the shift must be set before the render effects run. It tracks
    // only the geometry.
    createComputed(() => {
      const next = deps.geometry()
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
          scrollTop: readLogicalScrollTop(el) - deps.spacerInsetPx(),
          viewHeight: el.clientHeight,
          pinBottom: deps.pinBottom(),
          intraRowShift: pivot.takeIntraRowShift,
          sawHeight: deps.sawHeight,
          enteredFromAbove: deps.enteredFromAbove,
        }))
      })
    })
    // A geometry change or a new shift needs a new record of the pivot element. A fast
    // scroll needs no record when it ends: the pivot records once the scroll position holds
    // for a frame, also while the scroll still counts as fast (see createLayoutPivot).
    createEffect(on([deps.geometry, layoutShift], () => pivot.scheduleRecord()))
    return pivot
  }
  const pivot = deps.scrollElement === undefined
    ? undefined
    : followGeometry(deps.scrollElement)

  // The two elements that `rebase` writes: the spacer and the layer that holds the rows.
  // Each has its own ref, so each attaches and detaches on its own.
  let layoutSpacer: HTMLElement | undefined
  let layoutLayer: HTMLElement | undefined
  const attachSpacer = (spacer: HTMLElement | undefined): void => {
    layoutSpacer = spacer
  }
  const attachLayer = (layer: HTMLElement | undefined): void => {
    layoutLayer = layer
  }
  // Write the spacer and the layer for `shift`: the same two values that the style
  // bindings of ChatView write for that shift.
  const writeLayout = (shift: number): void => {
    if (layoutSpacer !== undefined)
      layoutSpacer.style.height = `${untrack(() => spacerHeightFor(shift))}px`
    if (layoutLayer !== undefined)
      layoutLayer.style.transform = `translateY(${shift}px)`
  }

  const rebase = (): number => {
    const shift = untrack(layoutShift)
    const el = deps.scrollElement?.()
    if (el === undefined || el.clientHeight === 0)
      return 0
    const kept = applyRebaseWrite(el, shift, writeLayout)
    // The style bindings write the same two values again on the next flush.
    setLayoutShift(kept)
    return shift - kept
  }

  return {
    layoutShift,
    spacerHeight,
    contentTopOf,
    scrollTopOf,
    placementScrollTopOf,
    attachSpacer,
    attachLayer,
    rebase,
    schedulePivotRecord: () => pivot?.scheduleRecord(),
  }
}
