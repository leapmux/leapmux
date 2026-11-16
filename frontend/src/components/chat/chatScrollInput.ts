import type { ScrollContext } from './useChatScroll'
import { batch } from 'solid-js'
import { isMac } from '~/lib/shortcuts/platform'
import { distFromBottom, EDGE_INTENT_TOLERANCE_PX, FOLLOW_TOLERANCE_PX, hasScrollRoom } from './chatScrollGeometry'

/**
 * Vertical overlap kept when paging (PageUp/PageDown): a page jump advances by
 * the viewport height minus this many pixels (~a couple of lines) so the reader
 * keeps a sliver of context across the jump.
 */
const PAGE_SCROLL_OVERLAP_PX = 48

/**
 * True when an element between `target` and `list` (the list itself excluded) scrolls
 * vertically and can still move in `direction` (-1 up, 1 down). The browser gives a wheel
 * or a key of that direction to that element, so the list does not move.
 */
function innerScrollerTakes(target: EventTarget | null, list: HTMLElement, direction: -1 | 1): boolean {
  if (!(target instanceof Element) || !list.contains(target))
    return false
  for (let node: Element | null = target; node !== null && node !== list; node = node.parentElement) {
    // The room first: an element without room in the direction is the common case, and
    // the room costs less than the computed style.
    if (node instanceof HTMLElement && hasScrollRoom(node, direction) && scrollsVertically(node))
      return true
  }
  return false
}

/** True when `el` has a vertical scrollbar of its own: a vertical overflow that it scrolls. */
function scrollsVertically(el: HTMLElement): boolean {
  const overflowY = getComputedStyle(el).overflowY
  return (overflowY === 'auto' || overflowY === 'scroll') && el.scrollHeight > el.clientHeight
}

/** True when `target` takes text input, so its keys edit the text and do not scroll the list. */
function isEditableTarget(target: EventTarget | null): boolean {
  return target instanceof HTMLElement
    && (target.isContentEditable
      || target instanceof HTMLInputElement
      || target instanceof HTMLTextAreaElement
      || target instanceof HTMLSelectElement)
}

const SCROLL_KEYS: ReadonlySet<string> = new Set([
  'ArrowUp',
  'ArrowDown',
  'PageUp',
  'PageDown',
  'Home',
  'End',
  ' ',
])

/**
 * True when `event` is a key that scrolls the chat list: a scroll key with no modifier or
 * with Shift, or on macOS Option or Cmd with ArrowUp or ArrowDown, which scroll natively
 * there. Any other key with Ctrl, Option, or Cmd is a shortcut of the OS or the app, and a
 * key in a text field edits the text. ChatView opens its activity windows for the same keys.
 */
export function isListScrollKey(event: KeyboardEvent): boolean {
  if (event.ctrlKey || !SCROLL_KEYS.has(event.key) || isEditableTarget(event.target))
    return false
  if (event.altKey || event.metaKey)
    return isMac() && (event.key === 'ArrowUp' || event.key === 'ArrowDown')
  return true
}

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
  // The hook actions that only this layer calls.
  forceScrollToBottom: () => void
  cancelPendingScroll: () => void
  /** Abandon the pending history jump: the reader took control. */
  cancelPendingSeek: () => void
  tryLoadOlderOnExplicitTopIntent: () => void
  tryLoadNewerOnExplicitBottomIntent: () => void
  setLastScrollDir: (dir: 'older' | 'newer') => void
  hasOlderMessages: () => boolean
  /**
   * Replace the window with the oldest page. Resolves `true` only when that page replaced
   * the window (see PaginationCallbacks.onJumpToLatest).
   */
  onJumpToOldest: ((signal?: AbortSignal) => Promise<boolean> | undefined) | undefined
}) {
  /**
   * Ask for the very top of the loaded rows. The first rows can be staged, so the view
   * moves when they are measured (see ScrollContext.requestPlace).
   */
  const scrollToTopPosition = () => {
    void ctx.requestPlace({ kind: 'top' })
  }

  /**
   * Jump to the very first message in history (Home). When older history exists
   * beyond the window, re-fetch the earliest page first (jumpToOldest replaces
   * the window), then place at the top; otherwise just place at the top of the
   * already-loaded window.
   *
   * The fetch is the pending history jump of the hook. Its landing runs only while its
   * signal is not aborted: a later command or an input of the reader stops it. It places
   * at the top only when its page replaced the window. When another history fetch of the
   * store replaced the jump, the window is not the oldest page, so the landing only
   * checks the position.
   *
   * The synchronous part runs in one batch, so the hook sees only the place that the
   * command leaves (see ScrollContext.requestPlace).
   */
  const jumpToTop = () => {
    ctx.cancelScrollAnimation()
    batch(() => {
      if (extras.hasOlderMessages()) {
        const signal = ctx.beginHistoryJump()
        void Promise.resolve(extras.onJumpToOldest?.(signal))
          .then((applied) => {
            if (signal.aborted)
              return
            if (applied === true)
              scrollToTopPosition()
            else
              ctx.checkPosition()
          })
          .catch(() => {
            if (!signal.aborted)
              ctx.checkPosition()
          })
        return
      }
      // This command starts no fetch, but it still ends one that runs.
      ctx.abandonHistoryJump()
      scrollToTopPosition()
    })
  }

  const pageScroll = (direction: -1 | 1) => {
    // A page is an input of the reader, like a wheel: it stops the pending history jump.
    // The page key of the list reaches here through takeIntent, which abandons the jump
    // already, but the application shortcut calls pageScroll directly. While loaded rows
    // exist and none is in the list, an input changes nothing: no row shows a position.
    const inputCounts = !ctx.awaitingFirstView()
    if (inputCounts)
      ctx.abandonHistoryJump()
    // A 0-height container (hidden/collapsed pane, or the cold-start frame before
    // measurement) can't scroll: clientHeight is 0, so the delta below is 0, and a
    // no-move would then fire a spurious edge pagination on every key press.
    const el = ctx.getVisibleEl()
    if (!el)
      return
    // A page is a placement: it moves the view relative to the current content, so the
    // layout shift goes first.
    ctx.rebase()
    // Advance by a viewport height minus a few lines of overlap, but never less
    // than half a page (guards against a degenerately short viewport).
    const delta = Math.max(el.clientHeight - PAGE_SCROLL_OVERLAP_PX, el.clientHeight / 2)
    const before = el.scrollTop
    // The writer records every page, also one that moves a pixel or less, so the `scroll`
    // event of the page reads as no move of the reader.
    ctx.writeOwnScrollTop(before + direction * delta, { refresh: true })
    // Compare with the SAME sub-pixel slack the edge-intent tests use, not exact
    // equality: at a fractional-DPI / browser-zoom edge the clamp can leave scrollTop a
    // fraction off `before` even when visually unmoved. A page away from an edge moves by
    // `delta` (>= half a viewport), never within this slack.
    if (Math.abs(el.scrollTop - before) > EDGE_INTENT_TOLERANCE_PX) {
      ctx.retakeScrollControl()
      // The rule of the scroll handler for a move of the reader: a page up ends the request
      // for the tail, and so does a page down while newer messages exist, when it leaves the
      // view more than FOLLOW_TOLERANCE_PX above the loaded bottom.
      if (inputCounts && (direction === -1 || (ctx.hasNewerMessages() && distFromBottom(el) > FOLLOW_TOLERANCE_PX)))
        ctx.leaveFollow()
      return
    }
    // Already clamped at the edge: the page could not move. Route its direction
    // through the explicit-edge-intent loader rather than the buffer filler. The
    // buffer-fill pause does not block the loader, so a PageDown at the loaded bottom (or
    // PageUp at the top) still pages newer/older right after a programmatic stop. It
    // loads ONE directional page; the buffer filler's own messages() effect tops up the
    // render-ahead once that page lands.
    if (direction === 1)
      extras.tryLoadNewerOnExplicitBottomIntent()
    else
      extras.tryLoadOlderOnExplicitTopIntent()
  }

  /**
   * An input that states a direction: it abandons the pending history jump, because the
   * reader takes control, and it records the direction for the pagination. The hook
   * decides in `cancelPendingSeek` whether the input counts: it changes nothing while
   * loaded rows exist and none is in the list.
   */
  const takeIntent = (dir: 'older' | 'newer') => {
    extras.cancelPendingSeek()
    extras.setLastScrollDir(dir)
  }

  /**
   * An upward step that the browser scrolls natively: a wheel, ArrowUp, Shift+Space, or
   * on macOS Option+ArrowUp and Cmd+ArrowUp. A scroller inside the list around `target`
   * that can move up takes the step, so the step is no input of the list and does
   * nothing here. Otherwise it states the direction. When the list can move, it ends
   * follow mode at once, before its `scroll` event, so no growth in between pulls the
   * reader back down. At the top edge, it loads older messages. While loaded rows exist
   * and none is in the list, it ends no follow mode: no row shows a position.
   */
  const stepOlder = (target: EventTarget | null) => {
    const el = ctx.getEl()
    if (el && innerScrollerTakes(target, el, -1))
      return
    takeIntent('older')
    if (el && hasScrollRoom(el, -1) && !ctx.awaitingFirstView())
      ctx.leaveFollow()
    extras.tryLoadOlderOnExplicitTopIntent()
  }

  /**
   * A downward step that the browser scrolls natively: a wheel, ArrowDown, Space, or on
   * macOS Option+ArrowDown and Cmd+ArrowDown. A scroller inside the list around `target`
   * that can move down takes the step, as for `stepOlder`. At the bottom edge, the step
   * loads newer messages.
   */
  const stepNewer = (target: EventTarget | null) => {
    const el = ctx.getEl()
    if (el && innerScrollerTakes(target, el, 1))
      return
    takeIntent('newer')
    extras.tryLoadNewerOnExplicitBottomIntent()
  }

  const handleWheel = (event: WheelEvent) => {
    if (event.ctrlKey)
      return
    // Only treat a wheel event as a VERTICAL scroll intent when the vertical
    // component dominates. A horizontal/diagonal trackpad swipe leaks a small
    // vertical deltaY; without this guard that leak fires a spurious edge-load and
    // mis-sets lastScrollDir (which then steers the buffer filler the wrong way).
    if (Math.abs(event.deltaY) > Math.abs(event.deltaX)) {
      if (event.deltaY < 0)
        stepOlder(event.target)
      else
        stepNewer(event.target)
    }
    else if (event.deltaX === 0 && event.deltaY === 0) {
      // A no-movement wheel event (deltaX and deltaY both 0): the trackpad
      // momentum-cancel the browser fires when the user rests fingers to stop an
      // inertial scroll. Halt the programmatic scroll and the buffer fill now.
      extras.cancelPendingScroll()
    }
  }

  const handleKeyDown = (event: KeyboardEvent) => {
    if (!isListScrollKey(event))
      return
    // On macOS, Option or Cmd with an arrow scrolls natively, as the arrow alone does.
    if (event.altKey || event.metaKey) {
      if (event.key === 'ArrowUp')
        stepOlder(event.target)
      else
        stepNewer(event.target)
      return
    }
    switch (event.key) {
      // Home/End jump to the very first / live-last message (fetching across the
      // window edge when needed); PageUp/PageDown page with a few lines of
      // overlap. We own the scroll for these, so preventDefault stops the
      // browser's native key-scroll from double-acting. Each also records the
      // scroll direction so pagination follows the user's intent even when the
      // viewport can't actually scroll (a hidden-only window page).
      case 'Home':
        event.preventDefault()
        takeIntent('older')
        jumpToTop()
        break
      case 'End':
        event.preventDefault()
        takeIntent('newer')
        extras.forceScrollToBottom()
        break
      case 'PageUp':
        event.preventDefault()
        takeIntent('older')
        pageScroll(-1)
        break
      case 'PageDown':
        event.preventDefault()
        takeIntent('newer')
        pageScroll(1)
        break
      // Arrow keys and Space keep the browser's native scroll; we only nudge
      // pagination when the user is explicitly pressed against an edge.
      case 'ArrowUp':
        stepOlder(event.target)
        break
      case 'ArrowDown':
        stepNewer(event.target)
        break
      // Space scrolls down a page and Shift+Space up a page, natively.
      case ' ':
        if (event.shiftKey)
          stepOlder(event.target)
        else
          stepNewer(event.target)
        break
    }
  }

  return { handleKeyDown, handleWheel, pageScroll }
}
