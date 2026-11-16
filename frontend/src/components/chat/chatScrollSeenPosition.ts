import { clampScrollTop, FOLLOW_TOLERANCE_PX, maxScrollTopOf } from './chatScrollGeometry'

// ---------------------------------------------------------------------------
// The seen scroll position of the chat list
//
// The scroll hook (useChatScroll.ts) records the position that its scroll handler or one
// of its own writes saw last. The browser can move the view before the `scroll` event of
// that move arrives, as a touch or scrollbar drag does. A comparison of the element with
// the recorded position then finds the move before its event does.
//
// The browser also moves the view with no move of the reader: it clamps `scrollTop` to the
// maximum position whenever the scroll range shrinks below it. The range shrinks when the
// content shrinks, and when the viewport grows taller, as it does when the composer below
// the list shrinks after a send. The unit keeps what it knows about the range since the
// last note, so that such a clamp reads as no move.
//
// The unit holds these values of the last note:
//
// - The seen position, clamped to the scroll range. The move test of the scroll handler
//   measures from it.
// - The raw position, as `scrollTop` read it. A rubber-band overscroll moves it while the
//   clamped position holds. The rest detector compares it.
// - The maximum position and the viewport height, and the lowest maximum position that a
//   check recorded since. `lowestMaxTop` derives from them the lowest position that the
//   range can have forced on the view.
//
// Each method that takes an element expects the list element while it is shown. A hidden
// pane (`display: none`) reads `scrollTop` and its scroll range as 0, so the hook passes
// only a shown element (its visibleListEl).
// ---------------------------------------------------------------------------

export interface SeenScrollPosition {
  /**
   * Record the position of `el` as the scroll handler or an own write saw it: clamped and
   * raw, with the maximum position and the viewport height of that moment.
   */
  note: (el: HTMLElement) => void
  /** The clamped position of the last note. */
  seenTop: () => number
  /**
   * The raw position of `el` differs from the raw position of the last note: the browser
   * moved the view, and the `scroll` event of that move did not arrive yet.
   */
  hasUnseenRawMove: (el: HTMLElement) => boolean
  /**
   * Record a clamp: keep the current maximum position of `el` when it is the lowest since
   * the last note. A clamp that a check recorded stays forced after the content grows again.
   */
  noteRange: (el: HTMLElement) => void
  /**
   * The lowest maximum position that the scroll range of `el` can have had since the last
   * note. The browser clamps the view to the maximum at each layout, so a position at or
   * above this value needs no move of the reader. It is the lowest of:
   *
   * - the current maximum;
   * - each maximum that noteRange recorded, and the maximum of the note;
   * - the maximum of the note less the growth of the viewport height since, never below 0.
   *   A viewport that grows by some pixels lowers the maximum by as many. The content can
   *   grow again before any check runs: a send shrinks the composer, and the thinking
   *   indicator mounts in the same frame. The current maximum then hides the clamp, and
   *   this term keeps it.
   *
   * The last term reads a move of the reader as forced in one case: the reader moved up by
   * no more than the viewport grew, between two notes, with the view near the bottom. The
   * reader then stays in follow mode, as after the send that grew the viewport.
   *
   * A content shrink that no check recorded before the content grew past it again is not
   * in this value, so the clamp of that shrink reads as a move.
   */
  lowestMaxTop: (el: HTMLElement) => number
  /**
   * The view sits more than FOLLOW_TOLERANCE_PX above the recorded raw position, and the
   * scroll range did not force that move (see lowestMaxTop): a touch or scrollbar drag
   * whose first `scroll` event did not arrive yet. The tolerance also covers a subpixel
   * `scrollTop`, which a browser can report below the integer maximum.
   *
   * It records nothing: a caller that needs a clamp of a content shrink to stay forced
   * calls noteRange.
   */
  hasUnseenUpwardMove: (el: HTMLElement) => boolean
}

/** Create the seen scroll position of the chat list (see the module comment). Starts at 0. */
export function createSeenScrollPosition(): SeenScrollPosition {
  let seenTop = 0
  let rawTop = 0
  // The maximum position and the viewport height at the last note.
  let maxTopAtNote = 0
  let heightAtNote = 0
  // The lowest maximum position that a check recorded since the last note, the maximum of
  // the note included.
  let lowestRecordedMaxTop = 0
  const lowestMaxTop = (el: HTMLElement): number => {
    const viewportGrowth = Math.max(0, el.clientHeight - heightAtNote)
    return Math.min(lowestRecordedMaxTop, maxScrollTopOf(el), Math.max(0, maxTopAtNote - viewportGrowth))
  }
  return {
    note: (el) => {
      rawTop = el.scrollTop
      seenTop = clampScrollTop(el, rawTop)
      maxTopAtNote = maxScrollTopOf(el)
      heightAtNote = el.clientHeight
      lowestRecordedMaxTop = maxTopAtNote
    },
    seenTop: () => seenTop,
    hasUnseenRawMove: el => el.scrollTop !== rawTop,
    noteRange: (el) => {
      lowestRecordedMaxTop = Math.min(lowestRecordedMaxTop, maxScrollTopOf(el))
    },
    lowestMaxTop,
    hasUnseenUpwardMove: el =>
      el.scrollTop < Math.min(rawTop, lowestMaxTop(el)) - FOLLOW_TOLERANCE_PX,
  }
}
