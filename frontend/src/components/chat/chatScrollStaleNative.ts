import { monotonicNow } from '~/lib/monotonicNow'
import { clampScrollTop, EDGE_INTENT_TOLERANCE_PX } from './chatScrollGeometry'

// ---------------------------------------------------------------------------
// Stale-native-scroll translator
//
// Browser momentum can deliver one or two native scroll events in the OLD coordinate
// space after a rebase of the layout shift moved scrollTop during a scroll -- the rebase
// that grows the scroll range near the reachable top for an older-page prepend. This
// unit owns that whole state machine -- ARMING a shift record when a rebase moves the
// viewport by more than a screen, EXTENDING or DISARMING it as further writes land, and
// TRANSLATING a recognized old-coordinate momentum event into the current coordinate
// space. Kept apart from useChatScroll so the two halves of one invariant live together
// and the proximity/direction classification is unit-testable without the whole hook.
// ---------------------------------------------------------------------------

/**
 * How long a shift record stays eligible to translate. Keep this window short: it is
 * only for compositor-delayed momentum echoes immediately after the coordinate-space
 * shift, not for later user navigation.
 */
const STALE_NATIVE_SCROLL_TRANSLATE_MS = 300
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
  at: number
}

export interface StaleNativeTranslatorDeps {
  /** The scroll container (a plain ref in the hook), or undefined before mount. */
  getEl: () => HTMLDivElement | undefined
  /** True while pointer/touch input is down (a drag is already in the current space). */
  isScrollInputActive: () => boolean
  /** True when the current `scroll` event did not move the position: the event of an own write. */
  isOwnWriteEvent: () => boolean
  /**
   * Advance the hook's direction/last-position baseline after a translation, so the
   * next user delta measures from the position after the rebase.
   */
  setLastScrollTopForDir: (top: number) => void
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

/**
 * Create the translator. `noteProgrammaticWrite` observes every programmatic scrollTop
 * write; `translate` is called at the top of the scroll handler and returns whether it
 * rewrote the event's position into the current coordinate space.
 */
export function createStaleNativeScrollTranslator(deps: StaleNativeTranslatorDeps) {
  const now = deps.now ?? monotonicNow
  let recentRebaseShift: RebaseShift | undefined

  /**
   * Observe a programmatic write. A 'rebase' write larger than a screen ARMS (or
   * extends) the shift record; a second rebase that compensates the first back to a small
   * cumulative movement DISARMS it; any other write source invalidates it outright (the
   * viewport is somewhere new, so the old coordinate space is no longer meaningful).
   */
  const noteProgrammaticWrite = (info: {
    source?: string
    beforeTop: number | undefined
    afterTop: number | undefined
    clientHeight: number
    dir: 'older' | 'newer'
  }) => {
    const { source, beforeTop, afterTop, clientHeight } = info
    if (
      source === 'rebase'
      && beforeTop !== undefined
      && afterTop !== undefined
      && clientHeight > 0
    ) {
      const at = now()
      const previous = recentRebaseShift
      const extendsRecentShift = previous !== undefined
        && at - previous.at <= STALE_NATIVE_SCROLL_TRANSLATE_MS
        && Math.abs(beforeTop - previous.afterTop) <= clientHeight
      const baseBeforeTop = extendsRecentShift ? previous.beforeTop : beforeTop
      const delta = afterTop - baseBeforeTop
      if (Math.abs(delta) > clientHeight) {
        recentRebaseShift = {
          beforeTop: baseBeforeTop,
          afterTop,
          delta,
          clientHeight,
          dir: info.dir,
          at,
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
    else if (source !== 'rebase') {
      recentRebaseShift = undefined
    }
  }

  /**
   * Recognize a compositor-delayed OLD-COORDINATE momentum event after a large rebase
   * and translate it into the current coordinate space. Returns whether a translation
   * was applied (the caller only branches on that; the shift details stay internal).
   */
  const translate = (): boolean => {
    const el = deps.getEl()
    const shift = recentRebaseShift
    if (!el || !shift)
      return false
    const ageMs = now() - shift.at
    if (ageMs > STALE_NATIVE_SCROLL_TRANSLATE_MS) {
      recentRebaseShift = undefined
      return false
    }
    // A direct drag or the event of an own write is already in the current coordinate
    // space. The stale case is only compositor-delayed momentum that lands near the
    // coordinate before the rebase after the rebase moved the viewport elsewhere.
    if (deps.isScrollInputActive() || deps.isOwnWriteEvent())
      return false

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
    deps.setLastScrollTopForDir(shift.afterTop)
    return true
  }

  return { noteProgrammaticWrite, translate }
}

export type StaleNativeScrollTranslator = ReturnType<typeof createStaleNativeScrollTranslator>
