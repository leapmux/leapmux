import type { Id } from '@thisbeyond/solid-dnd'
import type { EdgeScroller } from '~/lib/dragAutoScroll'
import { useDragDropContext } from '@thisbeyond/solid-dnd'
import { onCleanup, onMount } from 'solid-js'
import { scrollableAncestor, startEdgeScroll } from '~/lib/dragAutoScroll'
import { INPUT_OR_EDITABLE_SELECTOR } from '~/lib/textInputBehavior'
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
/** The transformer that adds the auto-scroll back to the dragged row's offset. */
const AUTO_SCROLL_TRANSFORMER_ID = 'pointer-sensor-auto-scroll'

/**
 * Presses that start inside embedded UI belong to that UI, not to a drag:
 * the inline rename inputs keep their native selection gestures, and a row's
 * open menu is a DOM child of the row, so a press on a menu item must not
 * lift the row from under the menu.
 *
 * `INPUT_OR_EDITABLE_SELECTOR` carries the text-entry group that all three
 * pointer guards share, and `[popover]` stands in the list itself. The value
 * equals the context menu's list today
 * (~/components/common/contextMenuGesture.ts), and the two still stay apart:
 * they guard two different gestures on one press.
 *
 * Do NOT adopt the wider list from ~/lib/dragActivators.ts. This sensor is the
 * floor under EVERY draggable, and a drag grip reaches it directly: the grip
 * carries the raw activators, which call `attach` below with the grip as the
 * event target. `[data-drag-handle]` in this list would decline that press,
 * and touch reorder would stop working on every surface. A row body gets the
 * wider list from `rowBodyActivators`, which runs before this guard, so the
 * two compose there without this one growing.
 */
const EMBEDDED_UI_SELECTOR = `${INPUT_OR_EDITABLE_SELECTOR}, [popover]`

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
 * Rendered once inside EVERY `DragDropProvider`, in place of
 * `<DragDropSensors />`. There are two providers: the shell's, in
 * ./SectionDragContext.tsx, and the composer's own, in
 * ~/components/chat/AgentInputQueue.tsx.
 */
export function GuardedPointerSensor() {
  const context = useDragDropContext()
  // `DragDropProvider` is always an ancestor at every mount site, but the hook is
  // typed as nullable and a test tree could render this bare.
  if (!context)
    return null

  const [state, {
    addSensor,
    removeSensor,
    sensorStart,
    sensorMove,
    sensorEnd,
    dragStart,
    dragEnd,
    addTransformer,
    removeTransformer,
    recomputeLayouts,
  }] = context

  const isActiveSensor = () => state.active.sensorId === SENSOR_ID

  const initialCoordinates = { x: 0, y: 0 }
  let activationDelayTimeoutId: ReturnType<typeof setTimeout> | null = null
  let holdReleaseTimeoutId: ReturnType<typeof setTimeout> | null = null
  let activationDraggableId: Id | null = null
  /** The element the press started on. The scroller to auto-scroll is above it. */
  let pressTarget: Element | null = null
  /** The pointer this sensor tracks. `null` when no press is live. */
  let trackedPointerId: number | null = null
  /** This press is a touch, so it activates on movement only — never on a hold timer. */
  let isTouchPress = false
  /**
   * A touch press that outlived the context menu's hold threshold without
   * moving. The menu owns it now; no later move may start a drag from it.
   */
  let touchHoldExpired = false
  /** The live edge-scroll loop, while a drag runs inside a scroller. */
  let edgeScroller: EdgeScroller | undefined
  /** The draggable this sensor registered the auto-scroll transformer on. */
  let autoScrollDraggableId: Id | null = null
  /** How far the auto-scroll moved the container since this drag started. */
  let autoScrolled = 0

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
    pressTarget = target
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
    stopEdgeScroll()
    trackedPointerId = null
    pressTarget = null
    document.removeEventListener('pointermove', onPointerMove)
    document.removeEventListener('pointerup', onPointerUp)
    document.removeEventListener('pointercancel', onPointerCancel)
    document.removeEventListener('selectionchange', clearSelection)
  }

  /**
   * Follow the pointer to the edges of the nearest scrolling ancestor, and
   * scroll it while the pointer rests there.
   *
   * Every draggable surface in this app lives in a scroller — the input queue's
   * own box, the mobile tab sheet's list, the sidebar — and the drag moves the
   * row with a CSS transform INSIDE it. Without this the row is clipped at the
   * edge and every slot past the fold is unreachable: the drop lands on the last
   * VISIBLE row rather than the one the user aimed at. A native HTML5 drag got
   * this from the browser, and a pointer drag has to do it.
   *
   * TWO corrections keep the drag consistent with the scroll, and both are
   * necessary:
   *
   *   - A TRANSFORMER on the dragged item. The drag transform is the pointer's
   *     own delta, so a container that scrolls by S moves the row's box up by S
   *     and the row leaves the cursor. Adding the accumulated scroll back keeps
   *     the row under the finger.
   *   - `recomputeLayouts()`. solid-dnd caches every droppable's rect at
   *     `dragStart` and never refreshes it during the drag, so after a scroll
   *     every cached rect names a position the row no longer occupies and the
   *     collision detector picks the wrong target.
   */
  function startEdgeScrollForDrag() {
    const scroller = scrollableAncestor(pressTarget)
    const draggableId = activationDraggableId
    if (!scroller || draggableId === null)
      return
    autoScrolled = 0
    autoScrollDraggableId = draggableId
    addTransformer('draggables', draggableId, {
      id: AUTO_SCROLL_TRANSFORMER_ID,
      // After the sensor's own transformer, so it corrects the pointer delta
      // rather than being corrected by it.
      order: 1,
      callback: transform => ({ x: transform.x, y: transform.y + autoScrolled }),
    })
    edgeScroller = startEdgeScroll(scroller, (applied) => {
      autoScrolled += applied
      recomputeLayouts()
    })
  }

  // Removes ONLY what `startEdgeScrollForDrag` added. `detach` runs for every
  // press, including the ones that never activate a drag, so removing by
  // `activationDraggableId` would ask the context to drop a transformer that no
  // press ever registered.
  function stopEdgeScroll() {
    edgeScroller?.stop()
    edgeScroller = undefined
    autoScrolled = 0
    if (autoScrollDraggableId === null)
      return
    removeTransformer('draggables', autoScrollDraggableId, AUTO_SCROLL_TRANSFORMER_ID)
    autoScrollDraggableId = null
  }

  function onActivate() {
    if (!state.active.sensor) {
      sensorStart(SENSOR_ID, initialCoordinates)
      dragStart(activationDraggableId!)
      startEdgeScrollForDrag()
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
      edgeScroller?.track(coordinates.y)
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
