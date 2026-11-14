import type { ScrollContext } from './useChatScroll'
import { EDGE_INTENT_TOLERANCE_PX, hasScrollRoom } from './chatScrollGeometry'

/**
 * Vertical overlap kept when paging (PageUp/PageDown): a page jump advances by
 * the viewport height minus this many pixels (~a couple of lines) so the reader
 * keeps a sliver of context across the jump.
 */
const PAGE_SCROLL_OVERLAP_PX = 48

/**
 * The keyboard / wheel input layer: Home/End/PageUp/PageDown/Arrow keys and wheel
 * direction, plus the discrete-page scroll those drive. Extracted from the scroll hook
 * so the input handling has a named home and a test seam. It owns no scroll state of its
 * own -- `lastScrollDir` lives in the hook because the scroll handler reads it, threaded
 * in as a setter -- so this is a thin policy layer over the hook's scroll primitives.
 * `forceScrollToBottom` stays in the hook (the viewport restore and the public API also
 * call it) and is passed in.
 */
export function createScrollInput(ctx: ScrollContext, extras: {
  // High-level scroll actions declared later in the hook than the shared context.
  checkAtBottom: () => void
  forceScrollToBottom: () => void
  cancelScrollAnimation: () => void
  cancelPendingScroll: () => void
  /** Abandon an in-flight out-of-window seek: the reader took control. */
  cancelPendingSeek: () => void
  /**
   * Resume the buffer fill that a press paused. A page writes through the controller, so
   * its `scroll` event is an own write, and only a move of the reader ends that pause.
   */
  retakeScrollControl: () => void
  tryLoadOlderOnExplicitTopIntent: () => void
  tryLoadNewerOnExplicitBottomIntent: () => void
  setLastScrollDir: (dir: 'older' | 'newer') => void
  hasOlderMessages: () => boolean
  onJumpToOldest: (() => void | Promise<void>) | undefined
}) {
  /** Place the view at the very top of the loaded rows. */
  const scrollToTopPosition = () => {
    ctx.placeAt(() => 0)
  }

  /**
   * Jump to the very first message in history (Home). When older history exists
   * beyond the window, re-fetch the earliest page first (jumpToOldest replaces
   * the window), then place at the top; otherwise just place at the top of the
   * already-loaded window.
   */
  const jumpToTop = () => {
    extras.cancelScrollAnimation()
    if (extras.hasOlderMessages()) {
      void Promise.resolve(extras.onJumpToOldest?.())
        .then(() => scrollToTopPosition())
        .catch(() => extras.checkAtBottom())
      return
    }
    scrollToTopPosition()
  }

  const pageScroll = (direction: -1 | 1) => {
    // A 0-height container (hidden/collapsed pane, or the cold-start frame before
    // measurement) can't scroll: clientHeight is 0, so the delta below is 0, and a
    // no-move would then fire a spurious edge pagination on every key press.
    const el = ctx.getEl()
    if (!el || el.clientHeight === 0)
      return
    // A page is a placement: it moves the view relative to the current content, so the
    // layout shift goes first, and the scroll coordinate of the page is a content one.
    ctx.rebase()
    // Advance by a viewport height minus a few lines of overlap, but never less
    // than half a page (guards against a degenerately short viewport).
    const delta = Math.max(el.clientHeight - PAGE_SCROLL_OVERLAP_PX, el.clientHeight / 2)
    const before = el.scrollTop
    el.scrollBy({ top: direction * delta, behavior: 'auto' })
    // Compare with the SAME sub-pixel slack the edge-intent tests use, not exact
    // equality: at a fractional-DPI / browser-zoom edge the clamp can leave scrollTop a
    // fraction off `before` even when visually unmoved. A genuine page moves by `delta`
    // (>= half a viewport), never within this slack.
    if (Math.abs(el.scrollTop - before) > EDGE_INTENT_TOLERANCE_PX) {
      extras.retakeScrollControl()
      if (direction === -1)
        ctx.leaveFollow()
      ctx.syncAfterWrite()
      return
    }
    // Already clamped at the edge: the page could not move. Route its direction
    // through the explicit-edge-intent loader rather than the buffer filler. The loader
    // is NOT gated by the buffer-fill pause, so a PageDown at the loaded bottom (or
    // PageUp at the top) still pages newer/older right after a programmatic stop. It
    // loads ONE directional page; the buffer filler's own messages() effect tops up the
    // render-ahead once that page lands.
    if (direction === 1)
      extras.tryLoadNewerOnExplicitBottomIntent()
    else
      extras.tryLoadOlderOnExplicitTopIntent()
  }

  const handleWheel = (event: WheelEvent) => {
    if (event.ctrlKey)
      return
    const el = ctx.getEl()
    // Only treat a wheel event as a VERTICAL scroll intent when the vertical
    // component dominates. A horizontal/diagonal trackpad swipe leaks a small
    // vertical deltaY; without this guard that leak fires a spurious edge-load and
    // mis-sets lastScrollDir (which then steers the buffer filler the wrong way).
    if (Math.abs(event.deltaY) > Math.abs(event.deltaX)) {
      extras.cancelPendingSeek()
      if (event.deltaY < 0) {
        extras.setLastScrollDir('older')
        // An upward step that can move the view ends follow mode at once, before its
        // `scroll` event, so no growth in between pulls the reader back down.
        if (el && hasScrollRoom(el, -1))
          ctx.leaveFollow()
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
      // inertial scroll. Halt the programmatic scroll and the buffer fill now.
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
        extras.cancelPendingSeek()
        extras.setLastScrollDir('older')
        jumpToTop()
        break
      case 'End':
        event.preventDefault()
        extras.cancelPendingSeek()
        extras.setLastScrollDir('newer')
        extras.forceScrollToBottom()
        break
      case 'PageUp':
        event.preventDefault()
        extras.cancelPendingSeek()
        extras.setLastScrollDir('older')
        pageScroll(-1)
        break
      case 'PageDown':
        event.preventDefault()
        extras.cancelPendingSeek()
        extras.setLastScrollDir('newer')
        pageScroll(1)
        break
      // Arrow keys and Space keep the browser's native scroll; we only nudge
      // pagination when the user is explicitly pressed against an edge.
      case 'ArrowUp': {
        extras.cancelPendingSeek()
        extras.setLastScrollDir('older')
        const el = ctx.getEl()
        if (el && hasScrollRoom(el, -1))
          ctx.leaveFollow()
        extras.tryLoadOlderOnExplicitTopIntent()
        break
      }
      case 'ArrowDown':
        extras.cancelPendingSeek()
        extras.setLastScrollDir('newer')
        extras.tryLoadNewerOnExplicitBottomIntent()
        break
      case ' ':
        extras.cancelPendingSeek()
        break
    }
  }

  return { handleKeyDown, handleWheel, pageScroll }
}
