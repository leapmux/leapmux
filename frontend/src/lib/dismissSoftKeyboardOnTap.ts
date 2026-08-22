import { PRESS_SLOP_PX } from '~/components/common/contextMenuGesture'
import { motion } from '~/styles/tokens'
import { monotonicNow } from './monotonicNow'
import { dismissSoftKeyboard, isSoftKeyboardTarget, isSoftKeyboardVisible } from './softKeyboard'
import { INPUT_OR_EDITABLE_SELECTOR } from './textInputBehavior'

/**
 * A TAP outside an editor puts the on-screen keyboard away.
 *
 * The keyboard covers half the screen, and reaching for a file, a terminal
 * canvas, or the transcript is the user saying they want to read that surface.
 * WebKit does not blur an editing host for a tap on a plain region, and
 * nothing in this app's path can be blamed for that -- gesture listeners are
 * passive and call no `preventDefault`. So the app states the behaviour
 * itself.
 *
 * MEASURED FROM POINTER EVENTS, not from a `click` handler, which took two
 * taps to fire. Solid delegates `click` to the document, so a container
 * carries no listener of its own, and WebKit's "is this element clickable"
 * heuristic then treats the first tap as a hover and withholds the click
 * until the second. Pointer events have no such rule and fire on the first
 * tap.
 *
 * A tap is short and still. The movement bound is the same slop the
 * long-press on chat rows uses, so a press that cancels the hold cannot still
 * count as a keyboard-dismiss tap. The duration bound keeps a long press
 * out: that gesture belongs to text selection and the quote popover, and the
 * composer it would quote INTO must stay in view.
 *
 * Document-level so every surface shares one recognizer. `dismissSoftKeyboard`
 * is already a no-op when no keyboard is covering the screen, so a desktop
 * click keeps the caret where it was.
 *
 * A surface that focuses an editor on the same tap must not undo the dismiss.
 * xterm focuses its helper textarea from a compatibility `mousedown`, which
 * on touch fires AFTER `pointerup`. Without a swallow, a tap on the terminal
 * hides the keyboard and immediately shows it again. The swallow lasts until
 * the next primary `pointerdown`, so the next tap on that surface can take
 * focus and show the keyboard — a toggle, not a flicker.
 */

/**
 * Presses that belong to a control or an editor, not to "tap the empty
 * surface".
 *
 * `INPUT_OR_EDITABLE_SELECTOR` is the text-entry group. Tapping inside one of
 * those elements IS typing -- the composer's ProseMirror host, file search,
 * xterm's helper textarea -- and blurring them would steal the caret the tap
 * just claimed.
 *
 * The rest are this gesture's own: a link, a button, a popover, a select.
 * Blurring before their click runs would jump the layout under the finger
 * and miss the action. Do NOT push those into the shared fragment: the other
 * pointer guards each have a different extra set, and a merge would change
 * what they decline.
 */
const OWNED_PRESS_SELECTOR = `a, button, [role="button"], [popover], select, ${INPUT_OR_EDITABLE_SELECTOR}`

function eventElement(event: Event): Element | null {
  const target = event.target
  if (target instanceof Element)
    return target
  return target instanceof Node ? target.parentElement : null
}

/**
 * Listen for a short still tap and release the focused editor.
 *
 * Returns the disposer. The default root is `document`, which is what the
 * shell attaches; tests pass a smaller root. A dismiss also swallows editor
 * focus until the next primary press, so a surface that focuses on the
 * same tap (the terminal) cannot bring the keyboard straight back.
 */
export function attachDismissSoftKeyboardOnTap(root: EventTarget = document): () => void {
  let tapStart: { pointerId: number, x: number, y: number, at: number } | null = null
  // Set on a dismiss tap; cleared on the next primary press. While it is
  // set, a focus that the same tap synthesizes (xterm's `mousedown` after
  // `pointerup`) is released so the keyboard stays down.
  let swallowEditorFocus = false

  const onPointerDown = (event: Event) => {
    if (!(event instanceof PointerEvent) || !event.isPrimary)
      return
    swallowEditorFocus = false
    tapStart = { pointerId: event.pointerId, x: event.clientX, y: event.clientY, at: monotonicNow() }
  }

  const onPointerUp = (event: Event) => {
    const start = tapStart
    tapStart = null
    if (!(event instanceof PointerEvent) || !start || event.pointerId !== start.pointerId)
      return
    const movedFar = Math.hypot(event.clientX - start.x, event.clientY - start.y) > PRESS_SLOP_PX
    if (movedFar || monotonicNow() - start.at >= motion.longPress)
      return
    const el = eventElement(event)
    if (el?.closest(OWNED_PRESS_SELECTOR))
      return
    // Keyboard already down: do not swallow. The terminal's own mousedown
    // then focuses its helper and the keyboard comes up — the other half
    // of the toggle.
    if (!isSoftKeyboardVisible())
      return
    dismissSoftKeyboard()
    swallowEditorFocus = true
  }

  const onPointerCancel = (event: Event) => {
    if (!(event instanceof PointerEvent))
      return
    if (tapStart && event.pointerId === tapStart.pointerId)
      tapStart = null
  }

  const onFocusIn = (event: Event) => {
    if (!swallowEditorFocus)
      return
    const el = eventElement(event)
    if (!(el instanceof HTMLElement) || !isSoftKeyboardTarget(el))
      return
    el.blur()
  }

  root.addEventListener('pointerdown', onPointerDown)
  root.addEventListener('pointerup', onPointerUp)
  root.addEventListener('pointercancel', onPointerCancel)
  root.addEventListener('focusin', onFocusIn, true)
  return () => {
    root.removeEventListener('pointerdown', onPointerDown)
    root.removeEventListener('pointerup', onPointerUp)
    root.removeEventListener('pointercancel', onPointerCancel)
    root.removeEventListener('focusin', onFocusIn, true)
  }
}
