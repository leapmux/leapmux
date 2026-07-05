import type { PreparedGeometry } from './chatScrollRailGeometry'
import { createRafCoalescer } from '~/lib/rafCoalesce'
import { centerAxisFraction, contentYForSeq, safeSeqNumber, seqNumberAtFraction } from './chatScrollRailGeometry'

// ---------------------------------------------------------------------------
// Scroll-rail thumb-drag controller
//
// Owns the pointer-capture + rAF-throttled move + release lifecycle of a rail thumb
// drag, extracted from ChatScrollRail so it can be unit-tested WITHOUT rendering a rail
// (the component just wires accessors + sinks and forwards pointerdown). The seq range and
// geometry are read through accessors -- called FRESH on each move/release -- because a
// live-tail advance mid-drag grows maxSeq, and the release must land against the same range
// the last move previewed against.
// ---------------------------------------------------------------------------

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
   * The pointer was released at rail fraction `fraction`. The controller does NOT clear the
   * drag preview here -- the owner holds the thumb at this position and clears it once the
   * seek has scrolled the view to match, so the thumb doesn't flash back to the pre-drag
   * position while an out-of-window seek fetches + lands.
   */
  onRelease: (fraction: number) => void
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
 * tracking; the drag ends on pointerup/pointercancel (seeking the mapped seq) or when the
 * caller invokes `cancel` (mid-drag unmount). All handlers are idempotent, so `cancel`
 * after a completed drag is a harmless no-op.
 */
export function createThumbDrag(deps: ThumbDragDeps): ThumbDragHandle {
  const { el, rect } = deps
  let capturedPointerId: number | null = null

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

  // Map a rail-relative pointer Y to the drag fraction, preview the thumb, and -- while the
  // target maps INSIDE the loaded window -- live-scroll the chat so the view tracks the thumb.
  const apply = (clientY: number) => {
    // Map the pointer to the drag fraction (offset-preserving; see fractionAt), preview the thumb
    // there, and -- while the mapped seq is in-window -- live-scroll so the view tracks the thumb.
    const f = fractionAt(clientY)
    deps.setDrag(f)
    // The absolute (fractional) seq under the thumb, via the SAME fail-closed mapping the resting
    // thumb geometry (fractionToSeq) uses -- so the drag and the rest of the rail agree on the
    // range->seq travel math. Null on a degenerate/unsafe range: preview the thumb, no live-scroll.
    const seqF = seqNumberAtFraction(f, deps.minSeq(), deps.maxSeq())
    if (seqF === null)
      return
    const wf = deps.windowFirstSeq()
    const wl = deps.windowLastSeq()
    const first = wf === undefined ? null : safeSeqNumber(wf)
    const last = wl === undefined ? null : safeSeqNumber(wl)
    if (first !== null && last !== null && seqF >= first && seqF <= last) {
      const cy = contentYForSeq(deps.prepared(), seqF)
      if (cy !== null)
        deps.previewScrollTo(cy)
    }
  }

  // rAF-coalesce pointermove through the shared helper: one apply() per frame with the latest
  // pointer Y, instead of re-hand-rolling the schedule-once + lastY bookkeeping.
  const moveCoalescer = createRafCoalescer<number>(apply)
  const onMove = (ev: PointerEvent) => moveCoalescer.push(ev.clientY)

  let ended = false
  const teardown = () => {
    moveCoalescer.abort()
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
    const f = fractionAt(ev.clientY)
    deps.onRelease(f) // owner keeps the preview until the seek settles -- see onRelease
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
      el.addEventListener('pointermove', onMove)
      el.addEventListener('pointerup', finish)
      el.addEventListener('pointercancel', abandon)
      apply(initialClientY)
    },
    cancel: abandon,
  }
}
