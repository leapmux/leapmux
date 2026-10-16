import type { ScrollContext } from './useChatScroll'
import { untrack } from 'solid-js'
import { EDGE_INTENT_TOLERANCE_PX, maxScrollTopOf } from './chatScrollGeometry'

/**
 * Keep the last pinned position and its content and viewport heights together.
 * Use that record to follow the live tail across layout changes.
 * The scroll controller supplies user intent and the shared position writer.
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
  let stickyClientHeight = -1

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
    // Route through the SHARED programmatic write so its write-time bookkeeping runs for
    // the restick too -- the highest-frequency programmatic write during an active turn: the
    // direction/last-position baseline advances at write time (a user wheel interleaving
    // before the echo would otherwise measure from the stale pre-stick baseline and
    // mis-infer its direction), and a stale-native repin shift is disarmed. Writing
    // scrollHeight relies on browser clamping to the real maximum; the guard behind
    // writeScrollTop arms an echo marker only when the write actually MOVED scrollTop
    // (see createProgrammaticScrollGuard.write), so a stick that lands on the current
    // bottom still leaves no stale marker to swallow a later near-bottom user scroll.
    // Read scrollTop back afterward so the sticky record stores the clamped visual
    // bottom, not the unclamped scrollHeight assignment.
    ctx.writeScrollTop(el.scrollHeight, 'stick-bottom')
    stickyScrollTop = el.scrollTop
    stickyScrollHeight = el.scrollHeight
    stickyClientHeight = el.clientHeight
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

  /** Update the pinned geometry after a shrink or a window trim clamps the position. */
  const rebaseStickyBottom = () => {
    const el = ctx.getEl()
    if (!el)
      return
    // Read the current maximum before saving the position. This forces layout and
    // prevents a stale position above the new maximum from entering the record.
    const maxTop = maxScrollTopOf(el)
    stickyScrollTop = Math.min(el.scrollTop, maxTop)
    stickyScrollHeight = el.scrollHeight
    stickyClientHeight = el.clientHeight
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

  /** Recover a known layout position and report that its event is not reader input. */
  const restickAfterLayoutChange = (): boolean => {
    const el = ctx.getEl()
    if (!el || el.clientHeight === 0 || ctx.isAnimating() || !ctx.isFollowing()
      || !untrack(ctx.atBottom) || stickyScrollTop === undefined
      || (stickyClientHeight === el.clientHeight && stickyScrollHeight === el.scrollHeight)) {
      return false
    }
    // The browser can clamp the old bottom before it delivers a scroll event.
    // New content can grow between that clamp and the event. Match the old geometry,
    // then use the new bottom. A different position belongs to the reader.
    const previousBottom = Math.max(0, Math.min(stickyScrollHeight, el.scrollHeight) - el.clientHeight)
    const clampedPosition = Math.min(stickyScrollTop, previousBottom)
    if (Math.abs(el.scrollTop - clampedPosition) > EDGE_INTENT_TOLERANCE_PX || !stickToBottomPosition())
      return false
    // A native clamp can already be at the new bottom. No write marker exists then,
    // but its displacement must still stay out of the reader's velocity samples.
    ctx.syncVelocityToProgrammatic(el.scrollTop)
    return true
  }

  return {
    stickToBottom,
    shouldRestickToBottom,
    restickIfAtBottom,
    restickAfterLayoutChange,
  }
}
