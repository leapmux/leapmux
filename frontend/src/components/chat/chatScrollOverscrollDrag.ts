/** Drag distance in pixels before an overscroll requests another page. */
const OVERSCROLL_DRAG_THRESHOLD_PX = 12

interface DragPoint {
  clientX: number
  clientY: number
}

/** Recognize vertical drags at either loaded edge, even when the pane cannot scroll. */
export function createOverscrollDrag(deps: {
  onDragAtTop: (target: EventTarget | null) => boolean
  onDragAtBottom: (target: EventTarget | null) => boolean
}) {
  let touchStart: DragPoint | undefined
  let pointerStart: DragPoint | undefined

  const pointOf = (event: DragPoint): DragPoint => ({
    clientX: event.clientX,
    clientY: event.clientY,
  })
  const tryFire = (start: DragPoint | undefined, current: DragPoint, target: EventTarget | null): boolean => {
    if (!start)
      return false
    const deltaY = current.clientY - start.clientY
    if (Math.abs(deltaY) < OVERSCROLL_DRAG_THRESHOLD_PX || Math.abs(deltaY) <= Math.abs(current.clientX - start.clientX))
      return false
    return deltaY > 0
      ? deps.onDragAtTop(target)
      : deps.onDragAtBottom(target)
  }

  return {
    onTouchStart: (event: TouchEvent) => {
      const touch = event.touches.length === 1
        ? event.touches[0]
        : undefined
      touchStart = touch && pointOf(touch)
    },
    onTouchMove: (event: TouchEvent) => {
      const touch = event.touches.length === 1
        ? event.touches[0]
        : undefined
      if (!touch || tryFire(touchStart, touch, event.target))
        touchStart = undefined
    },
    onTouchEnd: () => {
      touchStart = undefined
    },
    onPointerDown: (event: PointerEvent) => {
      if (event.pointerType !== 'mouse')
        pointerStart = pointOf(event)
    },
    onPointerMove: (event: PointerEvent) => {
      if (event.pointerType !== 'mouse' && tryFire(pointerStart, event, event.target))
        pointerStart = undefined
    },
    onPointerUp: () => {
      pointerStart = undefined
    },
  }
}
