import { createRafCoalescer } from '~/lib/rafCoalesce'

// ---------------------------------------------------------------------------
// Scroll rest detection for the chat list
//
// The scroll hook removes the layout shift (chatLayoutShift.ts) with a `scrollTop` write,
// the rebase. Rest is the default time of the rebase: a write during a gesture can end the
// momentum of the browser, and WebKit can deliver a scroll event in the old coordinates
// after it. The hook also rebases at two other times (see useChatScroll.ts):
//
// - During a scroll, when the shift is larger than the view near the reachable top, or
//   when the viewport top is above the first row and shows the blank band there
//   (`needsRebaseDuringScroll`).
// - Before each placement, such as a seek, a restore of the view, Home, or a page key. The
//   placement writes `scrollTop` itself.
//
// This unit decides when the scroller rests.
//
// The scroller rests when no pointer or touch is down and its raw `scrollTop` did not
// change for SCROLL_REST_QUIET_MS, confirmed by one animation frame. The `scrollend`
// event is no signal of rest: a browser also fires it when a programmatic scroll
// completes, so the event of a write during momentum would count as rest.
// ---------------------------------------------------------------------------

/** How long the raw scroll position must stay unchanged before the scroller rests. */
export const SCROLL_REST_QUIET_MS = 150

export interface ScrollRestDeps {
  /** A pointer or a touch is down. */
  isInputActive: () => boolean
  /**
   * The raw scroll position differs from the last raw position that the scroll handler
   * or an own write saw: a `scroll` event is still on its way.
   */
  hasUnseenMove: () => boolean
  /**
   * The scroller entered rest. Called from a timer, a frame, or an input listener, never
   * from a reactive computation. It can run again while the scroller already rests.
   */
  onRest: () => void
  /** Request the confirm frame. Default: `requestFrame` of `~/lib/animationFrame`. */
  requestFrame?: (cb: () => void) => number
  /** Cancel the confirm frame. Default: `cancelFrame` of `~/lib/animationFrame`. */
  cancelFrame?: (handle: number) => void
}

export interface ScrollRest {
  /** The scroller moved: it no longer rests, and the quiet time starts again. */
  noteScroll: () => void
  /**
   * The last pointer or touch went up. When no quiet time or confirm frame is pending,
   * the scroller enters rest now: a geometry change during a press with no scroll waits
   * for this call. When the press moved the scroller and the `scroll` event of that move
   * has not arrived (`hasUnseenMove`), the quiet time starts instead.
   */
  noteInputEnded: () => void
  /**
   * Whether the scroller rests now. True at creation. False while a move is on its way
   * (`hasUnseenMove`): the raw position changed, although its `scroll` event, which ends
   * the rest, did not arrive yet. A write at that time would record the moved position as
   * seen, and the event would then read as no move.
   */
  isResting: () => boolean
  /** Clear the timer and cancel the frame. */
  dispose: () => void
}

export function createScrollRest(deps: ScrollRestDeps): ScrollRest {
  let resting = true
  let quietTimer: ReturnType<typeof setTimeout> | undefined
  // A busy main thread can run the quiet timer before a delayed `scroll` event. The confirm
  // frame confirms that no move is still on its way.
  const confirmFrame = createRafCoalescer<void>(confirmRest, {
    requestFrame: deps.requestFrame,
    cancelFrame: deps.cancelFrame,
  })

  const clearQuietTimer = () => {
    if (quietTimer !== undefined) {
      clearTimeout(quietTimer)
      quietTimer = undefined
    }
  }

  // With input down, `noteInputEnded` completes the transition later.
  const enterRest = () => {
    if (deps.isInputActive())
      return
    clearQuietTimer()
    resting = true
    deps.onRest()
  }

  // The quiet time and the confirm frame start each other, so these are declarations.
  function startQuietTime(): void {
    clearQuietTimer()
    quietTimer = setTimeout(requestConfirmFrame, SCROLL_REST_QUIET_MS)
  }

  function requestConfirmFrame(): void {
    quietTimer = undefined
    confirmFrame.push()
  }

  function confirmRest(): void {
    if (deps.hasUnseenMove())
      startQuietTime()
    else
      enterRest()
  }

  return {
    noteScroll: () => {
      resting = false
      confirmFrame.abort()
      startQuietTime()
    },
    noteInputEnded: () => {
      if (quietTimer !== undefined || confirmFrame.isPending())
        return
      // The press moved the scroller, and the `scroll` event of that move has not arrived:
      // the scroller is not at rest yet, so the quiet time and the confirm frame decide.
      if (deps.hasUnseenMove()) {
        resting = false
        startQuietTime()
        return
      }
      enterRest()
    },
    isResting: () => resting && !deps.isInputActive() && !deps.hasUnseenMove(),
    dispose: () => {
      clearQuietTimer()
      confirmFrame.abort()
    },
  }
}
