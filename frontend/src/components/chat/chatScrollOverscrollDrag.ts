/** Drag distance (px) before an at-top overscroll counts as a load-older gesture. */
const OVERSCROLL_DRAG_THRESHOLD_PX = 12

/**
 * Tracks touch/pointer "overscroll at the top" drags: when the user drags
 * downward while the scroll container is already at scrollTop 0, fire
 * `onDragAtTop` (which loads older history). Touch and non-mouse pointer share
 * the same drag logic over separate start coordinates; mouse pointers are
 * ignored (they scroll via the wheel, handled separately). Extracted from the
 * scroll hook because it is a self-contained input-gesture unit with no coupling
 * to the sticky-bottom / anchoring math.
 */
export function createOverscrollDrag(deps: { atTop: () => boolean, onDragAtTop: () => boolean }) {
  let touchStartY: number | null = null
  let pointerStartY: number | null = null

  // Fire onDragAtTop once the downward drag crosses the threshold while at the
  // top; return true (so the caller resets the start) if it consumed the drag.
  const tryFire = (startY: number | null, currentY: number): boolean => {
    if (startY === null || !deps.atTop())
      return false
    if (currentY - startY < OVERSCROLL_DRAG_THRESHOLD_PX)
      return false
    return deps.onDragAtTop()
  }

  return {
    onTouchStart: (event: TouchEvent) => {
      touchStartY = event.touches[0]?.clientY ?? null
    },
    onTouchMove: (event: TouchEvent) => {
      if (tryFire(touchStartY, event.touches[0]?.clientY ?? 0))
        touchStartY = null
    },
    onTouchEnd: () => {
      touchStartY = null
    },
    onPointerDown: (event: PointerEvent) => {
      if (event.pointerType !== 'mouse')
        pointerStartY = event.clientY
    },
    onPointerMove: (event: PointerEvent) => {
      if (event.pointerType !== 'mouse' && tryFire(pointerStartY, event.clientY))
        pointerStartY = null
    },
    onPointerUp: () => {
      pointerStartY = null
    },
  }
}
