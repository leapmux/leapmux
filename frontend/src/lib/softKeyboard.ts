import { isContentEditableElement } from './textInputBehavior'

/**
 * The soft-keyboard facts, shared by the layout that reacts to the keyboard
 * (`~/hooks/useVisualViewportInset`) and the controls that dismiss it.
 *
 * One definition of "the on-screen keyboard comes up for this", because the
 * layout deciding the keyboard is up and the composer deciding to release it
 * must never disagree about which elements count.
 */

/**
 * Whether the on-screen keyboard comes up for this element.
 *
 * Not `isTypingElement`, which also counts a SELECT. That one asks whether the
 * user is typing, so a SELECT belongs in it and not in this set.
 */
export function isSoftKeyboardTarget(el: Element | null): boolean {
  if (!el)
    return false
  const tag = el.tagName
  if (tag === 'INPUT' || tag === 'TEXTAREA')
    return true
  // The property half of `isContentEditableElement` is browser-only: jsdom
  // implements neither `isContentEditable` nor `contentEditable`, so it
  // answers false there for every editable element. The attribute half is
  // what makes this branch reachable under the unit tests, and a browser
  // needs the property half for an element that INHERITS editing from an
  // editable ancestor. Both halves are load-bearing; neither one alone is.
  return isContentEditableElement(el)
}

/**
 * Whether an on-screen keyboard is currently covering part of the screen.
 *
 * MEASURED, never inferred from the device. `(pointer: coarse)` says a finger
 * is the primary pointer, which is NOT the same question: a phone or tablet
 * paired with a hardware keyboard reports a coarse pointer and shows no
 * on-screen keyboard at all, and releasing focus there would take the caret
 * away mid-flow and reclaim nothing. Only a viewport that actually lost height
 * proves a keyboard is in the way.
 *
 * `useVisualViewportInset` owns the measurement and publishes it here, because
 * it is the one place that holds the no-keyboard baseline every shrink is
 * measured against. A module-level fact rather than a prop, for the same
 * reason the viewport's size and place go out as CSS custom properties: there
 * is one viewport, and the composer is many layers below the shell that
 * watches it.
 */
let softKeyboardVisible = false

/** Publish the measured keyboard state. Called only by `useVisualViewportInset`. */
export function setSoftKeyboardVisible(visible: boolean): void {
  softKeyboardVisible = visible
}

/** Whether an on-screen keyboard is currently taking screen space. */
export function isSoftKeyboardVisible(): boolean {
  return softKeyboardVisible
}

/**
 * Put the on-screen keyboard away by releasing whatever editable holds focus.
 *
 * A no-op unless a keyboard is actually covering the screen — with a hardware
 * keyboard, or on a desktop, the caret stays where the user left it. Also a
 * no-op when focus sits anywhere else, so it cannot steal focus from a control
 * the user just moved to.
 */
export function dismissSoftKeyboard(): void {
  if (!isSoftKeyboardVisible())
    return
  const active = document.activeElement
  if (isSoftKeyboardTarget(active))
    (active as HTMLElement).blur()
}
