import type { ChatScrollState, ScrollContext } from './useChatScroll'
import type { ScrollAnchor } from '~/stores/chatTypes'

/**
 * The hidden->visible context captured at the top of handleResize (before browser
 * scroll-restoration can move the view) and handed whole to the three resize-outcome
 * helpers, so a savedScroll / followRequested can't disagree with the wasHidden they were
 * derived from. The flush of a visible resize reads followRequested again.
 */
interface ResizeContext {
  wasHidden: boolean
  savedScroll: ChatScrollState | undefined
  /**
   * The reader's request for the live tail: as of the ResizeObserver callback for a show,
   * and as of the flush for a resize of a visible pane (see handleResize).
   */
  followRequested: boolean
  /**
   * The anchor of the reader at the ResizeObserver callback. A scroll event of the browser
   * can arrive between the callback and the flush, and the scroll handler then replaces
   * the anchor of the hook with the position of that event.
   */
  anchor: ScrollAnchor | null
}

/**
 * Saved-viewport restore + resize handling for useChatScroll, extracted as a
 * factory (mirroring createStickyBottom / createScrollBufferFiller)
 * so the restore priority (anchor / raw-top / latest page / top) and the
 * resize-outcome routing (restore / stick / recheck) form one named unit with an
 * explicit dependency surface. It owns only the resize bookkeeping (prevClientHeight,
 * the pending-resize rAF); every reach into the rest of the hook is a named dep, so
 * the restore strategies can be reasoned about without the whole scroll machine.
 *
 * A restore that goes to a row, to the top, or to the tail asks the hook for that place
 * (ScrollContext.requestPlace): its rows can be staged, and the view then moves when they
 * are measured. The hook shows no place while the pane counts as hidden (see `paneHidden`),
 * so the flush of a show decides first, with the state from before the show.
 */
export function createViewportRestore(ctx: ScrollContext, extras: {
  // The hook builds these two from the shared context, so they are not members of it.
  stickToBottom: () => boolean
  forceScrollToBottom: () => void
  /** Clear the persisted saved-viewport scroll once a restore has consumed it. */
  clearSavedViewportScroll: () => void
  /** The persisted saved-viewport scroll for a hidden->visible restore. */
  savedViewportScroll: () => ChatScrollState | undefined
  /**
   * Place the view back at `anchor` for a reader who does not request the tail. Returns
   * whether it placed the view.
   */
  restoreAnchor: (anchor: ScrollAnchor | null) => boolean
  /** The anchor of the reader's last position. */
  lastAnchor: () => ScrollAnchor | null
  /**
   * Re-arm the geometry-derived memos after a resize settles the viewport. A resize can
   * change edge proximity (a keyboard/editor open clamps scrollTop flush to an edge)
   * WITHOUT a scroll event, so DOM-geometry memos would otherwise stay stale until the
   * next scroll.
   */
  onGeometrySettled: () => void
}) {
  // The viewport client height at the last resize, to detect hidden->visible.
  let prevClientHeight = 0
  // The pending resize-handling rAF, cancelled on the next resize and on dispose.
  let resizeRafId = 0

  // Strategy 2: no place resolved for a row anchor, but a raw scrollTop was saved (the
  // window was all-hidden at save -- no virtual spacer, so the offset was into non-virtual
  // content). Restore it ONLY while the window is STILL all-hidden: if virtual rows
  // appeared between save and restore (totalHeight > 0), the spacer now sits where the raw
  // offset pointed, so that pixel no longer maps to the saved content -- return false to
  // fall through to the next strategy rather than landing on the wrong rows. It declines
  // also while loaded rows exist and none is in the list: the total height is 0 then only
  // because no row joined yet.
  const restoreFromRawTop = (savedScroll: ChatScrollState): boolean => {
    const rawScrollTop = savedScroll.rawScrollTop
    if (rawScrollTop == null || ctx.virt.totalHeight() > 0 || ctx.awaitingFirstView())
      return false
    ctx.placeAt(() => rawScrollTop)
    return true
  }

  const restoreSavedViewport = (savedScroll: ChatScrollState) => {
    if (!ctx.getEl())
      return
    // Strategy 1: the saved ROW ANCHOR. The hook resolves it to a place: the row itself
    // when it is a loaded visible row, else the NEAREST loaded visible row by seq. So a
    // reader scrolled UP into older history whose rows were trimmed while the pane was
    // hidden lands on the nearest survivor, not at the unrelated live bottom. The place
    // waits until its rows are measured.
    const savedAnchor = savedScroll.anchor
    const place = savedAnchor === undefined
      ? undefined
      : ctx.placeForAnchor(savedAnchor)
    // Every strategy consumes the saved state.
    extras.clearSavedViewportScroll()
    if (place !== undefined) {
      void ctx.requestPlace(place)
      return
    }
    // An all-hidden non-bottom save carries a raw scroll offset but no resolvable
    // anchor; restore that exact offset BEFORE the hasMoreNewer tail-snap, so a
    // reader who scrolled UP into a hidden stretch while windowed away from the live
    // tail returns to where they were rather than being yanked to the live bottom
    // (the tail-snap is right for an AT-bottom save -- tryStickOnShow -- but this
    // path is reached only for a NON-bottom save). restoreFromRawTop only fires
    // while the window is still all-hidden (totalHeight 0), exactly where the raw
    // offset still maps to the saved content; otherwise it declines and the
    // tail-snap below stands as the anchor-less fallback.
    if (restoreFromRawTop(savedScroll))
      return
    // Strategy 3: the anchor cannot be ordered against the loaded rows (no seq, or no
    // loaded visible row), and the window is paged away from the live tail.
    if (savedScroll.hasMoreNewer) {
      extras.forceScrollToBottom()
      return
    }
    // Strategy 4: nothing to anchor to and no usable saved offset -- ask for the top as a
    // best-effort fallback.
    void ctx.requestPlace({ kind: 'top' })
  }

  // Hidden->visible with a saved NON-bottom scroll: restore the saved viewport (see
  // restoreSavedViewport). Returns true when it owns the resize.
  const tryRestoreHidden = (rc: ResizeContext): boolean => {
    if (!(rc.wasHidden && rc.savedScroll && !rc.savedScroll.atBottom))
      return false
    restoreSavedViewport(rc.savedScroll)
    return true
  }

  // Go to the bottom on any of: hidden->visible with a saved at-bottom scroll
  // (clear the save), hidden->visible without a saved scroll but a request for the tail
  // captured pre-RO, or an already-visible viewport resize (editor/keyboard) while the
  // reader requests the tail. (Content-height growth is handled by the geometry effect,
  // not here.) A pane that was hidden asks for the tail: the rows that arrived while it
  // was hidden are staged, so the view moves once, when the last rows are measured. A
  // resize of a visible pane sticks at once, unless a tail waits: a stick would put the
  // view at the bottom of the measured rows, and the wait would move it again. A
  // restored at-bottom view windowed away from the live tail (hasMoreNewer) is NOT
  // really at the bottom -- the in-memory tail isn't the live tail -- so re-fetch
  // the latest page and snap to the true tail (forceScrollToBottom jumps when
  // hasNewerMessages) rather than sticking to a stale window bottom, which would
  // strand the user above the live messages with the scroll-to-bottom button still
  // active. Returns true when it owns the resize.
  const tryStickOnShow = (rc: ResizeContext): boolean => {
    if (!(rc.followRequested || (rc.wasHidden && rc.savedScroll?.atBottom)))
      return false
    if (rc.wasHidden && rc.savedScroll?.hasMoreNewer) {
      extras.clearSavedViewportScroll()
      extras.forceScrollToBottom()
      return true
    }
    if (rc.wasHidden) {
      // A request that stands (the reader place is the tail, or a tail that waits) is
      // replaced by an equal one.
      void ctx.requestPlace({ kind: 'tail' })
    }
    else if (ctx.showWaitingPlace() === 'none') {
      extras.stickToBottom()
    }
    else {
      // A tail waits, or its placement just ran: record the position of the resized pane,
      // as recheckOnResize does, and write no stick.
      ctx.recordPosition()
    }
    if (rc.wasHidden && rc.savedScroll)
      extras.clearSavedViewportScroll()
    return true
  }

  // Neither restore nor stick (an already-visible resize, or a hidden->visible show
  // that wasn't at the bottom and had no saved non-bottom scroll). While the pane is
  // HIDDEN, a geometry change sets the layout shift to 0 and compensates nothing, so it
  // moves the content under the reader, and the browser can also move `scrollTop`. So a
  // show puts the reader back at the anchor captured in the ResizeObserver callback. Then
  // record the position as an own write: it re-seats the direction and velocity baselines
  // (a viewport shrink clamps scrollTop with no write, and a stale baseline would read that
  // clamp as the reader's move), runs the position check after the restore, and recomputes
  // the rendered slice (a taller pane reveals rows below the old slice; safe to mount
  // synchronously -- we're in a rAF, outside RO delivery). A restore that placed the view
  // recorded its write and refreshed the slice itself, and a second record would end the
  // anchor that `extras.restoreAnchor` holds for a row that does not show its height yet.
  //
  // A place that waits comes first. When it is reached, its placement puts the view there,
  // in place of the restore of the last anchor. While it still waits, the view stays, and
  // only the position of the pane is recorded.
  const recheckOnResize = (rc: ResizeContext) => {
    const shown = ctx.showWaitingPlace()
    if (shown === 'shown')
      return
    if (shown === 'waiting') {
      ctx.recordPosition()
      return
    }
    if (rc.wasHidden && extras.restoreAnchor(rc.anchor))
      return
    ctx.recordPosition()
  }

  const handleResize = () => {
    cancelAnimationFrame(resizeRafId)
    // Capture hidden→visible state, the request for the tail, and the reader's anchor
    // NOW (in the ResizeObserver callback), before browser scroll-restoration events can
    // fire and replace the anchor. The flush of a visible resize reads the request again.
    const el = ctx.getEl()
    const ch = el?.clientHeight ?? 0
    const wasHidden = prevClientHeight === 0 && ch > 0
    const rc: ResizeContext = {
      wasHidden,
      followRequested: ctx.followRequested(),
      savedScroll: wasHidden
        ? extras.savedViewportScroll()
        : undefined,
      anchor: extras.lastAnchor(),
    }
    const flush = () => {
      if (!ctx.getEl())
        return
      if (ctx.isAnimating()) {
        // A scroll animation is mid-flight; flushing now would fight it. A
        // hidden->visible RESTORE must still survive, though: it has no other
        // trigger (animation completion does NOT re-invoke handleResize) and a
        // dropped restore strands the viewport, so re-schedule it -- with the SAME
        // captured rc/ch -- until the animation settles. A non-hidden resize is
        // re-armed by the next resize, so dropping it here (the prior behavior) is
        // fine.
        if (rc.wasHidden)
          resizeRafId = requestAnimationFrame(flush)
        return
      }
      prevClientHeight = ch
      // A show keeps the request of the callback: the browser can move the position of a
      // pane that it hid and showed, and the `scroll` event of that move must not end the
      // request. A resize of a visible pane reads the request now, so a move of the reader
      // between the callback and this frame ends it.
      const outcome: ResizeContext = rc.wasHidden
        ? rc
        : {
            ...rc,
            followRequested: ctx.followRequested(),
          }
      // Three mutually-exclusive outcomes in priority order.
      if (!tryRestoreHidden(outcome) && !tryStickOnShow(outcome))
        recheckOnResize(outcome)
      // A place that was reached while the pane counted as hidden, and that no step above
      // replaced or showed, is shown now (see `paneHidden`).
      ctx.showWaitingPlace()
      // A write of the outcome re-arms the geometry-derived memos, but a resize of a pane
      // that the browser hid writes nothing, and the new viewport size is what they read.
      extras.onGeometrySettled()
    }
    resizeRafId = requestAnimationFrame(flush)
  }

  /**
   * Mount-time restore for a VISIBLE (re)mount. A tile split/merge or a workspace
   * switch recreates ChatView over a still-populated store: the fresh scroll
   * container mounts at scrollTop 0 with the reader's position saved by the previous
   * instance's unmount (see UseChatScrollOptions.onSaveViewportScroll). The RO
   * hidden->visible path can never see that remount -- the pane is visible from its
   * first layout, so wasHidden stays false -- so the restore must run at mount.
   *
   * Reuses the exact hidden->visible outcome pair (restore a non-bottom save;
   * the tail / jump-to-latest for an at-bottom one) through a synthetic wasHidden
   * context, so the two restore entry points cannot drift. Every branch consumes
   * (clears) the saved state, and sets the reader place of the hook at its call, so the
   * first-view rule and the auto-scroll effect of the hook see the place of the restore.
   * A HIDDEN mount (a pane with no layout box, clientHeight 0) declines and leaves the saved state for
   * the RO path; with no saved state the default mount placement of the hook stands.
   */
  const restoreOnMount = () => {
    if (!ctx.getVisibleEl())
      return
    const savedScroll = extras.savedViewportScroll()
    if (!savedScroll)
      return
    // followRequested is FALSE, not the request of the hook: the resize path captures the
    // live request because a resize with no saved scroll must still re-stick a pane that
    // follows the tail, but a mount always HAS a saved scroll (early return above), so
    // the outcome must derive solely from savedScroll.atBottom. Feeding the fresh request
    // (which starts true at a mount on the live tail) would make tryStickOnShow's
    // `followRequested || ...` first clause always true -- harmless only because
    // tryRestoreHidden already claims every non-bottom save, but a latent trap if that
    // routing ever changes.
    const rc: ResizeContext = {
      wasHidden: true,
      savedScroll,
      followRequested: false,
      anchor: null,
    }
    // tryRestoreHidden owns every non-bottom save; an at-bottom save always takes
    // tryStickOnShow's wasHidden branch (the tail, or jump-to-latest when the saved
    // window was paged away from the live tail).
    if (!tryRestoreHidden(rc))
      tryStickOnShow(rc)
    // Re-arm the geometry-derived memos for the mounted viewport: the jump to the latest
    // page of an at-bottom save writes nothing that would.
    extras.onGeometrySettled()
  }

  return {
    handleResize,
    restoreOnMount,
    /** Seed prevClientHeight from the freshly-measured viewport on mount. */
    initClientHeight() {
      prevClientHeight = ctx.getEl()?.clientHeight ?? 0
    },
    /**
     * Whether the pane counts as hidden: the height that the mount, or the last flush of a
     * resize, recorded is 0. The browser gives a pane that is shown again its layout box one
     * frame before the flush of the show, and staged rows can measure in that frame. The pane
     * still counts as hidden then, so the hook shows no place before the flush decided: the
     * flush holds the state from before the show (a saved viewport, the request for the
     * tail, the last anchor). A flush that waits for a scroll animation records no height
     * yet, so the pane counts as hidden until the flush runs its steps.
     */
    paneHidden: (): boolean => prevClientHeight === 0,
    /** Cancel any pending resize rAF (on cleanup). */
    cancelPendingResize() {
      cancelAnimationFrame(resizeRafId)
    },
  }
}
