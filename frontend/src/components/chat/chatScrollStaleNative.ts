import { monotonicNow } from '~/lib/monotonicNow'
import { clampScrollTop, EDGE_INTENT_TOLERANCE_PX, REPIN_MIN_DELTA_PX } from './chatScrollGeometry'

// ---------------------------------------------------------------------------
// Stale-native-scroll translator
//
// Browser momentum can deliver one or two native scroll events in the OLD coordinate
// space after a large anchor re-pin moved scrollTop to preserve position across an
// older-page prepend. This unit owns that whole state machine -- ARMING a shift record
// when a programmatic write moves the viewport by more than a screen, EXTENDING or
// DISARMING it as further writes land, and TRANSLATING a recognized old-coordinate
// momentum event into the current coordinate space. Extracted from useChatScroll (the
// arm half lived in its write path and the translate half ~100 lines away in its scroll
// handler) so the two halves of one invariant live together and the proximity/direction
// classification is unit-testable without the whole hook.
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
 * exempt samples that are still very close to the old coordinate after a large re-pin.
 */
const STALE_NATIVE_OLD_COORDINATE_PROXIMITY_CLIENT_RATIO = 0.4
const STALE_NATIVE_OLD_COORDINATE_PROXIMITY_DELTA_RATIO = 0.15
/**
 * Floor (px) of the "near the old coordinate" window. Historically the fixed fling
 * render-ahead cap (1800px): a momentum event can land up to roughly a render-ahead
 * beyond the pre-repin position, so the window must cover that travel even on a short
 * pane (where clientHeight * 2 alone would under-reach). Kept as its own constant now
 * that the render-ahead cap is derived from the live pane height.
 */
const OLD_COORDINATE_WINDOW_MIN_PX = 1800

interface AnchorRepinShift {
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
  /** True when the current scroll event is our own programmatic write echoing back. */
  isProgrammaticEcho: () => boolean
  /**
   * Advance the hook's direction/last-position baseline after a translation, so the
   * next user delta measures from the post-repin position (mirrors the setter
   * createViewportRestore takes for the same baseline).
   */
  setLastScrollTopForDir: (top: number) => void
  /** Clock, injectable for tests. */
  now?: () => number
}

/**
 * Create the translator. `noteProgrammaticWrite` observes every programmatic scrollTop
 * write; `translate` is called at the top of the scroll handler and returns whether it
 * rewrote the event's position into the current coordinate space (the caller then marks
 * the follow-up echo and skips its own echo classification for this event).
 */
export function createStaleNativeScrollTranslator(deps: StaleNativeTranslatorDeps) {
  const now = deps.now ?? monotonicNow
  let recentAnchorRepinShift: AnchorRepinShift | undefined

  /**
   * Observe a programmatic write. An 'anchor-repin' write larger than a screen ARMS (or
   * extends) the shift record; a second repin that compensates the first back to a small
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
      source === 'anchor-repin'
      && beforeTop !== undefined
      && afterTop !== undefined
      && clientHeight > 0
    ) {
      const at = now()
      const previous = recentAnchorRepinShift
      const extendsRecentShift = previous !== undefined
        && at - previous.at <= STALE_NATIVE_SCROLL_TRANSLATE_MS
        && Math.abs(beforeTop - previous.afterTop) <= clientHeight
      const baseBeforeTop = extendsRecentShift ? previous.beforeTop : beforeTop
      const delta = afterTop - baseBeforeTop
      if (Math.abs(delta) > clientHeight) {
        recentAnchorRepinShift = {
          beforeTop: baseBeforeTop,
          afterTop,
          delta,
          clientHeight,
          dir: info.dir,
          at,
        }
      }
      else if (extendsRecentShift) {
        // A second anchor re-pin can compensate the large coordinate-space jump
        // that created the stale-native guard (for example, trimming rows far above
        // the viewport after an older-page prepend). Once the cumulative movement
        // is small again, native momentum events are already in the current
        // coordinate space; translating them by the obsolete prepend delta causes
        // the exact jump this guard exists to prevent.
        recentAnchorRepinShift = undefined
      }
    }
    else if (source !== 'stale-native-scroll-translate' && source !== 'anchor-repin') {
      recentAnchorRepinShift = undefined
    }
  }

  /**
   * Recognize a compositor-delayed OLD-COORDINATE momentum event after a large anchor
   * re-pin and translate it into the current coordinate space. Returns whether a
   * translation was applied (the caller only branches on that; the shift details stay
   * internal).
   */
  const translate = (): boolean => {
    const el = deps.getEl()
    const shift = recentAnchorRepinShift
    if (!el || !shift)
      return false
    const ageMs = now() - shift.at
    if (ageMs > STALE_NATIVE_SCROLL_TRANSLATE_MS) {
      recentAnchorRepinShift = undefined
      return false
    }
    // A direct drag or a real programmatic echo is already in the current coordinate
    // space. The stale case is only compositor-delayed momentum that lands near the
    // pre-repin coordinate after the anchor-repin write moved the viewport elsewhere.
    if (deps.isScrollInputActive() || deps.isProgrammaticEcho())
      return false

    const staleTop = el.scrollTop
    const oldCoordinateWindowPx = Math.max(shift.clientHeight * 2, OLD_COORDINATE_WINDOW_MIN_PX)
    const oldCoordinateDistance = Math.abs(staleTop - shift.beforeTop)
    const nearOldCoordinate = oldCoordinateDistance <= oldCoordinateWindowPx
    const farFromNewCoordinate = Math.abs(staleTop - shift.afterTop) > Math.abs(shift.delta) / 2
    // A delayed old-coordinate momentum event should continue from the pre-repin
    // position in the user's known scroll direction. A normal current-coordinate
    // scroll after a prepend re-pin can pass through the midpoint between afterTop
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
      recentAnchorRepinShift = undefined
      return false
    }

    const translatedTop = clampScrollTop(el, staleTop + shift.delta)
    if (Math.abs(translatedTop - staleTop) < REPIN_MIN_DELTA_PX)
      return false

    // Write without arming the echo marker yet: this event is still a USER momentum
    // event and must flow through the normal anchor/velocity/range path. The caller
    // marks the post-write position after classification so the browser's follow-up
    // scroll event for this assignment is ignored without swallowing the current event.
    el.scrollTop = translatedTop
    deps.setLastScrollTopForDir(shift.afterTop)
    return true
  }

  return { noteProgrammaticWrite, translate }
}

export type StaleNativeScrollTranslator = ReturnType<typeof createStaleNativeScrollTranslator>
