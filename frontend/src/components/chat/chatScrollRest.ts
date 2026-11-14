// ---------------------------------------------------------------------------
// Scroll rest detection for the chat list
//
// The layout shift (chatLayoutShift.ts) may be removed with a `scrollTop` write only
// while the scroller rests: a write during a gesture can end the momentum of the
// browser, and WebKit can deliver a scroll event in the old coordinates after it. This
// unit decides when the scroller rests.
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
  requestFrame?: (cb: () => void) => number
  cancelFrame?: (handle: number) => void
}

export interface ScrollRest {
  /** The scroller moved: it no longer rests, and the quiet time starts again. */
  noteScroll: () => void
  /**
   * The last pointer or touch went up. When no quiet time or confirm frame is pending,
   * the scroller enters rest now: a geometry change during a press with no scroll waits
   * for this call.
   */
  noteInputEnded: () => void
  /** Whether the scroller rests now. True at creation. */
  isResting: () => boolean
  /** Clear the timer and cancel the frame. */
  dispose: () => void
}

function defaultRequestFrame(cb: () => void): number {
  if (typeof requestAnimationFrame !== 'function') {
    cb()
    return 0
  }
  return requestAnimationFrame(() => cb())
}

function defaultCancelFrame(handle: number): void {
  if (typeof cancelAnimationFrame === 'function')
    cancelAnimationFrame(handle)
}

export function createScrollRest(deps: ScrollRestDeps): ScrollRest {
  const requestFrame = deps.requestFrame ?? defaultRequestFrame
  const cancelFrame = deps.cancelFrame ?? defaultCancelFrame
  let resting = true
  let quietTimer: ReturnType<typeof setTimeout> | undefined
  // The pending confirm frame. An object, so that a frame that runs synchronously inside
  // `requestFrame` cannot leave a handle that marks it as still pending.
  let confirmFrame: { handle: number } | undefined

  const clearQuietTimer = () => {
    if (quietTimer !== undefined) {
      clearTimeout(quietTimer)
      quietTimer = undefined
    }
  }

  const cancelConfirmFrame = () => {
    if (confirmFrame !== undefined) {
      cancelFrame(confirmFrame.handle)
      confirmFrame = undefined
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

  // The two functions start each other, so they are declarations.
  function startQuietTime(): void {
    clearQuietTimer()
    quietTimer = setTimeout(requestConfirmFrame, SCROLL_REST_QUIET_MS)
  }

  // A busy main thread can run the timer before a delayed `scroll` event. The frame
  // confirms that no move is still on its way.
  function requestConfirmFrame(): void {
    quietTimer = undefined
    const frame = { handle: 0 }
    confirmFrame = frame
    frame.handle = requestFrame(() => {
      if (confirmFrame !== frame)
        return
      confirmFrame = undefined
      if (deps.hasUnseenMove())
        startQuietTime()
      else
        enterRest()
    })
  }

  return {
    noteScroll: () => {
      resting = false
      cancelConfirmFrame()
      startQuietTime()
    },
    noteInputEnded: () => {
      if (quietTimer === undefined && confirmFrame === undefined)
        enterRest()
    },
    isResting: () => resting && !deps.isInputActive(),
    dispose: () => {
      clearQuietTimer()
      cancelConfirmFrame()
    },
  }
}
