import type { ScrollContext } from './useChatScroll'
import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import type { SavedViewportScroll, ScrollAnchor } from '~/stores/chatTypes'
import { firstServerSeq, lastServerSeq } from '~/stores/chatMessageOrder'
import { nearestServerRowIndexBySeq } from './chatScrollAnchor'
import { clampScrollTop } from './chatScrollGeometry'

// ---------------------------------------------------------------------------
// Scroll-rail seek (jumpToSeq / previewScrollTo)
//
// The seek machine a scroll-rail dot click / track click / thumb release drives: an
// in-window aligned landing, or a fetch-around-seq window swap then land. Extracted from
// useChatScroll so the epoch-guarded pending-seek state and the landing logic have a named
// home + a test seam (useChatScroll.seek.test.ts), mirroring the sibling rail/scroll units.
// It owns its own pendingSeek / seekEpoch; the hook's handleScroll cancels an in-flight seek
// on a genuine user scroll via cancelPendingSeek. Reads the scroll primitives through the
// shared ScrollContext plus a small `extras` bag for the deps unique to it.
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
   * A guard-marked programmatic scroll write for in-window rail thumb-drag live-scroll.
   * Marked like the landing writes so the drag's scroll events don't trip velocity
   * sampling, edge pagination, or the buffer filler.
   */
  previewScrollTo: (top: number) => void
  /**
   * Abandon any in-flight out-of-window seek so its late-resolving fetch cannot land
   * (yank the viewport) after the user has taken manual control. The hook's handleScroll
   * calls this on a genuine user scroll, and the rail calls it (via the hook) when a fresh
   * thumb grab begins -- a drag scrolls only programmatically, so it never trips the
   * user-scroll path. Clearing pendingSeek makes an in-flight seek's `.then` short-circuit
   * (its epoch no longer matches a live pendingSeek).
   */
  cancelPendingSeek: () => void
}

/**
 * The deps createChatSeek needs beyond the shared {@link ScrollContext}: the live message
 * list, a handful of hook-private scroll actions (some declared later in the hook and threaded
 * in as thunks -- e.g. rearmScrollBufferFiller), and the host's window-swap / save-viewport
 * callbacks.
 */
export interface ChatSeekExtras {
  /** Live loaded messages (server rows + trailing optimistic locals). */
  messages: () => AgentChatMessage[]
  /** Clamped logical scrollTop read (Safari rubber-band safe). */
  readScrollTop: (el: HTMLDivElement) => number
  /** Stop any running scroll animation before a landing / preview write. */
  cancelScrollAnimation: () => void
  /** Cancel AND drop the deferred fling-settle so a landing isn't fought by leftover drift. */
  cancelFlingSettle: () => void
  /** Capture the current viewport anchor (leaving follow mode before an out-of-window swap). */
  captureAnchor: () => void
  /** Pin a row's top across the post-landing measurement storm; false if it isn't in the offset map. */
  captureRowTopAnchor: (id: string) => boolean
  /** Bump the geometry tick so DOM-reading memos (stall indicators) re-evaluate after a landing. */
  bumpGeomTick: () => void
  /** Re-take manual scroll control (clear the per-window filler pause / settle / suppress flags). */
  retakeScrollControl: () => void
  /** Drop the old window's per-side filler state when a jump replaces the message window. */
  rearmScrollBufferFiller: () => void
  /** Refresh atBottom off a fresh DOM read (used when an out-of-window fetch fails). */
  checkAtBottom: () => void
  /** Replace the window with a page centered on `seq` (the out-of-window seek). */
  onJumpToSeq: ((seq: bigint) => Promise<void> | void) | undefined
  /** Hand a hidden-mid-jump target to the SavedViewportScroll restore path. */
  onSaveViewportScroll: ((state: SavedViewportScroll) => void) | undefined
  /** Whether newer messages exist beyond the loaded window (windowed away from tail). */
  hasNewerMessages: () => boolean
}

export function createChatSeek(ctx: ScrollContext, extras: ChatSeekExtras): ChatSeek {
  // Pixels of breathing room left above the target row when landing on a seek.
  const SEEK_ALIGN_OFFSET_PX = 8

  // The in-flight out-of-window seek, guarded by an epoch so a superseding jump / a genuine
  // user scroll (which clears it via cancelPendingSeek) makes a late resolve a no-op.
  let pendingSeek: { seq: bigint, epoch: number } | null = null
  let seekEpoch = 0

  // The window's first/last SERVER seq (skipping seq-0n optimistic locals) come from the
  // shared chatMessageOrder helpers -- the same definition the rail's window bounds and the
  // windowing core use -- so the in-window seek decision can't drift from them.

  /**
   * The loaded server row nearest `seq` by absolute seq distance, via the same
   * nearestServerRowIndexBySeq scan the trim-restore anchor recovery uses (scrollTopNearAnchor),
   * so the seek landing and that recovery can't drift on the skip/tie-break rule.
   */
  const nearestRowIdBySeq = (seq: bigint): string | undefined => {
    const msgs = extras.messages()
    const idx = nearestServerRowIndexBySeq(msgs, seq)
    return idx < 0 ? undefined : msgs[idx].id
  }

  /**
   * Land the viewport on the loaded row nearest `seq`, pinning it through the
   * post-landing measurement storm. Instant, not animated: an animation over
   * mostly-estimated heights would fight the re-pin every frame.
   *
   * Returns whether the landing actually MOVED the scroll position (i.e. a scroll event,
   * and hence a settling metrics change, will follow). It returns false -- no scroll -- when
   * the container is detached/hidden, no target row resolves, OR the target is already the
   * current scrollTop; the rail's drag-release hold uses this to know whether to wait for a
   * metrics settle or clear immediately, so it never sticks when the landing scrolls nowhere.
   */
  const landOnSeq = (seq: bigint): boolean => {
    const el = ctx.getEl()
    if (!el || el.clientHeight === 0)
      return false
    const id = nearestRowIdBySeq(seq)
    if (!id)
      return false
    // Resolve the row's top scrollTop via the anchor resolver (offsetWithinRow 0 pins the
    // row top to the viewport top). Falls back to the nearest-survivor resolver if the id
    // isn't in the current offset map. Both are already on the narrowed virtualizer type.
    const anchor: ScrollAnchor = { id, offsetWithinRow: 0, seq }
    const resolved = ctx.virt.scrollTopForAnchor(anchor) ?? ctx.virt.scrollTopNearAnchor(anchor)
    if (resolved == null)
      return false
    extras.cancelScrollAnimation()
    extras.cancelFlingSettle()
    const target = clampScrollTop(el, resolved - SEEK_ALIGN_OFFSET_PX)
    // Whether this write will actually move the view (and so emit a scroll event): an equal
    // target is a no-op write that fires no event, so no metrics settle will come.
    const scrolled = target !== extras.readScrollTop(el)
    // Marked programmatic so the echo isn't misread as a user gesture (no velocity
    // sample, no edge pagination, no buffer fill on the still-echo).
    ctx.writeScrollTop(target, 'seek-jump')
    // Pin the TARGET row's top across the measurement commits that follow (the fresh
    // rows are mostly unmeasured). The hold survives the landing write's own echo and
    // auto-releases once the row measures; any genuine user gesture releases it too.
    // Fall back to a viewport anchor if the row isn't in the offset map yet.
    if (!extras.captureRowTopAnchor(id))
      ctx.setAnchor(ctx.virt.anchorAt(extras.readScrollTop(el)))
    ctx.setAtBottom(ctx.isAtBottom())
    ctx.refreshViewport()
    extras.bumpGeomTick()
    return scrolled
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
    const firstS = firstServerSeq(msgs)
    const lastS = lastServerSeq(msgs)
    // In-window: the seq sits within the loaded server span. Re-take control (clear the
    // per-window filler pause/settle/suppress flags, same as forceScrollToBottom) and land.
    if (firstS !== undefined && lastS !== undefined && seq >= firstS && seq <= lastS) {
      pendingSeek = null
      extras.retakeScrollControl()
      return Promise.resolve(landOnSeq(seq))
    }
    // Out-of-window: swap the window to a page centered on the seq, then land. The
    // preamble leaves follow mode and re-takes control so the swap's restick/auto-load
    // paths stay inert; the centered fetch handles the tail case too (BEFORE seq+1
    // returns the latest page when seq >= maxSeq).
    extras.cancelScrollAnimation()
    extras.cancelFlingSettle()
    extras.retakeScrollControl()
    extras.rearmScrollBufferFiller()
    if (ctx.isFollowing())
      extras.captureAnchor()
    ctx.setAtBottom(false)
    const epoch = ++seekEpoch
    pendingSeek = { seq, epoch }
    return Promise.resolve(extras.onJumpToSeq?.(seq))
      .then(() => {
        // Superseded by a newer jump, or cancelled by a genuine user scroll.
        if (pendingSeek?.epoch !== epoch)
          return false
        pendingSeek = null
        const cur = ctx.getEl()
        if (cur && cur.clientHeight > 0) {
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
            hasMoreNewer: extras.hasNewerMessages(),
          })
        }
        return false
      })
      .catch(() => {
        if (pendingSeek?.epoch === epoch)
          pendingSeek = null
        extras.checkAtBottom()
        return false
      })
  }

  const previewScrollTo = (top: number) => {
    const el = ctx.getEl()
    if (!el)
      return
    extras.cancelScrollAnimation()
    ctx.writeScrollTop(clampScrollTop(el, top), 'seek-drag')
  }

  const cancelPendingSeek = () => {
    pendingSeek = null
  }

  return { jumpToSeq, previewScrollTo, cancelPendingSeek }
}
