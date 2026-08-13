import type { DotCluster } from './chatRailPolicy'
import type { PreparedGeometry } from './chatScrollRailGeometry'
import type { VirtualItem } from './useChatVirtualizer'
import type { ChatRailData } from '~/stores/chatMessageMarks'
import { createEffect, createMemo, createSignal, For, onCleanup, Show } from 'solid-js'
import { createDotPreview } from './chatDotPreview'
import { clusterMarks, dotClustersEqual } from './chatRailPolicy'
import { clampScrollTop } from './chatScrollGeometry'
import * as styles from './ChatScrollRail.css'
import { createThumbDrag, SCRUB_ENGAGE_SLOP_PX } from './chatScrollRailDrag'
import { createDragReleaseHold } from './chatScrollRailDragHold'
import {
  computeSeqThumb,
  dragThumbPx,
  fixedThumbHeightPx,
  fractionToSeq,
  projectThumbPx,
  railYToSeq,
} from './chatScrollRailGeometry'
import { createRailMetrics } from './chatScrollRailMetrics'
import { DOT_PREVIEW_CARD_ID, dotLabel, DotPreviewCard } from './ChatScrollRailPreview'

// ---------------------------------------------------------------------------
// Seq-space scroll rail
//
// A custom scrollbar drawn over the WHOLE conversation [minSeq..maxSeq] (not just the
// ~150-message virtualized window), with teal jump dots for marked messages. This component
// orchestrates the extracted pieces and owns only the wiring + render:
//   - createRailMetrics      -- samples the scroll container reactively.
//   - chatScrollRailGeometry -- pure seq<->pixel math, the thumb rect, and dot clustering.
//   - createThumbDrag        -- the pointer-capture drag lifecycle.
//   - createDragReleaseHold  -- pins the thumb at its release fraction until the seek settles.
//   - createDotPreview       -- which dot the one preview card describes, and when it opens/closes.
//   - ChatScrollRailPreview  -- the dot hover/scrub preview card itself.
//
// EVERY press on the rail starts a drag, whatever it lands on and whatever the pointer type.
// A press on the track or a dot ALSO jumps at once, then scrubs on from the pressed point if
// the pointer travels; a press on the thumb only grabs it. That one rule is what makes the rail
// usable by a finger -- a touch has no hover to aim with and no second press to spend, so the
// press that reveals a position must be the same press that scrubs from it -- and it is exactly
// how a native scrollbar track already behaves under a mouse, so there is no pointer-type
// branch anywhere below.
// ---------------------------------------------------------------------------

/** Wheel-line height (px) used to translate line/page wheel deltas into pixels. */
const WHEEL_LINE_PX = 16

/** Fixed thumb height so the rail shows position without encoding viewport size. */
const THUMB_HEIGHT_PX = 24

export interface ChatScrollRailProps {
  /** The chat scroll container (the element the native scrollbar was hidden on). */
  scrollEl: HTMLDivElement | undefined
  /** Visible virtual rows (ascending; trailing optimistic locals carry seq 0n). */
  items: readonly VirtualItem[]
  /** Content-Y of the top of row `i`. */
  offsetOfIndex: (index: number) => number
  /** Total virtual content height (px). */
  totalHeight: number
  /** Bumped by the virtualizer whenever the offset map changes (measurement/prepend/trim). */
  geometryVersion: number
  /**
   * The rows' precomputed rowStartSeqs (null when the window holds no server row). Computed ONCE
   * by ChatView (which needs it for the scroll-owner resolution) and passed down so the O(n)
   * row-seq scan isn't repeated here -- see ChatView.railRowSeqs.
   */
  railRowSeqs: number[] | null
  /**
   * The seq-space rail data (loaded flag, whole-history min/max seq range, marked seqs, and the
   * loaded window's first/last server seq) as the SINGLE {@link ChatRailData} shape the store's
   * getRailData produces -- rather than re-flattening those six fields, which the view would then
   * have to keep in hand-sync (the exact drift ChatRailData exists to prevent).
   */
  rail: ChatRailData
  /**
   * Whether the rail hides itself, resolved by ChatView (railOwner() !== 'rail') so the ONE
   * scrollbar-owner decision -- and its single viewport-height source -- drives both this and the
   * native-scrollbar hide. The rail no longer re-resolves ownership from its own metrics: a second
   * evaluation against a different (padding-box) height was what could strand a viewport with zero
   * or two scrollbars. See resolveScrollbarOwner.
   */
  hidden: boolean
  /**
   * Whether the host's scroll-activity window is open (see ChatView's railActivity).
   * ORTHOGONAL to `hidden`: that one is the scrollbar-OWNER decision and unmounts the
   * rail, while this one is paint-only and must NEVER unmount it -- an unmount would
   * disconnect the rail's ResizeObserver, cancel a live thumb drag, and rebuild every
   * dot on each transition. The resulting `railIdle` class fades the rail on
   * EVERY screen/pointer; only the touch-safety `pointer-events: none` it also carries
   * is coarse-only.
   */
  scrollActive: boolean
  /**
   * The rail's OWN interaction reopens the host's activity window. Required, not a
   * nicety: a thumb drag captures the pointer, so its pointermove events retarget to the
   * rail element and the host's scroll container never sees them. The rail calls it on its
   * own traffic (a press, a wheel, focus reaching a dot) and again whenever it stops holding
   * ITSELF lit -- a drag, its release hold, or a dot popover ending. Without the second one,
   * any of those outlasting the host's idle timeout snaps the rail dark with no fade tail.
   */
  onActivity?: () => void
  hasMoreOlder: boolean
  hasMoreNewer: boolean
  /**
   * Seek to a seq: a press on a dot or the track, a scrub that settles on an out-of-window
   * position, or a scrub release. May resolve to whether the seek actually moved the view --
   * the drag-release hold awaits that to time its thumb hand-off.
   */
  onJumpToSeq: (seq: bigint) => void | Promise<boolean>
  /** Guard-marked programmatic scroll write for in-window thumb-drag live-scrolling. */
  previewScrollTo: (top: number) => void
  /**
   * Abandon the host's in-flight out-of-window seek, so its late fetch can't yank the viewport
   * -- the rail's own scroll writes are programmatic and never trip the host's user-scroll
   * cancellation, so nothing else drops one. One rule drives every call: a gesture abandons
   * each seek it issued and did not choose to land. That covers the grab (a PRIOR release's
   * seek), the moment the grab becomes a scrub (THIS press's own jump), each moment a scrub
   * leaves the position a settle seek asked for, and an aborted gesture, which drops the lot.
   * Optional.
   */
  onSeekInterrupt?: () => void
  /**
   * Reactive read of a marked message's hover-preview text: undefined = not resolved
   * yet, '' = resolved with no previewable text (show a label), else the snippet.
   */
  previewFor?: (seq: bigint) => string | undefined
  /** Kick off resolving a mark's preview (dot hover). Idempotent + deduped upstream. */
  warmPreview?: (seq: bigint) => void
}

export function ChatScrollRail(props: ChatScrollRailProps) {
  // The rail overlay's own pixel height, for thumb sizing and dot placement.
  const [railHeight, setRailHeight] = createSignal(0)

  let railEl: HTMLDivElement | undefined
  let railResizeObserver: ResizeObserver | undefined
  let dragCleanup: (() => void) | undefined

  // Scroll-container metrics (scrollTop / dist-from-bottom / clientHeight), sampled reactively
  // via a passive scroll listener + ResizeObserver and re-sampled on a geometry commit.
  const metrics = createRailMetrics({
    scrollEl: () => props.scrollEl,
    totalHeight: () => props.totalHeight,
    geometryVersion: () => props.geometryVersion,
  })

  // The drag-release "hold": the preview thumb fraction (null when idle) plus the state machine
  // that pins it at the release fraction until the post-release seek scrolls the view to match.
  // It clears one frame after the seek RESOLVES (not on the first ambient metrics change), so an
  // out-of-window seek's in-flight window swap / streaming churn can't hand off before the landing.
  const dragHold = createDragReleaseHold()
  const drag = dragHold.fraction

  // A MOUSE resting on the strip. Restricted to `pointerType === 'mouse'`: a touch fires
  // pointerenter on tap and does not leave until the next tap lands elsewhere, which would
  // pin the rail lit on a surface where it is meant to fade back out. A finger holds the
  // rail through drag() and activeDot() instead, both of which end with the gesture.
  const [hovered, setHovered] = createSignal(false)

  // The (n+1)-length row-seq map is computed ONCE in ChatView (railRowSeqs, memoized on the item
  // list) and handed down as a prop -- the scroll-owner resolution there already needs it, so the
  // O(n) row-seq scan runs once per item-list change instead of twice. The thin `geo` wrapper still
  // rebuilds per streaming-height commit (its offsets moved) while reusing that row-seq map
  // unchanged, so the scan stays off the streaming-commit path exactly as before.
  const prepared = createMemo<PreparedGeometry>(() => ({
    geo: { items: props.items, offsetOfIndex: props.offsetOfIndex, totalHeight: props.totalHeight },
    rowSeqs: props.railRowSeqs,
  }))

  // The current thumb height in px. Used to inset the thumb-CENTRE axis: dots, the track, and
  // track-clicks all map onto [thumbHalf, railHeight - thumbHalf] -- the range the thumb centre
  // can occupy -- so a dot always lines up with the thumb centre. Matches thumbPx's height in
  // both the resting and drag branches.
  const thumbHeightNow = createMemo(() => fixedThumbHeightPx(railHeight(), THUMB_HEIGHT_PX))

  // Dots, deduped by rounded rail pixel: many marks in a tall history collapse to the same
  // pixel, so they CLUSTER (one dot standing for its `count` marks) rather than dropping the
  // collisions. See clusterMarks. Compared by CONTENT (dotClustersEqual) so an unchanged
  // layout keeps the SAME array reference: maxSeq ticks up on every persisted row during a
  // streaming turn, but on a long conversation a +1 seq shift rounds to the same clusters, so
  // recomputing would otherwise hand <For> a fresh array each frame and tear down + rebuild
  // every dot button (and the listeners on each) for no visual change.
  const dots = createMemo<DotCluster[]>(
    // props.rail is a ChatRailData -- a structural superset of RailRange -- so pass it straight
    // through as the range rather than re-flattening {minSeq, maxSeq} (which the two would then
    // have to keep in hand-sync, the drift ChatRailData exists to prevent; see the `rail` prop).
    () => clusterMarks(props.rail.marks, props.rail, railHeight(), thumbHeightNow()),
    [],
    { equals: dotClustersEqual },
  )

  // The dot-preview state machine (which dot the single popover describes, its placement, and when
  // to warm its preview) extracted into its own unit alongside createRailMetrics /
  // createDragReleaseHold / createThumbDrag, so this component owns only the wiring + render.
  // The open card's measured height, reported by the card itself. The card's Y is clamped against
  // it (see cardTopPx), so the clamp holds a SHORT card near its own dot instead of pushing every
  // card half the max-height cap away from the dot it points at.
  const [cardHeightPx, setCardHeightPx] = createSignal(0)

  const { activeDot, cardTopPx, openDot, closeSoon, abandonDot, focusDot, blurDot, closeNow } = createDotPreview({
    dots,
    drag,
    railHeight,
    thumbHeightPx: thumbHeightNow,
    cardHeightPx,
    warmPreview: seq => props.warmPreview?.(seq),
  })

  // A press inside the preview card is live, so the card owns the pointer and the dots stand down
  // (see the dot's pointer handlers). Not a signal: nothing renders from it, and the dot handlers
  // that read it run outside any tracking scope.
  let cardPressed = false

  const cancelActiveDrag = () => {
    const cleanup = dragCleanup
    dragCleanup = undefined
    cleanup?.()
  }

  const disconnectRail = () => {
    cancelActiveDrag()
    railResizeObserver?.disconnect()
    railResizeObserver = undefined
    railEl = undefined
    setRailHeight(0)
    // Clear the hover here, with the other self-overrides. A DOM removal delivers no
    // pointerleave, so a cursor that rests on the strip when the rail hides would otherwise
    // leave this flag set: the rail then returns lit, with the cursor elsewhere, and only a
    // deliberate enter-then-leave clears it. Every override that holds the rail lit drops in
    // this one teardown, so the next one added is safe by default.
    setHovered(false)
    // No close delay here: the rail is going away, so there is no card left to move onto. The
    // press lock needs no reset: the card clears it from its own cleanup as it unmounts, which
    // keeps the card the single owner of its press lifecycle.
    closeNow()
  }

  const setRailRef = (el: HTMLDivElement) => {
    railResizeObserver?.disconnect()
    railEl = el
    const ro = new ResizeObserver(() => setRailHeight(el.clientHeight))
    railResizeObserver = ro
    ro.observe(el)
    setRailHeight(el.clientHeight)
  }

  const thumbRect = createMemo(() => {
    // While a drag is live the thumb is drawn from the drag fraction (dragThumbPx in
    // thumbPx), so this resting-thumb geometry is discarded -- bail BEFORE reading metrics
    // so the drag's per-frame previewScrollTo scroll echoes don't re-run computeSeqThumb's
    // two seqAtContentY binary searches every rAF for a rect nothing consumes.
    if (drag() !== null)
      return null
    const m = metrics()
    return computeSeqThumb(prepared(), {
      scrollTop: m.scrollTop,
      clientHeight: m.clientHeight,
      minSeq: props.rail.minSeq,
      maxSeq: props.rail.maxSeq,
      hasMoreOlder: props.hasMoreOlder,
      hasMoreNewer: props.hasMoreNewer,
      distFromBottomPx: m.dist,
    })
  })

  // Hidden unless this rail OWNS scrolling. The decision is resolved ONCE by ChatView
  // (railOwner) and handed down as `props.hidden`, the exact complement of the native-scrollbar
  // hide -- so the rail shows precisely when the native bar is hidden. Resolving it here too, from
  // the rail's own (padding-box) metrics height, is what previously let the two drift and strand
  // the viewport with zero or two scrollbars; the rail now trusts the single upstream decision.
  const hidden = () => props.hidden

  // The rail's OWN overrides of the host's activity window. Four states hold the rail lit, so
  // an interaction never fades under the reader's finger or cursor: a live thumb drag AND its
  // post-release settle (drag() stays non-null until the seek lands -- see
  // createDragReleaseHold), an open dot popover (activeDot() folds hover, KEYBOARD FOCUS, and
  // scrub into one signal, so no CSS :focus-within is needed), and a mouse that rests anywhere
  // on the strip.
  //
  // ONE definition, read by both the paint state below and the re-arm effect under it. An
  // override carries a two-sided contract -- hold the rail lit, then re-arm the host's window
  // when it releases -- and a term written into only one side either fails to hold the paint or
  // fails to re-arm, with no error either way.
  //
  // The hover term is a real state, not a `:hover { opacity: 1 }` paint override. The host's
  // window closes on its own timer, and a parked cursor sends no further pointermove to re-arm
  // it, so the rail faded out from under a cursor that rested on it -- and a CSS-only override
  // would have made it LOOK usable while the press guard below still read it as faded and
  // rejected the click. Folding hover in here keeps the look and the guard agreeing: hovered
  // means lit means clickable.
  const heldLit = createMemo(() => drag() !== null || activeDot() !== null || hovered())

  // Idle is a PAINT state, never an unmount (see the `scrollActive` prop).
  const idle = createMemo(() => !props.scrollActive && !heldLit())

  // Re-open the host's activity window on the FALLING edge of those overrides. Each of them
  // holds the rail lit while nothing re-arms that window: a captured drag's pointermove
  // retargets to the rail, so the host's scroll container never sees it, and the rail's own
  // onPointerMove relight is inert for the whole time (idle() is false). So without this, any
  // override that outlasts the host's idle timeout ends by snapping the rail dark instead of
  // fading it. Keying the re-arm on the overrides rather than on the pointer lifecycle is what
  // covers all four: a release whose out-of-window seek outlives the window re-arms when the
  // hold clears, not when the finger lifts. The hover term also re-arms on the way IN (see
  // onPointerEnter), so that a leave fades the rail instead of snapping it dark; both halves are
  // deliberate, and neither one is redundant.
  let wasHeldLit = false
  createEffect(() => {
    const lit = heldLit()
    // Not while hidden: the rail that just stopped rendering must not re-arm a window for
    // itself. Hiding tears the drag down (below), which is a falling edge of its own.
    if (wasHeldLit && !lit && !hidden())
      props.onActivity?.()
    wasHeldLit = lit
  })

  createEffect(() => {
    if (hidden())
      disconnectRail()
  })

  const thumbPx = createMemo(() => {
    const rh = railHeight()
    if (rh <= 0)
      return null
    const dragFraction = drag()
    if (dragFraction !== null) {
      // Preview: keep the resting thumb height (thumbHeightNow(), same inputs), positioned so
      // its CENTRE sits on the same centre axis the dots + track use -- dragThumbPx routes
      // through centerAxisY, so the "drag thumb lines up with the dots" invariant is one
      // tested formula rather than an inline re-derivation.
      return dragThumbPx(dragFraction, rh, thumbHeightNow())
    }
    const rect = thumbRect()
    if (!rect)
      return null
    return projectThumbPx(rect, rh, THUMB_HEIGHT_PX)
  })

  /**
   * The ONE way this component seeks. Every path goes through here -- a track or dot press, a
   * scrub's settle, a scrub release, and a keyboard activation -- so the seek contract is a
   * property of the code rather than of whichever call site remembered it:
   *
   *   - The result is normalised to the boolean the drag-release hold reads: `true` only when
   *     the seek reported that it scrolled. A seek that returns nothing resolves `false`, so
   *     the hold clears the thumb at once when no landing will follow.
   *   - A seek that rejects, or that throws synchronously, also resolves `false`. Thus a caller
   *     that ignores the result can never leak an unhandled rejection, and a throw from inside
   *     `onJumpToSeq` can never escape a pointer handler or a settle timer.
   *
   * A failure is degraded, not hidden: `false` is the honest answer for the hold (no landing
   * will follow, so release the thumb now), and the warn keeps the cause visible instead of
   * turning a broken seek into a thumb that silently springs back.
   */
  const seekTo = (seq: bigint): Promise<boolean> => {
    const failed = (err: unknown): boolean => {
      console.warn('chat scroll rail: seek failed', { seq: seq.toString(), err })
      return false
    }
    try {
      return Promise.resolve(props.onJumpToSeq(seq)).then(scrolled => scrolled === true, failed)
    }
    catch (err) {
      return Promise.resolve(failed(err))
    }
  }

  // The seek the PRESS issued, kept for the length of one grab. A track or dot press jumps
  // immediately (see pressJumpAndScrub); a thumb grab jumps nowhere and leaves this undefined.
  // A release that never became a scrub hands this promise to the hold instead of seeking again.
  let pressSeek: Promise<boolean> | undefined

  // Land a rail release. A SCRUB seeks to the mapped seq while the hold keeps the thumb pinned at
  // the release fraction until that seek settles (see createDragReleaseHold.release). A TAP --
  // the pointer never left the engage slop -- seeks NOTHING: its press already jumped, so the
  // hold just pins the thumb on the pressed point until THAT jump lands. Firing a second seek
  // there would fetch the same page twice and, on a dot, land on the fraction under the finger
  // instead of the dot's own seq. The seq is resolved synchronously here so the seek thunk closes
  // over locals -- the release runs from a pointer handler, not a tracked scope.
  const releaseDrag = (fraction: number, engaged: boolean) => {
    if (!engaged) {
      const pressed = pressSeek
      dragHold.release(fraction, () => pressed)
      return
    }
    const seq = fractionToSeq(fraction, props.rail.minSeq, props.rail.maxSeq)
    if (seq === null) {
      dragHold.release(fraction, () => {})
      return
    }
    // Seek EAGERLY and hand the hold the promise, exactly as the tap branch hands it the press's.
    // The hold invokes its thunk synchronously anyway, so this changes no ordering the reader can
    // see -- and it keeps the reactive `props.onJumpToSeq` read out of a deferred callback, where
    // it would be a stale-closure hazard rather than the release-time read this needs.
    const jumped = seekTo(seq)
    dragHold.release(fraction, () => jumped)
  }

  /**
   * Begin a grab on the rail. Returns false ONLY when a rival drag already owns it -- the caller
   * must then drop its press action too, so a second finger can't fire a jump that races the live
   * drag. Returns true otherwise, INCLUDING when the browser refuses pointer capture: the press
   * action stands on its own, and must not depend on a capture that never happened.
   */
  const startDrag = (
    event: PointerEvent,
    rect: DOMRect,
    opts: { grabThumbTopPx: number, engageSlopPx: number },
  ): boolean => {
    const el = railEl
    // Ignore a second concurrent grab while a drag is live (begin() returns false) -- it would
    // add a rival listener set and orphan the first's on the rail. begin() also claims the
    // preview so a prior release's async settle can't clear THIS drag mid-way.
    if (!el || !dragHold.begin())
      return false
    pressSeek = undefined
    // A fresh grab is manual control: abandon a prior release's still-fetching out-of-window
    // seek so it can't land and yank the viewport while this drag scrubs (the drag scrolls
    // only programmatically, so the host's user-scroll seek-cancel never fires for it).
    props.onSeekInterrupt?.()
    // The drag lifecycle (pointer capture, rAF-throttled move, release/cancel) lives in the
    // extracted, unit-tested createThumbDrag controller. Accessors hand it the LIVE seq
    // range/geometry so a mid-drag live-tail advance is picked up on the next move.
    const handle = createThumbDrag({
      el,
      rect,
      // Where the thumb sits at grab. A thumb grab passes the thumb's own resting top, so the
      // drag holds the pointer's within-thumb offset (no jump-on-grab); a track or dot press
      // passes the pressed point, so the thumb centres there (see pressJumpAndScrub).
      grabThumbTopPx: opts.grabThumbTopPx,
      engageSlopPx: opts.engageSlopPx,
      minSeq: () => props.rail.minSeq,
      maxSeq: () => props.rail.maxSeq,
      windowFirstSeq: () => props.rail.windowFirstSeq,
      windowLastSeq: () => props.rail.windowLastSeq,
      // `prepared` / `thumbHeightNow` are zero-arg memo accessors -- pass them directly rather
      // than re-wrapping. (`minSeq` etc. must stay wrapped: they read reactive props lazily.)
      prepared,
      thumbHeightPx: thumbHeightNow,
      setDrag: dragHold.preview,
      previewScrollTo: top => props.previewScrollTo(top),
      // Out of the loaded window there is nothing to live-scroll to, so a settled thumb pulls the
      // page it needs -- otherwise a scrub across a long history moves the thumb and the dot
      // preview over a transcript that never follows.
      scrubSeek: (seq) => { void seekTo(seq) },
      // Abandon a seek this gesture issued and no longer points at, so its late fetch cannot
      // land under the reader (see ThumbDragDeps.abandonSeeks for each moment it fires). The
      // host's own user-scroll cancellation never covers these: the drag scrolls only
      // programmatically, so nothing it does looks like a gesture to the host.
      abandonSeeks: () => props.onSeekInterrupt?.(),
      // The grab became a scrub: the reader took manual control of the thumb, so this press's own
      // jump (issued a moment ago, possibly still fetching) is stale and must not land under them.
      // The settle seeks drive the view from here on.
      onEngage: () => {
        props.onSeekInterrupt?.()
        // The press started ON a dot, so the pointer channel holds that dot, and the captured drag
        // fires no pointerleave to release it. Drop it now that the gesture owns the card: the
        // scrub target describes the card from here, and without this the card springs back to the
        // pressed dot the moment the drag ends -- pointing at a dot the reader scrubbed away from.
        abandonDot()
      },
      onRelease: releaseDrag,
      // The pointer lifecycle ended (release/cancel/unmount): free the "drag active" guard so
      // the next grab can start. Fires before onRelease, so the release's preview-hold is intact.
      onEnd: () => {
        dragCleanup = undefined
        dragHold.end()
      },
    })
    // Teardown for an unmount that lands mid-drag (onCleanup below); idempotent, so a
    // normal release leaving this set is harmless.
    dragCleanup = () => handle.cancel()
    handle.start(event.pointerId, event.clientY)
    return true
  }

  /**
   * A press on a POSITION (the bare track, or a dot): jump there at once AND scrub on from there,
   * so one press serves both the tap a mouse expects and the drag a finger expects. The thumb
   * centres on `centreY` -- the reader pressed a position, so the thumb belongs under it -- and
   * the grab carries the engage slop, which is what keeps the jump the only seek of a plain tap.
   */
  const pressJumpAndScrub = (event: PointerEvent, rect: DOMRect, seq: bigint | null) => {
    // Centre the thumb on the PRESSED POINT -- for a dot press exactly as for a track press. The
    // jump still goes to the seq the caller resolved (a dot passes its OWN seq, not the wide
    // fraction under the finger), but the thumb belongs under the finger, because the drag keeps
    // whatever finger-to-thumb offset it starts with for its whole length. Anchoring a dot press
    // on the dot instead left that offset at up to half a coarse hit circle (12px), so the thumb,
    // the dot preview it resolves against, and the release landing all sat that far from the
    // point the reader was pointing at -- and only for a dot press, so one gesture had two feels.
    const grabThumbTopPx = (event.clientY - rect.top) - thumbHeightNow() / 2
    if (!startDrag(event, rect, { grabThumbTopPx, engageSlopPx: SCRUB_ENGAGE_SLOP_PX }))
      return
    if (seq !== null)
      pressSeek = seekTo(seq)
  }

  // "You can't click what you can't see." A press on a FADED rail or dot only REVEALS it
  // (via onActivity); the grab or jump is rejected, so the NEXT press -- rail now lit -- acts.
  // Read `faded` BEFORE onActivity: that call reopens the host's window synchronously, so idle()
  // would read false the instant after. onActivity fires on EVERY press, faded or lit: a lit
  // track-click jump still wants the fade tail that the re-arm buys. preventDefault is held back
  // on a faded press too, so a rejected click does not steal focus or start a native selection
  // drag on something the reader could not see. On a coarse pointer the idle rail is
  // pointer-events:none, so this never runs faded there.
  const revealIfFaded = () => {
    const faded = idle()
    props.onActivity?.()
    return faded
  }

  /**
   * The prelude EVERY press on the rail runs, whatever it lands on: reject a press the rail
   * cannot serve, reveal a faded rail instead of acting on it, and claim the press from the
   * browser. Returns the rail's rect for the caller's hit-test, or null when the press is
   * rejected. One home for the rule, so a guard added later cannot reach the track press and
   * miss the dot press.
   */
  const beginRailPress = (event: PointerEvent): DOMRect | null => {
    // Only the primary button opens a gesture, and this runs FIRST so a secondary press neither
    // relights the rail nor loses its default. Every press now CAPTURES the pointer and owns the
    // rail until its pointerup, and a context menu can swallow the pointerup of a right press --
    // which would leave the rail owned by a gesture that never ends. (lostpointercapture in
    // createThumbDrag recovers from the capture losses this cannot exclude.)
    if (event.button !== 0)
      return null
    // Whether or not this grab is accepted, reaching for the rail is intent to use it, so it
    // must keep the rail lit rather than fade under the cursor (see revealIfFaded).
    if (revealIfFaded())
      return null
    const el = railEl
    if (!el || hidden())
      return null
    // A LIT rail owns this press even when it rejects it below, so preventDefault comes BEFORE
    // the rival-drag test rather than after: without it, a second finger landing on a dot
    // focuses that dot's button, and the focus opens the dot's preview popover with no
    // pointerleave to ever close it -- which pins activeDot() non-null and holds the whole rail
    // lit for the rest of the session.
    event.preventDefault()
    // Ignore a second pointerdown while a drag is already live (dragCleanup is set from
    // startDrag until the pointer lifecycle ends). Without this, a second finger landing on the
    // TRACK or a DOT would fire a rival onJumpToSeq that races the in-progress drag's
    // live-scroll and its eventual release seek.
    if (dragCleanup)
      return null
    return el.getBoundingClientRect()
  }

  // One pointerdown handler for the rail: hit-test the thumb (grab it where it lies) vs the
  // track (jump to the pressed position, then scrub). Dots stop propagation so they never
  // reach here.
  const onRailPointerDown = (event: PointerEvent) => {
    const rect = beginRailPress(event)
    if (!rect)
      return
    const y = event.clientY - rect.top
    const tp = thumbPx()
    // On the thumb -> drag it from where it was grabbed (the pointer keeps its within-thumb
    // offset, so there is no jump-on-grab) and engage on the FIRST pixel of travel: a press on
    // the thumb has no competing tap meaning, and a scrollbar thumb that waited would feel stuck.
    if (tp && y >= tp.topPx && y <= tp.topPx + tp.heightPx) {
      startDrag(event, rect, { grabThumbTopPx: tp.topPx, engageSlopPx: 0 })
      return
    }
    // On the bare track -> jump to the pressed position and scrub on from there.
    pressJumpAndScrub(event, rect, railYToSeq(y, rect.height, thumbHeightNow(), props.rail))
  }

  const onDotPointerDown = (event: PointerEvent, d: DotCluster) => {
    // Prioritize the dot over the thumb/track underneath: a dot press is always handled HERE
    // and never also as a track click -- stopPropagation runs before the faded guard so a
    // rejected (faded) press still doesn't fall through to onRailPointerDown.
    event.stopPropagation()
    // The same prelude the track press runs -- the reveal-but-reject rule, the primary-button
    // test, and the rival-drag guard (see beginRailPress). The dot hover/focus path keeps the
    // rail visible on its own, so the faded rejection only bites the very first press onto a
    // faded strip; a coarse-pointer idle rail is pointer-events:none, so the dot buttons under
    // it are unreachable there.
    const rect = beginRailPress(event)
    if (!rect)
      return
    // Jump to the dot's OWN seq (not the fraction under the finger, which on a long history is
    // hundreds of seqs wide) and scrub on from the dot's position. On a coarse pointer each dot
    // carries a 24px hit circle, so on a marked conversation most of the rail is dot rather than
    // track -- without this, most of a finger's presses could jump but never scrub.
    pressJumpAndScrub(event, rect, d.seq)
  }

  // Keyboard activation of a focused dot. Pointer devices jump on pointerdown (above) and
  // never fire keydown; the keyboard never fires pointerdown -- so exactly one jump per
  // activation, and the button's inert native click can't double it.
  const onDotKeyDown = (event: KeyboardEvent, seq: bigint) => {
    if (event.key !== 'Enter' && event.key !== ' ')
      return
    // preventDefault stops Space from also page-scrolling.
    event.preventDefault()
    // The rival-drag guard both press paths carry (see beginRailPress). A dot keeps keyboard
    // focus while a pointer scrubs the rail, so without this an Enter mid-scrub fires exactly
    // the rival jump those guards exist to stop.
    if (hidden() || dragCleanup)
      return
    // Through seekTo like every other seek, so this path cannot leak an unhandled rejection.
    void seekTo(seq)
  }

  // The rail overlays the strip where the native scrollbar used to be, and its ancestors
  // are overflow:hidden, so a wheel over it would otherwise scroll nothing (a dead zone).
  // Forward the delta to the chat container as a genuine user scroll -- exactly what
  // wheeling over the native scrollbar did before it was hidden.
  //
  // While idle on a COARSE pointer the rail is pointer-events:none (see railIdle), which
  // makes this handler unreachable -- but no dead zone comes back: the wheel then
  // hit-tests to messageList itself, which the rail OVERLAYS as a sibling rather than
  // nesting inside, and that element is the real scroller.
  const onRailWheel = (event: WheelEvent) => {
    const el = props.scrollEl
    if (!el)
      return
    // A wheel over the rail is genuine scroll intent, so relight the rail directly. The
    // re-dispatched wheel below also relights it indirectly (via the scroller's passive
    // listener -> noteScrollInput), but counting on that chain couples the relight to a
    // listener registration that a future refactor could move to capture phase or drop.
    props.onActivity?.()
    el.dispatchEvent(new WheelEvent('wheel', {
      bubbles: true,
      cancelable: false,
      ctrlKey: event.ctrlKey,
      shiftKey: event.shiftKey,
      altKey: event.altKey,
      metaKey: event.metaKey,
      deltaX: event.deltaX,
      deltaY: event.deltaY,
      deltaZ: event.deltaZ,
      deltaMode: event.deltaMode,
    }))
    // deltaMode: 0=pixels (trackpads, most mice), 1=lines, 2=pages -- normalize to pixels.
    const factor = event.deltaMode === 1 ? WHEEL_LINE_PX : event.deltaMode === 2 ? el.clientHeight : 1
    el.scrollTop = clampScrollTop(el, el.scrollTop + event.deltaY * factor)
    event.preventDefault()
  }

  onCleanup(() => {
    cancelActiveDrag()
    disconnectRail()
  })

  return (
    <Show when={!hidden()}>
      <div
        ref={setRailRef}
        class={`${styles.rail}${idle() ? ` ${styles.railIdle}` : ''}`}
        data-testid="chat-scroll-rail"
        onPointerDown={onRailPointerDown}
        // A cursor on the strip HOLDS the rail lit for as long as it rests there (see idle()),
        // and re-arms the host's window on the way in so leaving fades it out instead of
        // snapping it dark. Enter/leave rather than pointermove, because a parked cursor sends
        // no moves -- which is exactly how the rail used to disappear from under one.
        onPointerEnter={(event: PointerEvent) => {
          if (event.pointerType !== 'mouse')
            return
          setHovered(true)
          props.onActivity?.()
        }}
        // Mouse only, to match the enter handler. A touch never SETS the hover, so an unfiltered
        // leave could only clear one that a mouse holds: on a hybrid device a tap elsewhere would
        // then drop the rail out from under a parked cursor.
        onPointerLeave={(event: PointerEvent) => {
          if (event.pointerType !== 'mouse')
            return
          setHovered(false)
        }}
        // Two jobs, with different audiences.
        //
        // The relight (every pointer type) reopens the host's window from a move onto a faded
        // strip. A captured thumb drag's pointermove retargets here, so the scroll container
        // never sees it. Only while idle: once lit, further moves would replace a timer that the
        // next move replaces again, dozens of times a second through a drag.
        //
        // The hold (a MOUSE that is not dragging) is the enter handler's other half. A rail that
        // mounts or un-hides UNDER a stationary cursor receives no pointerenter, because nothing
        // entered, so its first move has to establish the hover -- otherwise a cursor that then
        // stops moving loses the rail to the host's timer, which is the case the hold exists for.
        // Skipped during a drag, because capture retargets moves from far outside the strip;
        // without capture, a move on this element means the pointer IS on it, so no hit test is
        // needed. Read idle() BEFORE the set, or the set hides the falling edge from the relight.
        onPointerMove={(event: PointerEvent) => {
          const wasIdle = idle()
          if (drag() === null && event.pointerType === 'mouse')
            setHovered(true)
          if (wasIdle)
            props.onActivity?.()
        }}
        // focusin bubbles from a dot button, so tabbing through the dots keeps the rail
        // lit and tabbing away gets a fade tail instead of an instant blackout.
        onFocusIn={() => props.onActivity?.()}
        onWheel={onRailWheel}
      >
        {/* Inset the track to the thumb-CENTRE travel range so its ends are where the thumb
            centre can reach (dots live on this same axis), not the thumb's top/bottom edge. */}
        <div class={styles.track} style={{ top: `${thumbHeightNow() / 2}px`, bottom: `${thumbHeightNow() / 2}px` }} />
        <Show when={thumbPx()}>
          {tp => (
            <div
              class={`${styles.thumb} ${drag() !== null ? styles.thumbDragging : ''}`}
              data-testid="chat-scroll-rail-thumb"
              style={{ top: `${tp().topPx}px`, height: `${tp().heightPx}px` }}
            />
          )}
        </Show>
        <For each={dots()}>
          {d => (
            <button
              type="button"
              class={`${styles.dot} ${d.count > 1 ? styles.dotCluster : ''}`}
              data-testid="chat-scroll-rail-dot"
              data-seq={d.seq.toString()}
              data-mark-type={d.type}
              data-count={d.count}
              aria-label={dotLabel(d)}
              // The open card describes the dot it belongs to, so a screen reader reads the
              // message preview and not just "Your message" -- the same shape Tooltip.tsx uses (a
              // stable id on the surface, aria-describedby on the target only while it is up).
              // Matched on the SEQ, because a re-anchor hands over a fresh cluster for the same
              // dot, and only for the dot the card actually describes: a scrub target outranks a
              // focused dot, so the focused one must not claim a card that moved elsewhere.
              aria-describedby={activeDot()?.seq === d.seq ? DOT_PREVIEW_CARD_ID : undefined}
              style={{ top: `${d.topPx}px` }}
              // Open the shared card immediately on hover AND keyboard focus (the warm effect on
              // activeDot fills it in). A pointer LEAVING only starts the close delay, which is
              // the window the reader crosses to reach the card; another dot opened in the
              // meantime cancels it. Focus is its own channel with no delay, and it TAKES the card
              // from the pointer, so tabbing along the dots is not masked by a parked cursor. See
              // POINTER_CLOSE_DELAY_MS.
              //
              // Both pointer handlers stand down while a press inside the card is live. The card
              // sits a gutter away from these dots, so selecting to the end of a line drags the
              // pointer across them; re-targeting the card there would swap its body under the
              // reader's own selection and collapse it.
              onPointerEnter={() => {
                if (!cardPressed)
                  openDot(d)
              }}
              onPointerLeave={() => {
                if (!cardPressed)
                  closeSoon()
              }}
              onFocus={() => focusDot(d)}
              onBlur={() => blurDot()}
              onPointerDown={e => onDotPointerDown(e, d)}
              onKeyDown={e => onDotKeyDown(e, d.seq)}
            />
          )}
        </For>
        {/* ONE preview card for the active dot (hover/focus OR scrub) -- never two. */}
        <Show when={activeDot()}>
          {d => (
            <DotPreviewCard
              topPx={cardTopPx()}
              dot={d()}
              previewFor={seq => props.previewFor?.(seq)}
              // The card holds ITSELF open while the reader is on it, and re-declares the dot it
              // shows -- so a card that a scrub opened stays when the scrub ends.
              onHoldStart={() => openDot(d())}
              onHoldEnd={closeSoon}
              onPressChange={(down) => { cardPressed = down }}
              onHeightChange={setCardHeightPx}
            />
          )}
        </Show>
      </div>
    </Show>
  )
}
