// ---------------------------------------------------------------------------
// Shared scroll-geometry primitives
//
// Pure helpers + the cross-cutting edge/re-pin thresholds that the scroll hook
// (useChatScroll) AND its extracted factories (chatScroll*.ts) both read. Kept in
// one leaf module so a factory can reuse them without importing the hook (which
// would be a cycle) and so the "how far can this scroll / is it at an edge"
// formulas live in exactly one place.
// ---------------------------------------------------------------------------

/** Below this, a re-pin's scrollTop correction is a no-op not worth a write (which can interrupt momentum). */
export const REPIN_MIN_DELTA_PX = 1
/**
 * Slack (px) for "is the viewport at the very top / bottom edge?" when deciding an
 * explicit page-older / page-newer intent. A re-pin or clamp can leave scrollTop a
 * sub-pixel off the edge (fractional DPI / browser zoom report non-integer
 * scrollTop), so an exact `=== 0` top test would never fire while the tolerant
 * bottom test does -- the two edges must use the SAME slack or they behave
 * asymmetrically at fractional offsets.
 */
export const EDGE_INTENT_TOLERANCE_PX = 1

/** High-res monotonic clock (perf.now), falling back to Date.now where absent. */
export function monotonicNow(): number {
  return typeof performance !== 'undefined' ? performance.now() : Date.now()
}

/**
 * Infer the scroll direction from a scrollTop delta: 'older' when the position
 * moved UP (toward older history), 'newer' when it moved DOWN, null when it
 * didn't move. Lets a non-wheel gesture -- scrollbar drag, touch scroll, or a
 * momentum fling, which fire only `scroll` events -- keep lastScrollDir current,
 * which handleWheel/handleKeyDown only do for wheel/keys. Pure so the policy can
 * be unit-tested at its boundaries.
 */
export function inferScrollDirection(prevTop: number, curTop: number): 'older' | 'newer' | null {
  if (curTop < prevTop)
    return 'older'
  if (curTop > prevTop)
    return 'newer'
  return null
}

/**
 * The maximum scrollTop for an element: the scrollable overflow, floored at 0 so
 * content shorter than the viewport yields 0 rather than a negative bound. The
 * one definition the sticky-bottom rebase, the anchor-restore raw fallback, and
 * clampScrollTop share, so the "how far can this scroll" formula lives once.
 */
export function maxScrollTopOf(el: HTMLDivElement): number {
  return Math.max(0, el.scrollHeight - el.clientHeight)
}

/**
 * Clamp a desired scrollTop into the element's valid range [0, maxScrollTopOf].
 * The one home for the full clamp shared by the raw-top viewport restore and the
 * discrete page-scroll target check, so neither can drift -- and so the raw-top
 * restore floors at 0 too (a scrollTop captured during elastic/rubber-band
 * overscroll can be negative; an upper-bound-only min would have written it back).
 */
export function clampScrollTop(el: HTMLDivElement, top: number): number {
  return Math.min(Math.max(0, top), maxScrollTopOf(el))
}

/**
 * Distance (px) from the viewport bottom to the content bottom: the visible
 * buffer of content BELOW the fold. The one definition the buffer filler, the
 * sticky-bottom thresholds, the bottom edge-intent test, and the scroll-to-bottom
 * chase share -- a module-scope twin of maxScrollTopOf so the createScrollBufferFiller
 * factory (which can't see the hook-local closures) reuses it instead of re-deriving it.
 */
export function distFromBottom(el: HTMLDivElement): number {
  return el.scrollHeight - el.scrollTop - el.clientHeight
}

/** Fraction of the viewport height that counts as the "near the top" band. */
export const NEAR_TOP_BAND_RATIO = 0.5

/**
 * True when `pos` (a scrollTop) sits in the top-half "near the top" band. The
 * single home for a band BOTH the post-restore older-load suppression sites read:
 * `armSuppressIfNearTop` (chatScrollViewportRestore) arms the suppression when a
 * restore LANDS in this band, and `handleScroll` (useChatScroll) clears it once the
 * user scrolls OUT of it. The two must agree on the threshold, so it lives here
 * rather than as a raw `clientHeight / 2` literal in each.
 */
export function isNearTopBand(el: HTMLDivElement, pos: number): boolean {
  return pos < el.clientHeight * NEAR_TOP_BAND_RATIO
}
