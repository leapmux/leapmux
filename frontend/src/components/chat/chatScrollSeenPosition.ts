import { FOLLOW_TOLERANCE_PX, maxScrollTopOf } from './chatScrollGeometry'

// ---------------------------------------------------------------------------
// The seen scroll position of the chat list
//
// The scroll hook (useChatScroll.ts) records the position that its scroll handler or one
// of its own writes saw last. The browser can move the view before the `scroll` event of
// that move arrives, as a touch or scrollbar drag does. A comparison of the element with
// the recorded position then finds the move before its event does.
//
// The unit holds three values:
//
// - The seen position, clamped to the scroll range. The move test of the scroll handler
//   measures from it.
// - The raw position, as `scrollTop` read it. A rubber-band overscroll moves it while the
//   clamped position holds. The rest detector compares it.
// - The raw position, lowered to the lowest maximum position that a check recorded since
//   the last note. A content shrink clamps `scrollTop` to the new maximum with no move of
//   the reader, and the upward move test must not read that clamp as a move.
//
// Each method that takes an element expects the list element while it is shown. A hidden
// pane (`display: none`) reads `scrollTop` and its scroll range as 0, so the hook passes
// only a shown element (its visibleListEl).
// ---------------------------------------------------------------------------

export interface SeenScrollPosition {
  /** Record the position that the scroll handler or an own write saw: clamped and raw. */
  note: (logicalTop: number, rawTop: number) => void
  /** The clamped position of the last note. */
  seenTop: () => number
  /**
   * The raw position of `el` differs from the raw position of the last note: the browser
   * moved the view, and the `scroll` event of that move did not arrive yet.
   */
  hasUnseenRawMove: (el: HTMLElement) => boolean
  /**
   * Record a clamp: lower the recorded raw position to the current maximum position of
   * `el`. A clamp that a check recorded stays forced after the content grows again.
   */
  noteRange: (el: HTMLElement) => void
  /**
   * The view sits more than FOLLOW_TOLERANCE_PX above the recorded raw position, and the
   * scroll range did not force that move: a touch or scrollbar drag whose first `scroll`
   * event did not arrive yet. The comparison uses the lower of the recorded position and
   * the current maximum, so a clamp to the current range reads as no move, also when no
   * check recorded it. A clamp that no check recorded before the content grew past it
   * again reads as a move. The tolerance also covers a subpixel `scrollTop`, which a
   * browser can report below the integer maximum.
   *
   * It records nothing: a caller that needs a clamp to stay forced calls noteRange.
   */
  hasUnseenUpwardMove: (el: HTMLElement) => boolean
}

/** Create the seen scroll position of the chat list (see the module comment). Starts at 0. */
export function createSeenScrollPosition(): SeenScrollPosition {
  let seenTop = 0
  let rawTop = 0
  let rawTopInRange = 0
  return {
    note: (logicalTop, raw) => {
      seenTop = logicalTop
      rawTop = raw
      rawTopInRange = raw
    },
    seenTop: () => seenTop,
    hasUnseenRawMove: el => el.scrollTop !== rawTop,
    noteRange: (el) => {
      rawTopInRange = Math.min(rawTopInRange, maxScrollTopOf(el))
    },
    hasUnseenUpwardMove: el =>
      el.scrollTop < Math.min(rawTopInRange, maxScrollTopOf(el)) - FOLLOW_TOLERANCE_PX,
  }
}
