import type { PreparedGeometry } from './chatScrollRailGeometry'
import { createRafCoalescer } from '~/lib/rafCoalesce'
import { centerAxisFraction, contentYForSeq, fractionToSeq, safeSeqNumber, seqNumberAtFraction } from './chatScrollRailGeometry'

// ---------------------------------------------------------------------------
// Scroll-rail thumb-drag controller
//
// Owns the pointer-capture + rAF-throttled move + release lifecycle of a rail thumb
// drag, extracted from ChatScrollRail so it can be unit-tested WITHOUT rendering a rail
// (the component just wires accessors + sinks and forwards pointerdown). The seq range and
// geometry are read through accessors -- called FRESH on each move/release -- because a
// live-tail advance mid-drag grows maxSeq, and the release must land against the same range
// the last move previewed against.
//
// One grab drives BOTH gestures the rail offers, told apart by how far the pointer travels:
// a press that stays within `engageSlopPx` is a TAP (the owner's press action stands alone),
// and the first move past it ENGAGES a scrub that tracks the pointer until release.
// ---------------------------------------------------------------------------

/**
 * Pointer travel (px) before a press becomes a scrub. A press whose action already jumped
 * (a track or dot press) needs this slop: a finger jitters a few pixels while it lifts, and
 * on a long history 2px of rail is hundreds of seqs -- so without it a tap would land
 * somewhere other than where it jumped. A thumb grab passes 0: its press has no competing
 * tap meaning, and a conventional scrollbar thumb must not wait before it moves.
 */
export const SCRUB_ENGAGE_SLOP_PX = 6

/**
 * How long the thumb must settle on an OUT-OF-WINDOW seq mid-scrub before the rail seeks to
 * it. In-window targets live-scroll on every frame for free; an out-of-window one costs a
 * page fetch plus a window swap, so only a rest pulls one. Deliberately longer than
 * SCRUB_WARM_DEBOUNCE_MS (see ./chatDotPreview): the cheap message preview resolves first,
 * and only a longer rest moves the view.
 */
export const SCRUB_SEEK_DEBOUNCE_MS = 200

export interface ThumbDragDeps {
  /** The rail element the pointer was captured on (also the move/up listener target). */
  el: HTMLElement
  /** The rail's bounding rect at grab time; drag Y maps against its top/height. */
  rect: DOMRect
  /**
   * The resting thumb's TOP pixel at grab time. The drag holds the pointer's offset WITHIN the
   * thumb (grabY - grabThumbTopPx) so the thumb tracks the pointer FROM where it was grabbed
   * rather than snapping its centre onto the cursor -- i.e. no jump-on-grab (conventional
   * scrollbar feel), which matters most when grabbing near the thumb's edge on a tall history.
   */
  grabThumbTopPx: number
  /**
   * Pointer travel (px) before this grab becomes a scrub; 0 (the default) engages on the first
   * move. See {@link SCRUB_ENGAGE_SLOP_PX} for which press passes which value.
   */
  engageSlopPx?: number
  /** Live whole-history seq range (read each move/release). */
  minSeq: () => bigint
  maxSeq: () => bigint
  /** Live loaded-window first/last SERVER seq; live-scroll only fires while inside it. */
  windowFirstSeq: () => bigint | undefined
  windowLastSeq: () => bigint | undefined
  /** Live prepared geometry for the in-window content-Y mapping. */
  prepared: () => PreparedGeometry
  /** Live thumb height (px); the drag maps the pointer onto the thumb-centre axis with it. */
  thumbHeightPx: () => number
  /** Set/clear the drag-preview thumb fraction (null clears the preview). */
  setDrag: (fraction: number | null) => void
  /** Guard-marked programmatic scroll write for in-window live-scroll. */
  previewScrollTo: (top: number) => void
  /**
   * Seek to a seq the thumb settled on while that seq is OUTSIDE the loaded window -- the
   * out-of-window counterpart of previewScrollTo, debounced by
   * {@link SCRUB_SEEK_DEBOUNCE_MS}. Without it a scrub across a long history moves the thumb
   * and the dot preview while the transcript stands still, because there is nothing loaded to
   * scroll to. Optional; omit it to keep the preview-only behaviour.
   */
  scrubSeek?: (seq: bigint) => void
  /**
   * The grab became a scrub: a pointermove arrived past `engageSlopPx`. Fires at most once per
   * grab, and never for a release that engages on its own coalesced position (a release
   * supersedes the press action by itself). Lets the owner drop the press action it took on
   * pointerdown -- the reader has taken manual control of the thumb.
   */
  onEngage?: () => void
  /**
   * The pointer was released at rail fraction `fraction`; `engaged` reports whether the grab
   * ever became a scrub (see `engageSlopPx`), so the owner can tell a tap -- whose press
   * action already stands -- from a scrub that must land where it was released. The controller
   * does NOT clear the drag preview here -- the owner holds the thumb at this position and
   * clears it once the seek has scrolled the view to match, so the thumb doesn't flash back to
   * the pre-drag position while an out-of-window seek fetches + lands.
   */
  onRelease: (fraction: number, engaged: boolean) => void
  /**
   * The pointer lifecycle ended (release, cancel, or an explicit cancel() from an unmount):
   * the controller has torn down its listeners + rAF. Fires at most once per drag, BEFORE
   * onRelease on a deliberate release, so the owner can free a "drag active" guard without
   * disturbing the release's preview-hold. Optional. Idempotent teardown means a normal
   * release followed by cancel() won't fire it twice.
   */
  onEnd?: () => void
}

export interface ThumbDragHandle {
  /** Begin the drag: capture the pointer, wire listeners, and apply the initial position. */
  start: (pointerId: number, initialClientY: number) => void
  /** Stop the rAF/listeners and clear the drag preview (an unmount landing mid-drag). */
  cancel: () => void
}

/**
 * Create a thumb-drag controller for one grab. `start` captures the pointer and begins
 * tracking; the drag ends on pointerup (reporting the release fraction and whether the grab
 * ever engaged), on pointercancel (no seek at all), or when the caller invokes `cancel`
 * (mid-drag unmount). All handlers are idempotent, so `cancel` after a completed drag is a
 * harmless no-op.
 */
export function createThumbDrag(deps: ThumbDragDeps): ThumbDragHandle {
  const { el, rect } = deps
  const engageSlopPx = deps.engageSlopPx ?? 0
  let capturedPointerId: number | null = null

  // The press Y, and whether the pointer has travelled far enough from it to become a scrub.
  // Until it has, every move is dropped: the preview stays where the press put it, nothing
  // live-scrolls, and no settle seek is armed -- so a tap keeps the meaning its press gave it.
  let pressClientY = 0
  let engaged = false

  // The thumb height + within-thumb grab offset, both frozen at grab (start) so the
  // pointer->fraction axis stays internally consistent for the whole drag: the offset is measured
  // against THIS height, and mixing it with a live re-read (a mid-drag rail resize) would map the
  // pointer against a different height than the one the offset was anchored to. The RENDERED thumb
  // still tracks the live height -- the fraction is normalised [0,1], so it projects correctly onto
  // whatever geometry the render holds. grabOffsetPx = grabY - grabThumbTopPx; holding it is what
  // makes the thumb track the pointer FROM where it was grabbed rather than recentering on the
  // cursor (no jump-on-grab).
  let grabThumbHeightPx = 0
  let grabOffsetPx = 0

  // Map a rail-relative pointer Y to the drag fraction: place the thumb TOP so the pointer keeps
  // its within-thumb grab offset, then project the thumb CENTRE onto the centre axis (the same
  // axis the dots + track are drawn on). Shared by the live move (apply) and the release (finish)
  // so the two can't drift on the geometry they map the pointer against.
  const fractionAt = (clientY: number) => {
    const thumbTop = clientY - rect.top - grabOffsetPx
    return centerAxisFraction(thumbTop + grabThumbHeightPx / 2, rect.height, grabThumbHeightPx)
  }

  const releasePointerCapture = () => {
    if (capturedPointerId === null)
      return
    try {
      el.releasePointerCapture?.(capturedPointerId)
    }
    catch {
      // The browser may have already dropped capture on pointercancel/pointerup.
    }
    capturedPointerId = null
  }

  // Whether the (fractional) seq under the thumb lies INSIDE the loaded window. This is the test
  // that splits the two ways a scrub moves the view: in-window the drag live-scrolls on every
  // frame for free, out of it only a settled thumb seeks (armScrubSeek), because that costs a page
  // fetch. Fail-closed on an absent/unsafe window bound: report "outside", so the reader gets the
  // debounced seek rather than a thumb that moves over a transcript that never does.
  const insideWindow = (seqF: number): boolean => {
    const wf = deps.windowFirstSeq()
    const wl = deps.windowLastSeq()
    const first = wf === undefined ? null : safeSeqNumber(wf)
    const last = wl === undefined ? null : safeSeqNumber(wl)
    return first !== null && last !== null && seqF >= first && seqF <= last
  }

  // The debounced out-of-window seek: the thumb must rest on a target for
  // SCRUB_SEEK_DEBOUNCE_MS before the rail pulls the page it needs. The last seq actually sought
  // is remembered so a rest, a jitter, and a second rest on the SAME seq fetch once.
  let scrubSeekTimer: ReturnType<typeof setTimeout> | undefined
  let lastScrubSeekSeq: bigint | null = null

  const cancelScrubSeek = () => {
    if (scrubSeekTimer !== undefined) {
      clearTimeout(scrubSeekTimer)
      scrubSeekTimer = undefined
    }
  }

  const fireScrubSeek = (fraction: number) => {
    const min = deps.minSeq()
    const max = deps.maxSeq()
    const seqF = seqNumberAtFraction(fraction, min, max)
    // Re-test against the LIVE window: an earlier rest's seek (or an edge page load) can swap the
    // window while this timer waits, which makes the target in-window and this fetch pointless.
    if (seqF === null || insideWindow(seqF))
      return
    const seq = fractionToSeq(fraction, min, max)
    if (seq === null || seq === lastScrubSeekSeq)
      return
    lastScrubSeekSeq = seq
    deps.scrubSeek?.(seq)
  }

  const armScrubSeek = (fraction: number) => {
    if (deps.scrubSeek === undefined)
      return
    // Restart the wait on every move, so a thumb sweeping across a dense history seeks only where
    // it STOPS -- the fly-over positions coalesce into one fetch, exactly as the dot-preview warm
    // debounce coalesces its fly-over previews (see ./chatDotPreview).
    cancelScrubSeek()
    scrubSeekTimer = setTimeout(() => {
      scrubSeekTimer = undefined
      fireScrubSeek(fraction)
    }, SCRUB_SEEK_DEBOUNCE_MS)
  }

  // Map a rail-relative pointer Y to the drag fraction, preview the thumb, and move the view to
  // match: live-scroll while the target is in-window, else arm the debounced out-of-window seek.
  const apply = (clientY: number) => {
    // Map the pointer to the drag fraction (offset-preserving; see fractionAt) and preview the
    // thumb there.
    const f = fractionAt(clientY)
    deps.setDrag(f)
    // The absolute (fractional) seq under the thumb, via the SAME fail-closed mapping the resting
    // thumb geometry (fractionToSeq) uses -- so the drag and the rest of the rail agree on the
    // range->seq travel math. Null on a degenerate/unsafe range: preview the thumb, no live-scroll.
    const seqF = seqNumberAtFraction(f, deps.minSeq(), deps.maxSeq())
    if (seqF === null)
      return
    if (insideWindow(seqF)) {
      // Back inside loaded rows: drop a pending out-of-window seek, which would now fetch a page
      // for a position the reader already scrubbed away from.
      cancelScrubSeek()
      const cy = contentYForSeq(deps.prepared(), seqF)
      if (cy !== null)
        deps.previewScrollTo(cy)
      return
    }
    // Only a SCRUB seeks. The initial apply() at start() runs before the grab engages, so the
    // press keeps whatever meaning the owner gave it (a track/dot press jumps on its own; a thumb
    // grab jumps nowhere) instead of this arming a second, later seek behind it.
    if (engaged)
      armScrubSeek(f)
  }

  /** Whether the pointer travelled far enough from the press to count as a scrub, not a tap. */
  const movedPastSlop = (clientY: number) => Math.abs(clientY - pressClientY) > engageSlopPx

  // rAF-coalesce pointermove through the shared helper: one apply() per frame with the latest
  // pointer Y, instead of re-hand-rolling the schedule-once + lastY bookkeeping.
  const moveCoalescer = createRafCoalescer<number>(apply)
  const onMove = (ev: PointerEvent) => {
    // Below the slop the grab is armed but not engaged: drop the move whole, so the preview stays
    // where the press put it and the release still reads as a tap.
    if (!engaged) {
      if (!movedPastSlop(ev.clientY))
        return
      engaged = true
      deps.onEngage?.()
    }
    moveCoalescer.push(ev.clientY)
  }

  let ended = false
  const teardown = () => {
    moveCoalescer.abort()
    cancelScrubSeek()
    releasePointerCapture()
    el.removeEventListener('pointermove', onMove)
    el.removeEventListener('pointerup', finish)
    el.removeEventListener('pointercancel', abandon)
    // Fire onEnd exactly once, even if teardown runs again (a normal release then a later
    // cancel() from an unmount): the owner's "drag active" guard must clear only once.
    if (!ended) {
      ended = true
      deps.onEnd?.()
    }
  }

  // Hoisted function declarations so `teardown` (declared above) can reference them without a
  // forward-declared `let` -- teardown detaches them and each tears down. Both run as pointer
  // listeners, so reading deps at call time is correct.

  // pointerup: a deliberate release -> seek. Runs as a pointerup listener.
  function finish(ev: PointerEvent) {
    teardown()
    // Test the release position against the slop as well, rather than trusting `engaged` alone: a
    // pointerup can carry a position no pointermove ever reported (the browser coalesces the last
    // move into it), and dropping that travel would silently turn a real drag into a tap.
    const released = engaged || movedPastSlop(ev.clientY)
    // A tap reports the PRESS position, not the release one: the pointer may have drifted within
    // the slop while the finger lifted, and the owner pins the thumb at this fraction -- so
    // reading the release Y would slide the thumb off the point the press jumped to.
    const f = fractionAt(released ? ev.clientY : pressClientY)
    deps.onRelease(f, released) // owner keeps the preview until the seek settles -- see onRelease
  }

  // pointercancel: the interaction was aborted (a system/edge gesture stole the pointer,
  // common on touch) -- do NOT seek; just drop the preview and stop tracking, as if the drag
  // never happened.
  function abandon() {
    teardown()
    deps.setDrag(null)
  }

  return {
    start(pointerId, initialClientY) {
      try {
        el.setPointerCapture(pointerId)
        capturedPointerId = pointerId
      }
      catch {
        teardown()
        deps.setDrag(null)
        return
      }
      // Freeze the grab-time thumb height and anchor the within-thumb grab offset BEFORE the first
      // apply() so it holds the thumb where it rests (no jump) and then tracks the pointer delta.
      grabThumbHeightPx = deps.thumbHeightPx()
      grabOffsetPx = (initialClientY - rect.top) - deps.grabThumbTopPx
      // The slop is measured from HERE, and the release reads it back for a tap's fraction.
      pressClientY = initialClientY
      el.addEventListener('pointermove', onMove)
      el.addEventListener('pointerup', finish)
      el.addEventListener('pointercancel', abandon)
      apply(initialClientY)
    },
    cancel: abandon,
  }
}
