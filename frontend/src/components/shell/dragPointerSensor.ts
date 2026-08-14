import type { Id } from '@thisbeyond/solid-dnd'
import { useDragDropContext } from '@thisbeyond/solid-dnd'
import { onCleanup, onMount } from 'solid-js'
import { motion } from '~/styles/tokens'

/**
 * solid-dnd's own default, kept verbatim. `PRESS_SLOP_PX` in
 * ~/components/common/contextMenuGesture.ts sits below it deliberately; the
 * ordering test in ./dragPointerSensor.test.tsx holds the two modules to that.
 */
export const ACTIVATION_DISTANCE_PX = 10

/**
 * solid-dnd's own default. Mouse only here -- see the component doc. Exported
 * for the same ordering test.
 */
export const ACTIVATION_DELAY_MS = 250

const SENSOR_ID = 'pointer-sensor'

/**
 * solid-dnd's pointer sensor with one policy change and one bug fix.
 *
 * **Policy: on touch, the 250ms hold CLAIMS the pointer instead of starting a
 * drag.** A touch press then resolves three ways, and each is unambiguous:
 *
 * ```
 *   move now (< 250ms)          -> the browser pans. A scroll.
 *   hold 250ms, then move       -> a drag. The claim blocked the pan, so this
 *                                  wins the race it used to lose.
 *   hold 500ms without moving   -> the claim releases and the row's context menu
 *                                  opens (~/components/common/contextMenuGesture.ts).
 * ```
 *
 * Upstream instead started the drag outright at 250ms, filtering only on
 * `event.button !== 0` -- which a touch pointerdown passes. That made a stationary
 * hold lift the row, so the hold could not also mean "open the menu", and a finger
 * resting on a row for a quarter second could no longer scroll.
 *
 * The claim is deliberately INVISIBLE: it calls `preventDefault` on the moves it
 * sees and nothing else. Starting the real drag at 250ms and cancelling it at
 * 500ms would reach the same three outcomes, but every long press would first lift
 * the row into a drag overlay and then snap it back -- a false start the user sees
 * on every single menu.
 *
 * A touch drag now REQUIRES the claim: a swipe that begins immediately is a scroll,
 * never a drag, however far it travels. Press-then-drag is the reorder gesture on
 * every mobile platform, and it is the only one that does not race the scroller.
 *
 * Mouse behaviour is identical to upstream -- 250ms delay or 10px of travel, either
 * one starts the drag -- so nothing on the desktop changes.
 *
 * **Fix: detach on `pointercancel`.** Upstream listens for `pointermove` and
 * `pointerup` only, so when the browser claims a touch for a pan -- which ends the
 * pointer stream with `pointercancel` and no `pointerup` -- its two document
 * listeners stay attached for the life of the page, and the pending activation
 * timer still fires.
 *
 * Rendered once, in ./SectionDragContext.tsx, in place of `<DragDropSensors />`.
 */
export function DragPointerSensor() {
  const context = useDragDropContext()
  // `DragDropProvider` is always an ancestor at the one mount site, but the hook is
  // typed as nullable and a test tree could render this bare.
  if (!context)
    return null

  const [state, { addSensor, removeSensor, sensorStart, sensorMove, sensorEnd, dragStart, dragEnd }] = context

  const isActiveSensor = () => state.active.sensorId === SENSOR_ID

  const initialCoordinates = { x: 0, y: 0 }
  let activationDelayTimeoutId: ReturnType<typeof setTimeout> | null = null
  let claimReleaseTimeoutId: ReturnType<typeof setTimeout> | null = null
  let activationDraggableId: Id | null = null
  /** The pointer this sensor tracks. `null` when no press is live. */
  let trackedPointerId: number | null = null
  /** This press is a touch, so it takes the claim path rather than upstream's delay. */
  let isTouchPress = false
  /**
   * The hold has claimed the pointer: every move from here is prevented, so the
   * browser cannot pan, and travel past the activation distance starts a drag.
   * False again once the claim window closes and the press becomes a menu.
   */
  let claimed = false

  // Declarations, not arrow constants: these handlers reference one another in a
  // cycle (attach -> onPointerMove -> onActivate -> detach -> onPointerMove), which
  // only hoisting can express without an arbitrary forward reference.
  function attach(event: PointerEvent, draggableId: Id) {
    // A secondary finger never owns the press; the primary one may still be
    // mid-hold elsewhere, and its release must not end this press.
    if (event.button !== 0 || !event.isPrimary)
      return
    // The inline rename inputs inside these rows keep their native selection
    // gestures (the sibling context-menu gesture exempts them too -- see
    // ~/components/common/contextMenuGesture.ts), and a row's open menu is a DOM
    // child of the row: a press on a menu item belongs to the menu, and a drag
    // must not start from under it.
    const target = event.target as Element | null
    if (target?.closest?.('input, textarea, [contenteditable="true"], [popover]'))
      return

    // A press while another is still tracked supersedes it. Clear the old press's
    // timers and listeners first, or its claim timer would fire under this one
    // and set `claimed` for a press that never earned it, and its release timer
    // would null this press's handle so a later `detach()` could not cancel it.
    detach()

    document.addEventListener('pointermove', onPointerMove)
    document.addEventListener('pointerup', onPointerUp)
    document.addEventListener('pointercancel', onPointerCancel)

    activationDraggableId = draggableId
    trackedPointerId = event.pointerId
    initialCoordinates.x = event.clientX
    initialCoordinates.y = event.clientY
    isTouchPress = event.pointerType === 'touch'
    claimed = false

    if (!isTouchPress) {
      activationDelayTimeoutId = setTimeout(onActivate, ACTIVATION_DELAY_MS)
      return
    }

    // Touch: claim at the same 250ms upstream used, then hand the press back at
    // the context menu's own threshold if it never moved. The two timers are what
    // make one press mean three different things.
    activationDelayTimeoutId = setTimeout(() => {
      activationDelayTimeoutId = null
      claimed = true
    }, ACTIVATION_DELAY_MS)
    claimReleaseTimeoutId = setTimeout(() => {
      claimReleaseTimeoutId = null
      claimed = false
    }, motion.longPress)
  }

  function detach() {
    if (activationDelayTimeoutId) {
      clearTimeout(activationDelayTimeoutId)
      activationDelayTimeoutId = null
    }
    if (claimReleaseTimeoutId) {
      clearTimeout(claimReleaseTimeoutId)
      claimReleaseTimeoutId = null
    }
    claimed = false
    trackedPointerId = null
    document.removeEventListener('pointermove', onPointerMove)
    document.removeEventListener('pointerup', onPointerUp)
    document.removeEventListener('pointercancel', onPointerCancel)
    document.removeEventListener('selectionchange', clearSelection)
  }

  function onActivate() {
    if (!state.active.sensor) {
      sensorStart(SENSOR_ID, initialCoordinates)
      dragStart(activationDraggableId!)
      clearSelection()
      document.addEventListener('selectionchange', clearSelection)
    }
    else if (!isActiveSensor()) {
      detach()
    }
  }

  function onPointerMove(event: PointerEvent) {
    if (trackedPointerId === null || event.pointerId !== trackedPointerId)
      return
    const coordinates = { x: event.clientX, y: event.clientY }

    if (!state.active.sensor) {
      // A touch that moves before the claim is a scroll, whatever distance it
      // covers. Only a claimed press can become a drag.
      if (isTouchPress && !claimed)
        return
      // Hold the browser off while the claim stands, so the pan cannot start under
      // a press that is about to become a drag. `cancelable` is false once the
      // browser has already begun scrolling; calling it then only logs a warning.
      if (claimed && event.cancelable)
        event.preventDefault()

      const transform = {
        x: coordinates.x - initialCoordinates.x,
        y: coordinates.y - initialCoordinates.y,
      }
      if (Math.sqrt(transform.x ** 2 + transform.y ** 2) > ACTIVATION_DISTANCE_PX)
        onActivate()
    }

    if (isActiveSensor()) {
      event.preventDefault()
      sensorMove(coordinates)
    }
  }

  function onPointerUp(event: PointerEvent) {
    if (trackedPointerId === null || event.pointerId !== trackedPointerId)
      return
    detach()
    if (isActiveSensor()) {
      event.preventDefault()
      dragEnd()
      sensorEnd()
    }
  }

  function onPointerCancel(event: PointerEvent) {
    if (trackedPointerId === null || event.pointerId !== trackedPointerId)
      return
    // The browser took the pointer -- a pan, or the gesture leaving the window. No
    // `pointerup` will follow, so this is the only chance to unwind.
    detach()
    if (isActiveSensor()) {
      dragEnd()
      sensorEnd()
    }
  }

  function clearSelection() {
    window.getSelection()?.removeAllRanges()
  }

  onMount(() => {
    addSensor({ id: SENSOR_ID, activators: { pointerdown: attach } })
  })

  onCleanup(() => {
    detach()
    removeSensor(SENSOR_ID)
  })

  return null
}
