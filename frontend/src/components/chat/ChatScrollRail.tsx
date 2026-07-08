import type { DotCluster } from './chatRailPolicy'
import type { PreparedGeometry } from './chatScrollRailGeometry'
import type { VirtualItem } from './useChatVirtualizer'
import type { ChatRailData } from '~/stores/chatMessageMarks'
import { createEffect, createMemo, createSignal, For, onCleanup, Show } from 'solid-js'
import { createDotPreview } from './chatDotPreview'
import { clusterMarks, dotClustersEqual } from './chatRailPolicy'
import { clampScrollTop } from './chatScrollGeometry'
import * as styles from './ChatScrollRail.css'
import { createThumbDrag } from './chatScrollRailDrag'
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
import { dotLabel, DotPreview } from './ChatScrollRailPreview'

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
//   - ChatScrollRailPreview  -- the dot hover/scrub tooltip presentation.
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
  hasMoreOlder: boolean
  hasMoreNewer: boolean
  /**
   * Seek to a seq (dot click, track click, thumb release). May resolve to whether the seek
   * actually moved the view -- the thumb-drag release awaits this to time its preview
   * hand-off; the dot/track callers ignore the result.
   */
  onJumpToSeq: (seq: bigint) => void | Promise<boolean>
  /** Guard-marked programmatic scroll write for in-window thumb-drag live-scrolling. */
  previewScrollTo: (top: number) => void
  /**
   * A fresh thumb-drag grab began. Lets the host abandon an in-flight out-of-window seek
   * (from a PRIOR release) so its late fetch can't yank the viewport mid-scrub -- the
   * drag's own scroll writes are programmatic and never trip the host's user-scroll
   * cancellation. Optional; the dot/track jump paths supersede the seek on their own.
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
  // every dot's tooltip (3 effects + 5 listeners each) for no visual change.
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
  const { activeDot, popoverTopPx, setHoverDot } = createDotPreview({
    dots,
    drag,
    railHeight,
    thumbHeightPx: thumbHeightNow,
    warmPreview: seq => props.warmPreview?.(seq),
  })

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
    setHoverDot(null)
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

  // Land a thumb release: seek to the mapped seq while the hold keeps the thumb pinned at the
  // release fraction until the seek settles (see createDragReleaseHold.release). The seq and
  // the jump callback are resolved synchronously here so the seek thunk closes over locals,
  // not reactive props -- the release runs from a pointer handler, not a tracked scope.
  const releaseDragAt = (fraction: number) => {
    const seq = fractionToSeq(fraction, props.rail.minSeq, props.rail.maxSeq)
    if (seq === null) {
      dragHold.release(fraction, () => {})
      return
    }
    const jump = props.onJumpToSeq
    dragHold.release(fraction, () => jump(seq))
  }

  const startDrag = (event: PointerEvent, rect: DOMRect, thumbTopPx: number) => {
    const el = railEl
    // Ignore a second concurrent grab while a drag is live (begin() returns false) -- it would
    // add a rival listener set and orphan the first's on the rail. begin() also claims the
    // preview so a prior release's async settle can't clear THIS drag mid-way.
    if (!el || !dragHold.begin())
      return
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
      // The thumb's resting top at grab, so the drag holds the pointer's within-thumb offset
      // (no jump-on-grab) rather than recentering the thumb on the cursor.
      grabThumbTopPx: thumbTopPx,
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
      onRelease: releaseDragAt,
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
  }

  // One pointerdown handler for the rail: hit-test the thumb (start a drag) vs the track
  // (jump to the clicked position). Dots stop propagation so they never reach here.
  const onRailPointerDown = (event: PointerEvent) => {
    const el = railEl
    // Ignore a second pointerdown while a thumb-drag is already live (dragCleanup is set from
    // startDrag until the pointer lifecycle ends). Without this, a second finger landing on the
    // TRACK -- the thumb branch is already guarded by dragHold.begin() -- would fire a rival
    // onJumpToSeq that races the in-progress drag's live-scroll and its eventual release seek.
    if (!el || hidden() || dragCleanup)
      return
    event.preventDefault()
    const rect = el.getBoundingClientRect()
    const y = event.clientY - rect.top
    const tp = thumbPx()
    // On the thumb -> drag (holding the pointer's within-thumb offset, no jump-on-grab); on the
    // bare track -> jump to the clicked position.
    if (tp && y >= tp.topPx && y <= tp.topPx + tp.heightPx) {
      startDrag(event, rect, tp.topPx)
    }
    else {
      const seq = railYToSeq(y, rect.height, thumbHeightNow(), props.rail)
      if (seq !== null) {
        props.onJumpToSeq(seq)
      }
    }
  }

  const onDotPointerDown = (event: PointerEvent, seq: bigint) => {
    // Prioritize the dot over the thumb/track underneath: jump to its message.
    event.stopPropagation()
    event.preventDefault()
    props.onJumpToSeq(seq)
  }

  // Keyboard activation of a focused dot. Pointer devices jump on pointerdown (above) and
  // never fire keydown; the keyboard never fires pointerdown -- so exactly one jump per
  // activation, and the button's inert native click can't double it. preventDefault stops
  // Space from also page-scrolling.
  const onDotKeyDown = (event: KeyboardEvent, seq: bigint) => {
    if (event.key === 'Enter' || event.key === ' ') {
      event.preventDefault()
      props.onJumpToSeq(seq)
    }
  }

  // The rail overlays the strip where the native scrollbar used to be, and its ancestors
  // are overflow:hidden, so a wheel over it would otherwise scroll nothing (a dead zone).
  // Forward the delta to the chat container as a genuine user scroll -- exactly what
  // wheeling over the native scrollbar did before it was hidden.
  const onRailWheel = (event: WheelEvent) => {
    const el = props.scrollEl
    if (!el)
      return
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
        class={styles.rail}
        data-testid="chat-scroll-rail"
        onPointerDown={onRailPointerDown}
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
              style={{ top: `${d.topPx}px` }}
              // Set/clear the active dot so the shared popover opens immediately on hover AND
              // keyboard focus (the warm effect on activeDot fills it in).
              onPointerEnter={() => setHoverDot(d)}
              onPointerLeave={() => setHoverDot(null)}
              onFocus={() => setHoverDot(d)}
              onBlur={() => setHoverDot(null)}
              onPointerDown={e => onDotPointerDown(e, d.seq)}
              onKeyDown={e => onDotKeyDown(e, d.seq)}
            />
          )}
        </For>
        {/* ONE preview popover for the active dot (hover/focus OR scrub) -- never two. */}
        <Show when={activeDot()}>
          {d => (
            <div class={styles.previewPopover} data-testid="chat-scroll-rail-preview" style={{ top: `${popoverTopPx()}px` }}>
              <DotPreview previewFor={() => props.previewFor?.(d().seq)} markType={d().type} count={d().count} />
            </div>
          )}
        </Show>
      </div>
    </Show>
  )
}
