import { createEffect, onCleanup } from 'solid-js'

/**
 * A solid-dnd `dragActivators` props object, as returned by the `dragActivators`
 * property of `createDraggable` / `createSortable` (`{ onPointerdown: ... }`).
 */
export type DragActivatorProps = Record<string, (event: PointerEvent) => void>

/**
 * Wrap a draggable's `dragActivators` so its handlers ignore touch presses.
 *
 * Touch drags start ONLY from dedicated drag handles (`data-drag-handle`,
 * `touch-action: none`), which carry the raw activators. Keeping touch off the
 * row bodies is what lets the stock upstream pointer sensor stay safe on touch:
 * a finger that swipes a row pans the list (the press never reaches the sensor,
 * so nothing leaks when the browser claims the pointer), and a finger that
 * holds a row opens the 500ms context menu without the sensor's own 250ms hold
 * timer ghost-starting a drag under it.
 *
 * Mouse presses pass through untouched, and a pen counts as a fine pointer like
 * a mouse — it points precisely, hovers, and usually has a barrel button, so it
 * drags rows the way a mouse does.
 */
export function finePointerOnlyActivators(activators: DragActivatorProps): DragActivatorProps {
  const guarded: DragActivatorProps = {}
  for (const [eventName, handler] of Object.entries(activators)) {
    guarded[eventName] = (event: PointerEvent) => {
      if (event.pointerType === 'touch')
        return
      handler(event)
    }
  }
  return guarded
}

/** `onPointerdown` → `pointerdown`, the DOM event name a handler key names. */
function eventNameOf(handlerKey: string): string {
  return handlerKey.replace(/^on/, '').replace(/^([A-Z])/, lead => lead.toLowerCase())
}

/**
 * Attach a draggable's `dragActivators` to an element, reactively.
 *
 * The handlers a `dragActivators` getter returns only list sensors that are
 * registered AT THE MOMENT OF THE CALL, and sensors register in a mount effect
 * after the first render — so spreading the props over JSX (a one-shot capture
 * at element creation) can bind dead, sensor-less handlers on rows that mount
 * in the same tick as the provider. Solid-dnd's own call-form ref solves this
 * with an effect keyed on the sensor store; this does the same for places that
 * register the node via the plain `.ref` (no activators) and put activation on
 * a subset of the row.
 *
 * `touch: 'block'` routes the handlers through
 * {@link finePointerOnlyActivators} — for row bodies, where a touch press must
 * not start a drag. `touch: 'allow'` attaches them raw — for drag handles.
 *
 * Must run under an owner (a component body or a keyed row); the effect and
 * its cleanup belong to it.
 */
export function attachDragActivators(
  elAccessor: () => HTMLElement | undefined,
  activators: () => DragActivatorProps | undefined,
  opts: { touch: 'allow' | 'block' },
): void {
  createEffect(() => {
    const el = elAccessor()
    if (!el)
      return
    const raw = activators() ?? {}
    const handlers = opts.touch === 'block' ? finePointerOnlyActivators(raw) : raw
    const bound: Array<[string, EventListener]> = []
    for (const [handlerKey, handler] of Object.entries(handlers)) {
      const eventName = eventNameOf(handlerKey)
      const listener: EventListener = (event) => {
        handler(event as PointerEvent)
      }
      el.addEventListener(eventName, listener)
      bound.push([eventName, listener])
    }
    onCleanup(() => {
      for (const [eventName, listener] of bound)
        el.removeEventListener(eventName, listener)
    })
  })
}
