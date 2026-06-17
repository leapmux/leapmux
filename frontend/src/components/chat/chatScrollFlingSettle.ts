import type { ScrollContext } from './useChatScroll'
import type { ScrollAnchor } from '~/stores/chatTypes'
import { REPIN_MIN_DELTA_PX } from './chatScrollGeometry'

/**
 * Idle gap (ms) after the last scroll event that marks a fling's end. Momentum
 * fires scroll events far more often than this, so a quiet period this long means
 * momentum has stopped and we can apply the deferred fling correction without
 * cancelling it. Long enough to clear any near-end momentum jitter; short enough
 * that the settle feels immediate.
 */
export const FLING_SETTLE_MS = 150

/**
 * The fling-settle unit: owns the deferred re-pin drift accumulated while the
 * user is flinging (writing scrollTop mid-fling cancels the browser's momentum).
 * Each suppressed shift is the amount the anchored content drifted that frame;
 * the running total is the fling's accumulated overshoot, applied in one write by
 * `settle` once momentum stops. Extracted from the scroll hook; the stable
 * scroll primitives it shares with the other extracted units come from the one
 * `ScrollContext` (mirroring createStickyBottom), with only the fling-specific
 * thunks passed as `extras`.
 */
export function createFlingSettle(ctx: ScrollContext, extras: {
  isRepinning: () => boolean
  getAnchor: () => ScrollAnchor | null
  captureAnchor: () => void
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
   * always computes `signed` against the CURRENT anchor, and a freshly-mounted row
   * measures SYNCHRONOUSLY in attachRow (useChatVirtualizer), so its height lands
   * in the offset map before the next scroll event captures a new anchor. A new
   * capture's `signed` therefore starts at 0 and only accumulates shifts that
   * happen AFTER it -- a prior capture's shift can never be re-measured against a
   * later anchor (the ResizeObserver flush only catches genuinely-later growth,
   * which is a separate, real deferral). So each capture contributes its own
   * unapplied correction exactly once; settle sums distinct deferrals (a near-end
   * momentum frame that adds no new shift keeps the earlier drift intact) rather
   * than re-counting a single shift across captures.
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
   * Apply the scroll correction deferred during a fling. Called on a debounce
   * after the last scroll event (FLING_SETTLE_MS of quiet => momentum stopped), so
   * writing scrollTop here can't cancel a fling. The per-frame corrections we
   * suppressed to keep momentum smooth are undone in one clean write, removing
   * the accumulated overshoot and landing on the content the fling aimed at.
   *
   * Drops the deferred drift rather than applying it when it no longer
   * corresponds to a scrolled-up fling: an animated scroll or an in-progress
   * re-pin establishes its own authoritative position, and a null anchor means
   * we've since stuck to the live tail. Stranding a stale value instead would let
   * a LATER fling's settle apply this gesture's drift on top of its own -- a jump.
   * (A trim that removes the anchored row mid-fling drops the drift at the
   * re-anchor site in repinToAnchor, so it never reaches here stale.)
   */
  const settle = () => {
    timer = undefined
    const el = ctx.getEl()
    if (!el || ctx.isAnimating() || extras.isRepinning() || extras.getAnchor() === null) {
      drift = 0
      return
    }
    if (Math.abs(drift) < REPIN_MIN_DELTA_PX) {
      drift = 0
      return
    }
    const target = el.scrollTop + drift
    drift = 0
    ctx.writeScrollTop(target)
    // Re-anchor to the settled position so a later geometry change re-pins from
    // here (not the pre-settle row), and mount the slice for the new position.
    extras.captureAnchor()
    ctx.refreshViewport()
  }

  /** (Re)arm the fling-end settle; each scroll event pushes it out, so it fires once momentum stops. */
  const schedule = () => {
    if (typeof setTimeout !== 'function')
      return
    cancel()
    timer = setTimeout(settle, FLING_SETTLE_MS)
  }

  /**
   * The deferred re-pin correction not yet written to scrollTop (0 when none is
   * pending). `el.scrollTop + pendingDrift()` is the scroll position the settle WILL
   * land on, so a consumer measuring scroll geometry mid-fling (e.g. the buffer
   * filler's older-buffer progress check) can read the intended position rather than
   * the suppressed one -- otherwise a productive older prepend whose corrective write
   * is deferred reads as no scrollTop growth and is mis-counted as no-progress.
   */
  const pendingDrift = () => drift

  return { accumulate, reset, rebase, schedule, cancel, pendingDrift }
}
