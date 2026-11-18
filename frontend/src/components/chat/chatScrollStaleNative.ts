import { monotonicNow } from '~/lib/monotonicNow'
import { clampScrollTop, EDGE_INTENT_TOLERANCE_PX } from './chatScrollGeometry'

// ---------------------------------------------------------------------------
// Stale-native-scroll translator
//
// Browser momentum can keep delivering native scroll events in the old coordinate space
// after a rebase moves scrollTop to make older rows reachable. This unit tracks the
// rebase delta and translates old-coordinate momentum until the browser reports a
// position in the current coordinates or the stream goes quiet.
// ---------------------------------------------------------------------------

/**
 * How long the translator keeps a shift after a rebase or the last translated event.
 * Safari can keep reporting old coordinates longer than this interval during a fling.
 * Each translation renews the interval because the browser is still using those coordinates.
 */
const STALE_NATIVE_SCROLL_QUIET_MS = 300
/**
 * A stale old-coordinate momentum sample can land slightly on the "wrong" side of
 * beforeTop after event coalescing, rubber-band bounce-back, or a quick direction
 * reversal. Keep the direction check for midpoint/current-coordinate protection, but
 * exempt samples that are still very close to the old coordinate after a large rebase.
 */
const STALE_NATIVE_OLD_COORDINATE_PROXIMITY_CLIENT_RATIO = 0.4
const STALE_NATIVE_OLD_COORDINATE_PROXIMITY_DELTA_RATIO = 0.15
/**
 * Below this, a translation moves the view by less than a pixel, so it is not worth a
 * write: a write can end the momentum.
 */
const MIN_TRANSLATION_PX = 1

interface RebaseShift {
  oldTop: number
  currentTop: number
  delta: number
  clientHeight: number
  dir: 'older' | 'newer'
  lastActivityAt: number
}

export interface StaleNativeTranslatorDeps {
  /** The scroll container (a plain ref in the hook), or undefined before mount. */
  getEl: () => HTMLDivElement | undefined
  /** True while pointer/touch input is down (a drag is already in the current space). */
  isScrollInputActive: () => boolean
  /** True when the current `scroll` event did not move the position: the event of an own write. */
  isOwnWriteEvent: () => boolean
  /**
   * The fling render-ahead cap (px) for a given pane height (see
   * useChatScroll.flingOverscanCapPx). A compositor-delayed momentum event can land up
   * to roughly a render-ahead beyond the position before the rebase, so the "near the old
   * coordinate" window must cover that travel. Sharing the SAME pane-derived cap the
   * fling overscan uses keeps the two coupled: a taller pane's larger render-ahead
   * widens this window in lockstep, instead of a frozen constant under-reaching it.
   */
  flingOverscanCapPx: (clientHeight: number) => number
  /** Clock, injectable for tests. */
  now?: () => number
}

/** The actual browser positions on either side of a rebase write. */
export interface RebaseWrite {
  beforeTop: number
  afterTop: number
  clientHeight: number
  /** The direction of the reader's scroll at the rebase. */
  dir: 'older' | 'newer'
}

/**
 * Create the translator. Report each rebase with `noteRebase`, and each placement or
 * fresh gesture with `invalidate`. Call `translate` before measuring a scroll event.
 */
export function createStaleNativeScrollTranslator(deps: StaleNativeTranslatorDeps) {
  const now = deps.now ?? monotonicNow
  let recentRebaseShift: RebaseShift | undefined

  /**
   * A rebase during motion can start a shift record. A settling rebase can extend
   * an active record without renewing its deadline. Clear the record when the
   * cumulative shift fits on one screen. A hidden pane leaves the record alone.
   */
  const noteRebase = ({ beforeTop, afterTop, clientHeight, dir }: RebaseWrite, atRest = false) => {
    if (clientHeight <= 0)
      return
    const at = now()
    const recent = recentRebaseShift
    const previous = recent !== undefined
      && at - recent.lastActivityAt <= STALE_NATIVE_SCROLL_QUIET_MS
      && Math.abs(beforeTop - recent.currentTop) <= clientHeight
      ? recent
      : undefined
    // Only coordinate writes contribute to the delta. The reader can move between rebases.
    const delta = afterTop - beforeTop + (previous?.delta ?? 0)
    // Settling the layout does not supply new evidence of compositor motion.
    const lastActivityAt = atRest ? previous?.lastActivityAt : at
    if (lastActivityAt === undefined || Math.abs(delta) <= clientHeight) {
      recentRebaseShift = undefined
      return
    }
    recentRebaseShift = {
      oldTop: previous === undefined
        ? beforeTop
        : previous.oldTop + (beforeTop - previous.currentTop),
      currentTop: afterTop,
      delta,
      clientHeight,
      dir,
      lastActivityAt,
    }
  }

  /**
   * Forget the shift record: an own write other than a rebase during a scroll placed the
   * view somewhere new, so the old coordinate space is no longer meaningful.
   */
  const invalidate = () => {
    recentRebaseShift = undefined
  }

  /**
   * Translate a compositor-delayed momentum event into the current coordinate space.
   * Returns whether it corrected the position. The caller measures travel from the
   * position it recorded before this event.
   */
  const translate = (): boolean => {
    const el = deps.getEl()
    const shift = recentRebaseShift
    if (!el || !shift)
      return false
    const sampleAt = now()
    if (sampleAt - shift.lastActivityAt > STALE_NATIVE_SCROLL_QUIET_MS) {
      recentRebaseShift = undefined
      return false
    }
    // A direct drag or the event of an own write is already in the current coordinate
    // space. The stale case is only compositor-delayed momentum that lands near the
    // coordinate before the rebase after the rebase moved the viewport elsewhere.
    if (deps.isOwnWriteEvent())
      return false
    if (deps.isScrollInputActive()) {
      invalidate()
      return false
    }

    const staleTop = el.scrollTop
    // Cover the full fling render-ahead (a stale momentum event lands up to ~one
    // render-ahead past the position before the rebase), plus a two-screen floor for very tall
    // panes where the hard-capped render-ahead is under two screens. Deriving the upper
    // bound from the live pane's render-ahead cap keeps it from under-reaching the way a
    // frozen constant did once the cap became pane-derived.
    const oldCoordinateWindowPx = Math.max(shift.clientHeight * 2, deps.flingOverscanCapPx(shift.clientHeight))
    const oldCoordinateDistance = Math.abs(staleTop - shift.oldTop)
    const nearOldCoordinate = oldCoordinateDistance <= oldCoordinateWindowPx
    const farFromNewCoordinate = Math.abs(staleTop - shift.currentTop) > Math.abs(shift.delta) / 2
    // A delayed old-coordinate momentum event should continue from the position before
    // the last old-coordinate sample in the user's known scroll direction. A normal current-coordinate
    // scroll after a rebase for a prepend can pass through the midpoint between afterTop
    // and beforeTop; without this side check, that legitimate scroll gets
    // misclassified and translated back down.
    const movesFromOldCoordinateInIntentDirection = shift.dir === 'older'
      ? staleTop <= shift.oldTop + EDGE_INTENT_TOLERANCE_PX
      : staleTop >= shift.oldTop - EDGE_INTENT_TOLERANCE_PX
    const stillClearlyOldCoordinate = oldCoordinateDistance <= Math.min(
      shift.clientHeight * STALE_NATIVE_OLD_COORDINATE_PROXIMITY_CLIENT_RATIO,
      Math.abs(shift.delta) * STALE_NATIVE_OLD_COORDINATE_PROXIMITY_DELTA_RATIO,
    )
    if (!nearOldCoordinate || !farFromNewCoordinate || (!movesFromOldCoordinateInIntentDirection && !stillClearlyOldCoordinate)) {
      // Once a real native scroll arrives in the current coordinate space, the
      // compositor has caught up. Do not keep a stale shift armed long enough to
      // translate a later legitimate scroll that merely crosses the old midpoint.
      recentRebaseShift = undefined
      return false
    }

    const translatedTop = clampScrollTop(el, staleTop + shift.delta)
    if (Math.abs(translatedTop - staleTop) < MIN_TRANSLATION_PX)
      return false

    // This event is still a USER momentum event and must flow through the normal
    // velocity/range path, so the write is not recorded as an own write here. The scroll
    // handler then records the translated position as the last seen one, so the browser's
    // follow-up scroll event for this assignment reads as no move.
    el.scrollTop = translatedTop
    shift.oldTop = staleTop
    shift.currentTop = el.scrollTop
    shift.lastActivityAt = sampleAt
    return true
  }

  return { noteRebase, invalidate, translate }
}

export type StaleNativeScrollTranslator = ReturnType<typeof createStaleNativeScrollTranslator>
