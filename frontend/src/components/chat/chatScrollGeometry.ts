// ---------------------------------------------------------------------------
// Shared scroll-geometry primitives
//
// Pure helpers + the cross-cutting edge/re-pin thresholds that the scroll hook
// (useChatScroll) AND its extracted factories (chatScroll*.ts) both read. Kept in
// one leaf module so a factory can reuse them without importing the hook (which
// would be a cycle) and so the "how far can this scroll / is it at an edge"
// formulas live in exactly one place.
// ---------------------------------------------------------------------------

import { clamp } from '~/lib/clamp'
import { createLogger } from '~/lib/logger'

/** Below this, a re-pin's scrollTop correction is a no-op not worth a write (which can interrupt momentum). */
export const REPIN_MIN_DELTA_PX = 1
/**
 * Distance-from-bottom (px) within which the viewport is treated as "stuck to the live
 * tail" and auto-follows growth. Also the floor below which a scrollable range is
 * effectively unscrollable: with 0 < maxScrollTop <= this, EVERY scroll position is inside
 * the sticky band, so a reader can never scroll OUT of it -- the older-history pre-fetch
 * suppressions clear at this bound, not at a strict 0, or a barely-scrollable hidden-heavy
 * page would wedge older loads off for good (see useChatScroll.suppressOlderPrefetchAtLiveTail
 * and the restore-suppression clears).
 */
export const STICKY_BOTTOM_THRESHOLD_PX = 32
/**
 * Slack (px) for "is the viewport at the very top / bottom edge?" when deciding an
 * explicit page-older / page-newer intent. A re-pin or clamp can leave scrollTop a
 * sub-pixel off the edge (fractional DPI / browser zoom report non-integer
 * scrollTop), so an exact `=== 0` top test would never fire while the tolerant
 * bottom test does -- the two edges must use the SAME slack or they behave
 * asymmetrically at fractional offsets.
 */
export const EDGE_INTENT_TOLERANCE_PX = 1

/**
 * Pixel slack for "does the content fit the viewport, so no scrollbar is needed?" -- a
 * sub-pixel rounding overflow (fractional DPI / zoom) needs no scrollbar. The SINGLE source
 * for this bound so the rail's self-hide (ChatScrollRail) and the native-scrollbar hide
 * (ChatView) agree on what "overflows" means; if they diverged, a viewport could be left with
 * NO usable scrollbar (both hidden) or two.
 */
export const SCROLLBAR_OVERFLOW_TOLERANCE_PX = 1

/**
 * A synchronous scroll-pipeline phase (refreshViewport render cascade, a premeasure row's
 * forced-layout measure, or an offset-map rebuild) that runs longer than this dropped
 * multiple frames and is a main-thread STALL -- the cause of the batched catch-up scroll
 * deltas Detector B reports as "unexpected jumps". Timing is always on (two clock reads per
 * phase, negligible); only a phase OVER this budget logs, so normal scrolling is silent.
 * ~3 frames at 60fps -- well clear of routine work (single-digit ms) but far below the
 * hundreds-of-ms freezes we are hunting.
 */
const SCROLL_PHASE_STALL_WARN_MS = 50

// The shared 'chatScroll' diagnostic channel for the always-on stall WARN. useChatScroll
// creates its own same-named logger for the scroll-anomaly WARNs (Detector A/B/C).
const scrollPhaseLog = createLogger('chatScroll')

/**
 * Emit the shared "slow scroll phase" WARN when a synchronous scroll-pipeline phase ran over
 * SCROLL_PHASE_STALL_WARN_MS. The single home for the threshold gate + Math.round + the
 * 'chatScroll' channel, so the refreshViewport (useChatScroll), premeasure (ChatView), and
 * geomRebuild (useChatVirtualizer) reports can't drift in shape. `extra` (the small per-phase
 * context) is cheap to build at every call site, so it is passed eagerly.
 */
export function warnSlowScrollPhase(phase: string, ms: number, extra?: Record<string, unknown>): void {
  if (ms < SCROLL_PHASE_STALL_WARN_MS)
    return
  scrollPhaseLog.warn('slow scroll phase', { phase, ms: Math.round(ms), ...extra })
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
 * True when the viewport CANNOT scroll out of the live-tail sticky band: its
 * whole scrollable range fits inside STICKY_BOTTOM_THRESHOLD_PX, so EVERY scroll
 * position reads as at-bottom and the reader can never scroll up to leave it.
 *
 * The single home for the `maxScrollTopOf(el) <= STICKY_BOTTOM_THRESHOLD_PX`
 * unwedge test the older-history pre-fetch machinery consults from three sites
 * (the live-tail suppression, the geometry-commit suppression clear, and the
 * restore-settle clear -- see useChatScroll and createViewportRestore). Keeping
 * it in one predicate stops the sites drifting on the bound: a prior edit
 * re-introduced the wedge at one site by using a strict `<= 0` here.
 */
export function cannotLeaveStickyBand(el: HTMLDivElement): boolean {
  return maxScrollTopOf(el) <= STICKY_BOTTOM_THRESHOLD_PX
}

/**
 * Clamp a desired scrollTop into the element's valid range [0, maxScrollTopOf].
 * The one home for the full clamp shared by the raw-top viewport restore and the
 * discrete page-scroll target check, so neither can drift -- and so the raw-top
 * restore floors at 0 too (a scrollTop captured during elastic/rubber-band
 * overscroll can be negative; an upper-bound-only min would have written it back).
 */
export function clampScrollTop(el: HTMLDivElement, top: number): number {
  return clamp(top, 0, maxScrollTopOf(el))
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
const NEAR_TOP_BAND_RATIO = 0.5

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
