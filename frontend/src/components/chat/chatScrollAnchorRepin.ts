import type { ChatScrollVirtualizer } from './useChatScroll'
import type { ScrollAnchor } from '~/stores/chatTypes'
import { maxScrollTopOf, REPIN_MIN_DELTA_PX } from './chatScrollGeometry'

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
}

export interface AnchorRepinDeps {
  /** The scroll container element (a plain ref in the hook), or undefined before mount. */
  getEl: () => HTMLDivElement | undefined
  /** The offset map: resolve an anchor to a scrollTop, and a scrollTop to its top row. */
  virt: Pick<ChatScrollVirtualizer, 'anchorAt' | 'scrollTopForAnchor'>
  /** True while a programmatic scroll animation is running (a re-pin defers into it). */
  isAnimating: () => boolean
  /** Programmatic scrollTop write whose echo the guard recognizes as ours. */
  writeScrollTop: (top: number) => void
  /** Velocity reads, lazily fetched at call time. */
  velocity: AnchorRepinVelocity
  /** The fling-settle unit, via a thunk to break the captureAnchor<->flingSettle cycle. */
  flingSettle: () => AnchorRepinFlingSettle
  /**
   * True only while a USER scroll event is mounting+measuring newly revealed rows (inside
   * handleScroll's refreshViewport). The geometry re-pin fires synchronously off those
   * measurements; while set it defers small corrections, because writing scrollTop
   * mid-fling cancels the browser's momentum and reads as a jump.
   */
  isUserScrolling: () => boolean
}

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
  type ScrollMode = { kind: 'following' } | { kind: 'anchored', anchor: ScrollAnchor }
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
  /** True while following the live tail (not anchored to a row). */
  const isFollowing = () => scrollMode.kind === 'following'
  /** Transition to following the live tail (drop any anchor). */
  const followTail = () => {
    scrollMode = { kind: 'following' }
  }
  /**
   * Transition to anchored at `a`, or to following when `a` is null (empty list). Records
   * the viewport position the anchor was resolved from. `captureTop` defaults to the live
   * scrollTop -- correct for every capture/re-anchor site, where the anchor IS resolved
   * from the current position; restoreSavedViewport passes the landed scrollTop explicitly
   * because it anchors around a programmatic write rather than the current position.
   */
  const setAnchor = (a: ScrollAnchor | null, captureTop?: number) => {
    scrollMode = a ? { kind: 'anchored', anchor: a } : { kind: 'following' }
    if (a) {
      const el = deps.getEl()
      anchorCaptureTop = captureTop ?? (el ? el.scrollTop : 0)
    }
  }

  /** Capture the current viewport-top anchor (used while scrolled up). */
  const captureAnchor = () => {
    const el = deps.getEl()
    if (el) {
      setAnchor(deps.virt.anchorAt(el.scrollTop))
      // A new capture re-baselines deferred-drift accounting: subsequent suppressed
      // corrections are measured from THIS anchor position.
      deps.flingSettle().rebase()
    }
  }

  // Re-anchor to the row now under the viewport top and DROP any deferred fling drift (the
  // accumulated correction to keep the ABANDONED anchor stationary, which would land as a
  // jump if applied to the new one). Shared by repinToAnchor's two re-anchor branches --
  // the stale-during-fling guard and the anchor-no-longer-resolves case -- so their
  // setAnchor + flingSettle.reset() can never drift apart.
  const reanchorAndDropDrift = () => {
    const el = deps.getEl()
    setAnchor(el ? deps.virt.anchorAt(el.scrollTop) : null)
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
      const a = currentAnchor()
      if (a) {
        const top = deps.virt.scrollTopForAnchor(a)
        if (top != null) {
          const fromTop = el.scrollTop
          const signed = top - fromTop
          const delta = Math.abs(signed)
          // Skip a write that wouldn't meaningfully move scrollTop (a measurement below the
          // anchor doesn't shift it): assigning scrollTop at all can interrupt a momentum
          // scroll, so don't do it for nothing. And while the user is actively flinging,
          // defer small corrections -- writing scrollTop mid-fling cancels the momentum, and
          // over a run of off-estimate diffs that reads as repeated jumps. We ACCUMULATE the
          // deferred shift into flingSettle instead of dropping it, so its settle can undo
          // the whole accumulated overshoot in one write once momentum stops (see
          // createFlingSettle). A correction big enough to be a real jump (a page-sized
          // prepend/trim above the anchor, or an outlier measurement) still applies
          // immediately, since landing off by that much would be worse than one interrupted
          // fling -- and supersedes any deferred drift.
          const flingSuppressPx = el.clientHeight / 2
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
            // Stale anchor (the "scroll up, snap back" loop): the viewport flew a full screen
            // from capture, so the captured row is no longer the right thing to pin to. Re-
            // anchor to the row now under the viewport top -- leaving the fling intact -- and
            // drop the deferred drift, which was measured against the now-abandoned anchor.
            //
            // NOTE: a LARGE correction over a ~STATIONARY viewport (movedSinceCapture ~0) is
            // deliberately NOT dropped here, even mid-fling. That is a real keep-position
            // shift -- a page-sized prepend/trim above the anchor -- and dropping it leaks the
            // whole shift as cumulative scroll drift (NOT writing IS the visible yank). It
            // falls through to the immediate-write branch below.
            reanchorAndDropDrift()
          }
          else if (deps.isUserScrolling() && deps.velocity.isFling() && delta < flingSuppressPx) {
            // A small correction during a FAST scroll (real momentum): defer it, so the
            // write doesn't cancel the fling. A slow deliberate scroll (isFling() false)
            // falls through and corrects immediately -- no drift.
            deps.flingSettle().accumulate(signed)
          }
          else if (maxScrollTopOf(el) <= 0) {
            // The content FITS the viewport (totalHeight <= clientHeight): there is nothing
            // to scroll, so keep-position is moot -- any write just clamps straight back to
            // 0. Skip it.
          }
          else {
            // Immediate keep-position write: the content moved under a (roughly) stationary
            // viewport, so writing scrollTop to `top` keeps the anchored row visually put.
            // This is the path a page-sized prepend/trim takes EVEN during an active fling --
            // compensating it synchronously (same flush as the spacer/row transforms) is what
            // makes the shift invisible; dropping or deferring it would leak its full height
            // as scroll drift. A slow deliberate scroll's small corrections also land here.
            deps.writeScrollTop(top)
            // The anchor now sits at `top` (we moved the viewport to keep it there), so
            // re-baseline the capture position; otherwise the next re-pin would measure this
            // intentional keep-position move as stale movement.
            anchorCaptureTop = top
            deps.flingSettle().reset()
          }
        }
        else {
          // The anchored row no longer resolves -- it was trimmed out of the window, or (the
          // common case) an optimistic local reconciled to its server echo under a new id /
          // a reseq changed its id. Re-anchor to whatever row now sits at the viewport top
          // (scrollTop is unchanged) so subsequent geometry changes keep a valid pin instead
          // of letting the view silently drift. Drop any deferred fling drift: it was the
          // accumulated correction to keep the now-gone row stationary, so applying it to the
          // re-anchored row at settle would be a jump.
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
    isFollowing,
    followTail,
    setAnchor,
    captureAnchor,
    repinToAnchor,
    applyDeferredRepinOnCancel,
    resetDeferredRepin,
    /** True while a re-pin is mid-flight (the reentrancy guard, read by fling-settle). */
    isRepinning: () => repinning,
  }
}

export type AnchorRepin = ReturnType<typeof createAnchorRepin>
