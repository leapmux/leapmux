import type { ScrollContext } from './useChatScroll'
import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import type { SavedViewportScroll, ScrollAnchor } from '~/stores/chatTypes'
import { firstMessageSeq, lastMessageSeq } from '~/stores/chatMessageOrder'
import { ROW_ALIGN_OFFSET_PX } from './chatLayoutShift'
import { nearestServerRowIndexBySeq } from './chatScrollAnchor'

// ---------------------------------------------------------------------------
// Scroll-rail seek (jumpToSeq / previewScrollTo)
//
// The seek machine a scroll-rail dot click / track click / thumb release drives: an
// in-window aligned landing, or a fetch-around-seq window swap then land. Extracted from
// useChatScroll so the epoch-guarded pending-seek state and the landing logic have a named
// home + a test seam (useChatScroll.seek.test.ts), mirroring the sibling rail/scroll units.
// It owns its own pendingSeek / seekEpoch; the hook's handleScroll cancels an in-flight seek
// on a genuine user scroll via cancelPendingSeek. Reads the scroll primitives through the
// shared ScrollContext plus an `extras` bag for its other deps.
// ---------------------------------------------------------------------------

export interface ChatSeek {
  /**
   * Seek to a message seq (a scroll-rail dot click, track click, or thumb release):
   * an in-window aligned landing, or a fetch-around-seq window swap then land. Resolves
   * to whether the seek actually moved the scroll position (so the rail's drag-release
   * hold knows whether to await the landing's metrics settle or clear the thumb now).
   */
  jumpToSeq: (seq: bigint) => Promise<boolean>
  /**
   * A placement for in-window rail thumb-drag live-scroll, at the content coordinate
   * `top`. A `top` at the content top (0) places the view at the top of the scroller,
   * above the inset of the spacer. An own write, like the landing, so the `scroll` event of
   * each placement reads as no move: it samples no speed and reports no momentum. The
   * scroll handler still tops up the buffer for the new position, as it does for every
   * event.
   */
  previewScrollTo: (top: number) => void
  /**
   * Abandon any in-flight out-of-window seek so its late-resolving fetch cannot land
   * (yank the viewport) after the user took manual control. The hook's handleScroll calls
   * this on a genuine user scroll, and the rail calls it (via the hook) at each moment its
   * gesture stops wanting a seek it issued -- a drag scrolls only programmatically, so it
   * never trips the user-scroll path.
   *
   * The seek stops in BOTH places it can still move the view. Clearing pendingSeek makes an
   * in-flight seek's `.then` short-circuit, because its epoch no longer matches a live
   * pendingSeek. Aborting its controller reaches the page fetch itself (onJumpToSeq threads
   * the signal to the paginator), so the request ends at the transport instead of completing
   * and swapping the window. Without the abort the landing was suppressed but the swap was
   * not: the reader kept the viewport they scrolled to, holding a window centred somewhere
   * they abandoned.
   */
  cancelPendingSeek: () => void
}

/**
 * The deps that only createChatSeek reads, beyond the shared {@link ScrollContext}: the
 * live message list, and the callbacks of the host that swap the window and save the
 * viewport.
 */
export interface ChatSeekExtras {
  /** Live loaded transcript messages. */
  messages: () => AgentChatMessage[]
  /** Replace the window with a page centered on `seq` (the out-of-window seek). */
  onJumpToSeq: ((seq: bigint, signal?: AbortSignal) => Promise<void> | void) | undefined
  /** Hand a hidden-mid-jump target to the SavedViewportScroll restore path. */
  onSaveViewportScroll: ((state: SavedViewportScroll) => void) | undefined
}

export function createChatSeek(ctx: ScrollContext, extras: ChatSeekExtras): ChatSeek {
  // The in-flight out-of-window seek. The epoch makes a late resolve a no-op; the controller
  // makes the fetch behind it genuinely stop. Both are needed and they are not the same thing:
  // the epoch is what THIS module checks before it lands, while the controller reaches the page
  // fetch itself (onJumpToSeq threads it to the paginator's abort signal), so a seek nobody
  // wants any more stops its round trip instead of completing and swapping the window.
  let pendingSeek: { seq: bigint, epoch: number, controller: AbortController } | null = null
  let seekEpoch = 0

  /**
   * Abandon the in-flight out-of-window seek: stop its fetch and forget it, so neither the fetch
   * nor this module's own landing can move the view. Used by the two ways a seek stops being
   * wanted -- a superseding jump, and a caller giving up (cancelPendingSeek).
   */
  const abandonPendingSeek = () => {
    pendingSeek?.controller.abort()
    pendingSeek = null
  }

  // Shared order helpers supply the window sequences. The rail and the window use
  // the same values for the in-window seek decision.

  /**
   * The loaded server row nearest `seq` by absolute seq distance, via the same
   * nearestServerRowIndexBySeq scan the trim-restore anchor recovery uses (scrollTopNearAnchor),
   * so the seek landing and that recovery can't drift on the skip/tie-break rule.
   */
  const nearestRowIdBySeq = (seq: bigint): string | undefined => {
    const msgs = extras.messages()
    const idx = nearestServerRowIndexBySeq(msgs, seq)
    // A matched index came from a scan over `msgs`, so it is in range; `?.` is the type-level guard alone.
    return idx < 0
      ? undefined
      : msgs[idx]?.id
  }

  /**
   * Land the viewport on the loaded row nearest `seq`, its top ROW_ALIGN_OFFSET_PX below
   * the viewport top. Instant, not animated. The resolved scroll coordinate counts the
   * inset of the spacer (see chatLayoutShift), so the row lands that far below the top of
   * the visible area. The first row resolves to the top of the scroller (see
   * placementScrollTop), so a landing on it writes 0 and shows the inset above it. A list
   * shorter than the view cannot scroll, so a landing there writes 0. The layout shift
   * then holds the top edge of that row where it landed while the fresh rows around it
   * measure: the shift rule gives the top edge of a row whose top lies that far below the
   * viewport top (see rowCutAt), also when the viewport top lies inside the row above it.
   *
   * Returns whether the landing actually MOVED the scroll position (i.e. a scroll event,
   * and hence a settling metrics change, will follow). It returns false -- no scroll -- when
   * the container is detached/hidden, no target row resolves, OR the target is already the
   * current scrollTop; the rail's drag-release hold uses this to know whether to wait for a
   * metrics settle or clear immediately, so it never sticks when the landing scrolls nowhere.
   */
  const landOnSeq = (seq: bigint): boolean => {
    if (!ctx.getVisibleEl())
      return false
    const id = nearestRowIdBySeq(seq)
    if (!id)
      return false
    ctx.cancelScrollAnimation()
    // Resolve the row's top via the anchor resolver (offsetWithinRow 0 is the row top),
    // after the rebase of the placement, so the resolved position holds under the shift
    // that the write lands in. scrollTopNearAnchor resolves the row itself first, and the
    // nearest survivor by seq when the id is not in the current offset map.
    const anchor: ScrollAnchor = { id, offsetWithinRow: 0, seq }
    const moved = ctx.placeAt(() => {
      const resolved = ctx.virt.scrollTopNearAnchor(anchor)
      return resolved == null
        ? null
        : resolved - ROW_ALIGN_OFFSET_PX
    })
    return moved === true
  }

  /**
   * Seek to a message `seq` (a scroll-rail dot click, track click, or thumb release).
   * In-window: land immediately. Out-of-window: fetch a page centered on the seq
   * (onJumpToSeq), then land once the window has swapped. A jump supersedes any prior
   * in-flight seek via the epoch, and a genuine user scroll cancels it.
   *
   * Resolves to whether the seek actually MOVED the scroll position (landOnSeq's result;
   * false when the container is detached, the tab is hidden, or the fetch fails). The rail's
   * drag-release hold awaits this to decide whether to wait for the landing's metrics settle
   * or clear the held thumb immediately -- so the thumb neither flashes back mid-fetch nor
   * sticks when the landing scrolls nowhere.
   */
  const jumpToSeq = (seq: bigint): Promise<boolean> => {
    const el = ctx.getEl()
    if (!el)
      return Promise.resolve(false)
    const msgs = extras.messages()
    const firstS = firstMessageSeq(msgs)
    const lastS = lastMessageSeq(msgs)
    // In-window: the seq sits within the loaded server span. Re-take control (clear the
    // filler pause and settle flags, same as forceScrollToBottom) and land.
    if (firstS !== undefined && lastS !== undefined && seq >= firstS && seq <= lastS) {
      // This jump needs no fetch, but it still supersedes one already running: without the
      // abort, that page would land afterwards and swap the window out from under this landing.
      abandonPendingSeek()
      ctx.retakeScrollControl()
      return Promise.resolve(landOnSeq(seq))
    }
    // Out-of-window: swap the window to a page centered on the seq, then land. The
    // preamble re-takes control and leaves follow mode, so the auto-scroll effect does not
    // stick for the swap. A swap that makes the tail live under a reader at the loaded
    // bottom enters follow mode again (see the hasNewerMessages effect of the hook), and the
    // landing then ends it. The centered fetch handles the tail case too (BEFORE seq+1
    // returns the latest page when seq >= maxSeq).
    ctx.cancelScrollAnimation()
    ctx.retakeScrollControl()
    ctx.rearmScrollBufferFiller()
    ctx.leaveFollow()
    // Supersede any seek already in flight -- abort its fetch, not just its landing.
    abandonPendingSeek()
    const epoch = ++seekEpoch
    const controller = new AbortController()
    pendingSeek = { seq, epoch, controller }
    return Promise.resolve(extras.onJumpToSeq?.(seq, controller.signal))
      .then(() => {
        // Superseded by a newer jump, or cancelled by a genuine user scroll.
        if (pendingSeek?.epoch !== epoch)
          return false
        pendingSeek = null
        if (ctx.getVisibleEl()) {
          // Visible: applyMessages already swapped the window synchronously and the
          // virtualizer memos are pull-based, so offsetOfId reflects the new window now.
          return landOnSeq(seq)
        }
        // Hidden mid-jump (tab switched away): hand the target to the SavedViewportScroll
        // restore path so the seek lands when the tab returns, instead of snapping to tail.
        const id = nearestRowIdBySeq(seq)
        if (id) {
          extras.onSaveViewportScroll?.({
            anchor: { id, offsetWithinRow: 0, seq },
            atBottom: false,
            hasMoreNewer: ctx.hasNewerMessages(),
          })
        }
        return false
      })
      .catch(() => {
        if (pendingSeek?.epoch === epoch)
          pendingSeek = null
        ctx.checkPosition()
        return false
      })
  }

  const previewScrollTo = (top: number) => {
    if (!ctx.getEl())
      return
    ctx.cancelScrollAnimation()
    // `top` is a content coordinate. The resolver converts it after the rebase of the
    // placement, which can leave a shift below one pixel in place. The ends of a scrub reach
    // the ends of the scroller: the content top goes to the top of the scroller (see
    // placementScrollTop), and the browser clamps a target past the bottom.
    ctx.placeAt(() => ctx.virt.placementScrollTopOf(top))
  }

  return { jumpToSeq, previewScrollTo, cancelPendingSeek: abandonPendingSeek }
}
