import type { Accessor } from 'solid-js'
import { createSignal, onCleanup } from 'solid-js'
import { monotonicNow } from '~/lib/monotonicNow'

// ---------------------------------------------------------------------------
// Scroll-activity window
//
// "Is the reader actively scrolling right now?", as one reactive boolean that opens on a
// qualifying event and closes after `idleMs` of quiet. ChatView instantiates it TWICE with
// different windows AND different event sets, which is why it is a unit rather than two
// near-identical setTimeout debounces inline:
//   - the syntax-highlight pause (short window) takes EVERY scroll, our own programmatic
//     writes included -- a streaming stick-to-bottom is exactly when the highlighter and the
//     premeasure warm-up must stay out of the way.
//   - the floating scroll rail (long window) takes USER INPUT only. A rail that lit up for
//     our own scrollTop writes would stay lit for a whole streaming response, which defeats
//     the auto-hide precisely when it matters most.
//
// A bare `scroll` event therefore has its own entry point, `noteScroll`, which opens the
// window ONLY within `momentumGraceMs` of the last `noteInput`. That is the momentum latch:
// after a touch flick no touch or pointer event fires while the content coasts -- only
// `scroll` -- so without it the rail would fade mid-fling. Measuring the grace from the last
// INPUT, and never letting `noteScroll` re-base it, is what stops a streaming turn's
// continuous programmatic writes from holding the window open forever.
//
// `now` is injected for determinism (mirroring createScrollVelocity); the idle timer is a
// bare setTimeout, driven in tests by vitest fake timers (mirroring createFlingSettle).
// ---------------------------------------------------------------------------

export interface ScrollActivityDeps {
  /** Quiet period (ms) after the last qualifying event before the window closes. */
  idleMs: number
  /**
   * How long after a `noteInput` a bare `scroll` event still counts as that gesture's
   * momentum. 0 (the default) disables the momentum extension, so only real input opens
   * the window.
   */
  momentumGraceMs?: number
  /** Monotonic clock, injected for tests. Defaults to {@link monotonicNow}. */
  now?: () => number
}

export interface ScrollActivity {
  /** True while the window is open. Reactive; safe to read in a memo or in JSX. */
  active: Accessor<boolean>
  /**
   * A genuine user scroll input: wheel, touch, pointer, a scroll-relevant keydown, or a
   * direct interaction with a scrollbar overlay. ALWAYS (re)opens the window and re-bases
   * the momentum grace.
   */
  noteInput: () => void
  /**
   * A bare `scroll` event. Opens the window ONLY within `momentumGraceMs` of the last
   * `noteInput`, so a post-flick coast keeps it open while our own programmatic writes
   * (stick-to-bottom, anchor re-pin, seek) never do.
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
  const graceMs = deps.momentumGraceMs ?? 0
  const [active, setActive] = createSignal(false)
  // NEGATIVE_INFINITY, not 0: monotonicNow() reads performance.now(), which starts near zero
  // at page load, so a zero epoch would put the first programmatic scroll of a restored
  // conversation INSIDE the grace and open the window with no user input at all. The scroll
  // hook guards the same trap the same way (see lastNativeKeyScrollAt in useChatScroll).
  let lastInputAt = Number.NEGATIVE_INFINITY
  let idleTimer: ReturnType<typeof setTimeout> | undefined
  let disposed = false

  const open = () => {
    // A re-notify while already open is dropped by the signal's default `===` equality, so
    // only the timer is re-armed. One clearTimeout + one setTimeout per event -- the same
    // per-event cost as the inline debounce this replaces.
    setActive(true)
    if (idleTimer !== undefined)
      clearTimeout(idleTimer)
    idleTimer = setTimeout(() => {
      idleTimer = undefined
      setActive(false)
    }, deps.idleMs)
  }

  const noteInput = () => {
    if (disposed)
      return
    lastInputAt = now()
    open()
  }

  const noteScroll = () => {
    if (disposed)
      return
    // Inclusive: a scroll dispatched in the same tick as the input that caused it is part of
    // that gesture, and with a grace of 0 it is the only scroll that still qualifies.
    if (now() - lastInputAt <= graceMs)
      open()
  }

  const dispose = () => {
    disposed = true
    if (idleTimer !== undefined) {
      clearTimeout(idleTimer)
      idleTimer = undefined
    }
    lastInputAt = Number.NEGATIVE_INFINITY
    setActive(false)
  }

  onCleanup(dispose)

  return { active, noteInput, noteScroll, dispose }
}
