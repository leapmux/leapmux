import { INPUT_OR_EDITABLE_SELECTOR } from '~/lib/textInputBehavior'

/**
 * A horizontal finger swipe over a region — the input that opens and closes the
 * mobile drawers.
 *
 * ## Why this refuses the browser's scroll, and how
 *
 * A touch that starts over a scroller belongs to the browser until it says
 * otherwise. Blink hands the first `pointermove` to the page, decides the touch
 * is a scroll, and then fires `pointercancel` and stops dispatching touch
 * events for that finger ENTIRELY — no more `touchmove`, no `touchend`. It does
 * this even when nothing in the scroll chain can move sideways, so a swipe over
 * the chat transcript died one move in and reported nothing.
 *
 * The counter-measure is a NON-PASSIVE `touchmove` listener that calls
 * `preventDefault()` once the gesture owns the finger. Blink then never starts
 * the scroll, so the pointer stream survives. The decision is per GESTURE,
 * which is what lets a finger over a wide code block still pan it: the
 * recognizer declines that press and therefore never prevents anything.
 *
 * The listener must be STANDING. One registered from `touchstart`, inside the
 * same finger's own sequence, suppressed two moves and then lost the pointer
 * anyway, because Blink decides a touch's disposition from the handler region
 * its compositor already holds.
 *
 * A standing non-passive listener makes this region's touches block on the main
 * thread before a scroll can start, which is a real cost. The handler is
 * written to pay as little of it as possible: one comparison when no gesture is
 * in flight, which covers every press the guards declined and every drag that
 * went vertical.
 *
 * It also makes the gesture a RACE, and losing it is silent. Blink waits only a
 * short deadline for the main thread to answer a touch move; past that it
 * assumes nothing was prevented, starts the scroll and cancels the pointer. A
 * main thread held for longer than that deadline therefore drops the swipe
 * outright — the finger travels and no drawer arrives. On a machine loaded
 * enough to stretch the app roughly ten times, the E2E specs in
 * `tests/e2e/183-mobile-swipe-drawers.spec.ts` lose the swipe this way while
 * every assertion around them still passes. There is no way to win that race
 * from here: `touch-action: pan-y` on the region moves the decision to the
 * compositor but does NOT keep the pointer alive (measured), and preventing
 * every move until the axis is known would delay the start of every vertical
 * scroll by one move.
 *
 * `touch-action: pan-y` on the region is the other way to stop Blink taking the
 * finger, and it costs no main-thread work. It is NOT used here.
 * ~/styles/global.css.ts states the rule this app follows -- a region declares
 * `touch-action` only to constrain a gesture it owns -- and warns that a value
 * on an ancestor narrows every descendant scroller under it. Chromium 2026 does
 * not reproduce that narrowing: with `pan-y` on the region a sideways scroller
 * inside it still panned. Do not take the measurement as permission. It covers
 * one engine, and the per-gesture decision above is correct on all of them.
 *
 * ## Who owns the finger
 *
 * The region is full of other gestures, so the recognizer decides twice and
 * declines at each point rather than compete:
 *
 * 1. At the press, against the element it landed on (`pressBelongsToAnotherOwner`).
 * 2. At the axis lock, against the direction the finger actually took
 *    (`scrollerConsumesSwipe`).
 *
 * It reports the swipe the moment the travel passes {@link SWIPE_MIN_PX},
 * rather than waiting for the lift: the drawer is what the gesture is for, and
 * a drawer that arrives under the finger reads as this gesture's answer. One
 * gesture reports at most once — the rest of the travel keeps suppressing the
 * scroll and nothing else.
 *
 * A real listener on an element, not a bag of JSX props, for the same reason as
 * ~/components/common/contextMenuGesture.ts: it needs the CAPTURE phase for the
 * `click` it swallows, and Solid delegates `onClick` to the document, which a
 * bubble-phase stop never beats.
 */

/** Which way the finger travelled across the screen. */
export type SwipeDirection = 'left' | 'right'

/**
 * Travel, in CSS pixels, at which the gesture picks its axis.
 *
 * It must sit BELOW the engine's own scroll threshold — 8dp on Android, about
 * 10px on iOS — because the axis lock is what arms the `preventDefault()` that
 * stops the scroll, and a lock that arrives after the scroll starts arrives
 * after the pointer is already cancelled. That is the whole reason this number
 * is small.
 *
 * It costs nothing on the other side: a swipe still has to travel
 * {@link SWIPE_MIN_PX} to report anything, and a lock that turns out to be
 * wrong gives the finger back on the next press.
 */
export const AXIS_LOCK_PX = 6

/**
 * Horizontal travel, in CSS pixels, at which the swipe reports.
 *
 * Measured from the press point, so a finger that goes out and comes back never
 * reaches it. Roughly a fifth of a phone's width, which is a deliberate
 * gesture and not a drifting tap: the drawer arrives UNDER the finger rather
 * than on the lift, so a short distance here reads as a drawer that opens by
 * itself. It is still inside the reach of a thumb that never leaves the lower
 * half of the screen.
 */
export const SWIPE_MIN_PX = 88

/** Sub-pixel tolerance for a scroll offset that reads as "already at the end". */
const SCROLL_END_EPSILON_PX = 1

/**
 * Presses this module must leave alone: the text-entry group every pointer
 * guard shares, and any popover.
 *
 * `INPUT_OR_EDITABLE_SELECTOR` carries the first, and `[popover]` stands in the
 * list itself — the same shape ~/components/common/contextMenuGesture.ts,
 * ~/lib/dragActivators.ts and ~/components/shell/guardedPointerSensor.ts use.
 * Do NOT merge the four lists: each one declines what its own gesture must not
 * take, and this one adds no name of its own (see `declaresOwnTouchAction`,
 * which covers what a selector cannot).
 */
const EMBEDDED_UI_SELECTOR = `${INPUT_OR_EDITABLE_SELECTOR}, [popover]`

/**
 * Whether `el` claims the touch gestures over it.
 *
 * `~/styles/global.css.ts` states the app-wide rule: a region declares
 * `touch-action` only to constrain a gesture it owns. Reading the property back
 * makes that rule the guard, so a region added later is declined without an
 * edit here — a selector list naming today's three (the drag grip, the chat
 * scroll rail, the tiling separator) would go stale on the fourth.
 *
 * An empty value reads as "declares nothing". A style engine reports one for a
 * property it does not implement, and reading that as a declaration would
 * decline every press on that engine.
 */
function declaresOwnTouchAction(el: Element): boolean {
  const touchAction = getComputedStyle(el).getPropertyValue('touch-action')
  return touchAction !== '' && touchAction !== 'auto'
}

/**
 * Whether the press belongs to something between `target` and `root`.
 *
 * `root` itself is excluded: it hosts this gesture, so a `touch-action` on the
 * region would be about the swipe rather than against it.
 */
function pressBelongsToAnotherOwner(target: Element, root: Element): boolean {
  for (let el: Element | null = target; el && el !== root; el = el.parentElement) {
    if (el.matches(EMBEDDED_UI_SELECTOR) || declaresOwnTouchAction(el))
      return true
  }
  return false
}

/**
 * Whether a sideways scroller under the finger can still move `direction`.
 *
 * A finger moving RIGHT drags the content right, which uncovers what sits to
 * the left of the viewport — so it has somewhere to go while `scrollLeft` is
 * above zero. A finger moving LEFT has somewhere to go while `scrollLeft` is
 * below the maximum.
 *
 * Asked at the axis lock and about that one direction, so a code block scrolled
 * to its right end still yields the leftward swipe it can no longer consume.
 * Declining inside every sideways scroller regardless of its offset would make
 * a wide code block a dead zone for the gesture, and this app's transcript is
 * full of them.
 *
 * This is also what keeps those blocks pannable at all. The gesture suppresses
 * the browser's scroll only once it has locked, and it never locks here — so
 * the finger reaches Blink's own scroll arbitration untouched.
 */
function scrollerConsumesSwipe(target: Element, root: Element, direction: SwipeDirection): boolean {
  for (let el: Element | null = target; el && el !== root; el = el.parentElement) {
    const maxScrollLeft = el.scrollWidth - el.clientWidth
    if (maxScrollLeft <= SCROLL_END_EPSILON_PX)
      continue
    const overflowX = getComputedStyle(el).getPropertyValue('overflow-x')
    if (overflowX !== 'auto' && overflowX !== 'scroll')
      continue
    const remaining = direction === 'right'
      ? el.scrollLeft
      : maxScrollLeft - el.scrollLeft
    if (remaining > SCROLL_END_EPSILON_PX)
      return true
  }
  return false
}

export interface HorizontalSwipeOptions {
  /** Act on a completed swipe. Called at most once per gesture. */
  onSwipe: (direction: SwipeDirection) => void
}

/**
 * Give `root` a horizontal swipe gesture. Returns the detach function.
 *
 * The two thresholds are module constants, not options. Both are tuned against
 * the ENGINE (one must beat its scroll threshold, the other its tap slop), so a
 * caller that overrode either would be tuning against a number it cannot see.
 */
export function attachHorizontalSwipe(root: HTMLElement, opts: HorizontalSwipeOptions): () => void {
  /** The touch being tracked. `null` means no gesture is in flight. */
  let pointerId: number | null = null
  let startX = 0
  let startY = 0
  /** What the press landed on, for the scroller test the axis lock runs. */
  let pressTarget: Element | null = null
  /**
   * The direction the axis lock chose. `null` while the axis is undecided, and
   * a value means this gesture owns the finger — which is exactly what
   * `onTouchMove` suppresses the browser's scroll on.
   */
  let lockedDirection: SwipeDirection | null = null
  /** This gesture already reported. The rest of its travel reports nothing. */
  let reported = false
  /** The next `click` belongs to the swipe, not to whatever sits under the finger. */
  let swallowClick = false

  function endGesture() {
    pointerId = null
    pressTarget = null
    lockedDirection = null
    reported = false
  }

  const onPointerDown = (e: PointerEvent) => {
    // Any new press ends the previous gesture and its claim on the click that
    // follows. A second finger — the start of a pinch — lands here too, and
    // ends the swipe the first one was making.
    swallowClick = false
    endGesture()

    if (e.pointerType !== 'touch' || !e.isPrimary || e.button !== 0)
      return
    const target = e.target instanceof Element ? e.target : null
    if (!target || pressBelongsToAnotherOwner(target, root))
      return

    pointerId = e.pointerId
    startX = e.clientX
    startY = e.clientY
    pressTarget = target
  }

  const onPointerMove = (e: PointerEvent) => {
    if (pointerId === null || e.pointerId !== pointerId)
      return
    const dx = e.clientX - startX
    const dy = e.clientY - startY

    if (lockedDirection === null) {
      if (Math.hypot(dx, dy) < AXIS_LOCK_PX)
        return
      if (Math.abs(dy) >= Math.abs(dx)) {
        // The finger is going down or up the page. That press is the scroller's,
        // and letting go here is what leaves its scroll untouched.
        endGesture()
        return
      }
      const direction: SwipeDirection = dx > 0 ? 'right' : 'left'
      if (pressTarget && scrollerConsumesSwipe(pressTarget, root, direction)) {
        endGesture()
        return
      }
      lockedDirection = direction
    }

    if (reported)
      return
    const travel = lockedDirection === 'right' ? dx : -dx
    if (travel < SWIPE_MIN_PX)
      return
    // Report under the finger. The gesture keeps the pointer to the release so
    // the remaining travel cannot report a second time, and so the browser
    // still cannot scroll from it.
    reported = true
    swallowClick = true
    opts.onSwipe(lockedDirection)
  }

  const onPointerUp = (e: PointerEvent) => {
    if (pointerId === null || e.pointerId !== pointerId)
      return
    endGesture()
  }

  const onPointerCancel = (e: PointerEvent) => {
    if (pointerId === null || e.pointerId !== pointerId)
      return
    // The browser claimed the touch after all — a system gesture, or a scroll
    // that started before the axis lock. No release follows, and no click.
    if (!reported)
      swallowClick = false
    endGesture()
  }

  /**
   * Refuse the browser's scroll for a finger this gesture owns.
   *
   * Reads the state the POINTER handlers own, because the two event models
   * report the same movement in a fixed order: `pointermove` is dispatched
   * before the `touchmove` it corresponds to, so the axis lock is already
   * decided by the time this runs on the very move that decided it.
   *
   * `cancelable` is false for every move after a scroll has begun. Calling
   * `preventDefault()` there does nothing but log a console warning.
   */
  const onTouchMove = (e: TouchEvent) => {
    if (lockedDirection === null)
      return
    if (e.cancelable)
      e.preventDefault()
  }

  /**
   * Swallow the click a completed swipe can synthesize.
   *
   * Most engines suppress the click after a touch travels this far, but the
   * suppression is a heuristic rather than a rule, and the cost of the one that
   * gets through is high: the release of a swipe that closes the workspaces
   * drawer lands on a workspace row and switches workspace.
   */
  const onClickCapture = (e: MouseEvent) => {
    if (!swallowClick)
      return
    swallowClick = false
    e.stopPropagation()
    e.preventDefault()
  }

  root.addEventListener('pointerdown', onPointerDown)
  // Passive: the gesture never prevents a POINTER event's default action. What
  // it prevents is the touch move below, which is the event the scroll comes
  // from.
  root.addEventListener('pointermove', onPointerMove, { passive: true })
  root.addEventListener('pointerup', onPointerUp)
  root.addEventListener('pointercancel', onPointerCancel)
  // Non-passive, and registered here rather than when a gesture starts. See the
  // module doc: this is the whole mechanism, and a late registration does not
  // work.
  root.addEventListener('touchmove', onTouchMove, { passive: false })
  root.addEventListener('click', onClickCapture, { capture: true })

  return () => {
    root.removeEventListener('pointerdown', onPointerDown)
    root.removeEventListener('pointermove', onPointerMove)
    root.removeEventListener('pointerup', onPointerUp)
    root.removeEventListener('pointercancel', onPointerCancel)
    root.removeEventListener('touchmove', onTouchMove)
    root.removeEventListener('click', onClickCapture, { capture: true })
    swallowClick = false
    endGesture()
  }
}
