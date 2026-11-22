import { createRafCoalescer } from '~/lib/rafCoalesce'

/** Visible viewport edges in layout-viewport coordinates. */
export interface ViewportRect {
  top: number
  left: number
  bottom: number
  right: number
}

/** Notification payload delivered to visual-viewport subscribers after phase-1 inset updates. */
export interface VisualViewportChange {
  /** True when viewport size or `--vvh`/`--vv-shift` changed (not a pure scroll pan). */
  resized: boolean
}

type InsetUpdater = () => boolean
type ViewportSubscriber = (change: VisualViewportChange) => void

/**
 * Resolve the visible viewport edges in layout-viewport coordinates.
 *
 * On iOS Safari with the on-screen keyboard up, `window.innerHeight` shrinks to
 * `visualViewport.height` while `visualViewport.offsetTop` shifts the visible
 * band down in layout coordinates. On iOS 26 after keyboard dismissal
 * (FB19889436), `visualViewport.offsetTop` can stay positive while
 * `useVisualViewportInset` publishes a negative `--vv-shift` on
 * `document.documentElement` so `body` shifts upward. Top-layer
 * `position: fixed` overlays (`DropdownMenu`, `Tooltip`) and counter-translated
 * fixed overlays (`SelectionQuotePopover`) all share layout coordinates with
 * `getBoundingClientRect()`, so honoring `--vv-shift` when set (and otherwise
 * `visualViewport.offsetTop`) keeps overlays aligned with anchors and inside
 * the visible band.
 */
export function readViewportRect(): ViewportRect {
  if (typeof window === 'undefined') {
    return { top: 0, left: 0, bottom: 0, right: 0 }
  }
  const vv = window.visualViewport
  if (!vv || !Number.isFinite(vv.width) || vv.width <= 0 || !Number.isFinite(vv.height) || vv.height <= 0) {
    return {
      top: 0,
      left: 0,
      bottom: window.innerHeight,
      right: window.innerWidth,
    }
  }
  const left = Number.isFinite(vv.offsetLeft)
    ? vv.offsetLeft
    : 0
  const rawShift = typeof document !== 'undefined'
    ? document.documentElement.style.getPropertyValue('--vv-shift').trim()
    : ''
  const parsedShift = rawShift
    ? Number.parseFloat(rawShift)
    : Number.NaN
  const top = Number.isFinite(parsedShift)
    ? parsedShift
    : Number.isFinite(vv.offsetTop)
      ? vv.offsetTop
      : 0
  return {
    top,
    left,
    bottom: top + vv.height,
    right: left + vv.width,
  }
}

// --- Phase-ordered visual-viewport hub ------------------------------------
//
// Coordinates `useVisualViewportInset` (phase 1: writes `--vvh` / `--vv-shift`
// onto `document.documentElement`) and floating overlay subscribers (phase 2:
// reads updated rects and repositions `DropdownMenu`, `Tooltip`, and
// `SelectionQuotePopover`) inside a single animation frame. Native listeners
// attach lazily when at least one updater or subscriber is active and detach as
// soon as none remain.

let insetUpdater: InsetUpdater | null = null
const subscribers = new Set<ViewportSubscriber>()

let windowAttached = false
let boundVisualViewport: VisualViewport | null = null

let pendingResize = false
let pendingScroll = false
let pendingInset = false

const hubCoalescer = createRafCoalescer<void>(() => {
  const hadResize = pendingResize
  const hadScroll = pendingScroll
  const hadInset = pendingInset
  pendingResize = false
  pendingScroll = false
  pendingInset = false

  // Phase 1: let `useVisualViewportInset` update `--vvh` and `--vv-shift` first.
  const insetChanged = insetUpdater
    ? insetUpdater()
    : false

  // Phase 2: notify floating overlays in the same frame after `body`'s transform
  // and height variables are settled.
  if (!hadResize && !hadScroll && !insetChanged)
    return
  // Skip notifying if an inset-only trigger (`focusin`/`focusout`/`pageshow`)
  // produced no `--vvh`/`--vv-shift` change and no viewport resize/scroll fired.
  if (!hadResize && !hadScroll && hadInset && !insetChanged)
    return

  const change: VisualViewportChange = { resized: hadResize || insetChanged }
  for (const listener of [
    ...subscribers,
  ]) {
    if (subscribers.has(listener))
      listener(change)
  }
})

function onWindowResize(): void {
  scheduleVisualViewportUpdate('resize')
}

function onVisualViewportResize(): void {
  scheduleVisualViewportUpdate('resize')
}

function onVisualViewportScroll(): void {
  scheduleVisualViewportUpdate('scroll')
}

function detachNativeListeners(): void {
  if (windowAttached && typeof window !== 'undefined') {
    window.removeEventListener('resize', onWindowResize)
    windowAttached = false
  }
  if (boundVisualViewport) {
    boundVisualViewport.removeEventListener('resize', onVisualViewportResize)
    boundVisualViewport.removeEventListener('scroll', onVisualViewportScroll)
    boundVisualViewport = null
  }
}

function syncNativeListeners(): void {
  if (typeof window === 'undefined')
    return
  const active = insetUpdater !== null || subscribers.size > 0
  if (!active) {
    detachNativeListeners()
    pendingResize = false
    pendingScroll = false
    pendingInset = false
    hubCoalescer.abort()
    return
  }

  if (!windowAttached) {
    // Belt-and-braces: listen to window.resize too. On some iOS Chrome builds
    // and embedded WebViews `visualViewport.resize` is silent when the virtual
    // keyboard appears, while `window.resize` still fires.
    window.addEventListener('resize', onWindowResize)
    windowAttached = true
  }

  const currentVv = window.visualViewport ?? null
  if (boundVisualViewport !== currentVv) {
    if (boundVisualViewport) {
      boundVisualViewport.removeEventListener('resize', onVisualViewportResize)
      boundVisualViewport.removeEventListener('scroll', onVisualViewportScroll)
    }
    boundVisualViewport = currentVv
    if (boundVisualViewport) {
      boundVisualViewport.addEventListener('resize', onVisualViewportResize)
      boundVisualViewport.addEventListener('scroll', onVisualViewportScroll)
    }
  }
}

/**
 * Queue a coalesced visual-viewport frame.
 *
 * `useVisualViewportInset` calls this with `'inset'` for non-viewport triggers
 * (`focusin`, `focusout`, `pageshow`) so they run phase 1 and notify phase-2
 * overlays in the same animation frame when `--vvh` or `--vv-shift` changes.
 */
export function scheduleVisualViewportUpdate(reason: 'resize' | 'scroll' | 'inset'): void {
  if (insetUpdater === null && subscribers.size === 0)
    return
  if (reason === 'resize')
    pendingResize = true
  else if (reason === 'scroll')
    pendingScroll = true
  else
    pendingInset = true
  hubCoalescer.push()
}

/**
 * Register the phase-1 inset updater (`useVisualViewportInset`). Returns a
 * cleanup function that unregisters it and detaches idle native listeners.
 */
export function registerVisualViewportInsetUpdater(updater: InsetUpdater): () => void {
  insetUpdater = updater
  syncNativeListeners()
  return () => {
    if (insetUpdater === updater)
      insetUpdater = null
    syncNativeListeners()
  }
}

/**
 * Subscribe a floating overlay to phase-2 visual-viewport updates (guaranteed to
 * run after `useVisualViewportInset` writes `--vvh` and `--vv-shift` in the
 * same animation frame). Returns an unsubscribe cleanup function.
 */
export function subscribeVisualViewport(listener: ViewportSubscriber): () => void {
  subscribers.add(listener)
  syncNativeListeners()
  return () => {
    subscribers.delete(listener)
    syncNativeListeners()
  }
}
