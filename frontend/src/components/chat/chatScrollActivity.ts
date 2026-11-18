import type { Accessor } from 'solid-js'
import { createSignal, onCleanup } from 'solid-js'
import { monotonicNow } from '~/lib/monotonicNow'

// ---------------------------------------------------------------------------
// Scroll-activity window
//
// Activity opens on input and closes after `idleMs` of quiet. ChatView uses a short window
// to pause highlighting during any scroll and a longer window to show the scroll rail.
// The rail opens on user input. Scroll movement can extend that window while it is open.
// The scroll hook filters its own writes before it reports that movement.
// ---------------------------------------------------------------------------

export interface ScrollActivityDeps {
  /** Quiet period (ms) after the last qualifying event before the window closes. */
  idleMs: number
  /** Monotonic clock, injected for tests. Defaults to {@link monotonicNow}. */
  now?: () => number
}

export interface ScrollActivity {
  /** True while the window is open. Reactive; safe to read in a memo or in JSX. */
  active: Accessor<boolean>
  /**
   * A genuine user scroll input: wheel, touch, pointer, a scroll-relevant keydown, or a
   * direct interaction with a scrollbar overlay. Opens the window and restarts its idle timer.
   */
  noteInput: () => void
  /**
   * Scroll movement reported by the scroll hook. Extends the window while it is open.
   */
  noteScroll: () => void
  /**
   * Cancel the pending timer and close the window for good. Registered on `onCleanup`;
   * ONE-WAY, so both note functions are inert afterwards and a late passive `touchend`
   * cannot arm a timer on a disposed owner. There is no reopen -- build a new instance.
   */
  dispose: () => void
}

/**
 * Create a scroll-activity window (see the module header).
 *
 * Must be created inside an owner scope: it registers its own `onCleanup`, so a late
 * passive `touchend` arriving after the owner disposes cannot arm a timer on a dead
 * component.
 */
export function createScrollActivity(deps: ScrollActivityDeps): ScrollActivity {
  const now = deps.now ?? monotonicNow
  const [active, setActive] = createSignal(false)
  let activeUntil = Number.NEGATIVE_INFINITY
  let idleTimer: ReturnType<typeof setTimeout> | undefined
  let disposed = false

  const close = () => {
    idleTimer = undefined
    activeUntil = Number.NEGATIVE_INFINITY
    setActive(false)
  }

  const open = (at: number) => {
    activeUntil = at + deps.idleMs
    if (idleTimer !== undefined)
      clearTimeout(idleTimer)
    idleTimer = setTimeout(close, deps.idleMs)
    setActive(true)
  }

  const noteInput = () => {
    if (disposed)
      return
    open(now())
  }

  const noteScroll = () => {
    if (disposed)
      return
    // A delayed timer can leave the rail visible past the deadline. Only input can open
    // a new window after that point.
    const at = now()
    if (at < activeUntil)
      open(at)
  }

  const dispose = () => {
    disposed = true
    if (idleTimer !== undefined)
      clearTimeout(idleTimer)
    close()
  }

  onCleanup(dispose)

  return {
    active,
    noteInput,
    noteScroll,
    dispose,
  }
}
