import type { ScrollContext } from './useChatScroll'
import type { ScrollAnchor } from '~/stores/chatTypes'
import { REPIN_MIN_DELTA_PX } from './chatScrollGeometry'

/**
 * Idle gap (ms) after the last scroll event that marks a fling's end. Momentum
 * fires scroll events far more often than this, so a quiet period this long means
 * momentum has stopped and any drift suppressed during the fling can be accepted
 * as the user's current visual position. Long enough to clear any near-end
 * momentum jitter; short enough that stale drift is dropped promptly.
 */
export const FLING_SETTLE_MS = 150

/**
 * The fling-settle unit: owns the deferred re-pin drift accumulated while the
 * user is flinging (writing scrollTop mid-fling cancels the browser's momentum).
 * Each suppressed shift is the amount the anchored content drifted that frame;
 * the running total tells `settle` whether any meaningful drift was accepted.
 * Settle recaptures the anchor at the current viewport instead of writing the
 * drift back as one post-momentum snap. Extracted from the scroll hook; the
 * stable scroll primitives it shares with the other extracted units come from
 * the one `ScrollContext` (mirroring createStickyBottom), with only the
 * fling-specific thunks passed as `extras`.
 */
export function createFlingSettle(ctx: ScrollContext, extras: {
  isRepinning: () => boolean
  getAnchor: () => ScrollAnchor | null
  captureAnchor: () => void
  hasDeferredWork?: () => boolean
  onSettleQuiet?: () => void
}) {
  let drift = 0
  // The absolute correction already folded into `drift` for the CURRENT anchor
  // capture. repinToAnchor passes the full correction-from-capture each time, and
  // multiple measurements can fire it several times for one capture without
  // scrollTop moving in between -- so each call re-reads the cumulative shift, not
  // just its own delta. Banking the increment (signed - bankedThisCapture) keeps
  // those repins from summing the same shift repeatedly (a settle overshoot).
  let bankedThisCapture = 0
  let timer: ReturnType<typeof setTimeout> | undefined

  const cancel = () => {
    if (timer !== undefined) {
      clearTimeout(timer)
      timer = undefined
    }
  }

  /**
   * Defer a re-pin correction the geometry re-pin suppressed mid-fling. `signed`
   * is the full correction from the captured anchor position to now; only the
   * increment since the last call (same capture) is banked, so two measurements
   * shifting the anchor within one scroll event don't double-count.
   */
  const accumulate = (signed: number) => {
    drift += signed - bankedThisCapture
    bankedThisCapture = signed
  }

  /**
   * Start a fresh per-capture baseline (called when the anchor is re-captured at
   * a new scroll position). Preserves `drift` banked from prior captures while
   * making the next accumulate measure its increment from zero again.
   *
   * Preserving cross-capture drift is correct, NOT a double-count: repinToAnchor
   * always computes `signed` against the CURRENT anchor. Normal geometry commits
   * therefore start each new capture at zero and only accumulate shifts that happen
   * AFTER it. During native momentum, visible-row measurements may be queued instead
   * of committed; settle captures the accepted viewport before releasing that queued
   * geometry, so a prior capture's drift is not replayed against the later anchor.
   * Each capture contributes its own accepted drift exactly once.
   */
  const rebase = () => {
    bankedThisCapture = 0
  }

  /**
   * Drop the deferred drift -- a full re-align (immediate write / settle)
   * superseded it, or the row it was measured against was trimmed away mid-fling
   * (repinToAnchor re-anchors and calls this), so the accumulated correction no
   * longer corresponds to any on-screen row.
   */
  const reset = () => {
    drift = 0
    bankedThisCapture = 0
  }

  /**
   * Accept the scroll correction and/or visible-measurement commits deferred during
   * a fling. Called on a debounce after the last scroll event (FLING_SETTLE_MS of
   * quiet => momentum stopped).
   * The user has already watched the viewport coast to its current position, so
   * writing the accumulated drift here is a visible post-fling snap. Instead,
   * recapture the anchor at the current viewport and drop the old drift.
   *
   * Drops the deferred drift rather than applying it when it no longer
   * corresponds to a scrolled-up fling: an animated scroll or an in-progress
   * re-pin establishes its own authoritative position, and a null anchor means
   * we've since stuck to the live tail. Stranding a stale value instead would let
   * a LATER fling's settle re-anchor against this gesture's stale drift -- a jump.
   * (A trim that removes the anchored row mid-fling drops the drift at the
   * re-anchor site in repinToAnchor, so it never reaches here stale.)
   */
  const settle = () => {
    timer = undefined
    const el = ctx.getEl()
    if (!el || ctx.isAnimating() || extras.isRepinning() || extras.getAnchor() === null) {
      reset()
      extras.onSettleQuiet?.()
      return
    }
    if (Math.abs(drift) < REPIN_MIN_DELTA_PX && !(extras.hasDeferredWork?.() ?? false)) {
      reset()
      extras.onSettleQuiet?.()
      return
    }
    reset()
    // Re-anchor to the accepted visual position so a later geometry change re-pins
    // from here instead of replaying the dropped drift against the old anchor.
    extras.captureAnchor()
    extras.onSettleQuiet?.()
  }

  /** (Re)arm the fling-end settle; each scroll event pushes it out, so it fires once momentum stops. */
  const schedule = () => {
    if (typeof setTimeout !== 'function')
      return
    cancel()
    timer = setTimeout(settle, FLING_SETTLE_MS)
  }

  return { accumulate, reset, rebase, schedule, cancel }
}
