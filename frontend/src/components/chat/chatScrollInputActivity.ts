import { onCleanup, onMount } from 'solid-js'
import { PRESS_SLOP_PX } from '~/components/common/contextMenuGesture'

// ---------------------------------------------------------------------------
// Pointer and touch activity on the chat list
//
// The scroll hook (useChatScroll.ts) must know whether a pointer or a finger is down on the
// list: the scroller does not rest during a press (see createScrollRest), and a move of the
// view during a press is a drag of the reader. This unit tracks that input.
//
// It reads the browser's own lists rather than a counter that each down and up balance. A
// release that never arrives (a capture that moved to another element, a gesture of the
// browser that takes the up, an unmount during a gesture) would leave such a counter above
// 0 for the session, and the scroller would never rest again.
//
// - The pointer map is keyed by pointerId, so a second down of one pointer changes nothing.
//   It holds the pointerType of each pointer. A PRIMARY pointer starts a fresh gesture, so
//   its down clears the map first. The window listeners below end each pointer.
// - The touch state is `event.touches.length` of the last touch event, so a missed end
//   corrects itself on the next touch event, and a gesture of several fingers stays active
//   while a finger remains.
// ---------------------------------------------------------------------------

export interface ScrollInputActivityDeps {
  /**
   * The last pointer or finger went up, or a window listener ended it, and no other input
   * stays down. Called from an event listener, never from a reactive computation.
   */
  onInputEnded: () => void
}

export interface ScrollInputActivity {
  /** A pointer or a finger is down on the list. */
  isActive: () => boolean
  /** The `pointerdown` handler of the list. The window listeners end the pointer. */
  onPointerDown: (event: PointerEvent) => void
  /** The `touchstart` handler of the list. */
  onTouchStart: (event: TouchEvent) => void
  /** The `touchend` and the `touchcancel` handler of the list. */
  onTouchEnd: (event: TouchEvent) => void
  /**
   * One finger moved vertically more than PRESS_SLOP_PX, and more than it moved
   * horizontally, since the count of fingers last changed. A horizontal swipe on a wide
   * code block and a pinch are no vertical drag.
   */
  isVerticalDrag: (event: TouchEvent) => boolean
}

interface Point {
  x: number
  y: number
}

/** The point of the one finger of `event`, or null when no finger or two or more fingers touch. */
function oneFingerPoint(event: TouchEvent): Point | null {
  const touch = event.touches.length === 1
    ? event.touches[0]
    : undefined
  return touch
    ? { x: touch.clientX, y: touch.clientY }
    : null
}

/**
 * Track the pointers and the fingers that are down on the chat list (see the module
 * comment). Call it inside a reactive owner: it adds its window listeners at the mount and
 * removes them at the cleanup of the owner.
 */
export function createScrollInputActivity(deps: ScrollInputActivityDeps): ScrollInputActivity {
  const pointers = new Map<number, string>()
  let touchActive = false
  // Where the one finger of the current touch was when the count of fingers last changed,
  // or null while no finger or two or more fingers touch. isVerticalDrag measures the
  // travel from there.
  let oneFingerStart: Point | null = null

  const isActive = () => pointers.size > 0 || touchActive
  const endIfIdle = () => {
    if (!isActive())
      deps.onInputEnded()
  }

  // End the pointers that `ended` selects, and report the end when no input stays down.
  const endPointers = (ended: (pointerId: number, pointerType: string) => boolean) => {
    let any = false
    for (const [pointerId, pointerType] of pointers) {
      if (ended(pointerId, pointerType)) {
        pointers.delete(pointerId)
        any = true
      }
    }
    if (any)
      endIfIdle()
  }

  // The window listeners own the end of each pointer that went down in the list. A press
  // that starts in the list and ends outside it never reaches the list's own handlers,
  // which receive only the events whose target is inside it. The window sees every
  // release, in the capture phase, before the list's own `pointerup` handler. Three more
  // events end a pointer whose release never arrives:
  // - `contextmenu`: the native menu of a right press can take the pointerup of a mouse.
  //   A touch or a pen stays down: a long press opens the menu under the finger.
  // - `lostpointercapture` with no button down. A capture also ends while the button
  //   stays down, as when the row that captured a pen or a finger unmounts, and that
  //   pointer is still down.
  // - `blur` of the window: the release goes to another window.
  onMount(() => {
    if (typeof window === 'undefined')
      return
    const onPointerEnd = (event: PointerEvent) => {
      endPointers(pointerId => pointerId === event.pointerId)
    }
    const onLostPointerCapture = (event: PointerEvent) => {
      if (event.buttons === 0)
        onPointerEnd(event)
    }
    const onContextMenu = () => {
      endPointers((_, pointerType) => pointerType === 'mouse')
    }
    const onBlur = () => {
      endPointers(() => true)
    }
    window.addEventListener('pointerup', onPointerEnd, true)
    window.addEventListener('pointercancel', onPointerEnd, true)
    window.addEventListener('lostpointercapture', onLostPointerCapture, true)
    window.addEventListener('contextmenu', onContextMenu, true)
    window.addEventListener('blur', onBlur)
    onCleanup(() => {
      window.removeEventListener('pointerup', onPointerEnd, true)
      window.removeEventListener('pointercancel', onPointerEnd, true)
      window.removeEventListener('lostpointercapture', onLostPointerCapture, true)
      window.removeEventListener('contextmenu', onContextMenu, true)
      window.removeEventListener('blur', onBlur)
    })
  })

  const noteTouches = (event: TouchEvent) => {
    touchActive = event.touches.length > 0
    oneFingerStart = oneFingerPoint(event)
  }

  return {
    isActive,
    onPointerDown: (event) => {
      if (event.isPrimary)
        pointers.clear()
      pointers.set(event.pointerId, event.pointerType)
    },
    onTouchStart: noteTouches,
    onTouchEnd: (event) => {
      // event.touches holds the touches that are STILL down after this one lifted: none
      // for the last finger, and more while another finger remains.
      noteTouches(event)
      endIfIdle()
    },
    isVerticalDrag: (event) => {
      const start = oneFingerStart
      const now = oneFingerPoint(event)
      if (!start || !now)
        return false
      const dy = Math.abs(now.y - start.y)
      return dy > PRESS_SLOP_PX && dy > Math.abs(now.x - start.x)
    },
  }
}
