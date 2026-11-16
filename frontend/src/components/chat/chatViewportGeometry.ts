/**
 * Pure viewport / layout geometry for the chat message list: design-token probing,
 * the size observer, and the overscan-band policy. Extracted from ChatView so the
 * math is coupling-free (no component, no reactive read) and testable at its
 * boundaries, mirroring the other chat leaves (chatHeightShared, chatRowGeometry).
 */

/**
 * Resolve a `--space-N` design token to pixels by measuring a hidden probe.
 * Used to feed the virtualizer the exact inter-row gaps the non-virtual layout
 * uses, so the offset map reproduces its vertical rhythm.
 */
export function measureSpaceToken(varName: string, fallback: number): number {
  if (typeof document === 'undefined')
    return fallback
  const probe = document.createElement('div')
  probe.style.cssText = `position:absolute;visibility:hidden;height:var(${varName})`
  document.body.appendChild(probe)
  const h = probe.getBoundingClientRect().height
  probe.remove()
  return h > 0
    ? h
    : fallback
}

/**
 * A ResizeObserver wrapper for the message-list scroll container that reports its
 * content-box WIDTH (exact, as the browser lays it out -- the width of each row) and
 * HEIGHT (rounded -- the overscan viewport) to two callbacks. Owns the change-detection
 * (per-axis last-value dedup, so a measure that left an axis unchanged fires nothing) and
 * defers each write to a microtask (defense-in-depth against a write-during-observe
 * layout thrash). `observe` is a no-op when ResizeObserver is unavailable
 * (jsdom / older Safari) so callers don't branch. Carries values out via callbacks
 * rather than reading any signal, so the dedup-and-defer dance is testable with a
 * stub observer independent of ChatView.
 */
export function createViewportSizeObserver(opts: {
  onWidth: (w: number) => void
  onHeight: (h: number) => void
}): { observe: (el: HTMLElement) => void, disconnect: () => void } {
  if (typeof ResizeObserver === 'undefined') {
    return {
      observe: () => {},
      disconnect: () => {},
    }
  }
  let lastWidth = -1
  let lastHeight = -1
  // A measure can fire its callback a microtask AFTER it was observed; if the consumer
  // tore us down (onCleanup -> disconnect) in between, that deferred write would land on
  // a disposed reactive owner. ro.disconnect() stops FUTURE observations but not an
  // already-queued microtask, so gate the deferred writes on this flag too.
  let disconnected = false
  const ro = new ResizeObserver((entries) => {
    // contentRect avoids a forced layout read; `?? 0` tolerates a stub entry.
    const rect = entries[entries.length - 1]?.contentRect
    const w = rect?.width ?? 0
    if (w !== lastWidth) {
      lastWidth = w
      queueMicrotask(() => {
        if (!disconnected)
          opts.onWidth(w)
      })
    }
    const h = Math.round(rect?.height ?? 0)
    if (h !== lastHeight) {
      lastHeight = h
      queueMicrotask(() => {
        if (!disconnected)
          opts.onHeight(h)
      })
    }
  })
  return {
    observe: el => ro.observe(el),
    disconnect: () => {
      disconnected = true
      ro.disconnect()
    },
  }
}

/**
 * Viewport-relative overscan band: mount ~1.5 screenfuls of admitted rows on EACH side
 * of the message-list viewport, so a scroll shows rendered rows, and each mounted row
 * takes its first DOM read BEFORE it scrolls into view. That read can correct the
 * height that a hidden copy measured for the row (the row stood behind a fling
 * skeleton, or its copy waited for late content), and the layout shift then holds the
 * view (see UseChatVirtualizerResult.showsHeight). Keyed off the LIST viewport height, NOT
 * window.innerHeight: ChatView renders inside tiles/splits, where the pane is
 * often a fraction of the window and a window-relative band would over-mount
 * badly. Floored for short panes and capped so a maximized pane can't mount an
 * unbounded band.
 */
const OVERSCAN_FLOOR_PX = 800
const OVERSCAN_VIEWPORT_RATIO = 1.5
const OVERSCAN_CEIL_PX = 2400

/**
 * Viewport-relative overscan in px: a ratio of the live viewport height, floored
 * for short panes and capped for tall ones. `viewportHeight <= 0` (the
 * pre-measurement frame) falls back to the floor. Pure, so the policy can be
 * unit-tested at its boundaries independent of the reactive read.
 */
export function computeOverscanPx(viewportHeight: number): number {
  if (viewportHeight <= 0)
    return OVERSCAN_FLOOR_PX
  return Math.min(OVERSCAN_CEIL_PX, Math.max(OVERSCAN_FLOOR_PX, viewportHeight * OVERSCAN_VIEWPORT_RATIO))
}
