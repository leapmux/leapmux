/**
 * Edge auto-scroll for a drag inside a scrolling container.
 *
 * Every draggable surface in this app sits in one: the input queue caps at
 * `min(40vh, 20rem)` and holds up to 100 rows, the mobile tab sheet's list
 * scrolls, and the sidebar scrolls. A pointer drag moves the row with a CSS
 * transform inside that box, so without this the row is CLIPPED at the edge and
 * every slot past the fold is unreachable -- the drop lands on the last VISIBLE
 * row instead of the one the user aimed at. A native HTML5 drag got this from
 * the browser; a pointer drag has to do it.
 *
 * The geometry is a pure function so a unit test can own it. `startEdgeScroll`
 * below is the loop around it.
 */

/** How close to an edge the pointer must come, in CSS pixels, before scrolling. */
export const EDGE_BAND_PX = 48
/** The fastest the container scrolls, in CSS pixels per frame. */
export const MAX_SCROLL_STEP_PX = 12

/**
 * How far to scroll this frame: negative to scroll up, positive to scroll down,
 * zero to stand still.
 *
 * The speed ramps with how far the pointer reaches into the edge band, so a
 * pointer that rests just inside the band creeps and one held at the very edge
 * runs at `maxStep`. A jump straight to full speed makes a short list
 * uncontrollable.
 *
 * A viewport shorter than two bands would have overlapping bands, and a pointer
 * in the middle would then belong to both. Such a box returns 0: it is small
 * enough that every row is already on screen.
 */
export function edgeScrollStep(
  viewport: { top: number, bottom: number },
  pointerY: number,
  opts: { band?: number, maxStep?: number } = {},
): number {
  const band = opts.band ?? EDGE_BAND_PX
  const maxStep = opts.maxStep ?? MAX_SCROLL_STEP_PX
  if (viewport.bottom - viewport.top < band * 2)
    return 0
  const topEdge = viewport.top + band
  if (pointerY < topEdge)
    return -maxStep * Math.min(1, (topEdge - pointerY) / band)
  const bottomEdge = viewport.bottom - band
  if (pointerY > bottomEdge)
    return maxStep * Math.min(1, (pointerY - bottomEdge) / band)
  return 0
}

/** The nearest ancestor that scrolls vertically, or undefined when none does. */
export function scrollableAncestor(el: Element | null): HTMLElement | undefined {
  for (let node = el; node instanceof HTMLElement; node = node.parentElement) {
    const overflowY = getComputedStyle(node).overflowY
    if ((overflowY === 'auto' || overflowY === 'scroll') && node.scrollHeight > node.clientHeight)
      return node
  }
  return undefined
}

/** A live edge-scroll loop. Feed it the pointer, and stop it when the drag ends. */
export interface EdgeScroller {
  /** Report where the pointer is now, in client coordinates. */
  track: (clientY: number) => void
  /** Stop the loop and release the frame callback. */
  stop: () => void
}

/**
 * Scroll `scroller` while the pointer rests near one of its edges.
 *
 * `onScrolled` runs after each step with the pixels actually applied, which is
 * how the caller keeps the drag consistent: solid-dnd caches every droppable's
 * rect at `dragStart` and never recomputes it mid-drag, so a scroll invalidates
 * all of them.
 *
 * The loop only runs while a step is non-zero, so a pointer in the middle of the
 * box costs one comparison per move and no frames at all.
 */
export function startEdgeScroll(
  scroller: HTMLElement,
  onScrolled: (appliedPx: number) => void,
): EdgeScroller {
  let pointerY = Number.NaN
  let frame = 0

  const step = () => {
    frame = 0
    if (Number.isNaN(pointerY))
      return
    const rect = scroller.getBoundingClientRect()
    const wanted = edgeScrollStep({ top: rect.top, bottom: rect.bottom }, pointerY)
    if (wanted === 0)
      return
    // The APPLIED delta, not the wanted one. A scroller already at either end
    // moves less than asked, or not at all, and reporting the wanted amount
    // would shift the drag's compensation past what the box actually did.
    const before = scroller.scrollTop
    scroller.scrollTop = before + wanted
    const applied = scroller.scrollTop - before
    if (applied === 0)
      return
    onScrolled(applied)
    frame = requestAnimationFrame(step)
  }

  return {
    track: (clientY) => {
      pointerY = clientY
      if (frame === 0)
        frame = requestAnimationFrame(step)
    },
    stop: () => {
      cancelAnimationFrame(frame)
      frame = 0
      pointerY = Number.NaN
    },
  }
}
