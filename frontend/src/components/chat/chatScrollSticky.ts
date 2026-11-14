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
   * It does not refresh the rendered rows: a ResizeObserver delivery can call it, and a
   * row that mounts there re-enters the delivery. The `scroll` event of the write, or the
   * deferred refresh of the geometry effect, refreshes them.
   */
  const restickIfAtBottom = (): void => {
    const el = ctx.getEl()
    if (!el || el.clientHeight === 0 || ctx.isAnimating() || !ctx.isFollowing())
      return
    if (distFromBottom(el) <= 0)
      return
    el.scrollTop = el.scrollHeight
    ctx.notePosition()
  }

  return { stickToBottom, restickIfAtBottom }
}
