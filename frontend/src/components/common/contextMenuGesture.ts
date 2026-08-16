import type { PopoverAnchorRect } from '~/lib/popoverPosition'
import { motion } from '~/styles/tokens'
import './contextMenuGesture.css'

/** Where a right-click or a long press landed, in viewport coordinates. */
export interface ContextMenuPress {
  clientX: number
  clientY: number
}

/**
 * The rect a context menu opens against: a zero-height band at the press point,
 * so the menu's top-left corner sits exactly there.
 *
 * One rule for both inputs, with no finger-clearance offset for touch, because a
 * long press opens its menu on RELEASE (see `scheduleOpen`) -- the finger has left
 * the glass by the time there is anything to cover. Anchoring to the pressed row
 * instead would put the menu at that row's bottom edge, which is nowhere near the
 * pointer on a tall row such as a chat message.
 *
 * `calcPopoverPosition` still flips the menu above the point when it would be
 * clipped at the foot of the viewport, which is what a context menu should do.
 */
export function pressAnchorRect(press: ContextMenuPress): PopoverAnchorRect {
  return { top: press.clientY, bottom: press.clientY, left: press.clientX }
}

/**
 * Pointer travel that abandons a hold, in CSS pixels.
 *
 * Inside iOS's 10-point `allowableMovement` and Android's 8dp touch slop, so
 * an unsteady finger that the platform would still call a long press is one
 * here too. (Touch drags start only from dedicated drag handles now, so a
 * finger drifting on a row body races no drag activation distance anymore —
 * see ~/lib/dragActivators.ts.)
 */
export const PRESS_SLOP_PX = 8

/** Row movement, in CSS pixels, past which a scroll counts as moving the row. */
const SCROLL_MOVE_PX = 1

/**
 * Marks an armed row. An ATTRIBUTE, never a class: the rows that mount this
 * gesture assign their own `class` reactively, and that assignment replaces the
 * whole class list -- a class added here once at attach would be silently
 * deleted the next time the row's class string re-ran (a tab becoming active, a
 * chat row reclassifying mid-stream). Nothing rewrites another attribute.
 * ~/components/common/contextMenuGesture.css.ts keys every indicator rule on it.
 */
const GESTURE_ATTR = 'data-ctx-menu'

/** Marks a row whose hold is in flight. The CSS ramps the tint off it. */
const PRESS_HOLD_ATTR = 'data-press-hold'

/**
 * True for the single task between a fired hold's release and its menu opening.
 *
 * The release must stay visible to document-level listeners -- the drag sensor
 * detaches on it, and the chat scroller drops the pointer from its input set --
 * so it is NOT stopped. `Tooltip`'s touch handler reads this flag and declines
 * to present, which is the one reaction a stop used to prevent: the tooltip is
 * a `popover="manual"` that enters the top layer a frame AFTER the menu, so an
 * unsuppressed one would stack above the menu. See `showOnTouch` in
 * ~/components/common/Tooltip.tsx.
 */
let releaseOpensMenu = false

/** Whether `pointerup` is the release of a long press whose menu is about to open. */
export function touchReleaseOpensMenu(): boolean {
  return releaseOpensMenu
}

/**
 * Presses this module must leave alone: the inline rename inputs inside these
 * rows (a long press there must keep the native selection handles and paste
 * callout), and any popover -- on the `contextMenuFor` surfaces the menu itself
 * is a DOM child of the row, so a press on a menu item belongs to the menu.
 * Arming on one would swallow the item's click after a decision hold, and a
 * drag would start from under the open menu.
 */
function pressBelongsToEmbeddedUi(e: PointerEvent | MouseEvent): boolean {
  const target = e.target as Element | null
  return Boolean(target?.closest?.('input, textarea, [contenteditable="true"], [popover]'))
}

export interface ContextMenuGestureOptions {
  /**
   * Open the menu at `press`. Called at most once per gesture, and always from a
   * task of its own -- never synchronously inside the pointer event that completed
   * the gesture. See `scheduleOpen` for why that matters.
   */
  onOpen: (press: ContextMenuPress) => void
  /**
   * Keep the element's text selectable. Only the iOS callout is suppressed, and
   * then only on a coarse pointer; a right-click inside an existing selection
   * yields to the native menu so its Copy still works. The chat message rows set
   * this; every other row owns its long press outright.
   */
  selectableText?: boolean
  /** Hold duration in ms. Defaults to `motion.longPress`. */
  holdMs?: number
  /** Movement cancel threshold in CSS pixels. Defaults to `PRESS_SLOP_PX`. */
  slopPx?: number
}

/**
 * Give `el` the two context-menu inputs that a hover-revealed kebab does not have:
 * a touch long press and a secondary-button click. Returns the detach function.
 *
 * The module attaches real listeners to an element rather than returning a bag of
 * JSX props for two reasons. It needs the CAPTURE phase for the `click` it
 * swallows -- the row must not also select, and Solid delegates `onClick` to
 * `document`, which a bubble-phase stop never beats. And it keeps the open state
 * inside `DropdownMenu`, which already owns `showPopover()` and the anchor,
 * instead of threading four wires through every row.
 */
export function attachContextMenuGesture(
  el: HTMLElement,
  opts: ContextMenuGestureOptions,
): () => void {
  const holdMs = opts.holdMs ?? motion.longPress
  const slopPx = opts.slopPx ?? PRESS_SLOP_PX

  /** The touch being timed. `null` means no hold is in flight. */
  let pointerId: number | null = null
  let startX = 0
  let startY = 0
  /** The row's position at pointerdown, to tell a scroll that moves it from one that does not. */
  let startTop = 0
  let startLeft = 0
  let holdTimer: ReturnType<typeof setTimeout> | undefined
  let openTimer: ReturnType<typeof setTimeout> | undefined
  /** Where this gesture will anchor, captured when it decided to open. */
  let firedAt: ContextMenuPress = { clientX: 0, clientY: 0 }
  /** This gesture decided to open a menu. */
  let fired = false
  /** The next `click` belongs to the gesture, not to the row. */
  let swallowClick = false
  /** Removes the release listeners armed by `openAfterRelease`. A no-op when none are. */
  let disarmRelease: () => void = () => {}

  el.setAttribute(GESTURE_ATTR, opts.selectableText ? 'selectable' : 'owned')

  /**
   * Cancel a hold when a scroll moves the pressed row.
   *
   * Blink fires `pointercancel` the moment it claims a touch for a pan, which is
   * the primary path; this is the fallback for engines that pan without one.
   * Armed only while a hold is in flight, so the module never holds a standing
   * document listener.
   *
   * The rect comparison is the whole test. The app scrolls itself constantly
   * (the chat list sticks to the bottom while the agent streams), and a scroll
   * ANYWHERE fires an event here; cancelling on it would make a long press
   * impossible while anything streams. Only a scroll that actually moved this
   * row -- an ancestor pan, a programmatic scrollIntoView -- ends the gesture.
   */
  function onDocumentScroll() {
    const rect = el.getBoundingClientRect()
    if (Math.abs(rect.top - startTop) <= SCROLL_MOVE_PX && Math.abs(rect.left - startLeft) <= SCROLL_MOVE_PX)
      return
    // The row moved under the finger, fired or not. The user is scrolling past
    // it; no `click` will follow to be swallowed. Mirrors `onPointerCancel`.
    fired = false
    swallowClick = false
    endGesture()
  }

  const armScrollCancel = () => {
    document.addEventListener('scroll', onDocumentScroll, { capture: true, passive: true })
  }

  const disarmScrollCancel = () => {
    document.removeEventListener('scroll', onDocumentScroll, { capture: true })
  }

  /**
   * End the gesture's pointer phase. Leaves `swallowClick` and `openTimer` alone -- both outlive it.
   *
   * A function declaration, not an arrow constant: `onDocumentScroll` above and
   * the pointer handlers below both end the gesture, and only hoisting lets a
   * definition sit after one caller and before the other without a forward
   * reference.
   */
  function endGesture() {
    if (pointerId === null && holdTimer === undefined)
      return
    pointerId = null
    clearTimeout(holdTimer)
    holdTimer = undefined
    el.removeAttribute(PRESS_HOLD_ATTR)
    disarmScrollCancel()
  }

  /** Abandon a hold that has not fired. A fired hold is finished by `endGesture` on release. */
  function cancelHold() {
    if (fired)
      return
    endGesture()
  }

  /**
   * Decide to open. Records the anchor x, but does NOT open -- `scheduleOpen` does
   * that after the pointer phase ends.
   *
   * Only a touch hold claims the click that follows it. A secondary-button press
   * produces no `click` at all (the spec dispatches `auxclick` instead), and the
   * keyboard path has no pointer events, so claiming there would leave a standing
   * flag that swallows the row's next legitimate left-click instead.
   */
  const fire = (press: ContextMenuPress, claimsClick: boolean) => {
    if (fired)
      return
    fired = true
    swallowClick = claimsClick
    firedAt = press
    clearTimeout(holdTimer)
    holdTimer = undefined
  }

  /**
   * Open in a task of its own.
   *
   * The HTML light-dismiss algorithm records the popover ancestor of the
   * `pointerdown` target and, on `pointerup`, hides every popover that is not in
   * that chain. A `popover="auto"` shown while the finger is still down on a row --
   * which has no popover ancestor -- is therefore hidden by the very release that
   * ends the gesture. Right-click has the same shape, because `contextmenu` fires
   * on button-down on macOS and Linux.
   *
   * So every caller reaches this only once the pointer is already up: the touch
   * path from `pointerup`, the still-held mouse path from `openAfterRelease`, and
   * the Windows / keyboard path straight from `contextmenu`, where nothing is
   * held. The `setTimeout` then puts the open one task beyond the release, after
   * the browser's light-dismiss pass for it has run.
   *
   * Two things fall out of that: the release can never land on a menu item,
   * because the menu does not exist yet, and the press indicator still completes
   * at `holdMs`, so a touch user gets the "ready" signal on time and the menu
   * follows on lift.
   */
  const scheduleOpen = () => {
    clearTimeout(openTimer)
    openTimer = setTimeout(() => {
      openTimer = undefined
      releaseOpensMenu = false
      const press = firedAt
      fired = false
      opts.onOpen(press)
    }, 0)
  }

  /**
   * Open once the currently-held secondary button comes back up.
   *
   * macOS and Linux dispatch `contextmenu` on button PRESS, so at that moment the
   * gesture is only half over and the release is still to come -- and that release
   * is exactly what light-dismiss acts on. Opening from the `contextmenu` event
   * itself produced a menu that appeared and then vanished the instant the user
   * let go, staying up only while they kept holding the button.
   *
   * Windows dispatches `contextmenu` on RELEASE instead, and the keyboard menu key
   * dispatches it with no buttons at all; both report `buttons === 0` and take the
   * immediate path, so this listener is never armed for them.
   *
   * Only the held button's own release completes the gesture. Another pointer's
   * release -- a touch tap elsewhere while the button is down -- still reports
   * `buttons` with the secondary bit set, and must not open the menu early: the
   * button's own release would light-dismiss it a moment later. A touch
   * `pointercancel` is another finger's pan and is ignored the same way.
   */
  const openAfterRelease = () => {
    disarmRelease()

    const onRelease = (e: Event) => {
      const pointer = e as PointerEvent
      if (e.type === 'pointerup') {
        if (pointer.button === 2 || pointer.buttons === 0) {
          disarmRelease()
          scheduleOpen()
        }
        return
      }
      // `pointercancel`: the held pointer's stream was cancelled outright (rare
      // for a mouse; pens lose it to palm rejection). No release follows, so
      // there is no menu to open.
      if (pointer.pointerType !== 'touch') {
        disarmRelease()
        fired = false
      }
    }

    document.addEventListener('pointerup', onRelease, true)
    document.addEventListener('pointercancel', onRelease, true)
    disarmRelease = () => {
      document.removeEventListener('pointerup', onRelease, true)
      document.removeEventListener('pointercancel', onRelease, true)
      disarmRelease = () => {}
    }
  }

  const onPointerDown = (e: PointerEvent) => {
    // Any new press ends the previous gesture's claim on the click that follows
    // it. That click has had its chance.
    swallowClick = false

    // A mouse never starts a hold. It has `contextmenu`, and a mouse hold must not
    // steal click-drag, text selection, or the row's own press states.
    if (e.pointerType !== 'touch' || !e.isPrimary || e.button !== 0)
      return
    if (pressBelongsToEmbeddedUi(e))
      return
    // A fresh tracked press supersedes a hold that already fired and is waiting
    // for its release: that release no longer matches `pointerId`.
    fired = false

    pointerId = e.pointerId
    startX = e.clientX
    startY = e.clientY
    const rect = el.getBoundingClientRect()
    startTop = rect.top
    startLeft = rect.left
    el.setAttribute(PRESS_HOLD_ATTR, '')
    armScrollCancel()
    holdTimer = setTimeout(() => {
      holdTimer = undefined
      // The press point, not wherever the finger drifted to inside the slop.
      fire({ clientX: startX, clientY: startY }, true)
    }, holdMs)
  }

  const onPointerMove = (e: PointerEvent) => {
    if (pointerId === null || e.pointerId !== pointerId)
      return
    if (Math.hypot(e.clientX - startX, e.clientY - startY) > slopPx)
      cancelHold()
  }

  const onPointerCancel = (e: PointerEvent) => {
    if (pointerId === null || e.pointerId !== pointerId)
      return
    // The browser took the touch for a pan. Drop the hold even if it fired -- the
    // user is scrolling, and no `click` will follow to be swallowed.
    fired = false
    swallowClick = false
    endGesture()
  }

  const onPointerUp = (e: PointerEvent) => {
    if (pointerId === null || e.pointerId !== pointerId)
      return
    if (fired) {
      // The release keeps propagating: the drag sensor detaches on it, and the
      // chat scroller drops the pointer from its input set. The one consumer
      // that must NOT react -- `Tooltip`, which would present over the menu --
      // checks `touchReleaseOpensMenu` instead.
      releaseOpensMenu = true
      scheduleOpen()
    }
    endGesture()
  }

  const onClickCapture = (e: MouseEvent) => {
    if (!swallowClick)
      return
    swallowClick = false
    // Solid delegates `onClick` to `document`, so stopping here is what keeps the
    // row from also selecting, expanding, or opening a tab.
    e.stopPropagation()
    e.preventDefault()
  }

  const onContextMenu = (e: MouseEvent) => {
    if (pressBelongsToEmbeddedUi(e))
      return
    if (opts.selectableText && clickIsInsideSelection(e)) {
      // Leave the native menu alone so its Copy still works over selected text.
      return
    }
    e.preventDefault()

    if (fired) {
      // Android Chrome synthesizes a `contextmenu` at its own long-press threshold.
      // The hold timer already claimed this gesture, so drop the duplicate.
      return
    }
    if (pointerId !== null) {
      // A `contextmenu` during a touch hold IS the long press. Where the platform
      // has an opinion about timing, it wins. The trailing click is this touch's,
      // so claim it; `pointerup` schedules the open.
      fire({ clientX: startX, clientY: startY }, true)
      return
    }
    // A mouse right-click, or the keyboard Menu key / Shift+F10 on a focused row.
    // The keyboard path reports no position at all, which reads as both
    // coordinates zero; a real pointer at the viewport's exact top-left pixel
    // matches that too, but nothing else does. Fall back to the row's own edges:
    // the menu opens below the row, left-aligned with it.
    const rect = el.getBoundingClientRect()
    const hasPosition = e.clientX !== 0 || e.clientY !== 0
    fire({
      clientX: hasPosition ? e.clientX : rect.left,
      clientY: hasPosition ? e.clientY : rect.bottom,
    }, false)
    if (e.buttons === 0)
      scheduleOpen()
    else
      openAfterRelease()
  }

  el.addEventListener('pointerdown', onPointerDown)
  el.addEventListener('pointermove', onPointerMove, { passive: true })
  el.addEventListener('pointercancel', onPointerCancel)
  el.addEventListener('pointerup', onPointerUp)
  el.addEventListener('click', onClickCapture, { capture: true })
  el.addEventListener('contextmenu', onContextMenu)

  return () => {
    el.removeEventListener('pointerdown', onPointerDown)
    el.removeEventListener('pointermove', onPointerMove)
    el.removeEventListener('pointercancel', onPointerCancel)
    el.removeEventListener('pointerup', onPointerUp)
    el.removeEventListener('click', onClickCapture, { capture: true })
    el.removeEventListener('contextmenu', onContextMenu)
    clearTimeout(holdTimer)
    clearTimeout(openTimer)
    holdTimer = undefined
    openTimer = undefined
    pointerId = null
    fired = false
    swallowClick = false
    releaseOpensMenu = false
    disarmRelease()
    disarmScrollCancel()
    el.removeAttribute(PRESS_HOLD_ATTR)
    el.removeAttribute(GESTURE_ATTR)
  }
}

/**
 * Whether a right-click landed on live selected text.
 *
 * The test is the click POINT against the selection's rects, not the click target
 * against the selected nodes. `Selection.containsNode` answers the wrong question
 * here: a row that holds the selection is not itself contained by it, so a
 * right-click over a highlighted word inside that row would read as "outside" and
 * lose the native Copy. Rects also give the correct answer for the reverse case --
 * a click on the row's padding, next to the highlight, opens the app menu.
 */
function clickIsInsideSelection(e: MouseEvent): boolean {
  const selection = window.getSelection()
  if (!selection || selection.isCollapsed || selection.rangeCount === 0)
    return false
  for (let i = 0; i < selection.rangeCount; i++) {
    // jsdom does no layout and does not implement `Range.getClientRects` at all, so
    // this stays optional. There, and anywhere else without geometry, the predicate
    // reports "not on the selection" and the app menu opens -- the same answer a
    // real browser gives for an empty rect list.
    const rects = selection.getRangeAt(i).getClientRects?.()
    if (!rects)
      continue
    for (let r = 0; r < rects.length; r++) {
      const rect = rects[r]
      if (e.clientX >= rect.left && e.clientX <= rect.right
        && e.clientY >= rect.top && e.clientY <= rect.bottom) {
        return true
      }
    }
  }
  return false
}
