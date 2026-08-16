import type { Id } from '@thisbeyond/solid-dnd'
import { useDragDropContext } from '@thisbeyond/solid-dnd'
import { onCleanup, onMount } from 'solid-js'
import { motion } from '~/styles/tokens'

/**
 * solid-dnd's own default activation distance, kept verbatim. `PRESS_SLOP_PX`
 * in ~/components/common/contextMenuGesture.ts sits below it on purpose: a
 * finger that drifts abandons the context-menu hold before the same travel
 * starts a drag. The ordering test in ./guardedPointerSensor.test.tsx holds
 * the two modules to that.
 */
export const ACTIVATION_DISTANCE_PX = 10

/**
 * solid-dnd's own default, applied to fine pointers only: a stationary mouse
 * or pen hold starts a drag after this delay, exactly as upstream does. Touch
 * never takes this path — see the component doc. Exported for the same
 * ordering test (it must stay below the context menu's hold).
 */
export const ACTIVATION_DELAY_MS = 250

const SENSOR_ID = 'pointer-sensor'

/**
 * Presses that start inside embedded UI belong to that UI, not to a drag:
 * the inline rename inputs keep their native selection gestures, and a row's
 * open menu is a DOM child of the row, so a press on a menu item must not
 * lift the row from under the menu.
 */
const EMBEDDED_UI_SELECTOR = 'input, textarea, [contenteditable="true"], [popover]'

/**
 * The stock upstream pointer sensor with the guards this app needs.
 *
 * Upstream's `PointerSensor` (solid-dnd 0.7.x) arms on any button-0 press,
 * activates a stationary hold of any pointer type after 250ms, tracks no
 * pointer id, and detaches only on `pointerup`. Each of those is wrong for
 * rows that also host a context-menu gesture and inline inputs:
 *
 * - **Embedded UI.** `attach` skips presses whose target sits inside
 *   `EMBEDDED_UI_SELECTOR`, so a text-selection sweep in a rename input
 *   never drags the row.
 * - **Primary pointer only.** A secondary finger never owns a press; the
 *   primary finger may still be mid-drag elsewhere, and its release must
 *   not end this press.
 * - **Pointer identity.** `pointermove`, `pointerup`, and `pointercancel`
 *   only act on the tracked `pointerId`, so a stray second pointer's
 *   release cannot end an in-flight drag, and a second press cannot
 *   retarget the first one's activation.
 * - **Superseded presses.** A new press detaches the previous one first,
 *   clearing its timer, so two near-simultaneous presses cannot fire each
 *   other's activation.
 * - **`pointercancel` unwinds.** Palm rejection and system-gesture
 *   takeovers end the pointer stream with no `pointerup`; the press
 *   detaches and the pending activation is cleared instead of firing a
 *   drag no pointer owns.
 * - **Touch needs movement.** A touch press activates only after travel
 *   past `ACTIVATION_DISTANCE_PX` — a stationary touch hold never lifts a
 *   row. At the context menu's own hold threshold
 *   (`motion.longPress`), a touch press that never moved stops being a
 *   drag candidate entirely: the menu owns it, and a later move must not
 *   start a drag under the open menu.
 *
 * Mouse and pen behavior stays identical to upstream: 250ms of hold or 10px
 * of travel, either one starts the drag.
 *
 * Rendered once, in ./SectionDragContext.tsx, in place of
 * `<DragDropSensors />`.
 */
export function GuardedPointerSensor() {
  const context = useDragDropContext()
  // `DragDropProvider` is always an ancestor at the one mount site, but the hook is
  // typed as nullable and a test tree could render this bare.
  if (!context)
    return null

  const [state, { addSensor, removeSensor, sensorStart, sensorMove, sensorEnd, dragStart, dragEnd }] = context

  const isActiveSensor = () => state.active.sensorId === SENSOR_ID

  const initialCoordinates = { x: 0, y: 0 }
  let activationDelayTimeoutId: ReturnType<typeof setTimeout> | null = null
  let holdReleaseTimeoutId: ReturnType<typeof setTimeout> | null = null
  let activationDraggableId: Id | null = null
  /** The pointer this sensor tracks. `null` when no press is live. */
  let trackedPointerId: number | null = null
  /** This press is a touch, so it activates on movement only — never on a hold timer. */
  let isTouchPress = false
  /**
   * A touch press that outlived the context menu's hold threshold without
   * moving. The menu owns it now; no later move may start a drag from it.
   */
  let touchHoldExpired = false

  // Declarations, not arrow constants: these handlers reference one another in
  // a cycle (attach -> onPointerMove -> onActivate -> detach -> onPointerMove),
  // which only hoisting can express without an arbitrary forward reference.
  function attach(event: PointerEvent, draggableId: Id) {
    if (event.button !== 0 || !event.isPrimary)
      return
    const target = event.target as Element | null
    if (target?.closest?.(EMBEDDED_UI_SELECTOR))
      return

    // A press while another is still live supersedes it. Clear the old press's
    // timers and listeners first, or its activation timer would fire under
    // this one and lift a row this press never selected.
    detach()

    document.addEventListener('pointermove', onPointerMove)
    document.addEventListener('pointerup', onPointerUp)
    document.addEventListener('pointercancel', onPointerCancel)

    activationDraggableId = draggableId
    trackedPointerId = event.pointerId
    isTouchPress = event.pointerType === 'touch'
    touchHoldExpired = false
    initialCoordinates.x = event.clientX
    initialCoordinates.y = event.clientY

    if (!isTouchPress) {
      activationDelayTimeoutId = setTimeout(onActivate, ACTIVATION_DELAY_MS)
      return
    }

    holdReleaseTimeoutId = setTimeout(() => {
      holdReleaseTimeoutId = null
      touchHoldExpired = true
    }, motion.longPress)
  }

  function detach() {
    if (activationDelayTimeoutId) {
      clearTimeout(activationDelayTimeoutId)
      activationDelayTimeoutId = null
    }
    if (holdReleaseTimeoutId) {
      clearTimeout(holdReleaseTimeoutId)
      holdReleaseTimeoutId = null
    }
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
      // The context menu owns a touch hold that outlived its threshold; a
      // drag must not start on top of the open menu.
      if (isTouchPress && touchHoldExpired)
        return
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
    // The browser took the pointer — palm rejection, or the gesture leaving
    // the window. No `pointerup` will follow, so this is the only chance to
    // unwind before the pending activation fires a drag no pointer owns.
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
