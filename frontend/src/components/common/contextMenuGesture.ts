import type { PopoverAnchorRect } from '~/lib/popoverPosition'
import { INPUT_OR_EDITABLE_SELECTOR } from '~/lib/textInputBehavior'
import { pointIsInsideSelection, selectionInside } from '~/lib/textSelection'
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
 * One rule for both inputs, and no finger-clearance offset for touch: the menu
 * grows down and to the right of the press point, so the fingertip covers a
 * corner of it for the moment before the lift and nothing after. Anchoring to
 * the pressed row instead would put the menu at that row's bottom edge, which is
 * nowhere near the pointer on a tall row such as a chat message.
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
 * How long the menu stays inert after the release while no click arrives.
 *
 * Long enough for the click a touch release synthesizes, which lands within a
 * frame or two of `pointerup` on a viewport that declares its width (the 300ms
 * wait is for pages that do not). Short enough that a release producing NO
 * click -- a cancelled touch, an engine that suppresses it after a long press
 * -- leaves the menu usable almost at once.
 */
export const CLICK_AFTER_RELEASE_GRACE_MS = 400

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
 * Marks the document while a fired hold's finger is STILL DOWN over the menu it
 * just opened.
 *
 * The menu now appears under that finger, and a held touch carries a hover
 * state with it: `[role^="menuitem"]:is(:hover, :focus)` paints the accent, so
 * an item sat there looking chosen before the user had lifted or decided
 * anything. `~/components/common/contextMenuGesture.css.ts` takes hit-testing
 * away from open popovers while this is set, which removes the hover with it.
 *
 * On the DOCUMENT, not the row: the menu is in the top layer, nowhere near the
 * row in the DOM, and this window is a property of the gesture rather than of
 * any one menu.
 */
const HOLD_OVER_MENU_ATTR = 'data-ctx-hold-over-menu'

/**
 * True for the single task between a fired hold's release and its menu being
 * re-asserted past that release.
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
 * Whether a long press is holding a finger over the menu it just opened.
 *
 * A menu opened in this window has to open as `popover="manual"`: the release
 * still to come is what the HTML light-dismiss pass acts on, and it hides every
 * `auto` popover whose chain excludes it. `DropdownMenu` asks this at show time
 * rather than taking the answer as a prop, because the two ways it opens -- the
 * gesture it attaches itself, and a controlled `open` that a singleton host
 * drives (the chat list's shared message menu) -- both need it and neither one
 * knows about the finger.
 */
export function holdIsOverMenu(): boolean {
  return typeof document !== 'undefined'
    && document.documentElement.hasAttribute(HOLD_OVER_MENU_ATTR)
}

/**
 * Presses this module must leave alone: the inline rename inputs inside these
 * rows (a long press there must keep the native selection handles and paste
 * callout), and any popover -- on the `contextMenuFor` surfaces the menu itself
 * is a DOM child of the row, so a press on a menu item belongs to the menu.
 * Arming on one would swallow the item's click after a decision hold, and a
 * drag would start from under the open menu.
 *
 * `INPUT_OR_EDITABLE_SELECTOR` carries the text-entry group that all three
 * pointer guards share, and `[popover]` stands in the list itself. This
 * gesture adds nothing else. ~/lib/dragActivators.ts and
 * ~/components/shell/guardedPointerSensor.ts compose the same fragment into a
 * constant of the same name.
 *
 * Do NOT merge the three lists into one. The drag activators decline `select`,
 * `button` and `[data-drag-handle]`, which are exclusions a DRAG needs. This
 * gesture must still act on a `button`: a long press anywhere on a row opens
 * that row's menu, and a failed chat row renders its recovery actions as
 * visible buttons -- see `attachRowMenu` in ~/components/chat/MessageBubble.tsx.
 * Touch has no hover toolbar to fall back on, so a decline over the row's own
 * controls loses the menu on that area outright.
 */
const EMBEDDED_UI_SELECTOR = `${INPUT_OR_EDITABLE_SELECTOR}, [popover]`

function pressBelongsToEmbeddedUi(e: PointerEvent | MouseEvent): boolean {
  const target = e.target as Element | null
  return Boolean(target?.closest?.(EMBEDDED_UI_SELECTOR))
}

export interface ContextMenuGestureOptions {
  /**
   * Open the menu at `press`, or re-point an already-open one at it.
   *
   * Called once per gesture, always from a task of its own -- never
   * synchronously inside the pointer event that decided it. See `scheduleOpen`.
   */
  onOpen: (press: ContextMenuPress) => void
  /**
   * Hide a menu this hold already opened. A pan after the hold fires
   * `pointercancel` and never delivers a release for light-dismiss to act on,
   * so the menu would otherwise stay at the original viewport point.
   */
  onCancel?: () => void
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
  /** The hold completed and its menu is up, so the release has one to re-assert. */
  let heldOpen = false
  /**
   * This gesture is the one holding a finger over an open menu.
   *
   * The marker it guards lives on the document, so without this a second row's
   * detach -- or a second finger's release -- would clear a marker it never set
   * and hand the menu back to a finger that is still pressing.
   */
  let markedHoldOverMenu = false
  /** The backstop that ends the inert window when no click follows the release. */
  let holdOverMenuTimer: ReturnType<typeof setTimeout> | undefined
  /** The next `click` belongs to the gesture, not to the row. */
  let swallowClick = false
  /**
   * This press landed on a row that already holds a live selection, so the
   * gesture stood aside for it.
   *
   * Read by `onContextMenu`, which the platform raises at its OWN long-press
   * threshold on Android: without the flag that event would take the branch for
   * a mouse right-click and open the menu anyway, undoing the decline.
   */
  let pressYieldsToSelection = false
  /** Removes the release listeners armed by `openAfterRelease`. A no-op when none are. */
  let disarmRelease: () => void = () => {}

  el.setAttribute(GESTURE_ATTR, opts.selectableText ? 'selectable' : 'owned')

  /** Give an open menu its pointers back, if THIS gesture took them. */
  function releaseHoldOverMenu() {
    if (!markedHoldOverMenu)
      return
    markedHoldOverMenu = false
    clearTimeout(holdOverMenuTimer)
    holdOverMenuTimer = undefined
    document.removeEventListener('click', onClickAfterRelease, true)
    document.documentElement.removeAttribute(HOLD_OVER_MENU_ATTR)
    document.querySelectorAll('[data-ctx-hold-inert]').forEach(node => node.removeAttribute('data-ctx-hold-inert'))
  }

  /**
   * The click that trails the release, seen from the document so the menu
   * cannot be the one to handle it.
   *
   * This listener does not stop anything -- it exists to hold the inert window
   * open until the click's target has already been decided. Hit-testing happens
   * when the browser builds the event, so by the time this runs the click
   * belongs to the ROW under the transparent menu, where `onClickCapture`
   * swallows it. Clearing the window any earlier is what let the release
   * activate whichever item the finger came down on, which closed the menu and
   * ran that item's action.
   */
  function onClickAfterRelease() {
    releaseHoldOverMenu()
  }

  /**
   * Hand the menu back once the release is fully over.
   *
   * NOT synchronously on `pointerup`: the click that follows it has not been
   * built yet, and restoring hit-testing before it is what put the item under
   * the finger back in its path. The timer is the backstop for a release that
   * produces no click at all -- a cancelled touch, or an engine that suppresses
   * the click after a long press.
   */
  function scheduleHoldOverMenuRelease() {
    if (!markedHoldOverMenu)
      return
    document.addEventListener('click', onClickAfterRelease, true)
    clearTimeout(holdOverMenuTimer)
    holdOverMenuTimer = setTimeout(releaseHoldOverMenu, CLICK_AFTER_RELEASE_GRACE_MS)
  }

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
    const wasHeldOpen = heldOpen
    fired = false
    heldOpen = false
    swallowClick = false
    endGesture()
    if (wasHeldOpen)
      opts.onCancel?.()
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
    if (pointerId !== null && el.hasPointerCapture?.(pointerId))
      el.releasePointerCapture(pointerId)
    pointerId = null
    clearTimeout(holdTimer)
    holdTimer = undefined
    el.removeAttribute(PRESS_HOLD_ATTR)
    // The menu takes its pointers back only once the trailing click has been
    // built and aimed -- WebKit hit-tests that click live, so a menu made
    // solid again here would catch it. See `scheduleHoldOverMenuRelease`.
    scheduleHoldOverMenuRelease()
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
   * A TOUCH HOLD CALLS THIS TWICE, and needs both. The first, at `holdMs`, puts
   * the menu under the still-pressed finger, because the hold is the gesture's
   * confirmation and a menu that waited for the lift read as belonging to the
   * lift. The release then light-dismisses that menu, so `onPointerUp` calls
   * this again from beyond it. `onOpen` is idempotent -- an already-open menu is
   * only re-pointed at the press -- so the second call is free wherever the
   * platform left the first menu alone, and restores it where it did not. Both
   * the hide and the re-open land before the next paint, so nothing blinks.
   *
   * The other callers reach this only once the pointer is already up: the
   * still-held mouse path from `openAfterRelease`, and the Windows / keyboard
   * path straight from `contextmenu`, where nothing is held. The `setTimeout`
   * puts the open one task beyond the release, after the browser's light-dismiss
   * pass for it has run.
   *
   * The release still cannot activate a menu item. A touch stream is captured to
   * the element that received `pointerdown`, so the `pointerup` and the `click`
   * that follow belong to the ROW however far the menu has grown under the
   * finger -- and that click is the one `onClickCapture` swallows.
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
    // it. That click has had its chance, and so has the last decline.
    swallowClick = false
    pressYieldsToSelection = false

    // A mouse never starts a hold. It has `contextmenu`, and a mouse hold must not
    // steal click-drag, text selection, or the row's own press states.
    if (e.pointerType !== 'touch' || !e.isPrimary || e.button !== 0)
      return
    if (pressBelongsToEmbeddedUi(e))
      return
    if (selectionInside(el)) {
      // The row holds a live selection, and dragging the platform's own handles
      // is the only way to adjust it. Those handles sit AT the edges of the
      // highlight and below its last line, so the press that reaches for one
      // lands on the row -- and a hold here would put the menu over the text the
      // user is still choosing. The finger belongs to the selection until the
      // selection is gone. See ~/lib/tapSelect.ts, which is how a finger makes
      // one in the first place.
      pressYieldsToSelection = true
      return
    }
    // A fresh tracked press supersedes a hold that already fired and is waiting
    // for its release: that release no longer matches `pointerId`.
    fired = false
    heldOpen = false

    pointerId = e.pointerId
    startX = e.clientX
    startY = e.clientY
    const rect = el.getBoundingClientRect()
    startTop = rect.top
    startLeft = rect.left
    el.setAttribute(PRESS_HOLD_ATTR, '')
    try {
      el.setPointerCapture(e.pointerId)
    }
    catch {
      // jsdom and a detached row have no capture.
    }
    armScrollCancel()
    holdTimer = setTimeout(() => {
      holdTimer = undefined
      // The press point, not wherever the finger drifted to inside the slop.
      fire({ clientX: startX, clientY: startY }, true)
      // Open NOW, under the still-pressed finger: the hold IS the confirmation,
      // and waiting for the lift made the menu feel like it belonged to the
      // release. `onPointerUp` re-asserts it, because this one does not survive
      // that release -- see `scheduleOpen`.
      heldOpen = true
      scheduleOpen()
      // The indicator has said everything it can now that the menu is here.
      // Removing the attribute hands the tint to the base rule, which fades it
      // out; leaving it would hold a full-strength tint under an open menu
      // until the finger lifted.
      el.removeAttribute(PRESS_HOLD_ATTR)
      // ...and the finger that opened the menu must not also point at it.
      markedHoldOverMenu = true
      document.documentElement.setAttribute(HOLD_OVER_MENU_ATTR, '')
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
    // user is scrolling, and no `click` will follow to be swallowed. A menu the
    // hold already opened has nothing to re-assert against, so cancel it.
    if (heldOpen)
      opts.onCancel?.()
    fired = false
    heldOpen = false
    swallowClick = false
    endGesture()
  }

  const onPointerUp = (e: PointerEvent) => {
    if (pointerId === null || e.pointerId !== pointerId)
      return
    if (heldOpen) {
      // The menu is already up -- the hold opened it, and nothing about this
      // release takes it away (see `showMenuPopover` in ./DropdownMenu.tsx).
      //
      // The release keeps propagating: the drag sensor detaches on it, and the
      // chat scroller drops the pointer from its input set. The one consumer
      // that must NOT react -- `Tooltip`, which would present over the menu --
      // checks `touchReleaseOpensMenu` instead, and needs the flag to outlive
      // this event by the task a tooltip would race it in.
      heldOpen = false
      releaseOpensMenu = true
      setTimeout(() => {
        releaseOpensMenu = false
      }, 0)
    }
    else if (fired) {
      // `fired` with no menu behind it: the platform's own `contextmenu`
      // claimed this hold before the timer could open one, so the release is
      // still the first chance to open it.
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
    if (pressYieldsToSelection) {
      // Android raises this at its own long-press threshold, on the press that
      // `onPointerDown` already stood aside for. Leaving it alone is the whole
      // point: the platform's menu is the one that can act on the selection the
      // finger is adjusting, and this row's menu cannot.
      return
    }
    if (opts.selectableText && pointIsInsideSelection(e.clientX, e.clientY)) {
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
    heldOpen = false
    swallowClick = false
    pressYieldsToSelection = false
    releaseOpensMenu = false
    disarmRelease()
    disarmScrollCancel()
    el.removeAttribute(PRESS_HOLD_ATTR)
    el.removeAttribute(GESTURE_ATTR)
    releaseHoldOverMenu()
  }
}
