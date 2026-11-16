import type { ScrollContext } from './useChatScroll'
import { hasScrollRoom } from './chatScrollGeometry'

/**
 * The two writes that put the view at the bottom of the content.
 *
 * Neither rebases the layout shift: the bottom of the content is the bottom of the
 * scroll range for every shift, so `scrollTop = scrollHeight` lands there under any.
 */
export function createStickyBottom(ctx: ScrollContext) {
  /**
   * Enter follow mode, put the view at the bottom, and refresh the rendered rows. False for
   * an absent or hidden element. The browser clamps `scrollHeight` to the maximum position.
   * The position check of the write publishes atBottom.
   */
  const stickToBottom = (): boolean => {
    const el = ctx.getVisibleEl()
    if (!el)
      return false
    ctx.followTail()
    ctx.writeOwnScrollTop(el.scrollHeight, { refresh: true })
    return true
  }

  /**
   * Follow content that grew below a reader in follow mode: a tail sibling of the spacer
   * (the thinking indicator, a banner) grows with no row change, so the layout shift does
   * not keep the bottom for it. Writes only while follow mode applies and the view sits a
   * whole pixel or more above the bottom.
   *
   * An automatic stick, unlike stickToBottom: follow mode does not apply while the browser
   * moved the view up and the scroll handler did not see that move yet (a touch or
   * scrollbar drag whose first `scroll` event is late), so it writes nothing then. The event
   * of the move then ends the request for the tail (see ScrollContext.isFollowing).
   *
   * It does not refresh the rendered rows: the `scroll` event of the write, or the
   * deferred refresh of the geometry effect, refreshes them. The geometry effect calls it
   * inside the flush of the row measurer, where a refresh would also be safe: the
   * measurer observes a row that mounts during its flush in the next frame (see
   * RowMeasurer.attachRow).
   */
  const restickIfAtBottom = (): void => {
    const el = ctx.getVisibleEl()
    if (!el || ctx.isAnimating())
      return
    // A shrink of a tail sibling can clamp the view to the bottom with no write. The record
    // keeps that clamp no move of the reader when the content grows again.
    ctx.noteScrollRange()
    if (!ctx.isFollowing())
      return
    // A sub-pixel distance is no room: at a fractional zoom the maximum position can lie a
    // fraction of a pixel below the position that the browser stores, and a write for it
    // changes nothing on each streaming frame.
    if (!hasScrollRoom(el, 1))
      return
    ctx.writeOwnScrollTop(el.scrollHeight, { refresh: false })
  }

  return { stickToBottom, restickIfAtBottom }
}
