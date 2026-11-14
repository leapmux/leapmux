import type { ScrollContext } from './useChatScroll'
import { distFromBottom } from './chatScrollGeometry'

/**
 * The two writes that put the view at the bottom of the content.
 *
 * Neither rebases the layout shift: the bottom of the content is the bottom of the
 * scroll range for every shift, so `scrollTop = scrollHeight` lands there under any.
 */
export function createStickyBottom(ctx: ScrollContext) {
  /**
   * Put the view at the bottom, enter follow mode, and refresh the rendered rows. False
   * for an absent or hidden element. The browser clamps `scrollHeight` to the maximum
   * position.
   */
  const stickToBottom = (): boolean => {
    const el = ctx.getEl()
    if (!el || el.clientHeight === 0)
      return false
    el.scrollTop = el.scrollHeight
    ctx.followTail()
    ctx.syncAfterWrite()
    ctx.setAtBottom(true)
    return true
  }

  /**
   * Follow content that grew below a reader in follow mode: a tail sibling of the spacer
   * (the thinking indicator, a banner) grows with no row change, so the layout shift does
   * not keep the bottom for it. Writes only while follow mode applies and the view is
   * above the bottom.
   *
   * An automatic stick, unlike stickToBottom: it writes nothing while the browser moved
   * the view up and the scroll handler did not see that move yet (a touch or scrollbar
   * drag whose first `scroll` event is late). The event of the move then ends follow mode.
   *
   * It does not refresh the rendered rows: a ResizeObserver delivery can call it, and a
   * row that mounts there re-enters the delivery. The `scroll` event of the write, or the
   * deferred refresh of the geometry effect, refreshes them.
   */
  const restickIfAtBottom = (): void => {
    const el = ctx.getEl()
    if (!el || el.clientHeight === 0 || ctx.isAnimating() || !ctx.isFollowing())
      return
    // Before the check of the distance: the call also records a clamp that left the view
    // at the bottom (see ScrollContext.hasUnseenUpwardMove).
    if (ctx.hasUnseenUpwardMove())
      return
    if (distFromBottom(el) <= 0)
      return
    el.scrollTop = el.scrollHeight
    ctx.notePosition()
  }

  return { stickToBottom, restickIfAtBottom }
}
