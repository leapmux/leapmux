import type { ScrollContext } from './useChatScroll'
import { untrack } from 'solid-js'
import { EDGE_INTENT_TOLERANCE_PX, maxScrollTopOf } from './chatScrollGeometry'

/**
 * Keep the last pinned position and its content and viewport heights together.
 * Use that record to follow the live tail across layout changes.
 * The scroll controller supplies user intent and the shared position writer.
 */
export function createStickyBottom(ctx: ScrollContext, extras: {
  /** End the position hold for one prepend when the viewport reaches the bottom. */
  clearPreserveBrowsingPosition: () => void
  /** Discard deferred fling corrections for the anchor that no longer applies. */
  dropDeferredFlingDrift: () => void
}) {
  // An undefined position or a height of -1 means that no pinned record exists.
  let stickyScrollTop: number | undefined
  let stickyScrollHeight = -1
  let stickyClientHeight = -1

  /**
   * Pin the position without recomputing the rendered rows.
   * This operation changes no observed size, so a ResizeObserver handler can call it without recursive delivery.
   * The deferred refresh or the resulting scroll event mounts newly visible rows.
   */
  const stickToBottomPosition = (): boolean => {
    const el = ctx.getEl()
    if (!el || el.clientHeight === 0)
      return false
    // The shared writer updates the direction baseline immediately and clears stale native scroll translation.
    // A wheel event before the write's scroll event then uses the correct baseline.
    // The browser clamps scrollHeight to its actual maximum position.
    // The writer records an echo only when the position changes, so an unchanged write cannot hide a later user scroll.
    // Read the position back to retain the actual bottom after the clamp.
    ctx.writeScrollTop(el.scrollHeight, 'stick-bottom')
    stickyScrollTop = el.scrollTop
    stickyScrollHeight = el.scrollHeight
    stickyClientHeight = el.clientHeight
    ctx.setAtBottom(true)
    extras.clearPreserveBrowsingPosition()
    ctx.followTail()
    // A deferred fling correction no longer applies after this explicit placement.
    // Clear it now so a new anchor cannot receive the old correction before the settle timer fires.
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
   * Follow content growth only while follow mode and the atBottom signal both apply.
   * A user scroll outside the sticky band captures an anchor and clears atBottom, so this rule cannot pull that reader down.
   * Inside the band, the scroll handler restores follow mode. The next growth then reaches the actual bottom.
   * The rule uses the whole sticky band, not a one-pixel match against the stored position.
   * The totalHeight effect can call this function because untrack prevents an extra dependency.
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
    // Read the maximum first to force layout and exclude a stale position above that maximum.
    const maxTop = maxScrollTopOf(el)
    stickyScrollTop = Math.min(el.scrollTop, maxTop)
    stickyScrollHeight = el.scrollHeight
    stickyClientHeight = el.clientHeight
  }

  /**
   * Follow a geometry change without mounting rows during ResizeObserver delivery.
   * Content growth or shrinkage moves a pinned viewport to its new bottom.
   * Otherwise, record the current geometry only when a fresh check confirms the bottom.
   * A real user scroll above the bottom must not create a new pinned record.
   */
  const restickIfAtBottom = () => {
    const el = ctx.getEl()
    if (!el || ctx.isAnimating())
      return
    if (shouldRestickToBottom()) {
      stickToBottomPosition()
      return
    }
    // A user scroll above the sticky band ends follow mode, so these corrections cannot move that reader.
    if (untrack(ctx.atBottom) && ctx.isFollowing()) {
      if (stickyScrollTop !== undefined && el.scrollHeight < stickyScrollHeight) {
        // A measurement or trim can shrink the content beyond the sticky band.
        // A fresh bottom check would then fail. Move to the new bottom to preserve follow mode.
        stickToBottomPosition()
      }
      else if (ctx.isAtBottom()) {
        // Record the current bottom, including the initial mount when no scroll write was necessary.
        // Later growth then has a pinned record to compare.
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
    // The browser can clamp the old bottom before content grows and the scroll event arrives.
    // Growth before the resize can instead keep the old position inside the new scroll range.
    // Accept either known position. A different position belongs to the reader.
    const previousBottom = Math.max(0, Math.min(stickyScrollHeight, el.scrollHeight) - el.clientHeight)
    const clampedPosition = Math.min(stickyScrollTop, previousBottom)
    const knownPosition = Math.abs(el.scrollTop - clampedPosition) <= EDGE_INTENT_TOLERANCE_PX
      || Math.abs(el.scrollTop - stickyScrollTop) <= EDGE_INTENT_TOLERANCE_PX
    if (!knownPosition || !stickToBottomPosition())
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
