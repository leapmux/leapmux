import type { ChatScrollState, ScrollContext } from './useChatScroll'
import { clampScrollTop, isNearTopBand, maxScrollTopOf } from './chatScrollGeometry'

/**
 * The hidden->visible context captured once at the top of handleResize (before
 * browser scroll-restoration can corrupt atBottom) and handed whole to the three
 * resize-outcome helpers, so a savedScroll / savedAtBottom can't disagree with the
 * wasHidden they were derived from.
 */
interface ResizeContext {
  wasHidden: boolean
  savedScroll: ChatScrollState | undefined
  savedAtBottom: boolean
}

/**
 * Saved-viewport restore + resize handling for useChatScroll, extracted as a
 * factory (mirroring createStickyBottom / createFlingSettle / createScrollBufferFiller)
 * so the three-strategy restore priority (anchor / raw-top / clamp-to-top) and the
 * resize-outcome routing (restore / stick / recheck) form one named unit with an
 * explicit dependency surface. It owns only the resize bookkeeping (prevClientHeight,
 * the pending-resize rAF); every reach into the rest of the hook is a named dep, so
 * the restore strategies can be reasoned about without the whole scroll machine.
 */
export function createViewportRestore(ctx: ScrollContext, extras: {
  // High-level scroll actions declared later in the hook than the shared context.
  checkAtBottom: () => void
  repinToAnchor: () => void
  stickToBottom: () => boolean
  forceScrollToBottom: () => void
  /** Clear the persisted saved-viewport scroll once a restore has consumed it. */
  clearSavedViewportScroll: () => void
  /** The persisted saved-viewport scroll for a hidden->visible restore. */
  savedViewportScroll: () => ChatScrollState | undefined
  /** Set the one-shot "don't auto-load older after a restore" flag -- the hook owns it (the buffer filler reads it). */
  setSuppressOlder: (v: boolean) => void
  /** Re-seat the scroll-direction baseline after a resize clamps scrollTop. */
  setLastScrollTopForDir: (v: number) => void
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

  // Arm the one-shot older-load suppression iff a restore LANDED in the top half of
  // the viewport, so older history loads only on an explicit scroll-to-top -- not as
  // a side effect of the restore's own refresh or the next passive scroll. Takes the
  // landed scroll position (read back AFTER the programmatic write + clamp), NOT the
  // pre-clamp anchor estimate: an over-estimated `top` that lands near the top after
  // browser clamping / a post-restore measurement shrink would otherwise skip arming
  // and paginate.
  const armSuppressIfNearTop = (pos: number) => {
    const el = ctx.getEl()
    if (el && el.clientHeight > 0 && isNearTopBand(el, pos))
      extras.setSuppressOlder(true)
  }

  // Strategy 1: restore by resolving the saved ROW ANCHOR against the offset map.
  // Returns false (try the next strategy) when no anchor was saved or it no longer
  // resolves to a scroll position.
  const restoreFromAnchor = (el: HTMLDivElement, savedScroll: ChatScrollState): boolean => {
    const savedAnchor = savedScroll.anchor ?? null
    const top = savedAnchor ? ctx.virt.scrollTopForAnchor(savedAnchor) : null
    if (top == null)
      return false
    ctx.writeScrollTop(top, 'viewport-restore-anchor')
    // Capture from the LANDED scrollTop (post browser-clamp), not the pre-clamp
    // `top`, so repinToAnchor's stale-anchor check measures movement from where
    // the viewport actually is, not an unreachable target in a shrunk window.
    ctx.setAnchor(savedAnchor, el.scrollTop)
    ctx.refreshViewport()
    ctx.setAtBottom(false)
    // A restore that lands NEAR THE TOP would otherwise let the next passive scroll
    // event (or this restore's own refresh) fire loadOlderMessages as a side
    // effect. Arm the one-shot suppression from the LANDED scrollTop (post write +
    // refresh + browser clamp), not the pre-clamp `top` estimate, so a near-top
    // landing always arms even when `top` over-estimated above half a viewport.
    // Only near-top restores need it; a mid/bottom restore keeps paginating
    // normally (the flag gates BOTH edges).
    armSuppressIfNearTop(el.scrollTop)
    extras.clearSavedViewportScroll()
    return true
  }

  // Strategy 2: no row anchor resolved, but a raw scrollTop was saved (the window
  // was all-hidden at save -- no virtual spacer, so the offset was into non-virtual
  // content and carried no estimation drift). Restore it ONLY while the window is
  // STILL all-hidden: if virtual rows appeared between save and restore
  // (totalHeight > 0), the spacer now sits where the raw offset pointed, so that
  // pixel no longer maps to the saved content -- return false to fall through to
  // the clamp-to-top best-effort rather than landing on the wrong rows. Clamped to
  // the scrollable range; arms auto-load-older suppression near top.
  const restoreFromRawTop = (el: HTMLDivElement, savedScroll: ChatScrollState): boolean => {
    if (savedScroll.rawScrollTop == null || ctx.virt.totalHeight() > 0)
      return false
    const clamped = clampScrollTop(el, savedScroll.rawScrollTop)
    ctx.writeScrollTop(clamped, 'viewport-restore-raw-top')
    armSuppressIfNearTop(el.scrollTop)
    ctx.refreshViewport()
    ctx.setAtBottom(false)
    return true
  }

  // Strategy 2.5: the saved row anchor no longer resolves (the row was TRIMMED away
  // while hidden) AND the window isn't all-hidden (so restoreFromRawTop declined) --
  // recover by landing on the NEAREST surviving row by seq instead of the live-tail
  // snap below. A reader scrolled UP into older history whose rows were trimmed as the
  // window advanced lands on the oldest loaded row (the nearest survivor), not at the
  // unrelated live bottom. Returns false (fall through) when the anchor has no seq or
  // no surviving server row resolves. Re-captures a fresh anchor at the landed row so
  // a later re-pin tracks real content; arms the near-top older-load suppression.
  const restoreNearAnchor = (el: HTMLDivElement, savedScroll: ChatScrollState): boolean => {
    const savedAnchor = savedScroll.anchor ?? null
    const top = savedAnchor ? ctx.virt.scrollTopNearAnchor(savedAnchor) : null
    if (top == null)
      return false
    ctx.writeScrollTop(top, 'viewport-restore-near-anchor')
    ctx.setAnchor(ctx.virt.anchorAt(el.scrollTop), el.scrollTop)
    ctx.refreshViewport()
    ctx.setAtBottom(false)
    armSuppressIfNearTop(el.scrollTop)
    return true
  }

  // Strategy 3: nothing to anchor to and no usable saved offset -- clamp to the top
  // as a best-effort fallback (and suppress auto-load-older, as a top landing does).
  const restoreToTop = () => {
    // Write through the programmatic guard (not a raw el.scrollTop = 0) so the
    // echoing scroll event is recognized as our own. A raw write from a non-zero
    // position emits a genuine scroll event that handleScroll treats as a user
    // scroll -- poisoning the velocity tracker with a phantom fling, capturing a
    // spurious anchor/lastScrollDir, and deferring the next re-pin into flingSettle.
    // Mirrors the sibling restoreFromAnchor/restoreFromRawTop strategies.
    ctx.writeScrollTop(0, 'viewport-restore-top')
    extras.setSuppressOlder(true)
    ctx.refreshViewport()
    ctx.setAtBottom(false)
  }

  const restoreSavedViewport = (savedScroll: ChatScrollState) => {
    const el = ctx.getEl()
    if (!el)
      return
    // Recompute the one-shot older-load suppression from THIS restore's landing
    // position alone: clear any prior arming up front so each restore branch only
    // has to set it true where its landing is near the top. A near-top restore
    // arms the flag and it survives (cleared otherwise only by an explicit
    // scroll-to-top) -- and this path early-returns before handleResize's own
    // `= false`, so without this reset a mid/bottom restore would keep whatever
    // the flag last held instead of "keep paginating normally" as its branches
    // below intend. Self-contained here rather than relying on a prior
    // hidden->visible transition having cleared it.
    extras.setSuppressOlder(false)
    // Restore strategies in priority order, each owning its write-then-settle
    // epilogue; restoreFromAnchor clears the saved viewport itself, the others clear
    // it here before falling through.
    if (restoreFromAnchor(el, savedScroll))
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
    if (restoreFromRawTop(el, savedScroll))
      return
    // The anchor row was trimmed away (restoreFromAnchor declined) and virtual rows are
    // present (restoreFromRawTop declined): recover to the NEAREST surviving row rather
    // than snapping to the live tail, which would yank a scrolled-up reader to the
    // bottom. Falls through only when the anchor can't be ordered (no seq / empty).
    if (restoreNearAnchor(el, savedScroll))
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
  // (clear the save), hidden->visible without a saved scroll but atBottom captured
  // pre-RO, or an already-visible viewport resize (editor/keyboard) while atBottom.
  // (Content-height growth is handled by the totalHeight effect, not here.) A
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
  // that wasn't at the bottom and had no saved non-bottom scroll): re-check atBottom
  // and re-seat geometry for the resized viewport. A prepend (older-history fetch /
  // hidden-page advancer) that landed while HIDDEN bumped totalHeight but
  // repinToAnchor bailed then (clientHeight 0), so re-resolve the anchor against the
  // grown offset map on show (no-op while following the tail -- the stick branch
  // owns that -- and when no meaningful move is needed, per repinToAnchor's delta
  // guard). refreshViewport recomputes the rendered slice (a taller pane reveals
  // rows below the old slice; safe to mount synchronously -- we're in a rAF, outside
  // RO delivery). Finally re-seat BOTH scroll baselines to the post-resize
  // scrollTop: a viewport shrink clamps scrollTop down without a write here (unlike
  // the restore/stick branches, whose programmatic writes refresh the baselines via
  // their echo). A stale DIRECTION baseline would make the next user scroll infer
  // the wrong direction and page the wrong way; a stale VELOCITY baseline would make
  // it measure the whole clamp delta (e.g. 5000 -> 3000 on a keyboard open) as one
  // huge sample and FALSE-fling, deferring re-pin corrections into flingSettle where
  // they land as a late jump. syncToProgrammatic advances lastPos without scoring a
  // velocity, exactly as a programmatic write would.
  const recheckOnResize = (rc: ResizeContext) => {
    extras.checkAtBottom()
    if (rc.wasHidden)
      extras.repinToAnchor()
    ctx.refreshViewport()
    const el = ctx.getEl()
    if (el) {
      extras.setLastScrollTopForDir(el.scrollTop)
      ctx.syncVelocityToProgrammatic(el.scrollTop)
    }
  }

  // Shared tail for both restore entry points (the resize flush and restoreOnMount):
  // once a restore/stick has PLACED the viewport, (1) drop the near-top older-load
  // suppression if the content now FITS -- a non-scrollable viewport emits no scroll
  // events, so an armed suppression would otherwise wedge older-history pre-fetch off
  // forever (the flag is cleared elsewhere only by a deliberate scroll into the top
  // band, and with everything visible a prepend re-pins around a stable anchor rather
  // than yanking the reader); and (2) re-arm the geometry-derived memos, since a
  // programmatic placement / edge clamp fires no scroll event they could react to.
  // Reads the element fresh so a flush that detached it between placement and here is a
  // no-op.
  const settleGeometryAfterPlacement = () => {
    const el = ctx.getEl()
    if (el && maxScrollTopOf(el) <= 0)
      extras.setSuppressOlder(false)
    extras.onGeometrySettled()
  }

  const handleResize = () => {
    cancelAnimationFrame(resizeRafId)
    // Capture hidden→visible state and atBottom NOW (in the ResizeObserver
    // callback), before browser scroll-restoration events can fire and corrupt
    // the atBottom signal.
    const el = ctx.getEl()
    const ch = el?.clientHeight ?? 0
    const wasHidden = prevClientHeight === 0 && ch > 0
    const rc: ResizeContext = {
      wasHidden,
      savedAtBottom: ctx.atBottom(),
      savedScroll: wasHidden ? extras.savedViewportScroll() : undefined,
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
      // Three mutually-exclusive outcomes in priority order. NOTE: the post-restore
      // older-load suppression (suppressAutoLoadOlderAfterRestore) is deliberately NOT
      // reset on a SCROLLABLE resize here. It is a one-shot meant to survive incidental
      // resizes (editor grow, keyboard) and passive scroll events, lasting until the
      // user takes DELIBERATE control by scrolling to the very top
      // (tryLoadOlderOnExplicitTopIntent clears it there, with its own loadOlder).
      // Clearing it on every non-restore resize wiped a near-top restore's arming
      // before the user had scrolled, re-opening the auto-older-load it was set to
      // prevent.
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
    // savedAtBottom is FALSE, not ctx.atBottom(): the resize path captures the live
    // pre-RO atBottom because a resize with no saved scroll must still re-stick an
    // at-bottom pane, but a mount always HAS a saved scroll (early return above), so
    // the outcome must derive solely from savedScroll.atBottom. Feeding the fresh
    // signal (which seeds true at mount) would make tryStickOnShow's `savedAtBottom ||
    // ...` first clause always true -- harmless only because tryRestoreHidden already
    // claims every non-bottom save, but a latent trap if that routing ever changes.
    const rc: ResizeContext = { wasHidden: true, savedScroll, savedAtBottom: false }
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
