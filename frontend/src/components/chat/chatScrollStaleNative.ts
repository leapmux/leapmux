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
  beforeTop: number
  afterTop: number
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

/** A rebase that moved `scrollTop` while the scroller moved. */
export interface RebaseWrite {
  beforeTop: number
  afterTop: number
  clientHeight: number
  /** The direction of the reader's scroll at the rebase. */
  dir: 'older' | 'newer'
}

/**
 * Create the translator. Every own `scrollTop` write reports to it: `noteRebase` for a
 * rebase while the scroller moves, and `invalidate` for every other write. `translate` is
 * called at the top of the scroll handler.
 */
export function createStaleNativeScrollTranslator(deps: StaleNativeTranslatorDeps) {
  const now = deps.now ?? monotonicNow
  let recentRebaseShift: RebaseShift | undefined

  /**
   * Observe a rebase while the scroller moves. A rebase larger than a screen ARMS (or
   * extends) the shift record; a second rebase that compensates the first back to a small
   * cumulative movement DISARMS it. A hidden pane (`clientHeight` 0) changes nothing.
   */
  const noteRebase = ({ beforeTop, afterTop, clientHeight, dir }: RebaseWrite) => {
    if (clientHeight <= 0)
      return
    const at = now()
    const previous = recentRebaseShift
    const extendsRecentShift = previous !== undefined
      && at - previous.lastActivityAt <= STALE_NATIVE_SCROLL_QUIET_MS
      && Math.abs(beforeTop - previous.afterTop) <= clientHeight
    const baseBeforeTop = extendsRecentShift ? previous.beforeTop : beforeTop
    const delta = afterTop - baseBeforeTop
    if (Math.abs(delta) > clientHeight) {
      recentRebaseShift = {
        beforeTop: baseBeforeTop,
        afterTop,
        delta,
        clientHeight,
        dir,
        lastActivityAt: at,
      }
    }
    else if (extendsRecentShift) {
      // A second rebase can compensate the large coordinate-space jump that created
      // the stale-native guard (for example, a shift of the opposite sign after an
      // older-page prepend). Once the cumulative movement is small again, native
      // momentum events are already in the current coordinate space; translating
      // them by the obsolete prepend delta causes the exact jump this guard exists to
      // prevent.
      recentRebaseShift = undefined
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
   * Recognize a compositor-delayed OLD-COORDINATE momentum event after a large rebase
   * and translate it into the current coordinate space. Returns the position after the
   * rebase when it translated the event: the caller measures the move of the event from
   * there. Returns undefined when it did not.
   */
  const translate = (): number | undefined => {
    const el = deps.getEl()
    const shift = recentRebaseShift
    if (!el || !shift)
      return undefined
    const sampleAt = now()
    if (sampleAt - shift.lastActivityAt > STALE_NATIVE_SCROLL_QUIET_MS) {
      recentRebaseShift = undefined
      return undefined
    }
    // A direct drag or the event of an own write is already in the current coordinate
    // space. The stale case is only compositor-delayed momentum that lands near the
    // coordinate before the rebase after the rebase moved the viewport elsewhere.
    if (deps.isScrollInputActive() || deps.isOwnWriteEvent())
      return undefined

    const staleTop = el.scrollTop
    // Cover the full fling render-ahead (a stale momentum event lands up to ~one
    // render-ahead past the position before the rebase), plus a two-screen floor for very tall
    // panes where the hard-capped render-ahead is under two screens. Deriving the upper
    // bound from the live pane's render-ahead cap keeps it from under-reaching the way a
    // frozen constant did once the cap became pane-derived.
    const oldCoordinateWindowPx = Math.max(shift.clientHeight * 2, deps.flingOverscanCapPx(shift.clientHeight))
    const oldCoordinateDistance = Math.abs(staleTop - shift.beforeTop)
    const nearOldCoordinate = oldCoordinateDistance <= oldCoordinateWindowPx
    const farFromNewCoordinate = Math.abs(staleTop - shift.afterTop) > Math.abs(shift.delta) / 2
    // A delayed old-coordinate momentum event should continue from the position before
    // the rebase in the user's known scroll direction. A normal current-coordinate
    // scroll after a rebase for a prepend can pass through the midpoint between afterTop
    // and beforeTop; without this side check, that legitimate scroll gets
    // misclassified and translated back down.
    const movesFromOldCoordinateInIntentDirection = shift.dir === 'older'
      ? staleTop <= shift.beforeTop + EDGE_INTENT_TOLERANCE_PX
      : staleTop >= shift.beforeTop - EDGE_INTENT_TOLERANCE_PX
    const stillClearlyOldCoordinate = oldCoordinateDistance <= Math.min(
      shift.clientHeight * STALE_NATIVE_OLD_COORDINATE_PROXIMITY_CLIENT_RATIO,
      Math.abs(shift.delta) * STALE_NATIVE_OLD_COORDINATE_PROXIMITY_DELTA_RATIO,
    )
    if (!nearOldCoordinate || !farFromNewCoordinate || (!movesFromOldCoordinateInIntentDirection && !stillClearlyOldCoordinate)) {
      // Once a real native scroll arrives in the current coordinate space, the
      // compositor has caught up. Do not keep a stale shift armed long enough to
      // translate a later legitimate scroll that merely crosses the old midpoint.
      recentRebaseShift = undefined
      return undefined
    }

    const translatedTop = clampScrollTop(el, staleTop + shift.delta)
    if (Math.abs(translatedTop - staleTop) < MIN_TRANSLATION_PX)
      return undefined

    shift.lastActivityAt = sampleAt
    // This event is still a USER momentum event and must flow through the normal
    // velocity/range path, so the write is not recorded as an own write here. The scroll
    // handler then records the translated position as the last seen one, so the browser's
    // follow-up scroll event for this assignment reads as no move.
    el.scrollTop = translatedTop
    return shift.afterTop
  }

  return { noteRebase, invalidate, translate }
}

export type StaleNativeScrollTranslator = ReturnType<typeof createStaleNativeScrollTranslator>
