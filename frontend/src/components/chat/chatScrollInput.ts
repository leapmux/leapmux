import type { ScrollContext } from './useChatScroll'
import { EDGE_INTENT_TOLERANCE_PX } from './chatScrollGeometry'

/**
 * Vertical overlap kept when paging (PageUp/PageDown): a page jump advances by
 * the viewport height minus this many pixels (~a couple of lines) so the reader
 * keeps a sliver of context across the jump.
 */
const PAGE_SCROLL_OVERLAP_PX = 48

/**
 * The keyboard / wheel input layer: Home/End/PageUp/PageDown/Arrow keys and wheel
 * direction, plus the discrete-page scroll those drive. Extracted from the scroll hook
 * so the input handling has a named home and a test seam (the hook body had no boundary
 * around edge-pagination). It owns no scroll state of its own -- `lastScrollDir` and
 * `discretePageTarget` live in the hook because handleScroll reads them, threaded in as
 * setters -- so this is a thin policy layer over the hook's scroll primitives, mirroring
 * the other extracted units (createViewportRestore / createStickyBottom). `forceScroll-
 * ToBottom` stays in the hook (createViewportRestore and the public API also call it) and
 * is passed in.
 */
export function createScrollInput(ctx: ScrollContext, extras: {
  // High-level scroll actions declared later in the hook than the shared context.
  captureAnchor: () => void
  checkAtBottom: () => void
  forceScrollToBottom: () => void
  cancelScrollAnimation: () => void
  cancelPendingScroll: () => void
  tryLoadOlderOnExplicitTopIntent: () => void
  tryLoadNewerOnExplicitBottomIntent: () => void
  setLastScrollDir: (dir: 'older' | 'newer') => void
  setDiscretePageTarget: (target: number | null) => void
  hasOlderMessages: () => boolean
  onJumpToOldest: (() => void | Promise<void>) | undefined
}) {
  /** Pin scrollTop to the very top WITHOUT recomputing the slice (RO-safe). */
  const scrollToTopPosition = () => {
    if (!ctx.getEl())
      return
    ctx.writeScrollTop(0)
    // Anchor to the first row so a later geometry change keeps us pinned at top.
    extras.captureAnchor()
    ctx.setAtBottom(false)
  }

  /**
   * Jump to the very first message in history (Home). When older history exists
   * beyond the window, re-fetch the earliest page first (jumpToOldest replaces
   * the window), then pin to the top; otherwise just scroll to the top of the
   * already-loaded window.
   */
  const jumpToTop = () => {
    extras.cancelScrollAnimation()
    if (extras.hasOlderMessages()) {
      void Promise.resolve(extras.onJumpToOldest?.())
        .then(() => {
          scrollToTopPosition()
          ctx.refreshViewport()
        })
        .catch(() => extras.checkAtBottom())
      return
    }
    scrollToTopPosition()
  }

  const pageScroll = (direction: -1 | 1) => {
    // A 0-height container (hidden/collapsed pane, or the cold-start frame before
    // measurement) can't scroll: clientHeight is 0, so the delta below is 0,
    // scrollBy is a no-op, and scrollTop === before would then fire a spurious
    // edge pagination on every key press. Bail like every other scroll method
    // (stickToBottomPosition / repinToAnchor / getScrollState all guard this).
    const el = ctx.getEl()
    if (!el || el.clientHeight === 0)
      return
    // Advance by a viewport height minus a few lines of overlap, but never less
    // than half a page (guards against a degenerately short viewport).
    const delta = Math.max(el.clientHeight - PAGE_SCROLL_OVERLAP_PX, el.clientHeight / 2)
    // scrollBy moves the position; the browser emits a single native `scroll`
    // event that runs handleScroll (anchor capture, slice refresh, edge
    // pagination). We do NOT synthesize an extra `scroll` event -- alongside
    // scrollBy's native one that double-processed each page jump (the synthetic
    // pass ran handleScroll a second time, re-dispatching pagination that the
    // in-flight-fetch guards then no-op'd).
    const before = el.scrollTop
    // Record this page's DESIRED (un-clamped) target so handleScroll recognizes the
    // resulting native scroll event as this page (re-pin immediately, not deferred
    // as fling drift) by position. Clamped to the scroll range at COMPARE time, not
    // here: a ResizeObserver flush can change scrollHeight between this scrollBy and
    // its native scroll event, re-clamping the browser's scrollTop -- a target
    // clamped against the now-stale scrollHeight would miss the <=1px match and the
    // page would be mishandled as a fling (deferred ~150ms). Re-clamping against the
    // CURRENT range in handleScroll tracks the browser's own clamp.
    extras.setDiscretePageTarget(before + direction * delta)
    el.scrollBy({ top: direction * delta, behavior: 'auto' })
    // Already clamped at the edge: scrollBy can't move (scrollTop is unchanged),
    // so the browser emits NO scroll event and the buffer fill would never run.
    // Top up the buffer directly instead, so PageUp at the top / PageDown at the
    // bottom still loads older / newer when more exists beyond the window.
    // handleKeyDown set lastScrollDir, so the fill pages the right way. When
    // scrollBy DID move, the native scroll event owns the fill -- no double-fill.
    //
    // Compare with the SAME sub-pixel slack the edge-intent tests use, not exact
    // equality: at a fractional-DPI / browser-zoom edge the clamp can leave
    // scrollTop a fraction off `before` even when visually unmoved, so `=== before`
    // would miss the at-edge case and the page-into-unloaded-history fill would
    // never run. A genuine page moves by `delta` (>= half a viewport), never within
    // this slack, so a real move is never misread as no-move; and even if it were,
    // the in-flight-fetch guards make the resulting direct + native fill idempotent.
    if (Math.abs(el.scrollTop - before) <= EDGE_INTENT_TOLERANCE_PX) {
      // No (meaningful) scroll event will fire, so the target would leak onto the
      // next genuine scroll; clear it here.
      extras.setDiscretePageTarget(null)
      // Route the page's direction through the explicit-edge-intent loader rather than
      // the buffer filler. The loader is NOT gated by the buffer-fill pause, so a
      // PageDown at the loaded bottom (or PageUp at the top) still pages newer/older
      // right after a programmatic stop -- exactly where fillScrollBuffer would decline
      // (bufferFillPaused). It loads ONE directional page; when not paused, the buffer
      // filler's own messages() effect tops up the render-ahead once that page lands,
      // so a single load here can't double-fetch the same direction.
      if (direction === 1)
        extras.tryLoadNewerOnExplicitBottomIntent()
      else
        extras.tryLoadOlderOnExplicitTopIntent()
    }
  }

  const handleWheel = (event: WheelEvent) => {
    if (event.ctrlKey)
      return
    // Only treat a wheel event as a VERTICAL scroll intent when the vertical
    // component dominates. A horizontal/diagonal trackpad swipe leaks a small
    // vertical deltaY; without this guard that leak fires a spurious edge-load and
    // mis-sets lastScrollDir (which then steers the buffer filler the wrong way).
    if (Math.abs(event.deltaY) > Math.abs(event.deltaX)) {
      if (event.deltaY < 0) {
        extras.setLastScrollDir('older')
        extras.tryLoadOlderOnExplicitTopIntent()
      }
      else {
        extras.setLastScrollDir('newer')
        extras.tryLoadNewerOnExplicitBottomIntent()
      }
    }
    else if (event.deltaX === 0 && event.deltaY === 0) {
      // A no-movement wheel event (deltaX and deltaY both 0): the trackpad
      // momentum-cancel the browser fires when the user rests fingers to stop an
      // inertial scroll. Halt the deferred settle so the view stops immediately.
      extras.cancelPendingScroll()
    }
  }

  const handleKeyDown = (event: KeyboardEvent) => {
    if (event.altKey || event.ctrlKey || event.metaKey)
      return
    switch (event.key) {
      // Home/End jump to the very first / live-last message (fetching across the
      // window edge when needed); PageUp/PageDown page with a few lines of
      // overlap. We own the scroll for these, so preventDefault stops the
      // browser's native key-scroll from double-acting. Each also records the
      // scroll direction so pagination follows the user's intent even when the
      // viewport can't actually scroll (a hidden-only window page).
      case 'Home':
        event.preventDefault()
        extras.setLastScrollDir('older')
        jumpToTop()
        break
      case 'End':
        event.preventDefault()
        extras.setLastScrollDir('newer')
        extras.forceScrollToBottom()
        break
      case 'PageUp':
        event.preventDefault()
        extras.setLastScrollDir('older')
        pageScroll(-1)
        break
      case 'PageDown':
        event.preventDefault()
        extras.setLastScrollDir('newer')
        pageScroll(1)
        break
      // Arrow keys keep the browser's native line scroll; we only nudge
      // pagination when the user is explicitly pressed against an edge.
      case 'ArrowUp':
        extras.setLastScrollDir('older')
        extras.tryLoadOlderOnExplicitTopIntent()
        break
      case 'ArrowDown':
        extras.setLastScrollDir('newer')
        extras.tryLoadNewerOnExplicitBottomIntent()
        break
    }
  }

  return { handleKeyDown, handleWheel, pageScroll }
}
