import type { Accessor } from 'solid-js'
import type { ChatScrollController } from './chatScrollPreserve'
import type { UseChatVirtualizerResult, ViewportLead } from './useChatVirtualizer'
import type { AgentChatMessage, AgentStatus } from '~/generated/proto/leapmux/v1/agent_pb'
import type { SavedViewportScroll, ScrollAnchor } from '~/stores/chatTypes'
import { createEffect, createMemo, createSignal, on, onCleanup, onMount, untrack } from 'solid-js'
import { trailingDebounce } from '~/lib/debounce'
import { monotonicNow } from '~/lib/monotonicNow'
import { createRafCoalescer } from '~/lib/rafCoalesce'
import { shallowEqualArrays } from '~/lib/shallowEqual'
import { firstMessageSeq, lastMessageSeq } from '~/stores/chatMessageOrder'
import { createScrollBufferFiller } from './chatScrollBufferFiller'
import { clampScrollTop, distFromBottom, EDGE_INTENT_TOLERANCE_PX, FOLLOW_TOLERANCE_PX, inferScrollDirection, maxScrollTopOf, readLogicalScrollTop, warnSlowScrollPhase } from './chatScrollGeometry'
import { createHmrSettleWindow } from './chatScrollHmrSettleWindow'
import { createScrollInput } from './chatScrollInput'
import { createScrollInputActivity } from './chatScrollInputActivity'
import { createOverscrollDrag } from './chatScrollOverscrollDrag'
import { createChatPaginationPolicy } from './chatScrollPaginationPolicy'
import { registerChatScrollController } from './chatScrollPreserve'
import { createScrollRest } from './chatScrollRest'
import { createSeenScrollPosition } from './chatScrollSeenPosition'
import { createStaleNativeScrollTranslator } from './chatScrollStaleNative'
import { createStickyBottom } from './chatScrollSticky'
import { createChatTailFollower } from './chatScrollTailFollower'
import { createScrollVelocity, VELOCITY_IDLE_MS } from './chatScrollVelocity'
import { createViewportRestore } from './chatScrollViewportRestore'
import { createChatSeek } from './chatSeek'

/** Saved/queried scroll position — anchored to a message, see SavedViewportScroll. */
export type ChatScrollState = SavedViewportScroll

/**
 * The geometry surface useChatScroll needs from the virtualizer. The scroll hook decides
 * the scroll position; the virtualizer owns the offset map and the layout shift that
 * keeps the content under the reader in place across a geometry change (see
 * chatLayoutShift). Declared as a `Pick` so it stays provably a subset of the
 * virtualizer's surface — a signature change there breaks here at compile time instead
 * of silently diverging. Every member is REQUIRED: the testkit supplies no-op defaults.
 */
export type ChatScrollVirtualizer = Pick<
  UseChatVirtualizerResult,
  'totalHeight' | 'updateViewport' | 'anchorAt' | 'scrollTopForAnchor' | 'scrollTopNearAnchor' | 'geometryVersion'
  | 'anchorAtContent' | 'contentTopForAnchor'
  | 'layoutShift' | 'rebase' | 'contentTopOf' | 'scrollTopOf' | 'placementScrollTopOf' | 'setFastScrollActive'
  | 'hasMeasuredHeight'
>

/**
 * The scroll primitives of useChatScroll, and the hook actions that two or more of its
 * extracted helpers call, as one value. The helpers are createStickyBottom,
 * createChatTailFollower, createScrollInput, createViewportRestore, and createChatSeek. The
 * hook builds the context ONCE and passes it to each helper. Each helper except
 * createStickyBottom also takes an `extras` object for the other deps that only it reads.
 * stickToBottom and forceScrollToBottom are not members: the hook builds them from this
 * context, so a helper that calls them takes them in its `extras` object.
 */
export interface ScrollContext {
  /** The list element while it is attached, shown or hidden. */
  getEl: () => HTMLDivElement | undefined
  /**
   * The list element while it is attached and shown, else undefined. A hidden pane
   * (`display: none`) reads clientHeight and scrollTop as 0, so a position read from it
   * means nothing, and a write to it does not move the view.
   */
  getVisibleEl: () => HTMLDivElement | undefined
  virt: ChatScrollVirtualizer
  /**
   * Follow mode: the reader requested the tail, the tail is loaded, and the view did not
   * move up with no `scroll` event. Every reader of follow mode reads it: the layout shift
   * (it holds the bottom of the content while this is true), the two automatic sticks, and
   * the suppression of the older pre-fetch.
   *
   * A touch or scrollbar drag moves the view before its first `scroll` event arrives. While
   * the view sits more than FOLLOW_TOLERANCE_PX above the position that the scroll handler
   * or an own write saw last, and the scroll range did not force that move (see
   * SeenScrollPosition.hasUnseenUpwardMove), follow mode does not apply: the layout shift
   * holds the rows under the reader, and an automatic stick writes nothing. The event of the
   * move then ends the request (leave by position). An explicit command (End, the
   * scroll-to-bottom button, a send, a completed jump to the latest page) still sticks.
   * With no shown list element, it is the request at the live tail.
   */
  isFollowing: () => boolean
  /**
   * Whether the reader's request for the live tail stands: the `following` flag of the
   * hook. It stays set while the newer pages are on their way, so it can hold while
   * isFollowing() is false.
   */
  followRequested: () => boolean
  /** Whether newer messages exist beyond the loaded window: the window is paged away from the live tail. */
  hasNewerMessages: () => boolean
  /**
   * Record a clamp of the browser to a scroll range that shrank: a content shrink clamps
   * scrollTop to the new maximum with no move of the reader. A clamp that this call or a
   * position check recorded stays forced after the content grows again, so isFollowing()
   * does not read it as an unseen move of the reader. A clamp that neither recorded before
   * the content grew past it again reads as such a move. Does nothing for an absent or
   * hidden element.
   */
  noteScrollRange: () => void
  /** True while a scroll animation is running. */
  isAnimating: () => boolean
  /** Record the reader's request for the live tail. */
  followTail: () => void
  /** End the reader's request for the live tail, and record the anchor of the view. */
  leaveFollow: () => void
  /**
   * The position check. It publishes the atBottom signal, enters follow mode at the bottom
   * of the live tail, and records the anchor of a reader who does not follow. It never ends
   * follow mode: it also runs after an own write, and an own write is no move of the
   * reader. Each own write runs it after the write, each step of the scroll animation
   * included. Does nothing for an absent or hidden element.
   */
  checkPosition: () => void
  /** Stop the running scroll animation, if one runs. */
  cancelScrollAnimation: () => void
  /**
   * End the pause of the buffer fill that a forced stop (a press, a touch, or a wheel event
   * that cancels momentum) or an HMR remount started. A jump, a landing, or a page is a
   * command of the reader, as a scroll is, but its `scroll` event is an own write, and only
   * a move of the reader ends the pause.
   */
  retakeScrollControl: () => void
  /** Drop the per-window state of the buffer filler, for a jump that replaces the message window. */
  rearmScrollBufferFiller: () => void
  /** Remove the layout shift with no visible change (see LayoutShiftSurface.rebase). */
  rebase: () => void
  /**
   * Place the view: rebase (a `scrollTop` write when the shift is SCROLL_ROUNDING_MAX_PX
   * or more), then call `resolve` for the scroll coordinate, then write it and record the
   * write. A placement that writes is a command, so it ends the request for the tail.
   * Returns `null` when the element is absent or hidden (nothing is written) or when
   * `resolve` returned `null` (only the rebase wrote), and otherwise whether `scrollTop`
   * changed.
   */
  placeAt: (resolve: () => number | null) => boolean | null
  /**
   * Place the view at `anchor` (see placeAt): at its row, or with `nearest`, at the nearest
   * surviving row by seq when its row is gone. When the row has only an estimated height,
   * the view lands again on the anchor once the row measures (see releaseAnchorHold).
   */
  placeAtAnchor: (anchor: ScrollAnchor, nearest: boolean) => boolean | null
  /**
   * Write `top` to `scrollTop`, and record the position after the write, so the `scroll`
   * event of the write reads as no move, whatever distance it moved. Every own write goes
   * through it, except the rebase and the translation of a stale native event, which record
   * their writes in their own way. The record also ends a held anchor (see placeAtAnchor)
   * and runs the position check. With `refresh`, it then refreshes the rendered rows. A
   * ResizeObserver delivery must not refresh them: a row that mounts there re-enters the
   * delivery. Does nothing for an absent or hidden element.
   *
   * It does not clamp `top`. A write of `scrollHeight` relies on the browser, which clamps
   * it to the maximum position, and that maximum can be fractional.
   */
  writeOwnScrollTop: (top: number, opts: { refresh: boolean }) => void
  /**
   * Record the current position as writeOwnScrollTop records its write, and refresh the
   * rendered rows: for a position that changed with no own write, as after a resize.
   */
  recordPosition: () => void
}

/**
 * True ONLY for the synchronous burst of component remounts Vite triggers right after an
 * HMR module swap -- the one case that opens the settle window of the buffer fill (see
 * createHmrSettleWindow). It stays here, because it marks the swap of this module. Vite runs
 * `dispose` on the OLD module instance before evaluating the NEW one, persisting a marker
 * across the swap via `import.meta.hot.data`; a cold page load / refresh evaluates this
 * module fresh with empty `data`, so this stays false and the cold-load pre-fetch runs
 * immediately (matching prod). That is the fix for the half-empty-on-refresh bug the old
 * blanket `import.meta.env.DEV` gate caused: it armed the settle on EVERY mount, and since
 * the timer that clears it is a non-reactive write that never re-kicks fill(), a
 * hidden-heavy newest page (its few visible rows fit the viewport, so nothing scrolls)
 * stayed stranded half-empty until a scroll or a new message. Cleared on the next
 * macrotask so a LATER mount (a newly-opened tab on the same module instance) isn't
 * mistaken for an HMR remount. `import.meta.hot` is undefined in prod (false there) and
 * never HMR-disposes under vitest (false in tests), so this is dev-server-only.
 */
let hmrRemount = !!import.meta.hot?.data?.remountFromHmr
if (import.meta.hot) {
  import.meta.hot.dispose((data) => {
    data.remountFromHmr = true
  })
  if (hmrRemount) {
    setTimeout(() => {
      hmrRemount = false
    }, 0)
  }
}
/**
 * Scroll speed (px/ms) at or above which a move of the reader starts a fast scroll:
 * rows that enter the window then mount as skeletons (see the virtualizer's
 * fastScrollActive), and no pivot element is recorded until the scroll position holds
 * for a frame (see createLayoutPivot). A fling fires events tens of px apart every ~16ms
 * (well above 1 px/ms); a deliberate scroll creeps a few px per event.
 */
const FAST_SCROLL_PX_PER_MS = 1
/**
 * Render-ahead look-ahead window (ms) for the fling overscan: the rendered slice is
 * extended in the scroll direction by `velocity * this`, covering the distance the
 * viewport travels between the compositor scrolling a frame and the next range
 * update. Sized to a few frames of lag (scroll events can coalesce under load) while
 * keeping the cap small enough that an extreme flick cannot mount dozens of rows in
 * one synchronous scroll commit. The same lead widens the margin of a rebase near the
 * reachable top (see needsRebaseDuringScroll).
 *
 * NO-SKIP INVARIANT -- a fast fling STALLS, it never SKIPS a message. Three
 * mechanisms together guarantee every message in a fling's path is shown:
 *  1. The scrollable area IS the loaded window (the spacer is sized to the loaded
 *     rows), so the browser clamps scrollTop at the loaded edge -- a fling physically
 *     cannot scroll INTO not-yet-loaded history.
 *  2. The buffer filler (createScrollBufferFiller) grows that loaded window ahead
 *     of the viewport as it nears an edge, so the edge keeps moving outward.
 *  3. This render-ahead paints the loaded rows the fling is ABOUT to reach, so the
 *     1-frame compositor lag can't flash an unrendered (blank) loaded row.
 * Net: a fling that outruns the FETCH buffer stalls at the loading edge until the
 * next page lands -- it cannot jump past unloaded or unrendered content. This is why
 * we do NOT cap the native scroll velocity: the loaded-window scroll bound already
 * turns any overrun into a stall, not a skip, and capping momentum would just make
 * fast scrolling feel broken. Tune the buffer (SCROLL_BUFFER_SCREENS) to make the
 * stall rarer, never a velocity clamp.
 */
const FLING_OVERSCAN_LOOKAHEAD_MS = 100
/**
 * Fling render-ahead cap, in SCREENS of the live pane (k x clientHeight), bounded by a
 * hard px ceiling. The cap was previously an ABSOLUTE px constant re-tuned against one
 * ~733px pane (4000 -> 1200 -> 1800): 4000px mounted 30+ rows in one viewport update
 * (30ms+ scroll commits); 1200px was too tight under coalesced momentum events (a
 * plausible blank-spacer gap when the compositor advanced farther before the next
 * main-thread range commit); 1800px -- about 2.5 screens there -- worked. But an
 * absolute value degrades toward ONE screen of coverage as panes get taller, re-entering
 * the blank-gap regime the raises were fixing. Deriving from clientHeight keeps the
 * SCREENS of forward coverage the tuning always reasoned in (2.5 reproduces the tuned
 * 1800px on the calibration pane); the hard ceiling bounds the row-mount burst a very
 * tall pane could otherwise request in a single commit.
 */
export const FLING_OVERSCAN_SCREENS = 2.5
export const FLING_OVERSCAN_HARD_CAP_PX = 3600
/** The fling render-ahead cap (px) for a pane of the given height. */
export function flingOverscanCapPx(clientHeight: number): number {
  return Math.min(FLING_OVERSCAN_SCREENS * clientHeight, FLING_OVERSCAN_HARD_CAP_PX)
}
/**
 * The render-ahead lead (px) of a scroll at `speed` (px/ms) in a pane of the given height:
 * the travel in FLING_OVERSCAN_LOOKAHEAD_MS, capped by flingOverscanCapPx.
 */
function renderAheadLeadPx(clientHeight: number, speed: number): number {
  return Math.min(flingOverscanCapPx(clientHeight), speed * FLING_OVERSCAN_LOOKAHEAD_MS)
}
/**
 * Screens of VISIBLE content the buffer filler keeps loaded beyond the viewport in
 * each direction (see createScrollBufferFiller). Big enough that a deliberate scroll
 * never reaches a short edge before the next page lands; the window's raw CEILING
 * (chat.store) bounds how many hidden rows this can pull in to reach it.
 */
const SCROLL_BUFFER_SCREENS = 3
/**
 * Frame budget for the animated scroll-to-bottom. The step halves the remaining
 * distance each frame, so a normal jump converges in well under this; the cap
 * only bites when the target keeps growing (active streaming), at which point we
 * hand off to sticky-bottom (~1s at 60fps) instead of chasing forever.
 */
const SCROLL_TO_BOTTOM_MAX_FRAMES = 60

/**
 * Rows the oldest-end trim must KEEP, counted from a given keep-from `anchor` (and its
 * resolved index `anchorIdx` in `msgs`, -1 when absent) down to the tail:
 *  - no anchor: 0, so the store applies the normal base cap. The sole production caller
 *    (the auto-scroll trim) never takes this path -- it derives a buffer-top anchor from
 *    the viewport position and maps an UNRESOLVABLE one to the whole window itself -- so
 *    this is just the neutral default for a null keep-from.
 *  - anchor set but UNRESOLVABLE in `msgs` (displaced / not present): the whole window,
 *    so no still-visible row is trimmed (the store clamps to the hard ceiling so memory
 *    stays bounded).
 *  - anchor resolvable: the rows from the anchor down to the tail.
 */
export function computeKeepNewest(msgs: AgentChatMessage[], anchor: ScrollAnchor | null, anchorIdx: number): number {
  if (!anchor)
    return 0
  if (anchorIdx < 0)
    return msgs.length
  return msgs.length - anchorIdx
}

/**
 * The oldest-end trim's keep-newest count, derived from the live buffer geometry so a
 * tail append can't reap content the buffer filler surfaced ABOVE the viewport.
 *
 * The trim must KEEP the viewport PLUS the older pre-fetch buffer the filler maintains
 * above it -- otherwise every tail append reaps the rows the filler just fetched (or the
 * hidden-page auto-advance just surfaced) and the filler refetches them: a trim/refetch
 * loop in a hidden-heavy stream. Hidden rows have zero height, so `viewportContentTop` (the
 * content above the viewport top, in content coordinates) measures the VISIBLE content
 * above the viewport top; the keep-from anchor sits one buffer (`bufferTargetPx`) above
 * it. The result is a SUPERSET of the viewport span -- it can only keep MORE rows, never
 * reaping a visible one -- and the store clamps it to [base, ceiling], so a NORMAL
 * followed tail still trims to the lean base while a hidden-heavy window holds what the
 * filler surfaced up to the ceiling.
 *
 * Pure given its inputs: the caller passes live DOM reads and an (untracked) anchor
 * resolver, so the branch logic is unit-testable in isolation. `anchorAt(bufTop)`
 * returns the row at the content coordinate `bufTop`, or null when the offset map can't
 * locate it.
 */
export function computeBufferAwareKeepNewest(
  msgs: AgentChatMessage[],
  viewportContentTop: number,
  clientHeight: number,
  bufferTargetPx: number,
  anchorAt: (bufTop: number) => ScrollAnchor | null,
): number {
  // Hidden/inactive tab: the viewport and buffer can't be measured. Apply the lean base
  // cap (keepNewest 0); a later restore re-evaluates when the tab becomes visible.
  if (clientHeight === 0)
    return 0
  const bufTop = viewportContentTop - bufferTargetPx
  // Less than a buffer of visible content sits above the viewport top (a hidden-heavy /
  // all-hidden window, or the viewport is near the top): keep the WHOLE window so the
  // trim never reaps content the filler surfaced. The store's base clamp still bounds a
  // small window; the ceiling bounds a grown one.
  if (bufTop <= 0)
    return msgs.length
  // A resolved buffer-top anchor delegates to computeKeepNewest (which itself keeps the
  // whole window if the row is no longer in msgs). An UNRESOLVABLE buffer top (anchorAt
  // returned null) keeps the whole window too -- never reap when we can't locate the
  // buffer's top edge.
  const bufTopAnchor = anchorAt(bufTop)
  return bufTopAnchor
    ? computeKeepNewest(msgs, bufTopAnchor, msgs.findIndex(m => m.id === bufTopAnchor.id))
    : msgs.length
}

/**
 * The pagination action callbacks the host (TileRenderer -> ChatView) wires to the
 * store's windowing methods. Shared by `ChatViewProps` and `UseChatScrollOptions`
 * so the five-callback contract is declared once and can't drift between the two
 * layers. The boolean SIGNALS (hasOlder/hasNewer/fetching*) are deliberately NOT
 * shared: ChatView passes them as plain values while the hook takes accessors, so
 * their wrapper types genuinely differ.
 */
export interface PaginationCallbacks {
  /** Called when the user scrolls near the top and older messages should be loaded. */
  onLoadOlderMessages?: () => void
  /** Called when the user scrolls near the bottom and newer messages should be loaded. */
  onLoadNewerMessages?: () => void
  /** Re-fetch the latest page and snap to the live tail. Returns when caught up. */
  onJumpToLatest?: () => Promise<void> | void
  /** Re-fetch the earliest page and snap to the first message (Home). */
  onJumpToOldest?: () => Promise<void> | void
  /**
   * Replace the window with a page centered on `seq` (the scroll rail's out-of-window
   * seek). Resolves once the window swapped, so the hook can land on the row. `signal`
   * abandons the fetch: the seek that issued it gave up (see ChatSeek.cancelPendingSeek),
   * so the page must not arrive and swap the window under a reader who moved on.
   */
  onJumpToSeq?: (seq: bigint, signal?: AbortSignal) => Promise<void> | void
  /**
   * Cap the in-memory window on a live-tail append. `minKeepNewest` is the count of
   * newest messages the trim must retain to leave a scrolled-up reader's viewport
   * intact (the anchored row down to the tail); 0 while following the bottom. The
   * store clamps it to [cap, ceiling] -- see trimOldestToViewport.
   */
  onTrimOldMessages?: (minKeepNewest: number) => void
}

export interface UseChatScrollOptions extends PaginationCallbacks {
  messages: Accessor<AgentChatMessage[]>
  messageVersion?: Accessor<number | undefined>
  agentWorking?: Accessor<boolean | undefined>
  /**
   * Agent lifecycle status. Tracked in the auto-scroll signature so a
   * STARTING transition (e.g. /clear's "Restarting <Provider>…" banner
   * appearing below the message list) scrolls the new banner into view.
   * Without this, the banner is added below the visible viewport and the
   * user has to scroll manually to see it.
   */
  agentStatus?: Accessor<AgentStatus | undefined>
  hasOlderMessages?: Accessor<boolean | undefined>
  fetchingOlder?: Accessor<boolean | undefined>
  /** Whether newer messages exist beyond the in-memory window (windowed away from tail). */
  hasNewerMessages?: Accessor<boolean | undefined>
  /** Whether a forward fetch / jump-to-latest is in progress. */
  fetchingNewer?: Accessor<boolean | undefined>
  /** Whether the window is within a page of the raw ceiling (can't grow); see chatStore.atWindowCeiling / createScrollBufferFiller's atCeiling dep. */
  atWindowCeiling?: Accessor<boolean | undefined>
  /** Virtualizer geometry surface. Required for virtualized rendering. */
  virtualizer: ChatScrollVirtualizer
  savedViewportScroll?: Accessor<ChatScrollState | undefined>
  onClearSavedViewportScroll?: () => void
  /**
   * Called on unmount with the final viewport scroll state so the host can persist it
   * for the NEXT mount of the same agent -- a tile split/merge or workspace switch
   * recreates ChatView over a still-populated store, and the fresh instance restores
   * through savedViewportScroll (restoreOnMount for a visible remount, the RO
   * hidden->visible path otherwise). NOT called when the pane is unmeasurable at
   * cleanup (hidden pane, detached ref): an unmeasurable unmount carries no position,
   * and clobbering an existing save (e.g. from a tab switch-away) would lose one.
   */
  onSaveViewportScroll?: (state: ChatScrollState) => void
  /**
   * Called for each `scroll` event that the scroll handler reads as a move while no
   * pointer or touch is down: the reader's momentum, the coast after a flick, or a native
   * scroll of a wheel or a key. Never for the event of an own write of this hook, which
   * records its position at once, never for a clamp of the browser to a scroll range that
   * shrank, and never for a drag still under the finger.
   *
   * It exists because only this hook can tell a move of the reader from its own writes.
   * The scroll rail keeps itself lit on it, so a fling holds the rail for however long it
   * coasts.
   */
  onMomentumScroll?: () => void
}

export interface UseChatScrollResult {
  /** Whether the view is at the bottom of the content, as of the last position check. */
  atBottom: Accessor<boolean>
  /**
   * Follow mode: the reader requested the tail, the tail is loaded, and the view did not
   * move up with no `scroll` event (see ScrollContext.isFollowing). ChatView passes it to
   * the virtualizer, whose layout shift holds the bottom of the content while it is true.
   */
  isFollowing: () => boolean
  /**
   * True only while the view is stalled hard against the loaded TOP edge waiting on an
   * in-flight older fetch (a scroll outran the pre-fetch buffer). Drives the "Loading
   * older messages..." indicator -- it stays dark during background pre-fetches.
   */
  stalledOlder: Accessor<boolean>
  /**
   * True only while the view is stalled hard against the loaded BOTTOM edge waiting on
   * an in-flight newer fetch. Drives the "Loading newer messages..." indicator AND hides
   * the scroll-to-bottom button (the indicator takes the same bottom-center slot).
   */
  stalledNewer: Accessor<boolean>
  attachListRef: (el: HTMLDivElement | undefined) => void
  /**
   * The floating scroll-to-bottom button's windowing-aware jump: forces a jump to
   * the latest page when windowed away from the live tail (the tail isn't loaded),
   * else an animated scroll. Owned here -- the hook holds the windowing state -- so
   * the view doesn't encode which primitive applies in which window state.
   */
  scrollToBottom: () => void
  /**
   * Seek to a message seq (a scroll-rail dot click, track click, or thumb release):
   * an in-window aligned landing, or a fetch-around-seq window swap then land. Resolves
   * to whether the seek actually moved the scroll position (so the rail's drag-release
   * hold knows whether to await the landing's metrics settle or clear the thumb now).
   */
  jumpToSeq: (seq: bigint) => Promise<boolean>
  /**
   * A placement for in-window rail thumb-drag live-scroll, at the content coordinate
   * `top`. A `top` at the content top (0) places the view at the top of the scroller,
   * above the inset of the spacer. An own write, so the `scroll` event of each placement
   * reads as no move: it samples no speed and reports no momentum. The scroll handler still
   * tops up the buffer for the new position, as it does for every event.
   */
  previewScrollTo: (top: number) => void
  /**
   * Abandon any in-flight out-of-window seek (a rail gesture that moved on, or a genuine
   * user scroll): its fetch is aborted and its landing dropped, so neither the page nor
   * the scroll can arrive after the user took manual control.
   */
  cancelPendingSeek: () => void
  /**
   * Re-stick to the bottom while follow mode applies and content below the viewport just
   * grew. For tail content that is NOT a virtualizer row -- the streaming-markdown block
   * and the thinking indicator, rendered as siblings of the virtual spacer -- whose growth
   * no ResizeObserver here observes (the virtualizer only measures rows, the scroll
   * container's content-box is fixed). ChatView calls this after that content renders.
   * Position-only and idempotent, so it is safe to call on every streaming frame. It
   * writes nothing while the browser moved the view up and the scroll handler did not see
   * that move yet: follow mode does not apply then (see ScrollContext.isFollowing).
   */
  restickIfAtBottom: () => void
  /** Capture the current viewport scroll position for tab-switch restoration. */
  getScrollState: () => ChatScrollState | undefined
  /** Imperative scroll-to-bottom (e.g. when the user sends a message). */
  forceScrollToBottom: () => void
  /** Page-wise scroll for keyboard navigation. */
  pageScroll: (direction: -1 | 1) => void
  handlers: {
    onScroll: () => void
    onWheel: (event: WheelEvent) => void
    onKeyDown: (event: KeyboardEvent) => void
    onTouchStart: (event: TouchEvent) => void
    onTouchMove: (event: TouchEvent) => void
    onTouchEnd: (event: TouchEvent) => void
    onTouchCancel: (event: TouchEvent) => void
    onPointerDown: (event: PointerEvent) => void
    onPointerMove: (event: PointerEvent) => void
    onPointerUp: (event: PointerEvent) => void
    onPointerCancel: (event: PointerEvent) => void
  }
}

/** What the scroll controller of `chatScrollPreserve` restores after a heavy re-render. */
interface PreservedScroll {
  /**
   * Where the view was: the anchor of the row at the viewport top, or for a window with no
   * row, the scroll position. The restore resolves the anchor again, and writes the scroll
   * position back as it is.
   */
  position: { anchor: ScrollAnchor } | { emptyWindowTop: number }
  /** The count of the reader's moves at the snapshot. */
  moves: number
  /** The reader's request for the live tail at the snapshot. */
  requested: boolean
  /** The latest-page jump that was pending at the snapshot. */
  jump: number | null
}

/**
 * Manages all scroll behavior for the chat message list: follow mode at the live tail,
 * auto-scroll on new content, the rebase of the layout shift that keeps the view in place
 * across a geometry change, viewport restoration on tab switch, and load-older on
 * overscroll for touch/wheel/keyboard.
 *
 * The scroll handler does not classify a `scroll` event. Every own write records the
 * position it wrote at once, and the handler lowers the recorded position to the maximum
 * of a scroll range that shrank. So the event of an own write and a clamp of the browser
 * read as no move, and every event that moved the position is the reader's (see the
 * handler).
 *
 * Attaches via ref callbacks so the same refs feed event handlers, effects,
 * and the imperative `getScrollState`/`forceScrollToBottom`/`pageScroll`
 * methods exposed on the result.
 */
export function useChatScroll(opts: UseChatScrollOptions): UseChatScrollResult {
  let messageListRef: HTMLDivElement | undefined
  const virt = opts.virtualizer

  // Keep the host accessor behind a hook-owned memo. Solid cleans this memo before
  // owner cleanups. The unmount save reads the plain snapshot. Reading the host
  // accessor there can update a stale parent and re-enter disposal.
  let hasNewerMessagesSnapshot = false
  const hasNewerMessages = createMemo(() => {
    hasNewerMessagesSnapshot = !!opts.hasNewerMessages?.()
    return hasNewerMessagesSnapshot
  })

  // The reader's request for the live tail. A mount on a window that is paged away from
  // the live tail holds no such request. Follow mode is this request while the tail is
  // loaded (isFollowing).
  let following = !untrack(hasNewerMessages)
  // Whether the view is at the bottom, as of the last position check, which is its one
  // writer. It starts as the request: a mount on a window that is paged away ran no
  // position check yet, and it is not at the bottom of the tail.
  const [atBottom, setAtBottom] = createSignal(following)
  // A monotonic tick bumped whenever the viewport's geometry (or its availability)
  // changes WITHOUT a signal of its own: every scroll event (a position-only edge
  // clamp/unclamp), every own write, and the ref (un)attaching (messageListRef is a plain
  // ref, so a memo that reads it can't react to it). The stall memos (stalledOlder /
  // stalledNewer) read the DOM fresh; this tick is the dependency that triggers that
  // re-read.
  const [geomTick, bumpGeomTick] = createSignal(0)

  // The position that the scroll handler or an own write saw last (see
  // createSeenScrollPosition). An own write notes its position at once, so the `scroll`
  // event of the write reads as no move.
  const seen = createSeenScrollPosition()
  // The count of the reader's moves: the scroll events that the handler reads as a move.
  // The controller of chatScrollPreserve compares it across a re-render to learn whether
  // the view moved.
  let moveCount = 0
  // The user's most recent scroll/keyboard intent. Drives pagination when the viewport
  // can't scroll (content fits, e.g. an all-hidden window page), the auto-advance through
  // hidden-only pages, and the stale-native translation after a large rebase.
  let lastScrollDir: 'older' | 'newer' = 'older'
  // The anchor of the reader's last position while follow mode does not apply. A resize
  // restores it after the pane was hidden (see createViewportRestore).
  let lastAnchor: ScrollAnchor | null = null
  // An anchor that a placement resolved while its row had only an estimated height, until
  // the row measures (see releaseAnchorHold). A move of the reader ends the hold, also a
  // move whose `scroll` event did not arrive yet, and so does every other own write that
  // places the view.
  let heldAnchor: ScrollAnchor | null = null
  let scrollAnimationId: number | null = null
  let savedScrollRecheckRaf = 0
  let autoScrollFirstSeq: bigint | undefined
  let autoScrollLastSeq: bigint | undefined
  let lastAutoScrollSig: unknown[] = []
  let lastTrimMessageCount: number | undefined
  let resizeObserver: ResizeObserver | undefined
  // The scroll-buffer filler's fill() and rearm(), assigned where the unit is created
  // (after the auto-scroll effect -- see the wiring comment at the creation site). A no-op
  // until wired, so an early scroll event is simply ignored.
  let fillScrollBuffer: () => void = () => {}
  let rearmScrollBufferFiller: () => void = () => {}

  // The pointers and the fingers that are down on the list (see createScrollInputActivity).
  // When the last one goes up, the scroller can enter rest (see noteInputEnded).
  const inputActivity = createScrollInputActivity({
    onInputEnded: noteInputEnded,
  })

  // True between a forced stop (grab/tap) and the user's next genuine scroll: pauses
  // the buffer fill so a stop also stops the older/newer pre-fetch ("stop = stop
  // loading too"). The scroll handler clears it on the next move of the reader, so a
  // fling or a slow scroll resumes pre-fetching; the pause does not block the explicit
  // wheel/key-at-edge loads, so the user can always still page at the very edge.
  let bufferFillPaused = false
  // DEV-SERVER-ONLY: the buffer fill of a mount after an HMR module swap waits until the
  // re-measure of the whole list settles (see createHmrSettleWindow). A move of the reader
  // and a command of the reader end the window sooner.
  const hmrSettle = createHmrSettleWindow(hmrRemount)

  // monotonicNow prefers perf.now() and falls back to Date.now() where `performance` is
  // absent (SSR / some test envs).
  const velocity = createScrollVelocity({ now: monotonicNow, idleMs: VELOCITY_IDLE_MS })

  /**
   * The anchor of the row at the viewport top, or null with no element or no row.
   * Untracked: no caller means to subscribe to the geometry that the anchor reads.
   */
  const anchorAtViewportTop = (): ScrollAnchor | null => {
    const el = messageListRef
    return el
      ? untrack(() => virt.anchorAt(readLogicalScrollTop(el)))
      : null
  }

  /**
   * The list element while it is attached and shown, else undefined. A hidden pane
   * (`display: none`) reads clientHeight and scrollTop as 0, and no position read from it
   * may become a recorded one.
   */
  const visibleListEl = (): HTMLDivElement | undefined => {
    const el = messageListRef
    return el !== undefined && el.clientHeight > 0
      ? el
      : undefined
  }

  /** Fresh DOM measurement — true when the view is within FOLLOW_TOLERANCE_PX of the bottom. */
  const isAtBottom = () =>
    !!messageListRef && distFromBottom(messageListRef) <= FOLLOW_TOLERANCE_PX

  /**
   * See ScrollContext.isFollowing. Follow mode exists only at the live tail: the loaded
   * bottom of a window that is paged away is not the tail, and following it would chase
   * every newer append.
   */
  const isFollowing = (): boolean => {
    if (!following || hasNewerMessages())
      return false
    const el = visibleListEl()
    return el === undefined || !seen.hasUnseenUpwardMove(el)
  }

  /** See ScrollContext.noteScrollRange. */
  function noteScrollRange(): void {
    const el = visibleListEl()
    if (el)
      seen.noteRange(el)
  }

  /** The content height above the viewport top, in content coordinates. */
  const viewportContentTop = () => messageListRef
    ? virt.contentTopOf(readLogicalScrollTop(messageListRef))
    : 0

  /**
   * Hard against the very TOP / BOTTOM edge of the loaded rows, within
   * EDGE_INTENT_TOLERANCE_PX (see chatScrollGeometry). The one home for the read each
   * edge uses (the content above the viewport top; distFromBottom at the bottom), shared
   * by the explicit edge-intent loaders, the stall indicators, and the overscroll-drag
   * gate so they can't drift asymmetrically.
   */
  const isAtTopEdge = () =>
    !!messageListRef && viewportContentTop() <= EDGE_INTENT_TOLERANCE_PX
  const isAtBottomEdge = () =>
    !!messageListRef && distFromBottom(messageListRef) <= EDGE_INTENT_TOLERANCE_PX

  // Stale-native-scroll translator: recognizes a compositor-delayed OLD-COORDINATE
  // momentum event after a large rebase during a scroll and rewrites it into the current
  // coordinate space (see createStaleNativeScrollTranslator). Every own write reports to
  // it (rebase and notePosition); the scroll handler consults it first.
  const staleNativeTranslator = createStaleNativeScrollTranslator({
    getEl: () => messageListRef,
    isScrollInputActive: inputActivity.isActive,
    isOwnWriteEvent: () => {
      const el = visibleListEl()
      return el !== undefined && !seen.hasUnseenRawMove(el)
    },
    // Same pane-derived render-ahead cap the fling overscan uses, so the stale-native
    // window covers the full fling travel on any pane height (see flingOverscanCapPx).
    flingOverscanCapPx,
  })

  // The rest detector: the scroller rests 150ms after its raw position last changed, with
  // no pointer or touch down. At rest, the layout shift goes (see rebase).
  const rest = createScrollRest({
    isInputActive: inputActivity.isActive,
    hasUnseenMove: () => {
      const el = visibleListEl()
      return el !== undefined && seen.hasUnseenRawMove(el)
    },
    onRest: () => rebase(),
  })

  /**
   * The last pointer or finger went up: the scroller can enter rest. The input tracker,
   * created above `rest`, calls it from an event listener only, after the hook body ran.
   */
  function noteInputEnded(): void {
    rest.noteInputEnded()
  }

  /** See ScrollContext.checkPosition. */
  function checkPosition(): void {
    const el = visibleListEl()
    if (!el)
      return
    // The check can enter follow mode at a bottom that a shrink clamped. Record that clamp,
    // or isFollowing() reads it as an unseen move of the reader once the content grows again.
    seen.noteRange(el)
    untrack(() => {
      const atBottomNow = isAtBottom()
      if (!following && !hasNewerMessages() && atBottomNow)
        following = true
      setAtBottom(atBottomNow)
      // The second condition keeps the anchor of the reader while the browser moved
      // scrollTop and the handler did not see that move yet, as after a pane was hidden
      // and shown.
      if (!isFollowing() && !seen.hasUnseenRawMove(el))
        lastAnchor = anchorAtViewportTop()
    })
  }

  /**
   * Record an own `scrollTop` write: the position it wrote, so its `scroll` event reads as
   * no move, the velocity baseline, and the position check. A hidden pane reads scrollTop
   * as 0, and that value must not become the recorded position.
   */
  function recordOwnWrite(): void {
    const el = visibleListEl()
    if (!el)
      return
    const top = readLogicalScrollTop(el)
    seen.note(top, el.scrollTop)
    velocity.syncToProgrammatic(top)
    checkPosition()
    bumpGeomTick(t => t + 1)
  }

  /**
   * Record an own write (see ScrollContext.writeOwnScrollTop): a record of the translator
   * no longer applies, and neither does a held anchor (the write replaces the placement
   * that held it).
   */
  function notePosition(): void {
    if (!visibleListEl())
      return
    heldAnchor = null
    recordOwnWrite()
    staleNativeTranslator.invalidate()
  }

  /**
   * Recompute the rendered row slice for the current scroll position. `lead` extends the
   * rendered slice ahead in the fling direction (see the scroll handler) so a fast scroll
   * paints the rows it is about to reach; other callers pass none and get the symmetric
   * overscan. Untracked: no caller means to subscribe to the geometry it reads.
   */
  function refreshViewport(lead?: ViewportLead): void {
    const el = messageListRef
    if (el)
      untrack(() => virt.updateViewport(readLogicalScrollTop(el), el.clientHeight, lead))
  }

  /** See ScrollContext.writeOwnScrollTop. */
  function writeOwnScrollTop(top: number, opts: { refresh: boolean }): void {
    const el = visibleListEl()
    if (!el)
      return
    el.scrollTop = top
    notePosition()
    if (opts.refresh)
      refreshViewport()
  }

  /** See ScrollContext.recordPosition. */
  function recordPosition(): void {
    if (!visibleListEl())
      return
    notePosition()
    refreshViewport()
  }

  /**
   * Remove the layout shift (see LayoutShiftSurface.rebase) and record the write.
   * A rebase while the scroller moves reports its move to the stale-native translator,
   * which can then translate a `scroll` event in the old coordinates; a rebase at rest
   * clears its record. It must not call notePosition: that clear removes the record that
   * the translator joins a second rebase to.
   */
  function rebase(): void {
    const el = visibleListEl()
    if (!el)
      return
    const beforeTop = el.scrollTop
    const wasResting = rest.isResting()
    if (virt.rebase() === 0)
      return
    recordOwnWrite()
    if (wasResting) {
      staleNativeTranslator.invalidate()
      return
    }
    staleNativeTranslator.noteRebase({
      beforeTop,
      afterTop: el.scrollTop,
      clientHeight: el.clientHeight,
      dir: lastScrollDir,
    })
  }

  /**
   * Whether the layout shift must go while the scroller moves. Otherwise it waits for
   * rest (see createScrollRest):
   *
   * - A shift larger than the view near the reachable top. A negative shift leaves loaded
   *   rows above scroll coordinate 0, out of reach; a positive one puts a blank band above
   *   the first row. A prepended page is such a shift. The margin is the view plus the
   *   render-ahead lead of the current speed, so a fling meets the grown range in time.
   * - The viewport top above the first row: the reader would see the blank band.
   *
   * A smaller shift waits for rest: the first measure of each row changes the shift by a
   * few pixels, and a write for each one ends the momentum in iOS WebKit.
   *
   * No shift must go while the browser moved the view and the `scroll` event of that move
   * did not arrive yet, as ScrollRest.isResting states: the rebase would record the moved
   * position as seen, so the late event would read as no move. The handler asks again at
   * that event.
   */
  function needsRebaseDuringScroll(): boolean {
    const el = visibleListEl()
    if (!el || seen.hasUnseenRawMove(el))
      return false
    const shift = untrack(virt.layoutShift)
    if (shift === 0)
      return false
    const scrollTop = readLogicalScrollTop(el)
    if (shift > 0 && scrollTop < shift)
      return true
    const rebaseMarginPx = el.clientHeight + renderAheadLeadPx(el.clientHeight, velocity.speed())
    return Math.abs(shift) > el.clientHeight && scrollTop - Math.max(0, shift) < rebaseMarginPx
  }

  /** See ScrollContext.placeAt. */
  function placeAt(resolve: () => number | null): boolean | null {
    const el = visibleListEl()
    if (!el)
      return null
    const placed = writePlacement(el, resolve)
    if (placed !== null)
      refreshViewport()
    return placed
  }

  /** The write of placeAt, with no refresh of the rendered rows. Returns as placeAt. */
  function writePlacement(el: HTMLDivElement, resolve: () => number | null): boolean | null {
    rebase()
    const top = resolve()
    if (top === null)
      return null
    following = false
    const before = el.scrollTop
    writeOwnScrollTop(clampScrollTop(el, top), { refresh: false })
    return el.scrollTop !== before
  }

  /** End the request for the tail, and record where the reader is. */
  function leaveFollow(): void {
    following = false
    if (visibleListEl())
      lastAnchor = anchorAtViewportTop()
  }

  function followTail(): void {
    following = true
  }

  /**
   * Place the view back at `anchor` for a reader who does not request the tail: at the
   * row itself, or at the nearest survivor by seq when the row is gone. It reads the
   * `following` flag, not isFollowing(): during a jump to the latest page the flag is set,
   * and this restore must not end that request.
   */
  function restoreAnchor(anchor: ScrollAnchor | null): boolean {
    if (following || anchor === null)
      return false
    return placeAtAnchor(anchor, true) !== null
  }

  /** See ScrollContext.placeAtAnchor. */
  function placeAtAnchor(anchor: ScrollAnchor, nearest: boolean): boolean | null {
    const placed = placeAt(() => nearest
      ? virt.scrollTopNearAnchor(anchor)
      : virt.scrollTopForAnchor(anchor))
    if (placed !== null && untrack(() => virt.scrollTopForAnchor(anchor) !== null && !virt.hasMeasuredHeight(anchor.id)))
      heldAnchor = anchor
    return placed
  }

  /**
   * Place the view on the held anchor once its row measures. The placement resolved the
   * anchor against the estimated height of the row, and the first measure of that row keeps
   * the bottom edge of the row in place (the shift rule holds the bottom edge of a row with
   * an estimated height, see nextLayoutShift). Without this placement, the view would move
   * inside the row by the difference between the estimate and the height. Runs in the
   * geometry effect, in the flush of the measure, so the reader sees no move. The effect
   * refreshes the rendered rows in the next frame, so this placement mounts no row inside
   * the flush.
   */
  function releaseAnchorHold(): void {
    const anchor = heldAnchor
    const el = visibleListEl()
    if (anchor === null || !el)
      return
    // A request for the tail replaces the restore, and a row that left the list holds
    // nothing. A move of the reader whose `scroll` event did not arrive yet ends the hold
    // too: a placement now would undo that move and record it as seen.
    if (following || seen.hasUnseenRawMove(el) || untrack(() => virt.scrollTopForAnchor(anchor)) === null) {
      heldAnchor = null
      return
    }
    if (untrack(() => virt.hasMeasuredHeight(anchor.id)))
      writePlacement(el, () => untrack(() => virt.scrollTopForAnchor(anchor)))
  }

  /**
   * Recompute the slice on a rAF, NOT synchronously. Mounting/unmounting rows
   * observes/unobserves them on the virtualizer's ResizeObserver, and a fresh observe()
   * fires a notification. If that happens inside the RO delivery cycle (which the
   * geometry effect runs within), it re-enters delivery in the same frame → "ResizeObserver
   * loop completed with undelivered notifications". Deferring the mount to the next frame
   * breaks that cycle. The pushes of one frame refresh once.
   */
  const viewportRefreshFrame = createRafCoalescer<void>(() => refreshViewport())

  // Fast-scroll flag for the fling skeletons and the pivot record (virt.setFastScrollActive):
  // set on any move of the reader at FAST_SCROLL_PX_PER_MS or faster -- momentum AND
  // direct drags -- and each such move restarts the trailing debounce below, which ends
  // the flag VELOCITY_IDLE_MS after the last fast move. A slower move changes neither the
  // flag nor the debounce. The speed is measured over the last gap between two events, so
  // a fling that decelerates through 1 px/ms reads above and below it on alternate events;
  // the debounce holds the flag through that jitter. The velocity tracker reads 0
  // VELOCITY_IDLE_MS after the last move of any speed, so the flag drops no later than
  // the tracker stops reporting a speed, and earlier when slower moves follow the last fast
  // one.
  const endFastScroll = trailingDebounce(() => virt.setFastScrollActive(false), VELOCITY_IDLE_MS)
  const markFastScroll = () => {
    virt.setFastScrollActive(true)
    endFastScroll()
  }

  const cancelScrollAnimation = () => {
    if (scrollAnimationId !== null) {
      cancelAnimationFrame(scrollAnimationId)
      scrollAnimationId = null
    }
  }

  // Halt all PROGRAMMATIC scroll (a running scroll animation) and pause the buffer-fill
  // pre-fetch until the next move of the reader. Called when the user takes manual
  // control (taps, touches, or sends a momentum-cancel wheel) so a forced stop halts the
  // view -- and the loading -- IMMEDIATELY rather than coasting through one more
  // pre-fetch page.
  const cancelPendingScroll = () => {
    cancelScrollAnimation()
    bufferFillPaused = true
  }

  /**
   * Clear the "buffer filler paused / settling" flags -- a deliberate jump (to bottom, to a
   * rail seq, a page) is the user re-taking control of position, exactly like a genuine
   * scroll, but its `scroll` event is an own write that never clears them. Callers that
   * REPLACE the window additionally rearmScrollBufferFiller() for the fresh window's
   * per-window state.
   */
  const retakeScrollControl = () => {
    bufferFillPaused = false
    hmrSettle.end()
  }

  // The state and the actions that the extracted scroll helpers share (see ScrollContext).
  const scrollCtx: ScrollContext = {
    getEl: () => messageListRef,
    getVisibleEl: visibleListEl,
    virt,
    isFollowing,
    followRequested: () => following,
    hasNewerMessages,
    noteScrollRange,
    isAnimating: () => scrollAnimationId !== null,
    followTail,
    leaveFollow,
    checkPosition,
    cancelScrollAnimation,
    retakeScrollControl,
    // A thunk: the buffer filler is created after the context, and it assigns the local.
    rearmScrollBufferFiller: () => rearmScrollBufferFiller(),
    rebase,
    placeAt,
    placeAtAnchor,
    writeOwnScrollTop,
    recordPosition,
  }

  const { stickToBottom, restickIfAtBottom } = createStickyBottom(scrollCtx)

  // ---- Pagination: maintain a VISIBLE-content buffer beyond the viewport ----
  //
  // Only VISIBLE rows have scroll height: the content above the viewport top is the
  // visible content ABOVE the viewport, distFromBottom the visible content BELOW. In a
  // hidden-heavy stretch a raw page adds almost no visible height, so a "load when within
  // half a screen of the edge" policy thrashed -- the viewport sat permanently in both
  // near-edge bands and every scroll event paginated. Instead, pre-fetch AHEAD: keep
  // SCROLL_BUFFER_SCREENS screens of visible content on each side. loadOlderMessages /
  // loadNewerPage grow the window to the raw CEILING (chat.store), so the buffer actually
  // accumulates rather than sliding. createScrollBufferFiller owns the loop.
  const paginationPolicy = createChatPaginationPolicy({
    getEl: () => messageListRef,
    hasOlder: () => !!opts.hasOlderMessages?.(),
    fetchingOlder: () => !!opts.fetchingOlder?.(),
    hasNewer: hasNewerMessages,
    fetchingNewer: () => !!opts.fetchingNewer?.(),
    geomVersion: geomTick,
    isAtTopEdge,
    isAtBottomEdge,
    onLoadOlder: () => opts.onLoadOlderMessages?.(),
    onLoadNewer: () => opts.onLoadNewerMessages?.(),
    isAtBottom,
    isFollowing,
    anchorAtViewportTop,
    bufferScreens: SCROLL_BUFFER_SCREENS,
  })
  const {
    bufferTargetPx,
    loadOlderMessages,
    tryLoadOlderOnExplicitTopIntent,
    loadNewerMessages,
    tryLoadNewerOnExplicitBottomIntent,
    stalledOlder,
    stalledNewer,
  } = paginationPolicy

  const scrollToBottomAnimated = () => {
    if (!messageListRef)
      return
    cancelScrollAnimation()

    // A streaming tail can grow on every frame. Limit the chase, then hand the
    // position to sticky-bottom so the animation cannot run without an end.
    let framesLeft = SCROLL_TO_BOTTOM_MAX_FRAMES
    const animate = () => {
      const el = messageListRef
      if (!el) {
        scrollAnimationId = null
        return
      }
      const remaining = distFromBottom(el)
      if (remaining < 1 || framesLeft <= 0) {
        scrollAnimationId = null
        stickToBottom()
        return
      }
      framesLeft--
      const step = remaining > 48
        ? remaining * 0.5
        : remaining * 0.4
      writeOwnScrollTop(readLogicalScrollTop(el) + Math.ceil(step), { refresh: true })
      scrollAnimationId = requestAnimationFrame(animate)
    }

    scrollAnimationId = requestAnimationFrame(animate)
  }

  const tailFollower = createChatTailFollower(scrollCtx, {
    jumpToLatest: () => opts.onJumpToLatest?.(),
    stickToBottom,
    animateToBottom: scrollToBottomAnimated,
  })
  const { forceScrollToBottom, scrollToBottom } = tailFollower

  // ---- Scroll-rail seek (jumpToSeq / previewScrollTo / cancelPendingSeek) -------------
  // The seek machine (dot/track/thumb -> land, or fetch-around-seq then land) lives in its
  // own tested unit, createChatSeek; wired here where all its deps are in scope. It owns the
  // pendingSeek/epoch state.
  const chatSeek = createChatSeek(scrollCtx, {
    messages: opts.messages,
    onJumpToSeq: opts.onJumpToSeq,
    onSaveViewportScroll: opts.onSaveViewportScroll,
  })

  // The keyboard / wheel input layer (createScrollInput). Owns no scroll state -- the
  // shared `lastScrollDir` stays here (the scroll handler reads it) and is written through
  // the setter below.
  const { handleKeyDown, handleWheel, pageScroll } = createScrollInput(scrollCtx, {
    forceScrollToBottom,
    cancelPendingScroll,
    cancelPendingSeek: () => chatSeek.cancelPendingSeek(),
    tryLoadOlderOnExplicitTopIntent,
    tryLoadNewerOnExplicitBottomIntent,
    setLastScrollDir: (dir) => {
      lastScrollDir = dir
    },
    hasOlderMessages: () => !!opts.hasOlderMessages?.(),
    // jump-to-oldest (Home) REPLACES the window with the earliest page; re-arm the
    // buffer-filler side-selection state. Mirrors forceScrollToBottom's jump-to-latest
    // handling.
    onJumpToOldest: opts.onJumpToOldest
      ? () => {
          rearmScrollBufferFiller()
          return opts.onJumpToOldest!()
        }
      : undefined,
  })

  // Saved-viewport restore + resize handling (createViewportRestore). It owns only the
  // resize bookkeeping (prevClientHeight, the pending rAF).
  const viewportRestore = createViewportRestore(scrollCtx, {
    stickToBottom,
    forceScrollToBottom,
    clearSavedViewportScroll: () => opts.onClearSavedViewportScroll?.(),
    savedViewportScroll: () => opts.savedViewportScroll?.(),
    restoreAnchor,
    lastAnchor: () => lastAnchor,
    onGeometrySettled: () => bumpGeomTick(t => t + 1),
  })

  /**
   * The scroll handler reads; it does not classify. An own write recorded its position
   * already, so its event has `moved === false`, with no marker and no time limit. A clamp
   * of the browser to a scroll range that shrank also reads as no move (step 2). Every
   * other event that moved the position is the reader's: it samples the speed, sets the
   * direction, ends the buffer-fill pause, and can end follow mode.
   */
  const handleScroll = () => {
    // A hidden pane reads scrollTop as 0: its event moved nothing that the reader sees, and
    // its position must not become the recorded one.
    const el = visibleListEl()
    if (!el)
      return
    // 1. A compositor-delayed event in the coordinates before a rebase. The translator
    //    writes the translated position, so the reads below see it, and returns the
    //    position after the rebase: the move of the event counts from there.
    let baseline = staleNativeTranslator.translate() ?? seen.seenTop()
    // 2. What moved since the last event or own write. A content shrink clamps scrollTop to
    //    the new maximum with no move of the reader. Lower the baseline and the baseline of
    //    the speed to the maximum first: a pure clamp then reads as no move, and the next
    //    move of the reader is measured from the clamped position.
    const maxTop = maxScrollTopOf(el)
    if (baseline > maxTop) {
      baseline = maxTop
      velocity.syncToProgrammatic(maxTop)
    }
    const st = readLogicalScrollTop(el)
    const rawTop = el.scrollTop
    const moved = st !== baseline
    const rawMoved = seen.hasUnseenRawMove(el)
    // 3. End the request for the tail when the reader moves away from the bottom: up and
    //    past FOLLOW_TOLERANCE_PX, or, while newer messages exist, in either direction to a
    //    position more than FOLLOW_TOLERANCE_PX above the loaded bottom. A reader who moves
    //    down to the loaded bottom while the newer pages load keeps the request.
    const awayFromBottom = !isAtBottom()
    if (moved && hasNewerMessages() && awayFromBottom)
      following = false
    if (following && awayFromBottom && st < baseline)
      leaveFollow()
    // 4. The scroller is not at rest.
    if (rawMoved)
      rest.noteScroll()
    // 5. A move of the reader.
    let lead: ViewportLead | undefined
    if (moved) {
      moveCount++
      // The reader's own position replaces a restored one.
      heldAnchor = null
      // A move resumes the buffer-fill pre-fetch a forced stop OR the dev HMR settle
      // window paused (see cancelPendingScroll and createHmrSettleWindow).
      bufferFillPaused = false
      hmrSettle.end()
      velocity.sample(st)
      const dir = inferScrollDirection(baseline, st)
      if (dir)
        lastScrollDir = dir
      // Scale the render-ahead by the just-sampled speed (0 while idle or before two
      // samples -> no lead), capped so an extreme flick can't mount an unbounded slice.
      // Head in THIS event's resolved direction -- not the stale lastScrollDir.
      const speed = velocity.speed()
      const px = dir
        ? renderAheadLeadPx(el.clientHeight, speed)
        : 0
      if (px > 0 && dir)
        lead = { dir, px }
      if (speed >= FAST_SCROLL_PX_PER_MS)
        markFastScroll()
      // A drag with a pointer or a finger (a scrollbar thumb, a touch) is the reader
      // taking control: an out-of-window seek must not land after it. A wheel, a key, and
      // a touch move cancel the seek in their own handlers.
      if (inputActivity.isActive())
        chatSeek.cancelPendingSeek()
      else
        opts.onMomentumScroll?.()
    }
    // 6. The position of this event is the last seen one.
    seen.note(st, rawTop)
    // 7. After step 6, so that it records the anchor of this position.
    checkPosition()
    // 8. Time the synchronous render cascade this scroll triggers: setting the range
    //    mounts newly-visible rows AND runs the premeasure computed (rendering the hidden
    //    look-ahead rows) in the same flush. If that blows the frame budget, it stalls the
    //    scroll loop. Only a slow pass logs (see warnSlowScrollPhase).
    const refreshStart = monotonicNow()
    refreshViewport(lead)
    warnSlowScrollPhase('refreshViewport', monotonicNow() - refreshStart, { rows: opts.messages().length })
    // 9. Re-arm the stall memos: this position move may have clamped the view against (or
    //    freed it from) a loaded edge, which is invisible to them otherwise.
    bumpGeomTick(t => t + 1)
    // 10. Grow the scroll range near the reachable top, or close a blank band above the
    //     first row.
    if (needsRebaseDuringScroll())
      rebase()
    // 11. Top up the visible buffer for the new scroll position (scrolling toward an
    //     edge shrinks that side's buffer).
    fillScrollBuffer()
  }

  // Touch/pointer overscroll-at-top → load older history. Self-contained gesture
  // tracker; the drag math lives in createOverscrollDrag.
  const overscroll = createOverscrollDrag({
    atTop: isAtTopEdge,
    onDragAtTop: tryLoadOlderOnExplicitTopIntent,
  })

  // After any geometry change -- older-message prepend, window trim, or a row-height
  // measurement -- the layout shift already kept the view in place (the virtualizer set
  // it before this effect runs). This effect follows the tail for a reader in follow
  // mode, runs the position check (rows can land below a reader who does not follow),
  // removes the shift when it may go, and refreshes the rendered rows.
  //
  // The effect depends on totalHeight and geometryVersion. totalHeight reads the offset map
  // of the virtualizer, a memo with a new value on each geometry change, so it alone reruns
  // the effect for a list change (prepend/trim) and for a measurement that keeps the
  // total. geometryVersion states the second case on its own: a measurement that keeps the
  // total must still run the effect if totalHeight compares its value one day.
  // `defer: true` so the first render does nothing before anything has been measured.
  //
  // This effect runs inside the measurer's ResizeObserver flush, so it mounts no row: the
  // refresh goes to the next frame (viewportRefreshFrame), and the position check and
  // the rebase mount nothing.
  createEffect(on([
    () => virt.totalHeight(),
    () => virt.geometryVersion(),
  ], () => {
    releaseAnchorHold()
    restickIfAtBottom()
    checkPosition()
    if (rest.isResting() || needsRebaseDuringScroll())
      rebase()
    viewportRefreshFrame.push()
  }, { defer: true }))

  // Enter follow mode when the tail becomes live under a reader who waited at the loaded
  // bottom. ChatView adds the tail siblings (the thinking indicator, banners) in the same
  // update that clears `hasNewerMessages`, before this effect runs, so a fresh position
  // check alone would find that reader above the bottom by their height. The atBottom
  // signal still holds the last check. The paginator merges a newer page in one update
  // and clears the flag in a later one, so the geometry effect of the merge lowered the
  // signal first when rows landed below the reader.
  createEffect(on(hasNewerMessages, (hasNewer) => {
    if (hasNewer)
      return
    // No position check ran while the pane was hidden, so the signal can be stale there;
    // the resize flush of the show runs the check.
    if (!visibleListEl())
      return
    if (untrack(atBottom) && !following) {
      following = true
      restickIfAtBottom()
    }
    checkPosition()
  }, { defer: true }))

  // Recompute the rendered slice whenever the message list changes (added,
  // removed, prepended, or trimmed) so newly-visible rows mount even when no
  // scroll event or height change fired. This is driven by store updates, not
  // ResizeObserver delivery, so mounting rows synchronously here is safe.
  createEffect(on(() => opts.messages(), () => {
    refreshViewport()
  }, { defer: true }))

  // Expose scroll state for viewport save on tab switch and on unmount (the
  // onSaveViewportScroll cleanup). Anchored to the viewport-top row (by seq) so
  // restoration survives the spacer's estimated height — a raw distance-from-bottom
  // would resolve to the wrong place. The at-bottom value is the reader's request for
  // the tail, not the position: a request that waits for its page is saved as at-bottom,
  // and a reader at the loaded bottom of a paged-away window is saved with an anchor.
  const captureScrollState = (hasMoreNewer: boolean): ChatScrollState | undefined => {
    const el = visibleListEl()
    if (!el)
      return undefined
    const requested = following
    const scrollTop = readLogicalScrollTop(el)
    const a = requested
      ? null
      : virt.anchorAt(scrollTop)
    // Scrolled away from the bottom but no resolvable anchor means the virtual
    // list is empty (e.g. an all-hidden window). There is no estimated spacer
    // here (totalHeight is 0, and so is the layout shift), so a clamped raw scrollTop
    // carries no drift risk -- save it as a fallback rather than losing the position to
    // bottom-follow on return. (A non-zero scrollTop comes from non-virtual content: a
    // tall streaming block or startup banner.)
    const rawScrollTop = !requested && a === null
      ? scrollTop
      : undefined
    return {
      ...(a !== null
        ? { anchor: a }
        : {}),
      ...(rawScrollTop !== undefined
        ? { rawScrollTop }
        : {}),
      atBottom: requested,
      hasMoreNewer,
    }
  }
  const getScrollState = (): ChatScrollState | undefined =>
    captureScrollState(hasNewerMessages())

  // Mount placement, part 1 of 2: restore a SAVED viewport on a visible (re)mount --
  // a tile split/merge or workspace switch saved the reading position at unmount (see
  // onSaveViewportScroll). Registered BEFORE the auto-scroll effect below (onMount is
  // an effect, and effects flush in creation order), so within the mount flush the
  // restore lands first: it consumes the saved state and ends the request for the tail,
  // which part 2 (the auto-scroll effect's creation run, the default tail stick for a
  // populated fresh mount) then respects. With no saved state -- or a hidden mount, which
  // the RO hidden->visible path owns -- this is a no-op and the tail stick stands.
  onMount(() => viewportRestore.restoreOnMount())

  // Auto-scroll the message list to the bottom when new content arrives
  // and the reader follows the tail. Its CREATION run doubles as
  // mount placement part 2: over an already-populated window with no saved viewport
  // (or an at-bottom one, already consumed above), it performs the initial tail
  // stick that moves a fresh container off its unplaced scrollTop 0.
  // messageVersion covers thread merges (tool_use_result merged into an
  // existing tool_use) which don't change messages.length.
  createEffect(() => {
    const msgs = opts.messages()
    // A prepend moves the first sequence earlier. An empty window has no sequence.
    const firstSeq = firstMessageSeq(msgs)
    const newestSeq = lastMessageSeq(msgs)
    const prependedOlderMessages = autoScrollFirstSeq !== undefined
      && firstSeq !== undefined
      && firstSeq < autoScrollFirstSeq
    const newestEdgeAdvanced = autoScrollLastSeq !== undefined
      && newestSeq !== undefined
      && newestSeq > autoScrollLastSeq
    const pureOlderPrepend = prependedOlderMessages && !newestEdgeAdvanced
    autoScrollFirstSeq = firstSeq
    autoScrollLastSeq = newestSeq
    // Cheap signature over the inputs that drive auto-scroll, so wake-ups
    // from same-reference store re-emits short-circuit before reading
    // scrollHeight (which forces layout). agentStatus is included so a
    // STARTING transition (which renders the inline AgentStartupBanner
    // below the message list) scrolls the banner into view.
    const sig = [
      msgs.length,
      opts.messageVersion?.() ?? 0,
      opts.agentWorking?.() ?? false,
      // -1 (outside the AgentStatus enum, which starts at 0) so "no accessor" can't
      // alias UNSPECIFIED(0) -- a transition between undefined and UNSPECIFIED would
      // otherwise leave the signature unchanged and could swallow a banner scroll.
      opts.agentStatus?.() ?? -1,
      hasNewerMessages(),
    ]
    if (pureOlderPrepend) {
      // Pure older prefetches are not live-tail growth. Record the new signature and
      // row count before returning so a later streaming/status wake-up does not treat
      // the prepended window as a fresh append and trim away the just-loaded older
      // buffer. If the newest edge advanced in the same update, tail handling still
      // runs below.
      lastAutoScrollSig = sig
      lastTrimMessageCount = msgs.length
      return
    }
    if (shallowEqualArrays(sig, lastAutoScrollSig))
      return
    lastAutoScrollSig = sig
    // Windowed away from the live tail: live messages are dropped from the
    // store, and the bottom of the in-memory list is not the real bottom.
    if (hasNewerMessages())
      return
    // Cap the in-memory window on every tail append, INDEPENDENT of scroll
    // position. While still at the live tail (hasNewerMessages false) but
    // scrolled up, live messages append to the tail and this is the ONLY window
    // cap -- the pagination trims fire only on overscroll, and the stick path below
    // is skipped. Without this an active tab watched from a scrolled-up position grows
    // the in-memory set without a limit during a long turn. Only run the raw-row cap when
    // the message count changed. Status or message-version updates can need sticky-bottom
    // handling below, but they do not increase the number of rows and must not reap the
    // older buffer.
    const messageCountChanged = lastTrimMessageCount !== msgs.length
    lastTrimMessageCount = msgs.length
    const el = messageListRef
    if (messageCountChanged && el) {
      // Buffer-top anchor resolution reads the offset map (reactive) -- untrack it so
      // this auto-scroll effect doesn't subscribe to every geometry nudge. The rest of
      // the keep-newest derivation is pure; see computeBufferAwareKeepNewest.
      const keepNewest = computeBufferAwareKeepNewest(
        msgs,
        untrack(viewportContentTop),
        el.clientHeight,
        bufferTargetPx(),
        bufTop => untrack(() => virt.anchorAtContent(bufTop)),
      )
      opts.onTrimOldMessages?.(keepNewest)
    }
    // Follow the tail: stick when content (a new row, the thinking indicator, a banner)
    // left the view above the bottom. Skip a hidden pane (an inactive tab with
    // display:none); the ResizeObserver sticks when the tab becomes visible. Stick
    // synchronously (the DOM is already updated by SolidJS) so a deferral to rAF can't
    // paint one frame scrolled up before snapping to bottom. A shrink that clamped the view
    // to the bottom is recorded first, so it stays no move of the reader when the content
    // grows again. While the browser moved the view up and the handler did not see that
    // move yet, follow mode does not apply, and the event of the move ends the request.
    if (visibleListEl()) {
      noteScrollRange()
      if (untrack(isFollowing) && !isAtBottom())
        stickToBottom()
    }
  })

  // Suppress the SPECULATIVE older-history pre-fetch while the view follows the live tail
  // (see suppressOlderPrefetchAtLiveTail): the older buffer only earns its keep once the
  // reader scrolls UP into history. Pre-fetch a VISIBLE-content buffer beyond the viewport
  // so scrolling stays smooth in hidden-heavy stretches (see createScrollBufferFiller).
  // Self-contained reactive unit; wired here so its createEffect runs AFTER the geometry /
  // messages-refresh / auto-scroll effects above. The AUTO-SCROLL ordering is the mount
  // placement invariant: within one flush, the initial stick-to-bottom (the auto-scroll
  // effect's creation run over an already-populated window -- a tile-split remount or a
  // reload that mounted after the replay landed) runs BEFORE the filler's first fill.
  // Created before the auto-scroll effect, the filler's creation pass read the UNPLACED
  // container (scrollTop 0) as an older-buffer deficit and fired a fetch -- stranding the
  // view at the top and paging older history to exhaustion (the reload climb-to-top
  // runaway).
  const bufferFiller = createScrollBufferFiller({
    getEl: () => messageListRef,
    // Each geometry read below is untracked. The geometry and the layout shift change on
    // each row measure and on each rebase, and the effect of the filler must not run fill()
    // for such a change: it runs inside the ResizeObserver flush, with no scroll and no new
    // message. The scroll handler fills for a move of the view.
    viewportContentTop: () => untrack(viewportContentTop),
    messages: opts.messages,
    bufferTargetPx,
    hasOlder: () => !!opts.hasOlderMessages?.(),
    hasNewer: hasNewerMessages,
    fetchingOlder: () => !!opts.fetchingOlder?.(),
    fetchingNewer: () => !!opts.fetchingNewer?.(),
    onLoadOlder: loadOlderMessages,
    onLoadNewer: loadNewerMessages,
    lastScrollDir: () => lastScrollDir,
    paused: () => bufferFillPaused || hmrSettle.isSettling(),
    suppressOlder: paginationPolicy.suppressOlderPrefetchAtLiveTail,
    atCeiling: () => !!opts.atWindowCeiling?.(),
    // Both sides' progress is measured against a stable reference row, not raw
    // scrollTop/distFromBottom, so a mid-fetch scroll in either direction can't mask a
    // productive load. The content top of the viewport-top row is the height ABOVE it, and
    // totalHeight - that is the height BELOW it. Content coordinates, so a layout shift
    // between the load and the measure does not read as progress or as none.
    captureAnchor: anchorAtViewportTop,
    contentAbove: anchor => untrack(() => virt.contentTopForAnchor(anchor)),
    contentBelow: anchor => untrack(() => {
      const top = virt.contentTopForAnchor(anchor)
      return top === null
        ? null
        : virt.totalHeight() - top
    }),
  })
  // The scroll handler tops up the buffer for the new scroll position through this ref;
  // the jump paths (forceScrollToBottom, jump-to-oldest -- both declared before this unit)
  // drop per-window filler state through the rearm ref.
  fillScrollBuffer = bufferFiller.fill
  rearmScrollBufferFiller = bufferFiller.rearm

  // Re-check the position after the parent clears saved scroll state.
  // Restoration itself is handled exclusively by the ResizeObserver's
  // hidden→visible path so that we avoid a race where this effect runs
  // before the tab is actually hidden (clearing saved state too early).
  // `defer: true`: this effect exists for the CLEAR transition only. Its creation run
  // (saved undefined on a fresh mount) would schedule a check that reads the container
  // BEFORE the mount placement has moved it off scrollTop 0.
  createEffect(on(
    () => opts.savedViewportScroll?.(),
    (saved) => {
      if (!saved && visibleListEl()) {
        // Cancel any prior pending re-check and store the handle so onCleanup can
        // cancel it -- otherwise a tile disposed between scheduling and firing
        // would still run the position check against a detached element.
        cancelAnimationFrame(savedScrollRecheckRaf)
        savedScrollRecheckRaf = requestAnimationFrame(() => checkPosition())
      }
    },
    { defer: true },
  ))

  onCleanup(() => {
    cancelScrollAnimation()
    viewportRefreshFrame.abort()
    cancelAnimationFrame(savedScrollRecheckRaf)
    endFastScroll.cancel()
    rest.dispose()
    // Save the final reading position for the next mount of this agent (a tile
    // split/merge or workspace switch remounts ChatView over the same store; see
    // restoreOnMount). Owner cleanups run before the DOM detaches, so the geometry
    // is still readable. An unmeasurable pane (hidden tab, clientHeight 0) yields
    // undefined and is skipped -- its tab-switch save must not be clobbered.
    const finalScrollState = captureScrollState(hasNewerMessagesSnapshot)
    if (finalScrollState)
      opts.onSaveViewportScroll?.(finalScrollState)
  })

  // Observe ONLY the scroll container (editor/window/keyboard resize, and
  // hidden→visible). We deliberately do NOT observe the content wrapper: under
  // virtualization its height is the spacer, which every row measure changes --
  // observing it would re-enter ResizeObserver delivery every time a row measures and
  // trip "ResizeObserver loop completed with undelivered notifications". Content-height
  // growth (streaming, expand/collapse, async syntax highlighting) is handled instead by
  // the virtualizer's own row observer via the geometry effect (restickIfAtBottom) and by
  // the auto-scroll effect (streaming/thinking).
  onMount(() => {
    viewportRestore.initClientHeight()
    // Render the initial slice for the freshly-measured viewport.
    refreshViewport()
    resizeObserver = new ResizeObserver(viewportRestore.handleResize)
    if (messageListRef)
      resizeObserver.observe(messageListRef)
    onCleanup(() => {
      viewportRestore.cancelPendingResize()
      resizeObserver?.disconnect()
      resizeObserver = undefined
    })
  })

  // The controller that chatScrollPreserve restores through after a heavy re-render. The
  // snapshot holds the position and the request for the tail; the restore writes only when
  // the view moved since the snapshot: the handler counted a move of the reader, or
  // scrollTop differs from the raw position that the handler or an own write saw last.
  // An own write, a geometry change (a rebase, a row that changed under the reader), and
  // a clamp of the browser whose `scroll` event arrived count as no move. A change whose
  // event did not arrive yet counts as a move, a clamp included.
  const scrollController: ChatScrollController<PreservedScroll> = {
    snapshot: () => {
      const el = visibleListEl()
      if (!el)
        return undefined
      const top = readLogicalScrollTop(el)
      const anchor = untrack(() => virt.anchorAt(top))
      return {
        position: anchor !== null
          ? { anchor }
          : { emptyWindowTop: top },
        moves: moveCount,
        requested: following,
        jump: tailFollower.pendingJump(),
      }
    },
    restore: (snapshot) => {
      const el = visibleListEl()
      if (!el)
        return
      // The second test finds a move whose `scroll` event did not arrive yet.
      const moved = moveCount !== snapshot.moves || seen.hasUnseenRawMove(el)
      if (!moved)
        return
      if (snapshot.requested && !hasNewerMessages()) {
        stickToBottom()
        return
      }
      const position = snapshot.position
      placeAt(() => 'anchor' in position
        ? virt.scrollTopForAnchor(position.anchor)
        : position.emptyWindowTop)
      // A restore is no command of the reader: a request whose jump is still pending
      // survives it. A request whose jump failed during the work stays ended, because no
      // fetch would fulfill it.
      if (snapshot.requested && snapshot.jump !== null && tailFollower.pendingJump() === snapshot.jump)
        followTail()
    },
  }

  return {
    atBottom,
    isFollowing,
    stalledOlder,
    stalledNewer,
    attachListRef: (el) => {
      messageListRef = el
      // So a repair that lives OUTSIDE this hook can restore a position through it --
      // see `chatScrollPreserve`. A raw scrollTop is no position under a layout shift,
      // and a bare `scrollTop =` from outside reads as a move of the reader.
      registerChatScrollController(el, scrollController)
      if (el) {
        // The first event then measures its move and its direction from the real position
        // of the element, not from 0.
        seen.note(readLogicalScrollTop(el), el.scrollTop)
      }
      // The stall memos read messageListRef (a plain ref they can't track) and were
      // eagerly computed at hook-construction time while it was still undefined. Bump
      // the geometry tick so they re-evaluate now that the element exists (and again if
      // it detaches), instead of staying latched at their initial value.
      bumpGeomTick(t => t + 1)
    },
    scrollToBottom,
    jumpToSeq: seq => chatSeek.jumpToSeq(seq),
    previewScrollTo: top => chatSeek.previewScrollTo(top),
    cancelPendingSeek: () => chatSeek.cancelPendingSeek(),
    restickIfAtBottom,
    getScrollState,
    forceScrollToBottom,
    pageScroll,
    handlers: {
      onScroll: handleScroll,
      onWheel: handleWheel,
      onKeyDown: handleKeyDown,
      onTouchStart: (event) => {
        // The user grabbed the surface -- stop any coasting programmatic scroll now.
        cancelPendingScroll()
        inputActivity.onTouchStart(event)
        overscroll.onTouchStart(event)
      },
      onTouchMove: (event) => {
        // One finger that drags vertically past the touch slop moves the content: the
        // reader takes control. A horizontal swipe on a wide code block and a pinch move
        // nothing in the list, and the seek stays. A drag that does move the list also
        // cancels the seek in the scroll handler.
        if (inputActivity.isVerticalDrag(event))
          chatSeek.cancelPendingSeek()
        overscroll.onTouchMove(event)
      },
      onTouchEnd: (event) => {
        inputActivity.onTouchEnd(event)
        overscroll.onTouchEnd()
      },
      onTouchCancel: (event) => {
        inputActivity.onTouchEnd(event)
        overscroll.onTouchEnd()
      },
      onPointerDown: (event) => {
        // A tap/press to stop a fling -- halt the animation and the pre-fetch now.
        cancelPendingScroll()
        inputActivity.onPointerDown(event)
        overscroll.onPointerDown(event)
      },
      onPointerMove: overscroll.onPointerMove,
      // The window listeners of the input tracker end the pointer. They run first, in the
      // capture phase, so the list ends only its drag gesture here.
      onPointerUp: () => overscroll.onPointerUp(),
      onPointerCancel: () => overscroll.onPointerUp(),
    },
  }
}
