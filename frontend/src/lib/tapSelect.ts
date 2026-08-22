import type { TextCaret } from '~/lib/textSelection'
import { PRESS_SLOP_PX } from '~/components/common/contextMenuGesture'
import { monotonicNow } from '~/lib/monotonicNow'
import { INPUT_OR_EDITABLE_SELECTOR } from '~/lib/textInputBehavior'
import { paragraphRangeAt, pointIsInsideSelection, selectionInside, wordRangeAt } from '~/lib/textSelection'
import { motion } from '~/styles/tokens'

/**
 * Double tap selects the word, triple tap selects the paragraph — the mouse's
 * two selection defaults, given to a finger.
 *
 * A pointing device gets both for free: every engine selects a word on the
 * second click and a paragraph on the third. A finger gets neither. What it gets
 * instead is a long press, which raises the platform's own selection handles —
 * and on the chat rows this app takes that press for the message menu, so touch
 * had no way to select part of a message at all. This gesture is the way back.
 *
 * ## Touch only, decided per press
 *
 * `pointerType === 'touch'` is the whole gate. It is MEASURED from the input
 * that arrived rather than inferred from the device, so a hybrid laptop keeps
 * its native double-click on the mouse and gains this on the screen, and neither
 * one has to guess what `(pointer: coarse)` means for a machine that has both.
 *
 * ## Giving the text back
 *
 * The chat rows set `user-select: none` on a coarse pointer, so the long press
 * belongs to the message menu instead of to the platform's selection callout
 * (see ~/components/common/contextMenuGesture.css.ts). Text under that rule
 * cannot be selected — not by a finger, and not by this module either: Blink
 * leaves it out of the selection it serializes, so a range set across it copies
 * as nothing.
 *
 * So the gesture LIFTS the suppression, on the elements that declare it, for
 * exactly as long as the selection it made stays alive. It is put back on the
 * next press that starts something new, and by the `selectionchange` that
 * reports the selection gone — whichever comes first. The row therefore has its
 * `user-select: none` back before any press can be held long enough to raise a
 * callout, which is what keeps the long press the menu's.
 *
 * ## A live selection owns the finger
 *
 * A press on or near the highlight is NOT something new. Adjusting a selection
 * means dragging the platform's own handles, and those sit at the edges of the
 * highlight and below its last line — so the press that reaches for one lands
 * outside every rect the selection reports, on the row. Dropping the lift there
 * would end the selection the finger came to change.
 *
 * ~/components/common/contextMenuGesture.ts stands aside for the same reason and
 * more widely: while a row holds a live selection its long press opens no menu
 * at all, because a menu over the text would leave the reader nothing to aim at.
 *
 * ## The mouse events a tap synthesizes
 *
 * A tap is followed by `mousedown`, `mouseup` and `click` for the benefit of
 * pages that only listen for a mouse, and the DEFAULT ACTION of that `mousedown`
 * collapses the selection to a caret. So the gesture answers it with a single
 * capture-phase `preventDefault()`, which leaves the `mouseup` and the `click`
 * behind it untouched.
 *
 * Measured in Chromium, driving a real double tap over a `user-select: none`
 * wrapper: without the guard the range reads back as the selected word right
 * after it is set and as the EMPTY STRING from `mouseup` onward — the collapse
 * lands between the two, which is where a mousedown's default action runs. With
 * the guard the word survives every one of the three events and the wait after
 * them. Nothing else in this module keeps it alive, so do not remove the guard
 * because the events look harmless.
 *
 * `preventDefault()` on the `touchend` instead would suppress all three at once,
 * and it is the wrong instrument: it also suppresses the `mouseup` that
 * ~/components/common/SelectionQuotePopover.tsx shows its Copy and Quote buttons
 * from, so the selection would arrive with no way to act on it.
 */

/** How much of the text one tap in a sequence claims. */
export type TapSelectGranularity = 'word' | 'paragraph'

/**
 * What makes a press a tap: short and still.
 *
 * Neither bound is this module's own, and neither one should be. `PRESS_SLOP_PX`
 * is the drift the long press on a chat row tolerates, so a press that cancels
 * the hold cannot still count as a tap here — one definition of "still" for
 * every gesture in the app, which ~/lib/dismissSoftKeyboardOnTap.ts already
 * reads for the same reason. `motion.longPress` is the other edge of the same
 * boundary: a press held long enough for the message menu to claim it belongs to
 * that menu, and counting it as a tap as well would let two long presses in a
 * row select a word behind the menu they opened.
 *
 * A press that fails either bound ends the sequence rather than counting as one
 * of its taps.
 */
const TAP_MAX_MS = motion.longPress

/**
 * The longest gap, in milliseconds, between two taps of one sequence.
 *
 * Above the ~300ms an engine allows its own double-tap gesture, because this one
 * asks for three taps and the third is the slowest: the reader has to see the
 * word highlight before deciding to widen it. Below the point where two
 * unrelated taps on the same word would join, which is what a longer window
 * costs.
 */
export const MULTI_TAP_MS = 400

/**
 * How far apart, in CSS pixels, two taps of one sequence may land.
 *
 * A finger coming back to the same place lands within about half its own contact
 * patch. Wider than that and two deliberate taps on two different words would
 * chain, and the second word's tap would widen the first word's selection to a
 * paragraph.
 */
export const MULTI_TAP_RADIUS_PX = 24

/**
 * How far outside the highlight a finger still counts as reaching FOR it.
 *
 * The platform draws its selection handles at the edges of the highlight and
 * below its last line, so a finger going for one lands outside every rect the
 * selection reports. About a fingertip's radius, which is what that reach costs.
 *
 * The same value as {@link MULTI_TAP_RADIUS_PX} today and for a different
 * reason: that one is how far a finger drifts coming back to the same word.
 * Neither should be derived from the other.
 */
const SELECTION_REACH_PX = 24

/**
 * How long the guard against the synthesized `mousedown` stays armed.
 *
 * The same window and the same reasoning as `CLICK_AFTER_RELEASE_GRACE_MS` in
 * ~/components/common/contextMenuGesture.ts: the mouse events a tap synthesizes
 * land within a frame or two of the release on a viewport that declares its
 * width. The guard disarms on the first `mousedown` whatever happens, so this
 * only limits how long it waits for one that never comes.
 */
const COMPAT_MOUSE_GRACE_MS = 400

/** Marks a subtree that this gesture must not select out of. */
const NO_TAP_SELECT_ATTR = 'data-no-tap-select'

/**
 * Presses this module must leave alone.
 *
 * `INPUT_OR_EDITABLE_SELECTOR` carries the text-entry group that every pointer
 * guard in the app shares, and `[popover]` stands in the list itself — the same
 * shape as ~/components/common/contextMenuGesture.ts, ~/lib/horizontalSwipe.ts
 * and ~/lib/dragActivators.ts. Do NOT merge the four lists: each one declines
 * what its own gesture must not take.
 *
 * What this one adds is the controls: a `button`, a `[role="button"]`, a link
 * and a `summary` all act on a tap, so a second tap on one is a second
 * activation and not a request to read its label as prose.
 * `[data-no-tap-select]` covers the app's own chrome that sits INSIDE the text
 * region — the quote popover, which is a child of the element this gesture
 * attaches to.
 */
const EMBEDDED_UI_SELECTOR = `${INPUT_OR_EDITABLE_SELECTOR}, [popover], [${NO_TAP_SELECT_ATTR}], button, [role="button"], a[href], summary`

function pressBelongsToEmbeddedUi(target: Element, root: Element): boolean {
  for (let el: Element | null = target; el && el !== root; el = el.parentElement) {
    if (el.matches(EMBEDDED_UI_SELECTOR))
      return true
  }
  return false
}

/**
 * The caret under a point.
 *
 * `caretPositionFromPoint` is the standard spelling and `caretRangeFromPoint` is
 * the older one that WebKit shipped first; between them every engine this app
 * runs on answers. Both are absent under jsdom, which has no layout to hit-test,
 * so the gesture finds no caret there unless a test supplies one.
 */
function caretAt(clientX: number, clientY: number): TextCaret | null {
  if (typeof document.caretPositionFromPoint === 'function') {
    const position = document.caretPositionFromPoint(clientX, clientY)
    if (position?.offsetNode)
      return textCaret(position.offsetNode, position.offset)
  }
  if (typeof document.caretRangeFromPoint === 'function') {
    const range = document.caretRangeFromPoint(clientX, clientY)
    if (range)
      return textCaret(range.startContainer, range.startOffset)
  }
  return null
}

/**
 * Normalize a caret onto a text node.
 *
 * An engine reports the position inside an element when the point falls between
 * that element's children — in the padding of a paragraph, or past the end of
 * its last line. `offset` is then a CHILD index, so the text to work from is the
 * child it names.
 */
function textCaret(node: Node, offset: number): TextCaret | null {
  if (node.nodeType === Node.TEXT_NODE)
    return { node: node as Text, offset }
  if (!(node instanceof Element))
    return null
  const children = node.childNodes
  const child = children[Math.min(offset, children.length - 1)]
  const text = firstTextNode(child ?? node)
  return text ? { node: text, offset: 0 } : null
}

function firstTextNode(node: Node): Text | null {
  if (node.nodeType === Node.TEXT_NODE)
    return node as Text
  const walker = document.createTreeWalker(node, NodeFilter.SHOW_TEXT)
  return walker.nextNode() as Text | null
}

export interface TapSelectOptions {
  /** Act on a selection this gesture just made. */
  onSelect?: (granularity: TapSelectGranularity) => void
}

/**
 * Give `root` the two multi-tap selections. Returns the detach function.
 *
 * A real listener on an element rather than a bag of JSX props, for the reason
 * ~/components/common/contextMenuGesture.ts states: the gesture needs the
 * document and the capture phase for events that Solid's delegation never sees.
 */
export function attachTapSelect(root: HTMLElement, opts: TapSelectOptions): () => void {
  /** The touch being tracked. `null` means no press is in flight. */
  let pointerId: number | null = null
  let startX = 0
  let startY = 0
  /** The press travelled past the slop, so it is no longer a tap. */
  let moved = false
  /** When the press landed, so its duration can be measured at the release. */
  let pressedAt = 0
  /** Taps counted so far in this sequence. Zero means no sequence is open. */
  let tapCount = 0
  let lastTapAt = 0
  let lastTapX = 0
  let lastTapY = 0

  /** Elements whose `user-select: none` this gesture is holding open, and what they had. */
  const lifted: { el: HTMLElement, userSelect: string, webkitUserSelect: string }[] = []
  /** Disarms the guard against the synthesized `mousedown`. A no-op when none is armed. */
  let disarmCompatMouse: () => void = () => {}

  /**
   * Let `from` and its ancestors up to `root` be selected again.
   *
   * Measured in Chromium, against a wrapper that declares `user-select: none`
   * around a paragraph: a range set over that text serializes as the empty
   * string, and the same range set after the wrapper is lifted serializes as the
   * text. So the lift is not a precaution — without it the gesture highlights
   * nothing and copies nothing. No style flush stands between the two: the write
   * and the selection in the same task were enough.
   *
   * The walk lifts EVERY element on the chain that computes `none`, not the
   * nearest one alone. Chromium re-enables a descendant from an ancestor's lift
   * on its own (the paragraph inside that wrapper computed `text` again with
   * nothing set on it), so the nearest one would do there. The walk costs a
   * handful of inline styles and does not have to be right about whether every
   * other engine honours the override.
   *
   * The prefixed property is read as well as written: Safari answered the
   * unprefixed one only from 16.4, and reading `''` there would find nothing to
   * lift on the very engine whose callout this suppression exists for.
   */
  function lift(from: Element) {
    for (let el: Element | null = from; el; el = el.parentElement) {
      const style = el instanceof HTMLElement ? getComputedStyle(el) : null
      if (style && (style.userSelect || style.webkitUserSelect) === 'none' && !lifted.some(entry => entry.el === el)) {
        const html = el as HTMLElement
        lifted.push({ el: html, userSelect: html.style.userSelect, webkitUserSelect: html.style.webkitUserSelect })
        html.style.userSelect = 'text'
        html.style.webkitUserSelect = 'text'
      }
      if (el === root)
        return
    }
  }

  /**
   * Take away the selection this gesture made, and then the lift that carried it.
   *
   * BOTH, and in that order. The browser normally collapses a selection when the
   * next press places a caret, and it places no caret in text it may not select
   * — so putting `user-select: none` back first strands the range. Measured in
   * Chromium: after the suppression returned, a tap elsewhere left `rangeCount`
   * at 1 and `isCollapsed` false, with only `Selection.toString()` reading
   * empty. Every guard that stands aside for a live selection (the message menu,
   * the drawer swipe) then had one forever and never stood down again.
   *
   * Only a selection this gesture is holding a lift for. Without a lift the text
   * was selectable on its own — a file view, or a mouse — and the browser's own
   * handling of the press is the right thing to leave alone.
   */
  function dropSelection() {
    if (lifted.length > 0 && selectionInside(root))
      window.getSelection()?.removeAllRanges()
    restoreLift(0)
  }

  /** Put back everything lifted from `first` onward. `restoreLift(0)` puts back all of it. */
  function restoreLift(first: number) {
    for (const entry of lifted.splice(first)) {
      entry.el.style.userSelect = entry.userSelect
      entry.el.style.webkitUserSelect = entry.webkitUserSelect
    }
  }

  /**
   * Refuse the default action of the `mousedown` a tap synthesizes.
   *
   * Capture on the document, because the row under the finger is where that
   * event lands and the gesture must beat every listener on the way to it.
   */
  function guardCompatMouse() {
    // Whatever a previous selection armed is disarmed first, so at most one
    // guard and one timer exist at a time.
    disarmCompatMouse()

    // Function declarations, so the timer receives `release` itself rather than
    // a closure that reads the mutable `disarmCompatMouse` when it fires -- by
    // then that binding could belong to a later gesture.
    let timer: ReturnType<typeof setTimeout> | undefined
    function release() {
      clearTimeout(timer)
      document.removeEventListener('mousedown', onMouseDown, true)
      disarmCompatMouse = () => {}
    }
    function onMouseDown(e: MouseEvent) {
      e.preventDefault()
      release()
    }

    timer = setTimeout(release, COMPAT_MOUSE_GRACE_MS)
    document.addEventListener('mousedown', onMouseDown, true)
    disarmCompatMouse = release
  }

  /**
   * Select the word or the paragraph at a point. Reports whether it found one.
   *
   * The lift runs from the CARET's parent and not from the element the press
   * landed on, which is a smaller chain and the only one that matters: the
   * selection is built out of the caret's text node, so a suppression on some
   * other branch below their common ancestor has nothing to suppress.
   *
   * It also runs AFTER the caret is resolved, because a caret hit-test reads
   * through the suppression: measured in Chromium, both spellings of it return
   * the right text node and offset inside a `user-select: none` wrapper. Lifting
   * first to protect that lookup would have been guarding a lookup that needs no
   * guard.
   */
  function selectAt(clientX: number, clientY: number, granularity: TapSelectGranularity): boolean {
    // Nothing may stay lifted for an attempt that selects nothing, and only what
    // THIS attempt adds may be put back: an earlier tap of the same sequence
    // owns the rest of the list.
    const before = lifted.length

    const caret = caretAt(clientX, clientY)
    if (!caret || !root.contains(caret.node))
      return false
    if (caret.node.parentElement)
      lift(caret.node.parentElement)

    const range = granularity === 'word' ? wordRangeAt(caret, root) : paragraphRangeAt(caret, root)
    const selection = range ? window.getSelection() : null
    if (!range || !selection) {
      restoreLift(before)
      return false
    }

    selection.removeAllRanges()
    selection.addRange(range)
    guardCompatMouse()
    return true
  }

  /**
   * Follow the press on the DOCUMENT until it ends.
   *
   * The rest of a press does NOT reliably reach this region. Another gesture on
   * an ANCESTOR can call `setPointerCapture`, and from that moment every further
   * event for that pointer is dispatched at the capturing element and travels
   * only its own ancestor chain -- so a listener on a descendant of it, which is
   * what this region is, never runs again. Measured in Chromium against the
   * mobile shell: the drawer swipe captures to the centre pane
   * (~/lib/horizontalSwipe.ts), and the `pointerup` that ends a tap arrives at
   * that pane with this region nowhere in its path.
   *
   * `pointerdown` stays on the region, because capture applies only from the
   * NEXT event and the press has to land inside the prose to count at all. The
   * document listeners are armed per press rather than kept standing, so an idle
   * transcript pays nothing for them.
   */
  function trackPress() {
    document.addEventListener('pointermove', onPointerMove, true)
    document.addEventListener('pointerup', onPointerUp, true)
    document.addEventListener('pointercancel', onPointerCancel, true)
  }

  function endPress() {
    pointerId = null
    document.removeEventListener('pointermove', onPointerMove, true)
    document.removeEventListener('pointerup', onPointerUp, true)
    document.removeEventListener('pointercancel', onPointerCancel, true)
  }

  const onPointerDown = (e: PointerEvent) => {
    if (e.pointerType !== 'touch' || !e.isPrimary || e.button !== 0)
      return
    const target = e.target instanceof Element ? e.target : null
    if (!target || !root.contains(target) || pressBelongsToEmbeddedUi(target, root)) {
      // The press belongs to something else on the surface. It is not a tap of
      // this sequence, and it ends the sequence rather than interrupting it.
      endPress()
      tapCount = 0
      return
    }

    const now = monotonicNow()
    const continues = tapCount > 0
      && now - lastTapAt <= MULTI_TAP_MS
      && Math.hypot(e.clientX - lastTapX, e.clientY - lastTapY) <= MULTI_TAP_RADIUS_PX
    if (!continues) {
      tapCount = 0
      // A finger landing on or near the highlight is reaching for it — the
      // platform's selection handles, or the popover beside them. Anywhere else
      // starts something new, and the row takes its `user-select: none` back
      // now, while the press is still too young to have raised a callout.
      if (!pointIsInsideSelection(e.clientX, e.clientY, SELECTION_REACH_PX))
        dropSelection()
    }

    pointerId = e.pointerId
    startX = e.clientX
    startY = e.clientY
    pressedAt = now
    moved = false
    trackPress()
  }

  function onPointerMove(e: PointerEvent) {
    if (pointerId === null || e.pointerId !== pointerId || moved)
      return
    if (Math.hypot(e.clientX - startX, e.clientY - startY) > PRESS_SLOP_PX)
      moved = true
  }

  function onPointerUp(e: PointerEvent) {
    if (pointerId === null || e.pointerId !== pointerId)
      return
    endPress()
    const releasedAt = monotonicNow()
    if (moved || releasedAt - pressedAt >= TAP_MAX_MS) {
      // A scroll, a drag, a fling, or a hold the message menu already claimed.
      // The sequence is over, and the next press starts a new one.
      tapCount = 0
      return
    }

    // The press point, not wherever the finger drifted to inside the slop, so a
    // sequence measures its own spread against one stable place.
    lastTapAt = releasedAt
    lastTapX = startX
    lastTapY = startY
    tapCount = Math.min(tapCount + 1, 3)
    if (tapCount < 2)
      return

    const granularity: TapSelectGranularity = tapCount === 2 ? 'word' : 'paragraph'
    if (selectAt(startX, startY, granularity))
      opts.onSelect?.(granularity)
  }

  function onPointerCancel(e: PointerEvent) {
    if (pointerId === null || e.pointerId !== pointerId)
      return
    // The browser took the touch for a scroll. No release follows.
    endPress()
    tapCount = 0
  }

  /**
   * The lift lives exactly as long as the selection it was made for.
   *
   * Without this the row would keep its text selectable after the popover's Copy
   * cleared the highlight, or after a tap somewhere else in the app took it —
   * and a long press there would then raise the platform's callout over the
   * message menu, which is the trade the suppression exists to make.
   */
  const onSelectionChange = () => {
    if (lifted.length > 0 && !selectionInside(root))
      restoreLift(0)
  }

  root.addEventListener('pointerdown', onPointerDown)
  document.addEventListener('selectionchange', onSelectionChange)

  return () => {
    root.removeEventListener('pointerdown', onPointerDown)
    document.removeEventListener('selectionchange', onSelectionChange)
    endPress()
    disarmCompatMouse()
    restoreLift(0)
    pointerId = null
    tapCount = 0
  }
}
