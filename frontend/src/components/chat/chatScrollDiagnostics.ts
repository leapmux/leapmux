import { EDGE_INTENT_TOLERANCE_PX } from './chatScrollGeometry'

// ---------------------------------------------------------------------------
// Scroll-anomaly diagnostics (always-on WARN logging) -- thresholds + the pure
// Detector B decision
//
// The virtualized list re-pins scrollTop to keep the anchored row stationary as
// geometry changes; a correct re-pin is invisible. Three WARNs flag the scrolls the
// user actually PERCEIVES as a jump, so one can be copied from the console to report
// a glitch. All are tuned to stay silent during normal scrolling. Unlike the old
// debug firehose, they fire without the debug-logging preference (a jump is worth
// surfacing regardless). The thresholds and the exclusion CLASSIFICATION live here,
// pure and unit-testable; useChatScroll owns the emission policy (payload assembly,
// burst suppression, rate limiting) around them.
// ---------------------------------------------------------------------------

/**
 * Detector B threshold (px): a viewport move between two consecutive scroll events larger
 * than this, with NO known cause, is an unexplained teleport (see classifyUnexplainedJump).
 * An ABSOLUTE floor (not a fraction of the viewport) so it catches a SMALL jump the user
 * perceives regardless of pane height. It stays quiet because every legitimate move is
 * excluded BEFORE this threshold is consulted -- our own programmatic write (a fresh echo,
 * or a delayed one whose baseline we already advanced at write time), a keyboard page, a
 * wheel/touch fling, a scrollbar/finger drag, a stale-native translation, and the tail
 * following a grow/shrink -- so what survives down at 32px is a genuinely unaccounted-for
 * move. Deliberate scrolling never trips it: it always carries wheel/touch/pointer input.
 */
export const UNEXPLAINED_JUMP_MIN_PX = 32
/**
 * Detector A floor: a keep-position re-pin that clamps against a scroll boundary moves
 * the anchored row off its captured line by the clamp amount. Below this the shift is
 * imperceptible; at or above it the row visibly jumps. Only reported when more history
 * still exists that direction (the clamp was avoidable -- the loaded buffer ran short);
 * a clamp at a genuinely exhausted edge is expected and stays silent.
 */
export const VISIBLE_ANCHOR_JUMP_PX = 8
/**
 * Detector C floor: a re-pin that leaves the anchored row displaced on-screen instead of
 * correcting it (an ABSORBED slow-scroll correction -- see useChatScroll's onAnchorDrift)
 * shifts content with no scroll event, so Detector B can't see it. Below this the shift
 * is imperceptible; at or above it the reader notices content move.
 */
export const ANCHOR_DRIFT_WARN_PX = 16
/**
 * How long after a native-scroll KEYDOWN (Space / ArrowUp / ArrowDown -- keys the input
 * layer deliberately leaves to the browser's own scrolling, see createScrollInput) a
 * scroll event is attributed to the keyboard. Sized to cover a smooth-scroll animation's
 * trailing events; key auto-repeat refreshes it per repeat. Consulted only by Detector
 * B's exclusion list -- without it every Space page / arrow line-scroll lands as an
 * input-less scroll event and WARNs as an "unexpected jump" on normal keyboard use.
 */
export const KEYBOARD_SCROLL_GRACE_MS = 750
/**
 * Detector B re-warn suppression window (SLIDING): consecutive unexplained deltas within
 * this gap of each other are one gesture or burst -- above all a scrollbar-thumb drag or
 * track click, which fires ONLY `scroll` events (Firefox dispatches no pointer events for
 * scrollbar interaction), so it cannot be excluded by input state and its first one or two
 * events land here before the velocity tracker recognizes the motion. Warn once at the
 * burst's head (with a count of what followed) instead of per event; the window slides so
 * a sustained drag stays one WARN, while a later isolated teleport still reports.
 */
export const UNEXPLAINED_JUMP_REWARN_MS = 1000
/**
 * Detector C re-warn window (FIXED, from the last emitted WARN): slow-scrolling through
 * never-measured history absorbs one estimate->measure correction per mounting row, and a
 * per-row WARN stream during routine reading is noise that buries real anomalies. At most
 * one WARN per window; the suppressed events' count and residual sum ride along on the
 * next one so the aggregate drift signal survives the rate limit.
 */
export const ANCHOR_DRIFT_REWARN_MS = 1000

export interface UnexplainedJumpParams {
  scrollTopAtStart: number
  lastScrollTopBeforeEvent: number
  /** maxScrollTopOf(el) read in the SAME pre-refresh epoch as scrollTopAtStart. */
  maxScrollTopAtStart: number
  programmaticEcho: boolean
  stillEcho: boolean
  discretePage: boolean
  staleNative: boolean
  wasActivelyFlingingBeforeEvent: boolean
  /** Scroll mode BEFORE the handler's own followTail() re-engagement could flip it. */
  wasFollowingBeforeEvent: boolean
  scrollInputActive: boolean
  recentMomentumInput: boolean
  recentKeyboardScroll: boolean
}

/**
 * Detector B decision: did the viewport teleport more than UNEXPLAINED_JUMP_MIN_PX between
 * two consecutive scroll events with NO known cause -- an unguarded scrollIntoView, browser
 * scroll anchoring, or a programmatic write that forgot to mark itself? Every legitimate
 * move is excluded here so deliberate scrolling never trips it: our own write
 * (programmaticEcho/stillEcho), a keyboard PageUp/Down (discretePage), a NATIVE keyboard
 * scroll (Space/arrows -- recentKeyboardScroll; the input layer leaves those to the
 * browser, so they carry no other signal), a pointer/finger drag (scrollInputActive), a
 * wheel/trackpad/touch fling (recentMomentumInput -- every real fling marks momentum
 * input, and its fast large-delta events all land inside that 750ms window), a trackpad
 * momentum coast that outlived that window but is still flinging
 * (wasActivelyFlingingBeforeEvent -- the velocity tracker sees it via the scroll events even
 * after the input grace lapses), a stale-native momentum translation, and the tail following
 * a grow/shrink to the clamped bottom (tailFollowToBottom, below).
 *
 * The mode and geometry inputs are PRE-EVENT SNAPSHOTS taken at the top of the scroll
 * handler, not live reads: the handler itself re-engages follow when the event LANDS in
 * the bottom band, so a live isFollowing() would let a genuine mid-list-to-bottom teleport
 * excuse itself; and refreshViewport's row mounts can grow scrollHeight in the same flush,
 * so a live maxScrollTopOf would compare positions captured in the pre-refresh coordinate
 * epoch against post-refresh geometry.
 *
 * KNOWN RESIDUALS: a scrollbar-thumb drag or track click fires only `scroll` events
 * (Firefox dispatches no pointer events for scrollbar interaction), so its first one or
 * two >32px deltas from rest are indistinguishable from a teleport and DO reach the WARN;
 * the burst suppression at the call site (UNEXPLAINED_JUMP_REWARN_MS) bounds that to one
 * WARN per gesture, and events 3+ of a fast drag are excluded by the velocity tracker.
 * Find-in-page navigation likewise scrolls the container straight from browser chrome
 * with no DOM event at all -- unexcludable client-side; each hop is at most one WARN.
 *
 * We deliberately do NOT exclude on THIS event's measured velocity: a genuine teleport's
 * OWN scroll event samples a huge instantaneous speed (the handler samples before calling
 * this), so a post-sample isFling/isActivelyFlinging gate would suppress the very jump we
 * want -- the pre-sample wasActivelyFlingingBeforeEvent avoids that (a teleport from rest
 * wasn't flinging on the prior event). Returns the signed delta plus whether it is an
 * unexplained jump. Pure: every input is a snapshot the caller takes, so the decision
 * table is unit-testable without a DOM or the scroll hook.
 */
export function classifyUnexplainedJump(params: UnexplainedJumpParams): { deltaFromLast: number, isUnexplained: boolean } {
  const deltaFromLast = params.scrollTopAtStart - params.lastScrollTopBeforeEvent
  const teleport = Math.abs(deltaFromLast) > UNEXPLAINED_JUMP_MIN_PX
  // A large delta that lands at the clamped BOTTOM while the view is glued to the live tail
  // is the tail following a geometry change, not a user teleport. Two shapes:
  //  - Content GREW (a big block arrived): the restick moves scrollTop up to the new bottom.
  //    Its echo can arrive past the programmatic-guard marker TTL under load.
  //  - Content SHRANK (a row re-measured shorter, a streaming block finalized, an indicator
  //    removed): maxScrollTop drops below the pinned position and the browser force-clamps
  //    scrollTop DOWN to the new bottom (no marker at all). Recognizable because the prior
  //    position is now beyond the range.
  // Gated on landing at the bottom AND (following the tail BEFORE the event OR the prior
  // position now exceeds the range) so it can't mask a mid-list teleport -- including one
  // that LANDS at the bottom, which flips the live mode to 'following' inside the very
  // handler that calls this. A shrink under a scrolled-UP reader leaves
  // scrollTop < maxScrollTop, so it never force-clamps here.
  const maxTop = params.maxScrollTopAtStart
  const tailFollowToBottom = params.scrollTopAtStart >= maxTop - EDGE_INTENT_TOLERANCE_PX
    && (params.wasFollowingBeforeEvent || params.lastScrollTopBeforeEvent > maxTop)
  const explained = params.programmaticEcho || params.stillEcho || params.discretePage
    || params.scrollInputActive || params.recentMomentumInput || params.recentKeyboardScroll
    || params.wasActivelyFlingingBeforeEvent
    || params.staleNative
    || tailFollowToBottom
  return { deltaFromLast, isUnexplained: teleport && !explained }
}
