import type { ChatScrollState, ScrollContext } from './useChatScroll'
import type { ScrollAnchor } from '~/stores/chatTypes'

/**
 * The hidden->visible context captured once at the top of handleResize (before
 * browser scroll-restoration can move the view) and handed whole to the three
 * resize-outcome helpers, so a savedScroll / savedAtBottom can't disagree with the
 * wasHidden they were derived from.
 */
interface ResizeContext {
  wasHidden: boolean
  savedScroll: ChatScrollState | undefined
  /** The reader's request for the live tail at the ResizeObserver callback. */
  savedAtBottom: boolean
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
 * so the three-strategy restore priority (anchor / raw-top / clamp-to-top) and the
 * resize-outcome routing (restore / stick / recheck) form one named unit with an
 * explicit dependency surface. It owns only the resize bookkeeping (prevClientHeight,
 * the pending-resize rAF); every reach into the rest of the hook is a named dep, so
 * the restore strategies can be reasoned about without the whole scroll machine.
 */
export function createViewportRestore(ctx: ScrollContext, extras: {
  // High-level scroll actions declared later in the hook than the shared context.
  stickToBottom: () => boolean
  forceScrollToBottom: () => void
  /** Clear the persisted saved-viewport scroll once a restore has consumed it. */
  clearSavedViewportScroll: () => void
  /** The persisted saved-viewport scroll for a hidden->visible restore. */
  savedViewportScroll: () => ChatScrollState | undefined
  /** Place the view back at `anchor` for a reader who does not follow the tail. */
  restoreAnchor: (anchor: ScrollAnchor | null) => void
  /** The anchor of the reader's last position. */
  lastAnchor: () => ScrollAnchor | null
  /** The reader's request for the live tail: the `following` flag of the hook. */
  followRequested: () => boolean
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

  // Strategy 1: restore by resolving the saved ROW ANCHOR against the offset map.
  // Returns false (try the next strategy) when no anchor was saved or it no longer
  // resolves to a scroll position. The placement rebases first, so the anchor resolves
  // under the layout shift that the write lands in.
  const restoreFromAnchor = (savedScroll: ChatScrollState): boolean => {
    const savedAnchor = savedScroll.anchor ?? null
    if (savedAnchor === null)
      return false
    if (ctx.placeAt(() => ctx.virt.scrollTopForAnchor(savedAnchor)) === null)
      return false
    extras.clearSavedViewportScroll()
    return true
  }

  // Strategy 2: no row anchor resolved, but a raw scrollTop was saved (the window
  // was all-hidden at save -- no virtual spacer, so the offset was into non-virtual
  // content and carried no estimation drift). Restore it ONLY while the window is
  // STILL all-hidden: if virtual rows appeared between save and restore
  // (totalHeight > 0), the spacer now sits where the raw offset pointed, so that
  // pixel no longer maps to the saved content -- return false to fall through to
  // the clamp-to-top best-effort rather than landing on the wrong rows.
  const restoreFromRawTop = (savedScroll: ChatScrollState): boolean => {
    const rawScrollTop = savedScroll.rawScrollTop
    if (rawScrollTop == null || ctx.virt.totalHeight() > 0)
      return false
    ctx.placeAt(() => rawScrollTop)
    return true
  }

  // Strategy 2.5: the saved row anchor no longer resolves (the row was TRIMMED away
  // while hidden) AND the window isn't all-hidden (so restoreFromRawTop declined) --
  // recover by landing on the NEAREST surviving row by seq instead of the live-tail
  // snap below. A reader scrolled UP into older history whose rows were trimmed as the
  // window advanced lands on the oldest loaded row (the nearest survivor), not at the
  // unrelated live bottom. Returns false (fall through) when the anchor has no seq or
  // no surviving server row resolves.
  const restoreNearAnchor = (savedScroll: ChatScrollState): boolean => {
    const savedAnchor = savedScroll.anchor ?? null
    if (savedAnchor === null)
      return false
    return ctx.placeAt(() => ctx.virt.scrollTopNearAnchor(savedAnchor)) !== null
  }

  // Strategy 3: nothing to anchor to and no usable saved offset -- place at the top as a
  // best-effort fallback.
  const restoreToTop = () => {
    ctx.placeAt(() => 0)
  }

  const restoreSavedViewport = (savedScroll: ChatScrollState) => {
    if (!ctx.getEl())
      return
    // Restore strategies in priority order, each owning its placement;
    // restoreFromAnchor clears the saved viewport itself, the others clear it here
    // before falling through.
    if (restoreFromAnchor(savedScroll))
      return
    extras.clearSavedViewportScroll()
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
    // The anchor row was trimmed away (restoreFromAnchor declined) and virtual rows are
    // present (restoreFromRawTop declined): recover to the NEAREST surviving row rather
    // than snapping to the live tail, which would yank a scrolled-up reader to the
    // bottom. Falls through only when the anchor can't be ordered (no seq / empty).
    if (restoreNearAnchor(savedScroll))
      return
    if (savedScroll.hasMoreNewer) {
      extras.forceScrollToBottom()
      return
    }
    restoreToTop()
  }

  // Hidden->visible with a saved NON-bottom scroll: restore by resolving the saved
  // anchor against the offset map. Returns true when it owns the resize.
  const tryRestoreHidden = (rc: ResizeContext): boolean => {
    if (!(rc.wasHidden && rc.savedScroll && !rc.savedScroll.atBottom))
      return false
    restoreSavedViewport(rc.savedScroll)
    return true
  }

  // Stick to the bottom on any of: hidden->visible with a saved at-bottom scroll
  // (clear the save), hidden->visible without a saved scroll but a request for the tail
  // captured pre-RO, or an already-visible viewport resize (editor/keyboard) while the
  // reader requests the tail. (Content-height growth is handled by the geometry effect,
  // not here.) A
  // restored at-bottom view windowed away from the live tail (hasMoreNewer) is NOT
  // really at the bottom -- the in-memory tail isn't the live tail -- so re-fetch
  // the latest page and snap to the true tail (forceScrollToBottom jumps when
  // hasNewerMessages) rather than sticking to a stale window bottom, which would
  // strand the user above the live messages with the scroll-to-bottom button still
  // active. Returns true when it owns the resize.
  const tryStickOnShow = (rc: ResizeContext): boolean => {
    if (!(rc.savedAtBottom || (rc.wasHidden && rc.savedScroll?.atBottom)))
      return false
    if (rc.wasHidden && rc.savedScroll?.hasMoreNewer) {
      extras.clearSavedViewportScroll()
      extras.forceScrollToBottom()
      return true
    }
    extras.stickToBottom()
    if (rc.wasHidden && rc.savedScroll)
      extras.clearSavedViewportScroll()
    return true
  }

  // Neither restore nor stick (an already-visible resize, or a hidden->visible show
  // that wasn't at the bottom and had no saved non-bottom scroll). A geometry change that
  // landed while the pane was HIDDEN moved nothing (the layout shift is 0 while hidden),
  // and the browser can move `scrollTop` while the pane is hidden, so a show puts the
  // reader back at the anchor captured in the ResizeObserver callback. Then record the
  // position as an own write: it re-seats the direction and velocity baselines (a viewport
  // shrink clamps scrollTop with no write, and a stale baseline would read that clamp as
  // the reader's move), runs the position check after the restore, and recomputes the
  // rendered slice (a taller pane reveals rows below the old slice; safe to mount
  // synchronously -- we're in a rAF, outside RO delivery).
  const recheckOnResize = (rc: ResizeContext) => {
    if (rc.wasHidden)
      extras.restoreAnchor(rc.anchor)
    ctx.syncAfterWrite()
  }

  // Shared tail for both restore entry points (the resize flush and restoreOnMount): once
  // a restore/stick has PLACED the viewport, re-arm the geometry-derived memos, since a
  // programmatic placement / edge clamp fires no scroll event they could react to.
  const settleGeometryAfterPlacement = () => {
    extras.onGeometrySettled()
  }

  const handleResize = () => {
    cancelAnimationFrame(resizeRafId)
    // Capture hidden→visible state, the request for the tail, and the reader's anchor
    // NOW (in the ResizeObserver callback), before browser scroll-restoration events can
    // fire and replace the anchor.
    const el = ctx.getEl()
    const ch = el?.clientHeight ?? 0
    const wasHidden = prevClientHeight === 0 && ch > 0
    const rc: ResizeContext = {
      wasHidden,
      savedAtBottom: extras.followRequested(),
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
      // Three mutually-exclusive outcomes in priority order.
      if (!tryRestoreHidden(rc) && !tryStickOnShow(rc))
        recheckOnResize(rc)
      settleGeometryAfterPlacement()
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
   * stick / jump-to-latest for an at-bottom one) through a synthetic wasHidden
   * context, so the two restore entry points cannot drift. Every branch consumes
   * (clears) the saved state. A HIDDEN mount (inactive tab, clientHeight 0)
   * declines and leaves the saved state for the RO path; with no saved state the
   * default mount placement (the auto-scroll effect's tail stick) stands.
   */
  const restoreOnMount = () => {
    const el = ctx.getEl()
    if (!el || el.clientHeight === 0)
      return
    const savedScroll = extras.savedViewportScroll()
    if (!savedScroll)
      return
    // savedAtBottom is FALSE, not the request of the hook: the resize path captures the
    // live request because a resize with no saved scroll must still re-stick a pane that
    // follows the tail, but a mount always HAS a saved scroll (early return above), so
    // the outcome must derive solely from savedScroll.atBottom. Feeding the fresh request
    // (which starts true at a mount on the live tail) would make tryStickOnShow's
    // `savedAtBottom || ...` first clause always true -- harmless only because
    // tryRestoreHidden already claims every non-bottom save, but a latent trap if that
    // routing ever changes.
    const rc: ResizeContext = {
      wasHidden: true,
      savedScroll,
      savedAtBottom: false,
      anchor: null,
    }
    // tryRestoreHidden owns every non-bottom save; an at-bottom save always takes
    // tryStickOnShow's wasHidden branch (stick, or jump-to-latest when the saved
    // window was paged away from the live tail).
    if (!tryRestoreHidden(rc))
      tryStickOnShow(rc)
    settleGeometryAfterPlacement()
  }

  return {
    handleResize,
    restoreOnMount,
    /** Seed prevClientHeight from the freshly-measured viewport on mount. */
    initClientHeight() {
      prevClientHeight = ctx.getEl()?.clientHeight ?? 0
    },
    /** Cancel any pending resize rAF (on cleanup). */
    cancelPendingResize() {
      cancelAnimationFrame(resizeRafId)
    },
  }
}
