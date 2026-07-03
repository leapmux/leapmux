import type { ChatScrollVirtualizer } from './useChatScroll'
import type { ScrollAnchor } from '~/stores/chatTypes'
import { clamp } from '~/lib/clamp'
import { clampScrollTop, EDGE_INTENT_TOLERANCE_PX, maxScrollTopOf, REPIN_MIN_DELTA_PX } from './chatScrollGeometry'

/**
 * The fling-settle surface the re-pin engine drives: accumulate a deferred mid-fling
 * correction, drop the accumulated drift, or re-baseline it to a fresh anchor. Passed as
 * a thunk (`() => ...`) because fling-settle closes over this engine's `captureAnchor`,
 * a genuine definition cycle the hook breaks with a forward `let`.
 */
interface AnchorRepinFlingSettle {
  accumulate: (px: number) => void
  reset: () => void
  rebase: () => void
}

/** The velocity read the re-pin uses to tell a momentum fling from a deliberate scroll. */
interface AnchorRepinVelocity {
  isFling: () => boolean
  isActivelyFlinging: () => boolean
  hasRecentMomentumInput: () => boolean
}

export interface AnchorRepinDeps {
  /** The scroll container element (a plain ref in the hook), or undefined before mount. */
  getEl: () => HTMLDivElement | undefined
  /** The offset map: resolve an anchor to a content Y, and a content Y to its row. */
  virt: Pick<ChatScrollVirtualizer, 'anchorAt' | 'scrollTopForAnchor'>
  /** True while a programmatic scroll animation is running (a re-pin defers into it). */
  isAnimating: () => boolean
  /** Programmatic scrollTop write whose echo the guard recognizes as ours. */
  writeScrollTop: (top: number, source?: string) => void
  /** Logical scrollTop read. Defaults to the raw DOM value for unit tests/helpers. */
  readScrollTop?: (el: HTMLDivElement) => number
  /** Velocity reads, lazily fetched at call time. */
  velocity: AnchorRepinVelocity
  /** The fling-settle unit, via a thunk to break the captureAnchor<->flingSettle cycle. */
  flingSettle: () => AnchorRepinFlingSettle
  /**
   * True only while a USER scroll event is mounting+measuring newly revealed rows (inside
   * handleScroll's refreshViewport). The geometry re-pin fires synchronously off those
   * measurements; while set it absorbs slow-scroll micro-corrections and defers fling
   * corrections, because writing scrollTop against the native scroll event reads as a jump.
   */
  isUserScrolling: () => boolean
  /**
   * Whether newer messages exist beyond the loaded window (the reader is windowed AWAY from
   * the live tail). Lets captureViewportAnchor pin the BOTTOM row at the loaded-window bottom
   * (the mirror of the top-edge pin): at the LIVE tail this is false and the hook follows the
   * tail instead of anchoring, so pinning the bottom row there would fight tail-follow.
   */
  hasNewerMessages: () => boolean
  /**
   * Reports an immediate keep-position write whose target CLAMPED against a scroll
   * boundary, so the anchored row could not land on its captured viewport line and
   * visibly moved by `clampPx` (= targetTop - idealTop; >0 clamped at the top, so the
   * row was pushed up; <0 clamped at the bottom). Fired only on a real clamp
   * (|clampPx| >= REPIN_MIN_DELTA_PX), never on the routine in-range write. The engine
   * stays mechanical -- it reports THAT a clamp happened; the hook owns the policy
   * (a visible-px floor, and whether more history exists that direction so the clamp
   * was avoidable) and decides whether to WARN. Optional: unit tests omit it.
   */
  onRepinClamp?: (info: {
    anchorId: string
    clampPx: number
    fromTop: number
    idealTop: number
    targetTop: number
    clientHeight: number
    maxScrollTop: number
  }) => void
  /**
   * Reports a re-pin that deliberately did NOT correct the anchored row back to its
   * captured viewport line, leaving it displaced on-screen by `residualPx` (the
   * correction we withheld). This is the OUTCOME-based counterpart to onRepinClamp: it
   * catches a content shift that produces NO scroll event, so the hook's scroll-event
   * detector can't see it. Two reasons:
   *  - 'absorbed': a small estimate->measure correction arrived during a slow/tailing
   *    scroll and we re-anchored to the shifted position instead of snapping back (a
   *    PERMANENT accepted shift, up to the small-absorb cap).
   *  - 'deferred-fling': a correction was deferred mid-fling as drift (transient -- the
   *    fling-settle re-anchors it when momentum stops).
   * The engine stays mechanical; the hook owns the WARN policy (a visible-px floor, and
   * ignoring the fast-fling frames where the shift blends into momentum). Optional: unit
   * tests omit it.
   */
  onAnchorDrift?: (info: {
    anchorId: string
    residualPx: number
    reason: 'absorbed' | 'deferred-fling'
    fromTop: number
    clientHeight: number
  }) => void
}

/**
 * Small estimate->measure corrections should not fight a user's slow/manual scroll. For
 * deltas this small, re-anchor to the live viewport and let the user's scroll trajectory
 * win. Larger shifts are structural enough (prepend/trim/tall outlier) that preserving
 * the existing anchor is less surprising than silently absorbing the movement.
 */
const SMALL_USER_SCROLL_REPIN_ABSORB_PX = 128
const VIEWPORT_MIDPOINT_RATIO = 0.5

/**
 * The anchor + re-pin engine: the hook's single most intricate invariant, extracted from
 * useChatScroll into its own unit (like createFlingSettle / createStickyBottom).
 *
 * It owns the scroll-mode state machine (following the tail vs anchored to a row) and the
 * re-pin that keeps the anchored row visually stationary across every geometry change
 * (prepend, trim, estimate->measured correction). The intricate part is distinguishing a
 * legitimate keep-position shift (correct scrollTop immediately) from a stale anchor a
 * fling outran or a live fling whose momentum a write would cancel (re-anchor / defer
 * instead) -- now isolated and unit-testable here.
 *
 * The anchor accessors (currentAnchor / isFollowing / followTail / setAnchor) are exposed
 * so the hook's ScrollContext -- consumed by the other scroll units -- delegates to this
 * one source. The engine takes its RAW deps (getEl / virt / writeScrollTop / isAnimating)
 * rather than the whole ScrollContext, so ScrollContext can be built FROM its accessors
 * without a creation cycle.
 */
export function createAnchorRepin(deps: AnchorRepinDeps) {
  const readScrollTop = (el: HTMLDivElement) => deps.readScrollTop?.(el) ?? el.scrollTop
  const viewportAnchorOffset = (el: HTMLDivElement, ratio: number) => Math.max(0, el.clientHeight * ratio)
  const captureViewportAnchor = (el: HTMLDivElement) => {
    const scrollTop = readScrollTop(el)
    // Which viewport line to pin the captured row to: the NEARER edge's row when at an edge,
    // else the viewport MIDPOINT for mid-scroll stability. Pinning the midpoint at an edge
    // lets an unmeasured row on the far side of the midpoint, once it measures taller than its
    // estimate, push scrollTop to re-center -- the "lands a few hundred px off the edge" drift.
    //  - TOP edge (ratio 0): a freshly-mounted top row growing would otherwise push scrollTop
    //    DOWN ("lands a few hundred px below the top"). Governs BOTH the live scroll-event
    //    capture (captureAnchor) AND the re-pin's recover-from-a-gone-anchor path
    //    (reanchorAndDropDrift), so a Home jump whose window replace / trim invalidates the
    //    anchored top row still recovers to the top rather than the midpoint.
    //  - BOTTOM edge (ratio 1), ONLY when windowed AWAY from the live tail (hasNewerMessages):
    //    a bottom row growing would otherwise slide the loaded-window bottom off-screen below
    //    the viewport. At the LIVE tail the hook follows the tail instead of capturing here, so
    //    this stays midpoint there (guarded on hasNewerMessages so it can never fight tail-follow).
    const atTop = scrollTop <= EDGE_INTENT_TOLERANCE_PX
    const atLoadedBottom = deps.hasNewerMessages() && maxScrollTopOf(el) - scrollTop <= EDGE_INTENT_TOLERANCE_PX
    const ratio = atTop ? 0 : atLoadedBottom ? 1 : VIEWPORT_MIDPOINT_RATIO
    const offset = viewportAnchorOffset(el, ratio)
    return {
      anchor: deps.virt.anchorAt(scrollTop + offset),
      captureTop: scrollTop,
      viewportOffsetRatio: ratio,
    }
  }

  // Scroll-mode state machine: is the viewport pinned to the live tail, or holding a
  // specific row stationary while scrolled up? Replaces a nullable `anchor` variable so
  // "following the tail" and "anchored to a row" are explicit, type-checked states
  // instead of null vs non-null. The anchored row is re-resolved against the offset map
  // after every geometry change so estimate->measured corrections, prepends, and trims
  // keep it visually stationary.
  //
  // This models ONLY the viewport-pinning mode. The other scroll guards are orthogonal
  // cross-cutting concerns, NOT alternative modes, so they stay in the hook:
  // `preserveBrowsingPosition` (one prepend's "don't auto-stick" window),
  // `userScrolling`/`repinning` (transient reentrancy guards that can BOTH be set while
  // anchored), and `suppressAutoLoadOlderAfterRestore` (a one-shot). Folding them into
  // this union would make illegal combinations representable.
  // `origin` distinguishes a normal viewport capture from a toggle row-top pin
  // (captureRowTopAnchor). A 'row-top' anchor must survive its own resize's echo scroll
  // events -- while it is current, the hook skips its handleScroll midpoint re-capture so the
  // multi-phase resize (estimate -> measured) keeps pinning the toggled row instead of yanking
  // to the midpoint. It lives HERE, as a property of the anchor, rather than as a loose
  // boolean: a fresh anchor is constructed with the default 'viewport', so every re-anchor
  // (trim, gone anchor, capture) structurally supersedes a prior hold -- an "anchored+held
  // while following" or "held after a viewport capture" state is unrepresentable, not merely
  // cleared by discipline. A genuine user gesture downgrades it via releaseRowTopHold.
  type ScrollMode
    = | { kind: 'following' }
      | { kind: 'anchored', anchor: ScrollAnchor, viewportOffsetRatio: number, origin: 'viewport' | 'row-top' }
  let scrollMode: ScrollMode = { kind: 'following' }
  // The scrollTop the current anchor was resolved FROM (the viewport position at
  // capture). repinToAnchor uses it to spot a STALE anchor: a keep-position write is only
  // valid while the viewport still sits where the anchor was captured. A fast fling fires
  // scroll events sparsely, so an async geometry re-pin (a row mounting mid-fling bumps
  // geometryVersion) can run AFTER momentum has carried the viewport far from the last
  // capture but BEFORE the next scroll event re-captures -- writing scrollTop back to the
  // now-off-screen anchor reverses the fling. Tracking the capture position lets the
  // re-pin detect that gap and re-anchor to the live viewport instead. A legit
  // keep-position shift (prepend/trim/streaming above) leaves the viewport in place, so
  // movement-since-capture stays ~0 and never trips it.
  let anchorCaptureTop = 0
  // Guards against synchronous re-entry while a re-pin's own viewport refresh mounts new
  // rows that measure and trigger another geometry change.
  let repinning = false
  // A keep-position re-pin (a prepend/trim/measure shifting content above the anchor)
  // arrived while a scroll animation was running, so repinToAnchor deferred it rather
  // than fight the animation. Applied when the animation is CANCELLED mid-flight (its
  // natural end lands at the bottom via stickToBottom, absorbing the shift already).
  let repinDeferredDuringAnimation = false

  /** The pinned row while anchored, or null while following the live tail. */
  const currentAnchor = (): ScrollAnchor | null => (scrollMode.kind === 'anchored' ? scrollMode.anchor : null)
  /** The pinned row plus where it is held inside the viewport. */
  const currentAnchorState = (): { anchor: ScrollAnchor, viewportOffsetRatio: number } | null =>
    scrollMode.kind === 'anchored'
      ? { anchor: scrollMode.anchor, viewportOffsetRatio: scrollMode.viewportOffsetRatio }
      : null
  /** True while following the live tail (not anchored to a row). */
  const isFollowing = () => scrollMode.kind === 'following'
  /** True while the current anchor is a toggle row-top pin the hook must not re-capture over. */
  const isHoldingRowTop = () => scrollMode.kind === 'anchored' && scrollMode.origin === 'row-top'
  /** Drop the toggle row-top hold (a user gesture is taking control). */
  const releaseRowTopHold = () => {
    // Downgrade the origin, KEEPING the anchor: the pinned row stays put (a later async
    // re-measure still re-pins it) until the next capture -- only handleScroll's midpoint
    // re-capture is re-enabled. A full followTail() here would wrongly drop the anchor.
    if (scrollMode.kind === 'anchored' && scrollMode.origin === 'row-top')
      scrollMode = { ...scrollMode, origin: 'viewport' }
  }
  /** Transition to following the live tail (drop any anchor). */
  const followTail = () => {
    scrollMode = { kind: 'following' }
  }
  /**
   * Transition to anchored at `a`, or to following when `a` is null (empty list). Records
   * the viewport position the anchor was resolved from and the viewport-relative line
   * where the anchor should remain pinned. `captureTop` defaults to the live scrollTop --
   * correct for every capture/re-anchor site, where the anchor IS resolved from the
   * current position; restoreSavedViewport passes the landed scrollTop explicitly because
   * it anchors around a programmatic write rather than the current position.
   *
   * `origin` defaults to 'viewport', so every capture/re-anchor/gone-anchor site
   * structurally supersedes a prior toggle row-top hold (see ScrollMode.origin); only
   * captureRowTopAnchor passes 'row-top'.
   */
  const setAnchor = (
    a: ScrollAnchor | null,
    captureTop?: number,
    viewportOffsetRatio = 0,
    origin: 'viewport' | 'row-top' = 'viewport',
  ) => {
    scrollMode = a
      ? { kind: 'anchored', anchor: a, viewportOffsetRatio: clamp(viewportOffsetRatio, 0, 1), origin }
      : { kind: 'following' }
    if (a) {
      const el = deps.getEl()
      anchorCaptureTop = captureTop ?? (el ? readScrollTop(el) : 0)
    }
  }

  /** Capture the current viewport-midpoint anchor (used while scrolled up). */
  const captureAnchor = () => {
    const el = deps.getEl()
    if (el) {
      const captured = captureViewportAnchor(el)
      setAnchor(captured.anchor, captured.captureTop, captured.viewportOffsetRatio)
      // A new capture re-baselines deferred-drift accounting: subsequent suppressed
      // corrections are measured from THIS anchor position.
      deps.flingSettle().rebase()
    }
  }

  /** Capture the row at the viewport top (used by explicit top jumps). */
  const captureTopAnchor = () => {
    const el = deps.getEl()
    if (el) {
      const scrollTop = readScrollTop(el)
      setAnchor(deps.virt.anchorAt(scrollTop), scrollTop, 0)
      deps.flingSettle().rebase()
    }
  }

  /**
   * Pin a SPECIFIC row's top edge at its CURRENT viewport line, so an imminent height
   * change of THAT row (a user expand/collapse or diff-view toggle) grows/shrinks BELOW the
   * pinned line and the row stays visually stationary.
   *
   * Unlike captureAnchor -- which pins whatever row sits at the viewport MIDPOINT, so a
   * toggled row ABOVE the midpoint scrolls away as the re-pin compensates the growth that
   * happened above the midpoint -- this pins the toggled row itself. The row's top offset is
   * INVARIANT under its own height change (rows above it don't move), so the geometry re-pin
   * the toggle triggers resolves to the same scrollTop and writes nothing: no scroll.
   *
   * Call while the DOM still reflects the PRE-toggle geometry (before applying the toggle),
   * so scrollTop and the row's offset are read pristine. Returns whether a hold was armed.
   * A no-op (returns false) when the row isn't in the offset map (not currently windowed) or
   * the viewport has no height -- both leave the current scroll mode untouched. On success it
   * anchors with origin 'row-top' (see ScrollMode.origin / isHoldingRowTop). The caller is
   * responsible for NOT invoking this while following the live tail (where switching to
   * 'anchored' would freeze auto-scroll) -- see useChatScroll's anchorRowForResize.
   */
  const captureRowTopAnchor = (id: string): boolean => {
    const el = deps.getEl()
    if (!el || el.clientHeight === 0)
      return false
    // Anchor the row's TOP edge (offsetWithinRow 0). scrollTopForAnchor of that anchor is
    // the row's content-Y top -- resolved by id, so it pins THIS exact row (unlike
    // anchorAt(offset), which walks back over zero-height siblings sharing the offset).
    const rowAnchor: ScrollAnchor = { id, offsetWithinRow: 0 }
    const rowTop = deps.virt.scrollTopForAnchor(rowAnchor)
    if (rowTop == null)
      return false
    const scrollTop = readScrollTop(el)
    // The row's top edge as a viewport-relative ratio -- the line to keep it pinned to.
    const rawRatio = (rowTop - scrollTop) / el.clientHeight
    // The row's top must be ON-SCREEN for a top-edge pin to hold scrollTop invariant. The
    // re-pin resolves the pinned line as clientHeight*ratio with ratio floored/capped to
    // [0,1] (setAnchor clamps it, and viewportAnchorOffset does Math.max(0, ...)), so a row
    // whose top sits ABOVE the viewport (ratio < 0) or BELOW it (ratio > 1) can't be
    // represented -- clamping would move the row onto the nearest edge and JUMP the viewport
    // by the clamp amount. Bail so the caller keeps the existing (midpoint) anchor, which
    // holds a VISIBLE row stationary through the resize instead of jumping. Reachable when a
    // message row is taller than the viewport and the user toggles an inner control (e.g. a
    // tool-result expander) while the row's OWN top is scrolled off the top edge.
    //
    // Compared with SUB-PIXEL slack, not a strict bound: scrollTop is a device-pixel-
    // quantized DOM read while rowTop is a Float64 offset-map sum, so a row whose top sits
    // exactly at the viewport top routinely computes an epsilon-negative ratio -- a strict
    // `< 0` would drop the pin at the single most common toggle position (the user clicked
    // the row's header at the top of the pane) and fall back to the midpoint anchor, whose
    // re-pin then scrolls the toggled row: the exact jump this function exists to prevent.
    // A hair outside the edge clamps onto it (an error under EDGE_INTENT_TOLERANCE_PX, below
    // the re-pin's no-op threshold); a genuinely off-screen top still bails.
    const ratioTolerance = EDGE_INTENT_TOLERANCE_PX / el.clientHeight
    if (rawRatio < -ratioTolerance || rawRatio > 1 + ratioTolerance)
      return false
    const ratio = clamp(rawRatio, 0, 1)
    // setAnchor banks scrollTop as the capture position and the ratio as the viewport line,
    // so repinToAnchor's targetTop = rowTop - clientHeight*ratio equals scrollTop now and
    // stays there as rowTop is invariant under the row's OWN resize (rows above don't move).
    // Origin 'row-top' arms the hold as part of the anchor, so the resize's echo scroll events
    // don't re-capture the midpoint over it (see ScrollMode.origin).
    setAnchor(rowAnchor, scrollTop, ratio, 'row-top')
    deps.flingSettle().rebase()
    return true
  }

  // Re-anchor to the row now under the viewport midpoint and DROP any deferred fling
  // drift (the accumulated correction to keep the ABANDONED anchor stationary, which
  // would land as a jump if applied to the new one). Shared by repinToAnchor's two
  // re-anchor branches -- the stale-during-fling guard and the anchor-no-longer-resolves
  // case -- so their setAnchor + flingSettle.reset() can never drift apart.
  const reanchorAndDropDrift = () => {
    const el = deps.getEl()
    if (el) {
      const captured = captureViewportAnchor(el)
      setAnchor(captured.anchor, captured.captureTop, captured.viewportOffsetRatio)
    }
    else {
      setAnchor(null)
    }
    deps.flingSettle().reset()
  }

  /**
   * Re-resolve scrollTop from the stored anchor after a geometry change (prepend, trim, or
   * height measurement) so the anchored row stays put.
   *
   * Runs SYNCHRONOUSLY (the caller is a createEffect on virt.totalHeight(), so SolidJS has
   * already flushed the render-effects that write the spacer height and each row's
   * translateY before this runs). Correcting scrollTop in the same flush lands it in the
   * same paint as the repositioning -- deferring to a rAF instead leaves one frame where
   * the rows moved but scrollTop didn't, which reads as a vertical wiggle (worst for large
   * under-estimated rows like syntax-highlighted diffs).
   *
   * It deliberately NEVER sticks to the bottom: a measurement of rows the user is scrolling
   * into fires this, and the cached atBottom signal can be a stale `true` mid-gesture --
   * sticking here would yank a fast scroll-up back down. With no anchor (following the
   * tail) it does nothing (the caller's refresh handles the slice).
   */
  const repinToAnchor = () => {
    const el = deps.getEl()
    if (repinning || !el || el.clientHeight === 0)
      return
    if (deps.isAnimating()) {
      // A keep-position correction arrived mid-animation. Writing scrollTop now would
      // fight the animation, so DEFER it -- but record that one is pending so a mid-flight
      // cancel can absorb it (otherwise the un-absorbed shift lands as a jump when the user
      // takes over). The natural animation end sticks to the bottom and absorbs it already;
      // only a cancel needs the deferred apply.
      repinDeferredDuringAnimation = true
      return
    }
    repinning = true
    try {
      const anchorState = currentAnchorState()
      if (anchorState) {
        const top = deps.virt.scrollTopForAnchor(anchorState.anchor)
        if (top != null) {
          // The scrollTop that keeps the anchored row on its captured viewport line,
          // BEFORE clamping. A clamp against a scroll boundary means the row can't be
          // held there and visibly moves by the clamp amount (see onRepinClamp).
          const idealTop = top - viewportAnchorOffset(el, anchorState.viewportOffsetRatio)
          const targetTop = clampScrollTop(el, idealTop)
          const fromTop = readScrollTop(el)
          const signed = targetTop - fromTop
          const delta = Math.abs(signed)
          // The two "we did NOT snap the anchored row back this frame, so it is `signed`
          // off its captured line" reports differ ONLY by reason; build the shared 5-field
          // payload in one place so the two sites can't drift on fields (a new field is
          // added once, not twice).
          const reportDrift = (reason: 'absorbed' | 'deferred-fling') =>
            deps.onAnchorDrift?.({
              anchorId: anchorState.anchor.id,
              residualPx: signed,
              reason,
              fromTop,
              clientHeight: el.clientHeight,
            })
          // Skip a write that wouldn't meaningfully move scrollTop (a measurement below the
          // anchor doesn't shift it): assigning scrollTop at all can interrupt a momentum
          // scroll, so don't do it for nothing. And while the user is actively flinging,
          // defer small corrections -- writing scrollTop mid-fling cancels the momentum, and
          // over a run of off-estimate diffs that reads as repeated jumps. We ACCUMULATE the
          // deferred shift into flingSettle so its settle can accept the resulting visual
          // position and re-anchor there once momentum stops (see createFlingSettle). A
          // correction big enough to be a real jump (a page-sized prepend/trim above the
          // anchor, or an outlier measurement) still applies immediately, since landing off
          // by that much would be worse than one interrupted fling -- and supersedes any
          // deferred drift.
          const flingSuppressPx = el.clientHeight / 2
          const smallUserScrollAbsorbPx = Math.min(SMALL_USER_SCROLL_REPIN_ABSORB_PX, flingSuppressPx)
          const flingLike = deps.velocity.isFling()
          const activeFling = deps.velocity.isActivelyFlinging()
          const recentMomentumInput = deps.velocity.hasRecentMomentumInput()
          // How far the viewport has moved since this anchor was captured. Large only when a
          // fling outran the per-scroll-event captures (see anchorCaptureTop) -- a
          // keep-position prepend/trim leaves the viewport in place, so this stays ~0 for
          // every legitimate shift.
          const movedSinceCapture = Math.abs(fromTop - anchorCaptureTop)
          // The anchor is STALE: the viewport itself was flung more than a full screen from
          // where this anchor was captured, so re-pinning to it would yank the view back to a
          // now-off-screen row. This is the ONLY case the re-pin drops the correction --
          // distinguished from a large correction over a ~stationary viewport (a page-sized
          // prepend/trim), which is a genuine keep-position shift and MUST be written below.
          const flungAway = movedSinceCapture > el.clientHeight
          if (delta < REPIN_MIN_DELTA_PX) {
            // No meaningful move.
          }
          else if (flungAway) {
            // Stale anchor (the "scroll up, snap back" loop): the viewport flew a full
            // screen from capture, so the captured row is no longer the right thing to pin
            // to. Re-anchor to the row now under the viewport midpoint -- leaving the fling
            // intact -- and drop the deferred drift, which was measured against the now-
            // abandoned anchor.
            //
            // NOTE: a LARGE correction over a ~STATIONARY viewport (movedSinceCapture ~0) is
            // deliberately NOT dropped here, even mid-fling. That is a real keep-position
            // shift -- a page-sized prepend/trim above the anchor -- and dropping it leaks the
            // whole shift as cumulative scroll drift (NOT writing IS the visible yank). It
            // falls through to the immediate-write branch below.
            reanchorAndDropDrift()
          }
          else if ((deps.isUserScrolling() || recentMomentumInput) && !flingLike && delta <= smallUserScrollAbsorbPx) {
            // A small estimate->measure correction arrived while the user is slowly
            // scrolling, or during the low-velocity tail just after the scroll handler
            // returned. Writing scrollTop here fights the native momentum event and
            // shows up as a tiny backward/forward bounce. Absorb the correction by making
            // the current viewport row the new anchor; large structural shifts still fall
            // through and preserve the old anchor.
            //
            // The anchored row is `signed` off its captured line and we are NOT correcting
            // it -- an on-screen content shift with no scroll event. Report it (BEFORE the
            // re-anchor discards the old anchor's displacement) so the hook can WARN.
            reportDrift('absorbed')
            reanchorAndDropDrift()
          }
          else if ((deps.isUserScrolling() ? flingLike : activeFling) && delta < flingSuppressPx) {
            // A small correction during a FAST scroll (real momentum): defer it, so the
            // write doesn't cancel the fling. Some measurements land just AFTER handleScroll
            // returns, so outside the handler we gate on `activeFling` -- isActivelyFlinging,
            // which is TRUE only while momentum is genuinely moving the viewport (a write
            // would cancel it) and FALSE once it has stopped. It is deliberately NOT the
            // 750ms momentum-input grace: that grace outlasts the momentum by ~600ms, and
            // deferring through it meant a look-ahead premeasure landing during the post-fling
            // SETTLE (viewport already stationary) accumulated as drift instead of correcting
            // -- the observed run of deferred-fling WARNs climbing to ~176px at a fixed
            // scrollTop. Once momentum stops, the viewport is stationary, so the else branch
            // below writes the (off-screen, invisible) correction immediately and the anchor
            // never drifts. Slow/manual scrolls are absorbed by the branch above.
            // The row is `signed` off its line this frame -- transient drift the settle
            // re-anchors when momentum stops. Report it; the hook ignores the fast-fling
            // frames (where it blends into momentum) and surfaces only a lingering shift.
            reportDrift('deferred-fling')
            deps.flingSettle().accumulate(signed)
          }
          else if (maxScrollTopOf(el) <= 0) {
            // The content FITS the viewport (totalHeight <= clientHeight): there is nothing
            // to scroll, so keep-position is moot -- any write just clamps straight back to
            // 0. Skip it.
          }
          else {
            // Immediate keep-position write: the content moved under a (roughly) stationary
            // viewport, so writing scrollTop to `targetTop` keeps the anchored row visually put.
            // This is the path a page-sized prepend/trim takes EVEN during an active fling --
            // compensating it synchronously (same flush as the spacer/row transforms) is what
            // makes the shift invisible; dropping or deferring it would leak its full height
            // as scroll drift. Slow deliberate scrolls still land here for corrections above
            // the small-jump absorb threshold.
            deps.writeScrollTop(targetTop, 'anchor-repin')
            // The write clamped against a scroll boundary: the anchored row could not reach
            // its captured line and jumped by the clamp amount. Report it so the hook can
            // WARN when the clamp was avoidable (more history exists that direction). A
            // routine in-range write leaves clampPx ~0 and is not reported.
            const clampPx = targetTop - idealTop
            if (Math.abs(clampPx) >= REPIN_MIN_DELTA_PX) {
              deps.onRepinClamp?.({
                anchorId: anchorState.anchor.id,
                clampPx,
                fromTop,
                idealTop,
                targetTop,
                clientHeight: el.clientHeight,
                maxScrollTop: maxScrollTopOf(el),
              })
            }
            // The anchor now sits at its stored viewport line (we moved the viewport to keep
            // it there), so re-baseline the capture position; otherwise the next re-pin
            // would measure this intentional keep-position move as stale movement.
            anchorCaptureTop = targetTop
            deps.flingSettle().reset()
          }
        }
        else {
          // The anchored row no longer resolves -- it was trimmed out of the window, or (the
          // common case) an optimistic local reconciled to its server echo under a new id /
          // a reseq changed its id. Re-anchor to whatever row now sits at the viewport
          // midpoint (scrollTop is unchanged) so subsequent geometry changes keep a valid
          // pin instead of letting the view silently drift. Drop any deferred fling drift:
          // it was the accumulated correction to keep the now-gone row stationary, so
          // applying it to the re-anchored row at settle would be a jump.
          reanchorAndDropDrift()
        }
      }
    }
    finally {
      repinning = false
    }
  }

  /**
   * Apply a keep-position re-pin that was deferred DURING an animation, on a mid-flight
   * CANCEL (the animation is ending without landing at the bottom, so the shift it deferred
   * must be absorbed now rather than left as a jump). A no-op when nothing was deferred.
   */
  const applyDeferredRepinOnCancel = () => {
    if (repinDeferredDuringAnimation) {
      repinDeferredDuringAnimation = false
      repinToAnchor()
    }
  }

  /**
   * Drop the deferred-repin flag without applying it -- used when an animation reaches its
   * NATURAL end (it sticks to the bottom, absorbing any deferred shift) or aborts because
   * the element vanished.
   */
  const resetDeferredRepin = () => {
    repinDeferredDuringAnimation = false
  }

  return {
    currentAnchor,
    currentAnchorState,
    isFollowing,
    isHoldingRowTop,
    releaseRowTopHold,
    followTail,
    setAnchor,
    captureAnchor,
    captureTopAnchor,
    captureRowTopAnchor,
    repinToAnchor,
    applyDeferredRepinOnCancel,
    resetDeferredRepin,
    /** True while a re-pin is mid-flight (the reentrancy guard, read by fling-settle). */
    isRepinning: () => repinning,
  }
}

export type AnchorRepin = ReturnType<typeof createAnchorRepin>
