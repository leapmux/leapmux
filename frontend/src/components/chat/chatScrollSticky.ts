import type { ScrollContext } from './useChatScroll'
import { untrack } from 'solid-js'
import { maxScrollTopOf } from './chatScrollGeometry'

/**
 * The sticky-bottom unit: owns the "follow the live tail" record -- the last
 * clamped bottom we pinned to (scrollTop + the scrollHeight at that instant) --
 * and the re-stick / re-baseline logic that keeps the viewport glued to the
 * bottom across content growth, a shrink, and trims. Pulling it out of the hook
 * localizes the invariant "the record is always the clamped bottom" so an
 * unrelated edit elsewhere can't leave a stale-high record (which silently drops
 * stickiness). Its dependencies on the live scroll state are passed in (mirrors
 * createFlingSettle).
 */
export function createStickyBottom(ctx: ScrollContext, extras: {
  /** Clear the one-prepend "don't auto-stick" window once we pin to the bottom. */
  clearPreserveBrowsingPosition: () => void
  /** Drop any fling-settle drift deferred against the now-cleared anchor. */
  dropDeferredFlingDrift: () => void
}) {
  // The last bottom we pinned to. `stickyScrollTop === undefined` means we have
  // never stuck (no record yet); `stickyScrollHeight` starts at -1 (no record).
  let stickyScrollTop: number | undefined
  let stickyScrollHeight = -1

  /**
   * Pin scrollTop to the bottom WITHOUT recomputing the rendered slice. Safe to
   * call from inside ResizeObserver delivery (the geometry re-pin effect): it
   * changes only scrollTop, which doesn't resize any observed element, so it
   * can't re-enter RO delivery. Mounting newly-visible rows is left to the
   * deferred refresh / the resulting scroll event.
   */
  const stickToBottomPosition = (): boolean => {
    const el = ctx.getEl()
    if (!el || el.clientHeight === 0)
      return false
    // Writing scrollHeight relies on browser clamping to the real maximum.
    // Read scrollTop back afterward so the sticky record stores the clamped
    // visual bottom, not the unclamped scrollHeight assignment.
    const before = el.scrollTop
    el.scrollTop = el.scrollHeight
    stickyScrollTop = el.scrollTop
    stickyScrollHeight = el.scrollHeight
    ctx.setAtBottom(true)
    extras.clearPreserveBrowsingPosition()
    ctx.followTail()
    // Sticking establishes an authoritative position, so any fling correction
    // deferred against the now-cleared anchor is moot -- drop it. The settle
    // already drops drift when it fires with a null anchor, but a fresh scroll
    // that re-anchors before the FLING_SETTLE_MS debounce elapses would otherwise
    // let this gesture's stale drift land on the new anchor (a jump). Resetting
    // here closes that window.
    extras.dropDeferredFlingDrift()
    // The clamped bottom is our write; recognize its scroll event as ours -- but only
    // when the write actually MOVED scrollTop. A stick that lands on the current bottom
    // (already pinned) fires no scroll event, so marking it would leave a stale marker
    // that swallows a real user scroll to within 1px of the bottom for the marker TTL.
    if (el.scrollTop !== before)
      ctx.markProgrammaticScroll()
    return true
  }

  const stickToBottom = (): boolean => {
    const ok = stickToBottomPosition()
    if (ok)
      ctx.refreshViewport()
    return ok
  }

  /**
   * Sticky-record heuristic shared by the geometry-growth re-stick and the
   * stale-scroll-event re-stick: we are FOLLOWING the live tail within the 32px sticky
   * band (the `atBottom` signal) and content has since grown (scrollHeight beyond the
   * last record). `untrack` so it is safe to call from inside the totalHeight effect.
   *
   * Requires FOLLOW mode AND the `atBottom` band -- a fast scroll-up that moved OUT of
   * the band captures an anchor (mode -> 'anchored') and clears atBottom, so it is
   * never yanked back down. Within the band, follow is re-engaged (see the handleScroll
   * re-engage gate, which sets follow whenever atBottom holds at the live tail), so a
   * reader resting a few px above the bottom DOES stick on the next growth -- the "auto-
   * scroll when sitting slightly above the bottom" behavior. (This is gated by the band,
   * not by `scrollTop` being within 1px of the recorded bottom: that stricter check kept
   * a within-band reader anchored, which the unified-band design replaces.)
   */
  const shouldRestickToBottom = (): boolean => {
    const el = ctx.getEl()
    return !!el
      && untrack(ctx.atBottom)
      && ctx.isFollowing()
      && stickyScrollTop !== undefined
      && el.scrollHeight > stickyScrollHeight
  }

  /**
   * Re-baseline the sticky record to the CURRENT clamped bottom. Called when the
   * geometry changed while we were following the bottom but didn't grow past the
   * record -- a shrink (an over-estimated row measuring smaller; the estimator
   * biases UP, so this is the common case) or a window trim, after which the
   * browser clamps scrollTop down to a new, lower bottom. Without this, the record
   * keeps its pre-shrink (higher) values, and the NEXT growth's restick guard
   * (`scrollTop + 1 >= stickyScrollTop`) compares the new lower scrollTop against
   * the stale higher record, misreads it as a user scroll-up, and drops the
   * re-stick -- silently losing bottom stickiness.
   */
  const rebaseStickyBottom = () => {
    const el = ctx.getEl()
    if (!el)
      return
    // Record the CLAMPED bottom. On a shrink the browser clamps scrollTop down to
    // the new (lower) max; reading scrollHeight forces that reflow, but clamp
    // explicitly so a pre-clamp stale-high scrollTop can never be banked as the
    // record -- otherwise the next growth's restick guard (scrollTop + 1 >=
    // stickyScrollTop) would compare a real lower scrollTop against a stale-high
    // record, misread it as a scroll-up, and silently drop bottom stickiness.
    const maxTop = maxScrollTopOf(el)
    stickyScrollTop = Math.min(el.scrollTop, maxTop)
    stickyScrollHeight = el.scrollHeight
  }

  /**
   * Re-stick to the bottom when content grew while the user was pinned there.
   * Position-only (no mount) for RO-delivery safety. On a geometry change that
   * did NOT grow past the record (a shrink/trim that clamped scrollTop down),
   * re-baseline the record to the new bottom instead -- but only while we're
   * still genuinely at the bottom (a fresh isAtBottom check, which is true after
   * a clamp but false mid-growth), so a real scroll-up is never re-baselined into
   * a false "at bottom".
   */
  const restickIfAtBottom = () => {
    const el = ctx.getEl()
    if (!el || ctx.isAnimating())
      return
    if (shouldRestickToBottom()) {
      stickToBottomPosition()
      return
    }
    // A geometry change that did NOT grow past the record while we were following
    // the tail. Gated on isFollowing() (a user scroll-up captures an anchor ->
    // 'anchored', so 'following' guarantees we are NOT mid-scroll-up and
    // re-pinning can't yank the user).
    if (untrack(ctx.atBottom) && ctx.isFollowing()) {
      if (stickyScrollTop !== undefined && el.scrollHeight < stickyScrollHeight) {
        // SHRINK (an over-estimated row above measured smaller, or a trim) clamped
        // scrollTop down off the bottom. A shrink larger than
        // STICKY_BOTTOM_THRESHOLD_PX makes isAtBottom() read false, which would
        // otherwise leave a stale-high record and silently drop stickiness on the
        // next growth -- so re-pin to the new bottom rather than only rebasing.
        stickToBottomPosition()
      }
      else if (ctx.isAtBottom()) {
        // No shrink and still at the bottom: re-baseline the record. Also SEEDS it on
        // the first run (stickyScrollTop undefined), when we mounted already at the
        // bottom -- the auto-scroll effect calls this when no scroll is needed, so a
        // later growth's restick guard is armed instead of silently undefined.
        rebaseStickyBottom()
      }
    }
  }

  return {
    stickToBottom,
    shouldRestickToBottom,
    restickIfAtBottom,
  }
}
