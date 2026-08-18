import { createEffect, onCleanup } from 'solid-js'
import { INPUT_OR_EDITABLE_SELECTOR } from '~/lib/textInputBehavior'

/**
 * A solid-dnd `dragActivators` props object, as returned by the `dragActivators`
 * property of `createDraggable` / `createSortable` (`{ onPointerdown: ... }`).
 */
export type DragActivatorProps = Record<string, (event: PointerEvent) => void>

/**
 * Presses that belong to the row's embedded UI, not to the row body: inline
 * inputs and their native selection gestures, controls such as the close
 * button, an open menu popover, and the drag grip (which forwards its own
 * press through its raw activators — a second activation from the bubbled
 * press would race the first).
 *
 * `INPUT_OR_EDITABLE_SELECTOR` carries the text-entry group that all three
 * pointer guards share, and `[popover]` stands in the list itself, as it does
 * in the other two. The last three entries are this guard's own and make it
 * the widest: `select` and `button` keep a slow or drifting press on a row
 * control a click, and `[data-drag-handle]` keeps one grip press to one
 * activation.
 *
 * Do NOT push those three into the shared fragment or into the other two
 * lists. ~/components/shell/guardedPointerSensor.ts would then decline the
 * grip's own press and break touch reorder, and
 * ~/components/common/contextMenuGesture.ts would stop opening a row's menu
 * on a long press over that row's buttons. This guard runs BEFORE the sensor
 * on a row body, so the two lists already compose where the wider one is
 * wanted.
 */
const EMBEDDED_UI_SELECTOR = `${INPUT_OR_EDITABLE_SELECTOR}, [popover], select, button, [data-drag-handle]`

/**
 * Wrap a draggable's `dragActivators` for a ROW BODY: the press must start on
 * the row itself, with a fine pointer.
 *
 * Touch never passes — touch drags start ONLY from dedicated drag handles
 * (`data-drag-handle`, `touch-action: none`), which carry the raw activators.
 * Keeping touch off the row bodies is what lets a finger that swipes a row
 * pan the list (the press never reaches the sensor, so nothing leaks when the
 * browser claims the pointer) and lets a finger that holds a row open the
 * 500ms context menu undisturbed.
 *
 * A fine pointer passes only when the press starts on the row itself. A press
 * on an embedded control (`EMBEDDED_UI_SELECTOR`) belongs to that control: a
 * text-selection sweep in the rename input must not drag the row, and a slow
 * or drifting press on the close button must stay a click. A press on the
 * grip passes only through the grip's own raw activators, so one press
 * activates the sensor exactly once.
 *
 * A pen counts as a fine pointer like a mouse — it points precisely, hovers,
 * and usually has a barrel button, so it drags rows the way a mouse does.
 */
export function rowBodyActivators(activators: DragActivatorProps): DragActivatorProps {
  const guarded: DragActivatorProps = {}
  for (const [eventName, handler] of Object.entries(activators)) {
    guarded[eventName] = (event: PointerEvent) => {
      if (event.pointerType === 'touch')
        return
      const target = event.target as Element | null
      if (target?.closest?.(EMBEDDED_UI_SELECTOR))
        return
      handler(event)
    }
  }
  return guarded
}

/** `onPointerdown` → `pointerdown`, the DOM event name a handler key specifies. */
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
 * {@link rowBodyActivators} — for row bodies, where a touch press must
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
    const handlers = opts.touch === 'block' ? rowBodyActivators(raw) : raw
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
