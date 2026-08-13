import type { Accessor } from 'solid-js'
import type { DotCluster } from './chatRailPolicy'
import { createEffect, createMemo, createSignal, onCleanup, untrack } from 'solid-js'
import { clamp } from '~/lib/clamp'
import { trailingDebounce } from '~/lib/debounce'
import { dotClusterEqual, nearestDotWithin } from './chatRailPolicy'
import * as styles from './ChatScrollRail.css'
import { centerAxisY } from './chatScrollRailGeometry'

// ---------------------------------------------------------------------------
// Scroll-rail dot preview
//
// The rail's "which dot the single card describes + when to open and close it + when to warm its
// preview" state machine: a scrub target (the dot the dragging thumb is over) takes precedence
// over a pointer or a keyboard focus, the card is placed on the dot's centre-axis Y (clamped off
// the rail edges), and the preview is warmed instantly on hover/focus but DEBOUNCED during a
// scrub -- so dragging across a dense rail doesn't fire a GetAgentMessage per fly-over dot.
// Opening is always immediate; the POINTER channel closes after a short delay (see
// POINTER_CLOSE_DELAY_MS), because the card is a place the reader goes rather than only a label.
// Extracted from ChatScrollRail (where it was a signal, three memos, an effect, and a debounce
// timer tangled through the component) so the subtle precedence/delay/cleanup behaviour is one
// named, unit-tested unit -- the sibling of createRailMetrics / createDragReleaseHold /
// createThumbDrag.
// ---------------------------------------------------------------------------

/**
 * Rail px within which a dragging thumb counts as "over" a dot, revealing its scrub preview.
 *
 * Derived from the coarse hit circle, not chosen next to it. A finger presses anywhere inside that
 * circle and the press centres the thumb on the pressed point, so the card that press must open is
 * resolved by distance from the dot's rail-Y. A range narrower than the circle's RADIUS would
 * leave a rim of the dot's own hit area that jumps to the message but shows no preview of it --
 * the one thing tests/e2e/047c-chat-scroll-rail-scrub.spec.ts exists to protect, and a silent
 * failure the moment either number moved on its own.
 */
const SCRUB_PREVIEW_RANGE_PX = styles.DOT_COARSE_HIT_PX / 2

/**
 * How long the card stays open after the POINTER lets go of it: it left the dot, it left the
 * card, or a press inside the card ended. One window, two jobs. It bridges the gutter between the
 * dot and the card: for that moment the pointer is over neither, so without the delay the card
 * closes before the reader who aims at it arrives, and its selectable, scrollable text is
 * unreachable. It is also the tail that lets a reader who overshoots come back without aiming at
 * a 6px dot again.
 *
 * Nothing that OPENS a card waits for it: reaching another dot -- by hover, by focus, or by a
 * scrub -- replaces the card at once (see openDot, focusDot, and activeDot). Nor does anything
 * that ABANDONS one: a scrub and a rail teardown both drop the card immediately.
 *
 * The FOCUS channel has no delay in either direction. Focus moves in discrete steps, so there is
 * no gap for the reader to cross and nothing to wait out.
 */
export const POINTER_CLOSE_DELAY_MS = 300

/**
 * How long the thumb must settle on a dot mid-scrub before its preview is fetched. A hover/focus
 * warms instantly; a SCRUB debounces, because dragging across a dense rail passes many dots and
 * warming each would fire a GetAgentMessage RPC per out-of-window mark crossed. Reset on every
 * scrub dot change, so only a dot the thumb lingers near is fetched -- the fly-over dots coalesce.
 */
export const SCRUB_WARM_DEBOUNCE_MS = 120

export interface DotPreviewDeps {
  /** The rail's current dot clusters (ascending by topPx). */
  dots: Accessor<DotCluster[]>
  /** The live drag fraction (null when not dragging), so a scrub can take card precedence. */
  drag: Accessor<number | null>
  /** The rail's pixel height, for the centre-axis mapping and the card clamp. */
  railHeight: Accessor<number>
  /** The current thumb height (px), for mapping a drag fraction to the thumb-centre rail-Y. */
  thumbHeightPx: Accessor<number>
  /**
   * The open card's MEASURED height (px), or 0 before the first measurement.
   *
   * The clamp reads this rather than PREVIEW_CARD_MAX_H_PX, because that constant is the CAP and
   * not the height. A one-line card clamped against half the cap lands up to ~90px from the dot it
   * points at, and on a rail shorter than twice the cap the clamp interval collapses to a single
   * point, so EVERY card sits at the rail's midpoint whatever dot it describes.
   */
  cardHeightPx: Accessor<number>
  /** Kick off resolving a mark's preview (dot hover/focus or scrub-settle). Deduped upstream. */
  warmPreview?: (seq: bigint) => void
}

// Named ...Controller (not DotPreviewCard) to avoid clashing with the sibling DotPreviewCard
// presentation component in ChatScrollRailPreview -- this is the state machine that decides WHICH
// dot that component renders, not the component itself.
export interface DotPreviewController {
  /** The dot the single preview card describes: the scrub target while dragging, else hover/focus. */
  activeDot: Accessor<DotCluster | null>
  /** The card's rail-Y, clamped so a dot near the top/bottom doesn't clip past the wrapper. */
  cardTopPx: Accessor<number>
  /**
   * The POINTER reached a dot, or reached the open card: show that dot's card NOW, and cancel a
   * pending close. The card re-declares the dot it shows, so a card that a scrub opened stays
   * when the scrub ends.
   */
  openDot: (dot: DotCluster) => void
  /** The POINTER left a dot or the card: close after {@link POINTER_CLOSE_DELAY_MS}. */
  closeSoon: () => void
  /**
   * A GESTURE took the pointer over: drop the pointer channel at once, with no delay to wait out.
   * The rail calls this when a grab becomes a scrub, because the press started ON a dot and the
   * captured drag fires no pointerleave to release it -- so without this the card springs back to
   * the pressed dot the moment the drag ends, pointing at a dot the reader scrubbed away from.
   * Leaves the FOCUS channel alone: a keyboard reader's entitlement to their card survives a
   * gesture they did not make, and no blur is coming to restore it.
   */
  abandonDot: () => void
  /**
   * Keyboard focus reached a dot. Focus TAKES the card: it drops whatever the pointer holds,
   * because the reader steered to this dot and must read THIS dot's message, not the one their
   * parked cursor still rests on.
   */
  focusDot: (dot: DotCluster) => void
  /** Keyboard focus left the dot. Closes the focus channel at once. */
  blurDot: () => void
  /** Close every channel with no delay. For a teardown (the rail hides or unmounts). */
  closeNow: () => void
}

/**
 * Create the rail's dot-preview state machine (see the module header). Must be created within an
 * owner scope (it wires createEffect + onCleanup); ChatScrollRail creates it once at component top
 * level, exactly like its createRailMetrics / createDragReleaseHold siblings.
 */
export function createDotPreview(deps: DotPreviewDeps): DotPreviewController {
  // Two channels can hold the card open, because they end at different moments. POINTER: the dot
  // under the pointer, or the dot of the card the pointer moved onto; it closes on a delay. FOCUS:
  // the dot with keyboard focus; it opens and closes at once. One channel would make the pointer
  // leaving the card close a card that a focused dot is still entitled to -- a keyboard reader
  // would lose it by touching the mouse.
  //
  // They are separate, not independent: focus TAKES the card from the pointer (see focusDot), so
  // the pointer can only win a dot that focus has not claimed since. Without that, a cursor parked
  // on one dot -- the state the reader is in the moment after they use the rail -- masks the focus
  // channel outright, and tabbing along the dots shows one dot's card under every other dot's
  // focus ring.
  const [pointerDot, setPointerDot] = createSignal<DotCluster | null>(null)
  const [focusedDot, setFocusedDot] = createSignal<DotCluster | null>(null)

  // The pointer wins when both hold a dot: it is the input the reader is steering right now, and
  // focus already cleared the pointer channel when it took a dot of its own.
  const hoverOrFocusDot = createMemo(() => pointerDot() ?? focusedDot())

  // "The pointer let go; close unless it comes back" (see POINTER_CLOSE_DELAY_MS). Built on the
  // shared trailingDebounce primitive rather than a hand-rolled timer pair, exactly as the sibling
  // createThumbDrag does, so the arm/cancel bookkeeping is the one tested implementation the rest
  // of the app already uses.
  const closePointerDot = trailingDebounce(() => setPointerDot(null), POINTER_CLOSE_DELAY_MS)
  onCleanup(closePointerDot.cancel)

  const openDot = (dot: DotCluster) => {
    closePointerDot.cancel()
    setPointerDot(dot)
  }
  const closeSoon = () => closePointerDot()
  const abandonDot = () => {
    closePointerDot.cancel()
    setPointerDot(null)
  }
  const focusDot = (dot: DotCluster) => {
    abandonDot()
    setFocusedDot(dot)
  }
  const blurDot = () => setFocusedDot(null)
  const closeNow = () => {
    abandonDot()
    setFocusedDot(null)
  }

  // While dragging, the dot the thumb is currently over (nearest within range), so a scrub --
  // mouse OR touch -- reveals each marked message's preview as the thumb passes it. Null when
  // not dragging or the thumb is between dots.
  const scrubDot = createMemo(() => {
    const f = deps.drag()
    if (f === null)
      return null
    const rh = deps.railHeight()
    if (rh <= 0)
      return null
    const y = centerAxisY(f, rh, deps.thumbHeightPx()) // the thumb centre's rail-Y at this drag fraction
    return nearestDotWithin(deps.dots(), y, SCRUB_PREVIEW_RANGE_PX)
  })

  // The dot the single preview card describes: the scrub target while the thumb is over one (it
  // takes precedence, so hovering a dot mid-scrub cannot open a second card), else the pointer's
  // or the focused dot, which the close delay keeps open a moment longer (see
  // POINTER_CLOSE_DELAY_MS). One source -> one card, shown immediately (no open delay).
  //
  // A live drag still OWNS the card, but the rail ABANDONS the pointer channel when the grab
  // becomes a scrub (see abandonDot) rather than this memo masking that channel for as long as
  // `drag()` is non-null. Masking left two holes that clearing closes. The channel came back the
  // moment the mask lifted, so the card sprang onto the PRESSED dot after the reader had scrubbed
  // away from it -- the very bug the mask was added to fix, moved to the end of the gesture. And
  // `drag()` stays non-null for the whole post-release hold (createDragReleaseHold pins the thumb
  // until an out-of-window seek lands), so a reader who hovered a dot while that fetch was in
  // flight got no card at all.
  //
  // scrubDot re-runs on every drag frame (deps.drag() changes per animation frame while the thumb
  // moves), but it returns the SAME cluster between two dots, so this memo does not re-run with it.
  const activeDot = createMemo(() => scrubDot() ?? hoverOrFocusDot())

  // Keep a held dot anchored to the CURRENT cluster for its seq.
  //
  // The held cluster can change identity while still standing for the same seq: a streaming turn
  // ticks maxSeq and re-rounds its topPx by a pixel, or a fresh mark lands in its pixel and bumps
  // the count. <For> is reference-keyed, so it tears the held button down and mounts a new one --
  // and removing the element under the cursor fires no pointerleave, so the channel still holds
  // the STALE cluster. Re-anchor so the card the reader is looking at FOLLOWS the dot instead of
  // vanishing out from under them; clear only when that seq is genuinely gone (its message was
  // deleted or reseq'd).
  //
  // Writes each signal directly rather than through openDot/closeNow, so a re-anchor NEVER touches
  // the close timer: a streaming turn re-anchors repeatedly, and cancelling the timer on each one
  // would pin open a card the pointer already left.
  const reanchor = (
    held: DotCluster | null,
    currentDots: readonly DotCluster[],
    write: (next: DotCluster | null) => void,
  ) => {
    if (!held)
      return
    // The exact held cluster is still present -- leave the card alone.
    if (currentDots.some(d => dotClusterEqual(d, held)))
      return
    write(currentDots.find(d => d.seq === held.seq) ?? null)
  }
  createEffect(() => {
    const currentDots = deps.dots()
    reanchor(untrack(pointerDot), currentDots, setPointerDot)
    reanchor(untrack(focusedDot), currentDots, setFocusedDot)
  })

  // The card's rail-Y: centred on the active dot but clamped so a dot near the rail's top
  // or bottom doesn't push the card past the overflow-hidden wrapper and clip it.
  //
  // Clamped against the card's MEASURED half-height, not against half the max-height cap. The cap
  // is what the card may grow to, and most previews are far shorter: clamping a one-line card as
  // though it were 200px tall pushed it up to ~90px away from the dot it describes, and on a rail
  // shorter than twice the cap it left no interval at all, so every card was pinned to the rail
  // midpoint. Before the first measurement the height is 0, which clamps to nothing and puts the
  // card exactly on its dot -- the right answer for one frame, and better than a fixed offset.
  const cardTopPx = createMemo(() => {
    const d = activeDot()
    if (!d)
      return 0
    const rh = deps.railHeight()
    const half = Math.min(deps.cardHeightPx() / 2, rh / 2)
    return clamp(d.topPx, half, rh - half)
  })

  // Warm the active dot's preview so the card fills in. A HOVER/focus warms instantly; a SCRUB
  // (thumb drag) debounces -- dragging across a dense rail changes activeDot dot-by-dot, and
  // warming each would fire a GetAgentMessage RPC per out-of-window mark it crosses. The effect
  // tracks ONLY activeDot (its scrub source, scrubDot, dedups per dot, so it changes when the
  // thumb reaches a NEW dot, not on every drag pixel); the scrub-vs-hover classification reads
  // drag() untracked so a fraction change doesn't itself reset the debounce.
  //
  // The seq rides in a plain local beside the debounce rather than inside the timer callback,
  // because trailingDebounce takes no argument -- the same shape createThumbDrag uses for its own
  // debounced settle. Every arm overwrites it, which is exactly the coalescing this wants: the
  // pending warm always stands for the dot the thumb is on NOW.
  let pendingWarmSeq: bigint | null = null
  const warmScrubDot = trailingDebounce(() => {
    if (pendingWarmSeq !== null)
      deps.warmPreview?.(pendingWarmSeq)
  }, SCRUB_WARM_DEBOUNCE_MS)
  onCleanup(warmScrubDot.cancel)
  createEffect(() => {
    const d = activeDot()
    // A new active dot supersedes any pending scrub warm (the thumb moved on before it settled).
    warmScrubDot.cancel()
    if (!d)
      return
    if (untrack(() => deps.drag()) === null) {
      deps.warmPreview?.(d.seq) // hover/focus: instant
      return
    }
    // Scrub: warm only once the thumb has settled on this dot for the debounce window.
    pendingWarmSeq = d.seq
    warmScrubDot()
  })

  return { activeDot, cardTopPx, openDot, closeSoon, abandonDot, focusDot, blurDot, closeNow }
}
