import type { ScrollContext } from './useChatScroll'
import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import type { SavedViewportScroll, ScrollAnchor } from '~/stores/chatTypes'
import { batch } from 'solid-js'
import { firstMessageSeq, lastMessageSeq } from '~/stores/chatMessageOrder'
import { ROW_ALIGN_OFFSET_PX } from './chatLayoutShift'
import { nearestServerRowIndexBySeq } from './chatScrollAnchor'

// ---------------------------------------------------------------------------
// Scroll-rail seek (jumpToSeq / previewScrollTo)
//
// The seek machine a scroll-rail dot click / track click / thumb release drives: an
// in-window aligned landing, or a fetch-around-seq window swap then land. Extracted from
// useChatScroll so the landing logic has a named home + a test seam
// (useChatScroll.seek.test.ts), mirroring the sibling rail/scroll units. The fetch of a
// seek outside the window is the pending history jump of the hook (see
// ScrollContext.beginHistoryJump): a later command, or an input by which the reader takes
// control, aborts it. Reads the scroll primitives through the shared ScrollContext plus an
// `extras` bag for its other deps.
//
// The row of a seek can be staged: loaded, and not measured yet (see chatRowAdmission). So
// the landing asks the hook for the place of the row (ScrollContext.requestPlace), and the
// view moves once, when the row and the rows of its view are measured.
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
}

/**
 * The deps that only createChatSeek reads, beyond the shared {@link ScrollContext}: the
 * live message list, and the callbacks of the host that swap the window and save the
 * viewport.
 */
export interface ChatSeekExtras {
  /** Live loaded transcript messages. */
  messages: () => AgentChatMessage[]
  /**
   * Replace the window with a page centered on `seq` (the out-of-window seek). Resolves
   * `true` only when that page replaced the window (see PaginationCallbacks.onJumpToLatest).
   */
  onJumpToSeq: ((seq: bigint, signal?: AbortSignal) => Promise<boolean> | undefined) | undefined
  /** Hand a hidden-mid-jump target to the SavedViewportScroll restore path. */
  onSaveViewportScroll: ((state: SavedViewportScroll) => void) | undefined
}

export function createChatSeek(ctx: ScrollContext, extras: ChatSeekExtras): ChatSeek {
  // Shared order helpers supply the window sequences. The rail and the window use
  // the same values for the in-window seek decision.

  /**
   * The loaded server row nearest `seq` by absolute seq distance, via the same
   * nearestServerRowIndexBySeq scan the trim-restore anchor recovery uses (scrollTopNearAnchor).
   * Only for the handoff of a seek whose pane was hidden when its page arrived: that row can
   * be a hidden message, and the restore of the saved viewport resolves it to the nearest
   * visible row. A landing takes its row from `ctx.loadedRows`, the loaded visible rows.
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
   * Land the viewport on the loaded visible row nearest `seq`, its top ROW_ALIGN_OFFSET_PX
   * below the viewport top, once that row and the rows of its view are measured (see
   * ScrollContext.requestPlace). Instant, not animated. The resolved scroll coordinate counts the
   * inset of the spacer (see chatLayoutShift), so the row lands that far below the top of
   * the visible area. The first row resolves to the top of the scroller (see
   * placementScrollTop), so a landing on it writes 0 and shows the inset above it. A list
   * shorter than the view cannot scroll, so a landing there writes 0. The layout shift
   * then holds the top edge of that row where it landed while the fresh rows around it
   * measure: the shift rule gives the top edge of a row whose top lies that far below the
   * viewport top (see rowCutAt), also when the viewport top lies inside the row above it.
   *
   * Resolves to whether the landing actually MOVED the scroll position (i.e. a scroll event,
   * and hence a settling metrics change, will follow). It resolves false -- no scroll -- when
   * the container is detached/hidden, no target row resolves, the wait for the row ended
   * without its placement (a later command, an input of the reader, a row that left the
   * loaded rows), OR the target is already the current scrollTop; the rail's drag-release
   * hold uses this to know whether to wait for a metrics settle or clear immediately, so it
   * never sticks when the landing scrolls nowhere. The promise settles on every path,
   * because that of requestPlace does.
   */
  const landOnSeq = (seq: bigint): Promise<boolean> => {
    if (!ctx.getVisibleEl())
      return Promise.resolve(false)
    const id = ctx.loadedRows.nearestId(seq)
    if (!id)
      return Promise.resolve(false)
    ctx.cancelScrollAnimation()
    // Resolve the row's top via the anchor resolver (offsetWithinRow 0 is the row top),
    // after the rebase of the placement, so the resolved position holds under the shift
    // that the write lands in. scrollTopNearAnchor resolves the row itself first, and the
    // nearest survivor by seq when the id is not in the current offset map.
    const anchor: ScrollAnchor = { id, offsetWithinRow: 0, seq }
    let moved = false
    return ctx.requestPlace({ kind: 'row', anchor }, () => {
      moved = ctx.placeAt(() => {
        const resolved = ctx.virt.scrollTopNearAnchor(anchor)
        return resolved == null
          ? null
          : resolved - ROW_ALIGN_OFFSET_PX
      }) === true
    }).then(shown => shown && moved)
  }

  /**
   * Seek to a message `seq` (a scroll-rail dot click, track click, or thumb release).
   * In-window: land on the row. Out-of-window: fetch a page centered on the seq
   * (onJumpToSeq), then land once the window has swapped. The fetch is the pending history
   * jump of the hook: a later jump aborts it, and so does each input by which the reader
   * takes control.
   *
   * Each branch runs its synchronous part in one batch, so the hook sees only the place
   * that the command leaves, and not each step toward it (see requestPlace).
   *
   * Resolves to whether the seek actually MOVED the scroll position (landOnSeq's result;
   * false when the container is detached, the tab is hidden, the fetch fails, the seek was
   * abandoned, or another history fetch replaced its page). The rail's
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
      return batch(() => {
        // This jump needs no fetch, but it still supersedes one already running: without the
        // abort, that page would land afterwards and swap the window out from under this
        // landing. The abort comes before the landing: it also ends the wait of an earlier
        // seek.
        ctx.abandonHistoryJump()
        ctx.retakeScrollControl()
        return landOnSeq(seq)
      })
    }
    // Out-of-window: swap the window to a page centered on the seq, then land. The
    // preamble re-takes control and ends the request for the tail before the fetch, so the
    // auto-scroll effect does not stick for the swap. A swap that makes the tail live under
    // a reader at the loaded bottom enters follow mode again (see the hasNewerMessages effect
    // of the hook), and the landing then replaces that place. The centered fetch handles the
    // tail case too (BEFORE seq+1 returns the latest page when seq >= maxSeq).
    const { signal, fetched } = batch(() => {
      ctx.cancelScrollAnimation()
      ctx.retakeScrollControl()
      ctx.rearmScrollBufferFiller()
      ctx.leaveFollow()
      // Supersede any history jump already in flight -- abort its fetch, not just its
      // landing. The signal reaches the page fetch itself (onJumpToSeq passes it to the
      // paginator), so a seek that nobody wants any more ends its request instead of
      // completing and swapping the window.
      const jumpSignal = ctx.beginHistoryJump()
      return {
        signal: jumpSignal,
        fetched: extras.onJumpToSeq?.(seq, jumpSignal),
      }
    })
    return Promise.resolve(fetched)
      .then((applied) => {
        // Superseded by a newer jump, or abandoned by an input of the reader.
        if (signal.aborted)
          return false
        // Another history fetch of the store replaced this one, or no handler exists: the
        // window does not hold the page of this seek, so nothing lands on it.
        if (applied !== true)
          return false
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

  return { jumpToSeq, previewScrollTo }
}
