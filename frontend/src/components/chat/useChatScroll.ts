import type { Accessor } from 'solid-js'
import type { UseChatVirtualizerResult, ViewportLead } from './useChatVirtualizer'
import type { AgentChatMessage, AgentStatus } from '~/generated/leapmux/v1/agent_pb'
import type { SavedViewportScroll, ScrollAnchor } from '~/stores/chatTypes'
import { createEffect, createMemo, createSignal, on, onCleanup, onMount, untrack } from 'solid-js'
import { createLogger } from '~/lib/logger'
import { monotonicNow } from '~/lib/monotonicNow'
import { shallowEqualArrays } from '~/lib/shallowEqual'
import { firstServerSeq, lastServerSeq } from '~/stores/chatMessageOrder'
import { isOptimisticLocal } from '~/stores/chatReconcile'
import { createAnchorRepin } from './chatScrollAnchorRepin'
import { createScrollBufferFiller } from './chatScrollBufferFiller'
import { ANCHOR_DRIFT_REWARN_MS, ANCHOR_DRIFT_WARN_PX, classifyUnexplainedJump, KEYBOARD_SCROLL_GRACE_MS, UNEXPLAINED_JUMP_REWARN_MS, VISIBLE_ANCHOR_JUMP_PX } from './chatScrollDiagnostics'
import { createFlingSettle, FLING_SETTLE_MS } from './chatScrollFlingSettle'
import { clampScrollTop, distFromBottom, EDGE_INTENT_TOLERANCE_PX, inferScrollDirection, isNearTopBand, maxScrollTopOf, REPIN_MIN_DELTA_PX, warnSlowScrollPhase } from './chatScrollGeometry'
import { createScrollInput } from './chatScrollInput'
import { createOverscrollDrag } from './chatScrollOverscrollDrag'
import { createProgrammaticScrollGuard } from './chatScrollProgrammaticGuard'
import { createStaleNativeScrollTranslator } from './chatScrollStaleNative'
import { createStickyBottom } from './chatScrollSticky'
import { createScrollVelocity } from './chatScrollVelocity'
import { createViewportRestore } from './chatScrollViewportRestore'

/** Saved/queried scroll position — anchored to a message, see SavedViewportScroll. */
export type ChatScrollState = SavedViewportScroll

/**
 * The geometry surface useChatScroll needs from the virtualizer. The scroll
 * hook owns the scroll position; the virtualizer owns the offset map. Sizing
 * the spacer to `totalHeight()` keeps the DOM's scrollHeight truthful, so the
 * existing scrollHeight/scrollTop math continues to work. Declared as a `Pick`
 * so it stays provably a subset of the virtualizer's surface — a signature
 * change there breaks here at compile time instead of silently diverging.
 * Every member is REQUIRED: the measurement-deferral surface was once optional
 * for test fakes, which forced `?.`-guards at every hook call site and meant a
 * production caller could silently disable the fling deferral machinery by
 * omitting a method — the testkit supplies no-op defaults instead.
 */
export type ChatScrollVirtualizer = Pick<
  UseChatVirtualizerResult,
  'totalHeight' | 'updateViewport' | 'anchorAt' | 'scrollTopForAnchor' | 'scrollTopNearAnchor' | 'geometryVersion'
  | 'setVisibleMeasurementDeferral' | 'hasDeferredMeasurements' | 'flushDeferredMeasurements' | 'lastMeasurement' | 'hasMeasuredHeight'
  | 'setFastScrollActive'
>

/**
 * The STABLE scroll primitives the extracted scroll helpers (createStickyBottom /
 * createScrollInput / createViewportRestore) all reach back into useChatScroll for:
 * the element + virtualizer handles, the atBottom / scroll-mode reads, the viewport
 * refresh, the programmatic write and its velocity/guard hooks, and the anchor set.
 * Built ONCE in the hook and threaded as a single value so a new shared primitive is
 * added in one place instead of re-listed in each helper's dependency bag (the leaky
 * decomposition those three near-identical bags were). Each helper additionally takes
 * a small `extras` object for the deps unique to it -- including the high-level
 * composite actions (captureAnchor / repinToAnchor / checkAtBottom / stickToBottom /
 * forceScrollToBottom), which are declared LATER in the hook than this context and so
 * are wired per helper where they are already in scope rather than forward-referenced.
 */
export interface ScrollContext {
  getEl: () => HTMLDivElement | undefined
  virt: ChatScrollVirtualizer
  /** The atBottom signal accessor. */
  atBottom: () => boolean
  setAtBottom: (v: boolean) => void
  /** Fresh DOM check: scrollTop within the sticky threshold of the bottom. */
  isAtBottom: () => boolean
  /** True while the scroll-mode machine is following the tail (not anchored to a row). */
  isFollowing: () => boolean
  /** True while a scroll animation is running. */
  isAnimating: () => boolean
  /** Transition the scroll-mode machine to following the tail. */
  followTail: () => void
  /** Recompute the rendered row slice for the current scroll position. */
  refreshViewport: () => void
  /** Programmatic scrollTop write whose echo the guard recognizes as ours. */
  writeScrollTop: (top: number, source?: string) => void
  /** Advance the velocity baseline without scoring a sample (a programmatic write is not a gesture). */
  syncVelocityToProgrammatic: (pos: number) => void
  setAnchor: (a: ScrollAnchor | null, captureTop?: number, viewportOffsetRatio?: number) => void
}

const STICKY_BOTTOM_THRESHOLD_PX = 32
/**
 * DEV-SERVER-ONLY: how long after a genuine HMR remount the buffer fill stays paused so
 * it doesn't pile onto the hot-reload's whole-list re-measure storm (see
 * bufferFillSettling). A developer scroll ends it sooner. Never bites a cold page load /
 * refresh, prod, or tests -- only a real HMR module swap arms it (see hmrRemount).
 */
const HMR_SETTLE_MS = 600

/**
 * True ONLY for the synchronous burst of component remounts Vite triggers right after an
 * HMR module swap -- the one case the buffer-fill settle window guards. Vite runs
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
 * Scroll speed (px/ms) at or above which a wheel/trackpad scroll counts as an
 * inertial FLING whose momentum a scrollTop write would cancel -- so the geometry
 * re-pin defers a small correction into flingSettle instead of writing it. Below
 * it the scroll is a slow DELIBERATE gesture: medium estimate corrections are
 * absorbed by re-anchoring to the live viewport, while large structural shifts
 * still write immediately. A fling fires events tens of px apart every ~16ms
 * (well above 1 px/ms); a deliberate scroll creeps a few px per event.
 * Unknown/idle velocity is treated as a fling (defer), so the prior always-defer
 * behavior holds until a slow cadence is established.
 */
const FLING_VELOCITY_THRESHOLD_PX_PER_MS = 1
/**
 * Render-ahead look-ahead window (ms) for the fling overscan: the rendered slice is
 * extended in the scroll direction by `velocity * this`, covering the distance the
 * viewport travels between the compositor scrolling a frame and the next range
 * update. Sized to a few frames of lag (scroll events can coalesce under load) while
 * keeping the cap small enough that an extreme flick cannot mount dozens of rows in
 * one synchronous scroll commit.
 *
 * NO-SKIP INVARIANT -- a fast fling STALLS, it never SKIPS a message. Three
 * mechanisms together guarantee every message in a fling's path is shown:
 *  1. The scrollable area IS the loaded window (the spacer is sized to
 *     virt.totalHeight() = the sum of loaded row heights), so the browser clamps
 *     scrollTop at the loaded edge -- a fling physically cannot scroll INTO
 *     not-yet-loaded history.
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
 * How long after a wheel/trackpad or touch-end gesture we consider asynchronous
 * geometry re-pins unsafe to write synchronously. Scroll events can trail the input
 * event by a few frames; keep the window comfortably above one fling-settle debounce
 * without letting unrelated later layout work inherit "momentum" semantics.
 */
const MOMENTUM_INPUT_GRACE_MS = 750
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
// Scroll-anomaly diagnostics: the detector thresholds and the pure Detector B
// classification live in chatScrollDiagnostics; this hook owns only the emission
// policy around them (payload assembly, burst suppression, rate limiting) and the
// shared 'chatScroll' channel they log to.
const scrollLog = createLogger('chatScroll')

/**
 * Rows the oldest-end trim must KEEP, counted from a given keep-from `anchor` (and its
 * resolved index `anchorIdx` in `msgs`, -1 when absent) down to the tail:
 *  - no anchor: 0, so the store applies the normal base cap. The sole production caller
 *    (the auto-scroll trim) never takes this path -- it derives a buffer-top anchor from
 *    scrollTop and maps an UNRESOLVABLE one to the whole window itself -- so this is just
 *    the neutral default for a null keep-from.
 *  - anchor set but UNRESOLVABLE in `msgs` (displaced / not present): the whole window,
 *    so no still-visible row is trimmed (the store clamps to the hard ceiling so memory
 *    stays bounded).
 *  - anchor resolvable: the SERVER rows from the anchor down to the tail. msgs carries
 *    trailing optimistic locals (seq 0n), but the store's oldest-end trim caps SERVER
 *    messages only and pins locals separately -- counting locals here would inflate the
 *    cap and over-retain old rows.
 */
export function computeKeepNewest(msgs: AgentChatMessage[], anchor: ScrollAnchor | null, anchorIdx: number): number {
  if (!anchor)
    return 0
  if (anchorIdx < 0)
    return msgs.length
  return msgs.slice(anchorIdx).filter(m => !isOptimisticLocal(m)).length
}

/**
 * The oldest-end trim's keep-newest count, derived from the live buffer geometry so a
 * tail append can't reap content the buffer filler surfaced ABOVE the viewport.
 *
 * The trim must KEEP the viewport PLUS the older pre-fetch buffer the filler maintains
 * above it -- otherwise every tail append reaps the rows the filler just fetched (or the
 * hidden-page auto-advance just surfaced) and the filler refetches them: a trim/refetch
 * loop in a hidden-heavy stream. Hidden rows have zero height, so `scrollTop` measures
 * the VISIBLE content above the viewport top; the keep-from anchor sits one buffer
 * (`bufferTargetPx`) above it. The result is a SUPERSET of the viewport span -- it can
 * only keep MORE rows, never reaping a visible one -- and the store clamps it to
 * [base, ceiling], so a NORMAL followed tail still trims to the lean base while a
 * hidden-heavy window holds what the filler surfaced up to the ceiling.
 *
 * Pure given its inputs: the caller passes live DOM reads and an (untracked) anchor
 * resolver, so the branch logic is unit-testable in isolation. `anchorAt(bufTop)`
 * returns the row at the buffer top, or null when the offset map can't locate it.
 */
export function computeBufferAwareKeepNewest(
  msgs: AgentChatMessage[],
  scrollTop: number,
  clientHeight: number,
  bufferTargetPx: number,
  anchorAt: (bufTop: number) => ScrollAnchor | null,
): number {
  // Hidden/inactive tab: the viewport and buffer can't be measured. Apply the lean base
  // cap (keepNewest 0); a later restore re-evaluates when the tab becomes visible.
  if (clientHeight === 0)
    return 0
  const bufTop = scrollTop - bufferTargetPx
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
  streamingText: Accessor<string>
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
}

export interface UseChatScrollResult {
  atBottom: Accessor<boolean>
  /** Fresh DOM-measured atBottom check (read this when the signal might be stale). */
  isAtBottomFresh: () => boolean
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
  /** Animated scroll-to-bottom (the in-window case behind {@link scrollToBottom}). */
  scrollToBottomAnimated: () => void
  /**
   * The floating scroll-to-bottom button's windowing-aware jump: forces a jump to
   * the latest page when windowed away from the live tail (the tail isn't loaded),
   * else an animated scroll. Owned here -- the hook holds the windowing state -- so
   * the view doesn't encode which primitive applies in which window state.
   */
  scrollToBottom: () => void
  /** Synchronous jump to bottom (used by the inline "show more" button). */
  jumpToBottom: () => void
  /**
   * Re-stick to the bottom if we were pinned there and content below the
   * viewport just grew. For tail content that is NOT a virtualizer row -- the
   * streaming-markdown block and the thinking indicator, rendered as siblings of
   * the virtual spacer -- whose growth no ResizeObserver here observes (the
   * virtualizer only measures rows, the scroll container's content-box is fixed).
   * ChatView calls this after that content renders. Position-only and idempotent,
   * so it is safe to call on every streaming frame.
   */
  restickIfAtBottom: () => void
  /** Capture the current viewport scroll position for tab-switch restoration. */
  getScrollState: () => ChatScrollState | undefined
  /** Imperative scroll-to-bottom (e.g. when the user sends a message). */
  forceScrollToBottom: () => void
  /** Page-wise scroll for keyboard navigation. */
  pageScroll: (direction: -1 | 1) => void
  /**
   * Pin the given row's top at its current viewport position so a user toggle that
   * changes THAT row's height (expand/collapse, diff-view switch) keeps it visually
   * stationary instead of scrolling it. Call immediately BEFORE applying the toggle,
   * while the DOM still reflects the pre-toggle geometry; the geometry re-pin the toggle
   * triggers then holds the row in place. See createAnchorRepin.captureRowTopAnchor.
   */
  anchorRowForResize: (messageId: string) => void
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

/**
 * Manages all scroll behavior for the chat message list:
 * sticky-bottom tracking, auto-scroll on new content, scroll anchoring when
 * older messages are prepended, viewport restoration on tab switch, and
 * load-older-on-overscroll for touch/wheel/keyboard.
 *
 * Attaches via ref callbacks so the same refs feed event handlers, effects,
 * and the imperative `getScrollState`/`forceScrollToBottom`/`pageScroll`
 * methods exposed on the result.
 */

export function useChatScroll(opts: UseChatScrollOptions): UseChatScrollResult {
  let messageListRef: HTMLDivElement | undefined

  const [atBottom, setAtBottom] = createSignal(true)
  // A monotonic tick bumped whenever the viewport's geometry (or its availability)
  // changes WITHOUT a signal of its own: every scroll event (a position-only edge
  // clamp/unclamp) and the ref (un)attaching (messageListRef is a plain ref, so a memo
  // that reads it can't react to it). The stall memos (stalledOlder / stalledNewer) read
  // the DOM fresh; this tick is the dependency that triggers that re-read.
  const [geomTick, bumpGeomTick] = createSignal(0)
  // Plain ref: only read inside `untrack` from the auto-scroll effect, never
  // as a reactive dependency.
  let preserveBrowsingPosition = false
  let scrollAnimationId: number | null = null
  let suppressAutoLoadOlderAfterRestore = false
  let autoScrollFirstSeq: bigint | undefined
  let autoScrollLastSeq: bigint | undefined
  let lastAutoScrollSig: unknown[] = []
  let lastTrimMessageCount: number | undefined
  let resizeObserver: ResizeObserver | undefined
  // resizeRafId / prevClientHeight now live inside createViewportRestore.
  // The scroll-buffer filler's fill(), assigned where the unit is created (after the
  // geometry re-pin effect, so its reactive pass runs once the re-pin has settled
  // scrollTop). handleScroll calls it through this forward ref to top up the buffer
  // for the new scroll position. A no-op until wired, so an early scroll event is
  // simply ignored.
  let fillScrollBuffer: () => void = () => {}
  // The fling-settle unit, assigned below. The anchor engine (createAnchorRepin, created
  // earlier) and createStickyBottom reference it lazily through thunks; flingSettle in turn
  // closes over the anchor engine's captureAnchor/currentAnchor -- a genuine definition
  // cycle (the engine's captureAnchor rebases flingSettle, flingSettle re-captures via the
  // engine). A single forward `let` (read only at call time, never before assignment)
  // breaks it without the old pair of reassigned `() => {}` stubs, which would silently
  // no-op if a future edit forgot to wire them.
  let flingSettle!: ReturnType<typeof createFlingSettle>

  const virt = opts.virtualizer
  // Distinguishes a fast fling (defer corrections to protect momentum) from a slow
  // deliberate wheel/trackpad scroll (correct immediately, no drift-then-settle).
  // handleScroll samples it on each real scroll event; repinToAnchor consults it.
  // Created before progGuard so the guard's onMark can feed it (below).
  const scrollVelocity = createScrollVelocity({
    // monotonicNow prefers perf.now() and falls back to Date.now() where `performance`
    // is absent (SSR / some test envs) -- never a constant 0, which would pin velocity
    // to its Infinity seed and make every scroll defer as a fling.
    now: monotonicNow,
    thresholdPxPerMs: FLING_VELOCITY_THRESHOLD_PX_PER_MS,
    idleMs: FLING_SETTLE_MS,
  })
  // Programmatic-scroll guard: recognizes our own scrollTop writes (by position)
  // so their echoing scroll events don't trigger pagination/fling handling.
  // Destructured to the verbose call-site names the hook already uses. onMark feeds
  // every programmatic write's landing position to the velocity tracker so a re-pin's
  // displacement is excluded from the user's measured gesture speed.
  const progGuard = createProgrammaticScrollGuard(
    () => messageListRef,
    pos => scrollVelocity.syncToProgrammatic(pos),
  )
  const isProgrammaticEcho = progGuard.isEcho

  // The user's most recent scroll/keyboard intent. Drives pagination when the
  // viewport can't scroll (content fits, e.g. an all-hidden window page), the
  // auto-advance through hidden-only pages, and stale-native-scroll translation
  // after large coordinate-space shifts.
  let lastScrollDir: 'older' | 'newer' = 'older'

  // The scrollTop observed on the previous scroll event, so handleScroll can infer the
  // direction from the position delta. handleWheel/handleKeyDown set lastScrollDir for
  // wheel/key input, but a scrollbar drag, touch scroll, or momentum fling fires only
  // `scroll` events -- this keeps lastScrollDir correct for those too. Kept in sync on our
  // own programmatic writes: writeScrollTopProgrammatically advances it to the written
  // position immediately (the write's echo event later re-affirms it without changing
  // direction), so the next user delta -- and the scroll-anomaly WARN -- measures from the
  // real current position, not a stale one, even when the echo is delivered late.
  let lastScrollTopForDir = 0
  // Timestamp (monotonicNow) of the previous scroll event, so Detector B can report the gap
  // since it. A large gap means scroll events STALLED -- a heavy measurement pass blocking
  // the main thread while momentum coasted -- after which the browser delivers one catch-up
  // delta that reads as an isolated jump. undefined until the first event.
  let lastScrollEventAt: number | undefined
  // The most recent native-scroll keydown (Space / arrows -- see KEYBOARD_SCROLL_GRACE_MS)
  // and Detector B's burst-suppression bookkeeping (see UNEXPLAINED_JUMP_REWARN_MS).
  // NEGATIVE_INFINITY so the first real event can't fall inside a grace window measured
  // from a zero epoch (performance.now() starts near 0 at page load).
  let lastNativeKeyScrollAt = Number.NEGATIVE_INFINITY
  const hasRecentKeyboardScroll = () => monotonicNow() - lastNativeKeyScrollAt <= KEYBOARD_SCROLL_GRACE_MS
  let lastUnexplainedJumpAt = Number.NEGATIVE_INFINITY
  let unexplainedJumpsSuppressed = 0
  // Detector C's rate-limit bookkeeping (see ANCHOR_DRIFT_REWARN_MS).
  let lastAnchorDriftWarnAt = Number.NEGATIVE_INFINITY
  let anchorDriftWarnsSuppressed = 0
  let anchorDriftSuppressedPxSum = 0

  const scrollDomDebugSnapshot = () => {
    const el = messageListRef
    if (!el)
      return undefined
    return {
      scrollTop: el.scrollTop,
      scrollHeight: el.scrollHeight,
      clientHeight: el.clientHeight,
      maxScrollTop: maxScrollTopOf(el),
      virtTotalHeight: virt.totalHeight(),
      rowCount: opts.messages().length,
      hasOlder: !!opts.hasOlderMessages?.(),
      hasNewer: !!opts.hasNewerMessages?.(),
      fetchingOlder: !!opts.fetchingOlder?.(),
      fetchingNewer: !!opts.fetchingNewer?.(),
    }
  }

  // True only while a USER scroll event is refreshing newly revealed rows (inside
  // handleScroll's refreshViewport). The geometry re-pin can fire synchronously
  // from same-flush geometry changes; while this is set it suppresses small
  // scrollTop corrections, because writing scrollTop mid-fling cancels the
  // browser's momentum and reads as a jump. Idle/async re-pins (post-mount
  // growth, prepend, trim, restore) leave it false and write normally.
  let userScrolling = false
  // The "a toggle row-top anchor is held; don't re-capture the midpoint" state now lives in
  // the anchor engine (isHoldingRowTop / releaseRowTopHold), with the anchor it guards --
  // see createAnchorRepin.captureRowTopAnchor and the handleScroll gate below.
  // Direct-manipulation input tracking: pointers currently down + whether a touch
  // is active. A scroll WHILE input is down is a DRAG (scrollbar thumb or finger)
  // with no inertia, so its re-pin correction applies immediately; a scroll with
  // NO input down is wheel/trackpad MOMENTUM (a fling), where writing scrollTop
  // cancels the browser's momentum. This is what lets the re-pin distinguish a
  // slow drag (correct immediately, no drift-then-snap) from a fling (defer).
  // Track the live pointer/touch state from the browser's authoritative lists
  // rather than a hand-balanced counter: a DROPPED pointerup/touchend (pointer-
  // capture transfer to another element, a browser gesture intercept swallowing
  // the up, an unmount mid-gesture) would otherwise leave a counter stuck above 0
  // for the session, latching isScrollInputActive() true and wedging fling-settle
  // off (every re-pin would then write mid-fling as if dragging). The pointer set
  // is keyed by pointerId (idempotent) and cleared when a PRIMARY pointer starts a
  // fresh gesture; touch state is read from event.touches.length, so a missed end
  // self-corrects on the next touch event and a multi-touch gesture stays active
  // while a finger remains.
  const activePointers = new Set<number>()
  let touchActive = false
  const isScrollInputActive = () => activePointers.size > 0 || touchActive
  let momentumInputUntil = 0
  const markMomentumInput = () => {
    momentumInputUntil = monotonicNow() + MOMENTUM_INPUT_GRACE_MS
  }
  const clearMomentumInput = () => {
    momentumInputUntil = 0
  }
  const hasRecentMomentumInput = () => monotonicNow() <= momentumInputUntil
  // The clamped target scrollTop of an in-flight keyboard PageUp/PageDown (null =
  // none). A discrete page is NOT a momentum fling: the geometry re-pin it
  // triggers must apply immediately (so the page lands where the user can already
  // read it), never deferred into flingSettle's accept-and-reanchor path. Matched
  // by POSITION rather than "the next scroll event" so an unrelated fling that
  // interleaves before the page's own native scroll event can't consume it.
  let discretePageTarget: number | null = null
  let viewportRefreshRafId = 0
  // Safari/WebKit rubber-band overscroll can expose transient negative/over-max
  // scrollTop values on read. Treat those as native edge physics, not list
  // coordinates: anchor resolution, viewport ranges, velocity, and saved state must
  // operate in the clamped scroll coordinate space without writing to the DOM.
  const readLogicalScrollTop = (el: HTMLDivElement) => clampScrollTop(el, el.scrollTop)

  // Stale-native-scroll translator: recognizes a compositor-delayed OLD-COORDINATE
  // momentum event after a large anchor re-pin and rewrites it into the current
  // coordinate space (see createStaleNativeScrollTranslator). Fed every programmatic
  // write below; consulted at the top of handleScroll.
  const staleNativeTranslator = createStaleNativeScrollTranslator({
    getEl: () => messageListRef,
    isScrollInputActive,
    isProgrammaticEcho,
    setLastScrollTopForDir: (top) => { lastScrollTopForDir = top },
  })

  const writeScrollTopProgrammatically = (top: number, source?: string) => {
    const el = messageListRef
    const beforeTop = el?.scrollTop
    const beforeClientHeight = el?.clientHeight ?? 0
    progGuard.write(top, source)
    const afterTop = el?.scrollTop
    // Arm/extend/disarm the stale-native shift record for this write (an 'anchor-repin'
    // larger than a screen arms it; other sources invalidate it).
    staleNativeTranslator.noteProgrammaticWrite({
      source,
      beforeTop,
      afterTop,
      clientHeight: beforeClientHeight,
      dir: lastScrollDir,
    })
    // Advance the direction / last-position baseline to where we just moved. handleScroll
    // otherwise only re-syncs it from a programmatic write's ECHO scroll event -- but the
    // browser can deliver that echo LATER than the programmatic guard's echo-marker TTL
    // (~150ms) when the main thread is busy, e.g. a keep-position re-pin during a long
    // older-page prepend re-measuring a huge list. A late echo is then no longer matched
    // as ours, and measured against the stale pre-write baseline it reads as a large
    // unexplained user jump: a spurious scroll-anomaly WARN and a mis-inferred scroll
    // direction. Syncing at write time makes the baseline reflect our own move
    // immediately, so a delayed echo shows ~0 delta and a genuine later user scroll
    // measures from the real current position. The large-prepend case stays correct too:
    // its stale-native momentum events are recognized via the translator's shift record
    // (armed above) and translated in handleScroll, independent of this baseline.
    if (afterTop !== undefined)
      lastScrollTopForDir = afterTop
  }

  // The anchor + re-pin engine (scroll-mode state machine + the keep-position re-pin),
  // extracted into its own unit (createAnchorRepin). It owns scrollMode / anchorCaptureTop
  // / repinning / the deferred-during-animation flag; the hook keeps the orthogonal scroll
  // guards. Built here from raw deps (not scrollCtx) so ScrollContext below can delegate to
  // its anchor accessors without a creation cycle. `flingSettle` is read through a thunk:
  // the engine's captureAnchor and the fling-settle unit close over each other (a genuine
  // definition cycle the forward `let flingSettle` breaks).
  const anchorRepin = createAnchorRepin({
    getEl: () => messageListRef,
    virt,
    isAnimating: () => scrollAnimationId !== null,
    writeScrollTop: writeScrollTopProgrammatically,
    velocity: {
      isFling: () => scrollVelocity.isFling(),
      isActivelyFlinging: () => scrollVelocity.isActivelyFlinging() && hasRecentMomentumInput(),
      hasRecentMomentumInput,
    },
    flingSettle: () => flingSettle,
    isUserScrolling: () => userScrolling,
    hasNewerMessages: () => !!opts.hasNewerMessages?.(),
    readScrollTop: readLogicalScrollTop,
    // Detector A: a keep-position re-pin clamped against a scroll boundary, so the
    // anchored row jumped by the clamp amount. WARN only when the shift is visible AND
    // more history still exists that direction (clampPx > 0 -> clamped at the top, so
    // older content above would have held the row; < 0 -> clamped at the bottom, newer
    // content below would have). A clamp at a genuinely exhausted edge is expected --
    // there is nothing left to reveal, so the row MUST move -- and stays silent.
    onRepinClamp: (info) => {
      const hasHistoryThatDirection = info.clampPx > 0
        ? !!opts.hasOlderMessages?.()
        : !!opts.hasNewerMessages?.()
      if (hasHistoryThatDirection && Math.abs(info.clampPx) >= VISIBLE_ANCHOR_JUMP_PX) {
        scrollLog.warn('anchor re-pin clamped at a loaded edge -- anchored row jumped', {
          ...info,
          clampedAt: info.clampPx > 0 ? 'top' : 'bottom',
          dom: scrollDomDebugSnapshot(),
        })
      }
    },
    // Detector C (outcome-based): the re-pin left the anchored row displaced by
    // residualPx instead of correcting it -- a content shift that produces NO scroll
    // event, so Detector B can't see it. Only an ABSORBED shift is reported: it is a
    // permanent, user-visible displacement. A 'deferred-fling' shift is transient by
    // construction -- the engine defers only under a live (or cold-seed presumed) fling
    // and the fling-settle re-anchors it once momentum stops -- and warning on it
    // produced ONLY cold-start false positives: on a gesture's first-ever event the
    // velocity tracker's Infinity seed makes the engine defer (isFling true) while
    // isActivelyFlinging still reads false, so every such benign one-shot warned.
    // Surface absorbed shifts above the visible floor, skip the fast-fling frames
    // (isActivelyFlinging -- the shift blends into momentum there), and rate-limit the
    // rest (see ANCHOR_DRIFT_REWARN_MS): what survives is an aggregate of the shifts
    // the reader perceives while scrolling slowly or just after stopping.
    onAnchorDrift: (info) => {
      if (info.reason !== 'absorbed'
        || Math.abs(info.residualPx) < ANCHOR_DRIFT_WARN_PX
        || scrollVelocity.isActivelyFlinging()) {
        return
      }
      const now = monotonicNow()
      if (now - lastAnchorDriftWarnAt <= ANCHOR_DRIFT_REWARN_MS) {
        anchorDriftWarnsSuppressed += 1
        anchorDriftSuppressedPxSum += info.residualPx
        return
      }
      lastAnchorDriftWarnAt = now
      scrollLog.warn('anchored content drifted without correction', {
        ...info,
        // Absorbed shifts rate-limited away since the previous WARN (count + signed sum),
        // so the aggregate drift is still visible in the emitted stream.
        suppressedSinceLastWarn: anchorDriftWarnsSuppressed,
        suppressedResidualPxSum: Math.round(anchorDriftSuppressedPxSum),
        // The measurement that moved the geometry this re-pin absorbed: firstMeasure +
        // delta tell whether it was an estimate->real correction (per-kind median off /
        // outran premeasure) or a re-measure (premeasured-vs-visible mismatch), and
        // whether this single commit's delta accounts for residualPx or a batch did.
        measurement: virt.lastMeasurement(),
        dom: scrollDomDebugSnapshot(),
      })
      anchorDriftWarnsSuppressed = 0
      anchorDriftSuppressedPxSum = 0
    },
  })
  const { currentAnchor, currentAnchorState, isFollowing, isHoldingRowTop, releaseRowTopHold, followTail, setAnchor, captureAnchor, captureTopAnchor, captureRowTopAnchor, repinToAnchor } = anchorRepin

  // Fast-scroll flag for the fling skeletons (virt.setFastScrollActive): set on
  // any genuine user scroll at fling velocity — momentum AND direct drags — and
  // cleared by this trailing debounce, because a scrollbar/touch drag has no
  // fling-settle to clear it (the settle is armed for momentum scrolls only).
  // FLING_SETTLE_MS matches the velocity tracker's own idle window, so the flag
  // drops as soon as the tracker would stop reporting a fling anyway.
  let fastScrollResetTimer: ReturnType<typeof setTimeout> | undefined
  const clearFastScrollReset = () => {
    if (fastScrollResetTimer !== undefined) {
      clearTimeout(fastScrollResetTimer)
      fastScrollResetTimer = undefined
    }
  }
  const armFastScrollReset = () => {
    clearFastScrollReset()
    fastScrollResetTimer = setTimeout(() => {
      fastScrollResetTimer = undefined
      virt.setFastScrollActive(false)
    }, FLING_SETTLE_MS)
  }
  onCleanup(clearFastScrollReset)

  const hasDeferredMeasurements = () => virt.hasDeferredMeasurements()
  const releaseDeferredMeasurements = () => {
    clearMomentumInput()
    virt.setVisibleMeasurementDeferral(false)
    // The fast scroll is over (fling settled, or the user took manual control):
    // upgrade any skeleton rows now instead of waiting out the debounce.
    clearFastScrollReset()
    virt.setFastScrollActive(false)
    virt.flushDeferredMeasurements()
  }
  const acceptDeferredMeasurementsAtCurrentViewport = () => {
    if (hasDeferredMeasurements())
      captureAnchor()
    releaseDeferredMeasurements()
  }

  const cancelScrollAnimation = () => {
    if (scrollAnimationId !== null) {
      cancelAnimationFrame(scrollAnimationId)
      scrollAnimationId = null
    }
    // Apply a keep-position re-pin that arrived DURING the animation: the animation is
    // ending here without landing at the bottom (a mid-flight cancel), so the shift it
    // deferred (a prepend/trim above the anchor) must be absorbed now rather than left as
    // a jump until the next scroll/geometry event. A no-op when nothing was deferred.
    anchorRepin.applyDeferredRepinOnCancel()
  }

  /** Fresh DOM measurement — true if the scroll position is at/near the bottom. */
  const isAtBottom = () =>
    !!messageListRef && distFromBottom(messageListRef) < STICKY_BOTTOM_THRESHOLD_PX

  /**
   * Fresh DOM measurement — true only when the scroll position is at the GENUINE
   * clamped bottom (within a re-pin's no-op delta), not merely inside the 32px
   * sticky band. Used to gate RE-ENGAGING tail-follow from an anchored (scrolled-up)
   * state: a small downward scroll toward a freshly-trimmed, now-shorter bottom must
   * not snap to the live tail. Staying sticky while ALREADY following keeps using the
   * looser isAtBottom, so a sub-pixel gap mid-stream can't drop the follow.
   */
  const isAtClampedBottom = () =>
    !!messageListRef && distFromBottom(messageListRef) <= REPIN_MIN_DELTA_PX

  /**
   * At the LIVE tail: the loaded window's bottom IS the real tail (no newer rows
   * beyond the window) and the viewport sits inside the 32px sticky band of it.
   * The single definition behind handleScroll's two follow re-engage gates and the
   * older-prefetch suppression, so the sites can't drift on what counts as the
   * live tail (atBottom must imply following ONLY here -- a windowed-away loaded
   * bottom is not the tail, and following it would chase every newer append).
   */
  const isAtLiveTail = () => !opts.hasNewerMessages?.() && isAtBottom()

  /**
   * Hard against the very TOP / BOTTOM edge, within EDGE_INTENT_TOLERANCE_PX -- the 1px
   * edge-intent band (see chatScrollGeometry), NOT the looser 32px sticky band. The one
   * home for the read each edge uses (the clamped logical scrollTop at the top;
   * distFromBottom at the bottom), shared by the explicit edge-intent loaders, the
   * stall indicators, and the overscroll-drag gate so they can't drift asymmetrically.
   */
  const isAtTopEdge = () =>
    !!messageListRef && readLogicalScrollTop(messageListRef) <= EDGE_INTENT_TOLERANCE_PX
  const isAtBottomEdge = () =>
    !!messageListRef && distFromBottom(messageListRef) <= EDGE_INTENT_TOLERANCE_PX

  /**
   * Recompute the rendered row slice for the current scroll position. `lead`
   * extends the rendered slice ahead in the fling direction (see handleScroll) so a
   * fast scroll paints the rows it is about to reach; correction/settle callers pass
   * none and get the symmetric overscan.
   */
  const refreshViewport = (lead?: ViewportLead) => {
    if (messageListRef)
      virt.updateViewport(readLogicalScrollTop(messageListRef), messageListRef.clientHeight, lead)
  }

  /**
   * Recompute the slice on a rAF, NOT synchronously. Mounting/unmounting rows
   * (setRange) observes/unobserves them on the virtualizer's ResizeObserver,
   * and a fresh observe() fires a notification. If that happens inside the RO
   * delivery cycle (which the synchronous geometry re-pin runs within), it
   * re-enters delivery in the same frame → "ResizeObserver loop completed with
   * undelivered notifications". Deferring the mount to the next frame breaks
   * that cycle; the scroll-position correction stays synchronous (see
   * repinToAnchor) so the wiggle fix is preserved.
   */
  const scheduleViewportRefresh = () => {
    if (typeof requestAnimationFrame !== 'function') {
      refreshViewport()
      return
    }
    cancelAnimationFrame(viewportRefreshRafId)
    viewportRefreshRafId = requestAnimationFrame(() => refreshViewport())
  }

  // The shared STABLE scroll primitives threaded through the extracted scroll helpers
  // (see ScrollContext). Built once here from values already in scope -- including the
  // anchor engine's accessors (isFollowing / followTail / setAnchor) so the helpers and
  // ScrollContext share one anchor source. Each helper takes this plus a small `extras`
  // bag of its own deps -- the remaining high-level composite actions (checkAtBottom /
  // stickToBottom / forceScrollToBottom) are declared further down and wired into the
  // late helpers' extras where they are in scope, rather than forward-referenced here.
  const scrollCtx: ScrollContext = {
    getEl: () => messageListRef,
    virt,
    atBottom,
    setAtBottom,
    isAtBottom,
    isFollowing,
    isAnimating: () => scrollAnimationId !== null,
    followTail,
    refreshViewport,
    writeScrollTop: writeScrollTopProgrammatically,
    syncVelocityToProgrammatic: pos => scrollVelocity.syncToProgrammatic(pos),
    setAnchor,
  }

  // Sticky-bottom record + re-stick logic (see createStickyBottom). It owns the
  // "last clamped bottom" record; the hook borrows its three public operations.
  // dropDeferredFlingDrift is passed lazily (it's wired to flingSettle.reset
  // below, after this unit is created).
  const stickyBottom = createStickyBottom(scrollCtx, {
    clearPreserveBrowsingPosition: () => { preserveBrowsingPosition = false },
    dropDeferredFlingDrift: () => flingSettle.reset(),
  })
  const { stickToBottom, shouldRestickToBottom, restickIfAtBottom } = stickyBottom

  // Fling-settle: accumulates the re-pin corrections deferred mid-fling and
  // accepts them by re-anchoring to the current viewport once momentum stops.
  // Assigns the forward `let` above; stickyBottom (drop-on-stick) and the anchor engine's
  // captureAnchor (rebase) reach it through their thunks now that it exists.
  flingSettle = createFlingSettle(scrollCtx, {
    isRepinning: anchorRepin.isRepinning,
    getAnchor: currentAnchor,
    captureAnchor,
    hasDeferredWork: hasDeferredMeasurements,
    onSettleQuiet: releaseDeferredMeasurements,
  })

  // True between a forced stop (grab/tap) and the user's next genuine scroll: pauses
  // the buffer fill so a stop also stops the older/newer pre-fetch ("stop = stop
  // loading too"). handleScroll clears it on the next non-echo scroll, so a fling or
  // a slow scroll resumes pre-fetching; the explicit wheel/key-at-edge loads are NOT
  // gated by it, so the user can always still page at the very edge.
  let bufferFillPaused = false

  // DEV-SERVER-ONLY: a short settle window after a genuine HMR REMOUNT (hmrRemount --
  // false on a cold page load / refresh and in prod/tests) during which the buffer fill
  // stays paused. A hot reload re-renders the WHOLE message list at once -> every row
  // re-measures in a burst -> totalHeight churns for ~a second; pre-fetching on top of
  // that churn is what makes the view drift on its own after an HMR. Holding the
  // pre-fetch until the geometry settles (or the developer scrolls -- handleScroll
  // clears it too) keeps the reload quiet. Gated on hmrRemount, NOT a blanket DEV check:
  // arming it on every mount also paused the cold-load pre-fetch, and -- since the timer
  // that clears it is a non-reactive write that never re-kicks fill() -- left a
  // hidden-heavy newest page stranded half-empty on refresh until a scroll / new message.
  let bufferFillSettling = hmrRemount

  // Halt all PROGRAMMATIC scroll: a running scroll animation AND the deferred
  // fling-settle (which would otherwise write scrollTop ~FLING_SETTLE_MS after the
  // last momentum event -- a drift the view keeps moving by even after momentum has
  // stopped). Also pauses the buffer-fill pre-fetch until the next scroll. Called
  // when the user takes manual control (taps, touches, or sends a momentum-cancel
  // wheel) so a forced stop halts the view -- and the loading -- IMMEDIATELY rather
  // than coasting through one more settle / pre-fetch page. cancel() kills the
  // pending timer; reset() drops the accumulated drift so a later gesture's settle
  // can't apply this one's.
  const cancelPendingScroll = () => {
    cancelScrollAnimation()
    flingSettle.cancel()
    flingSettle.reset()
    acceptDeferredMeasurementsAtCurrentViewport()
    bufferFillPaused = true
  }

  const checkAtBottom = () => {
    if (!messageListRef || scrollAnimationId !== null)
      return
    // Stale scroll event after content grew: the browser reports the
    // pre-growth scrollTop matching our last sticky record. Re-stick to
    // the new visual bottom rather than dropping atBottom.
    if (shouldRestickToBottom()) {
      stickToBottom()
      return
    }
    setAtBottom(isAtBottom())
  }

  // ---- Pagination: maintain a VISIBLE-content buffer beyond the viewport ----
  //
  // Only VISIBLE rows have scroll height: scrollTop is the visible content ABOVE the
  // viewport, distFromBottom the visible content BELOW. In a hidden-heavy stretch a
  // raw page adds almost no visible height, so a "load when within half a screen of
  // the edge" policy thrashed -- the viewport sat permanently in both near-edge bands
  // and every scroll event paginated. Instead, pre-fetch AHEAD: keep
  // SCROLL_BUFFER_SCREENS screens of visible content on each side. loadOlderMessages
  // / loadNewerPage now grow the window to the raw CEILING (chat.store), so the
  // buffer actually accumulates rather than sliding. The fill loops through hidden
  // runs until the buffer is full, history is exhausted, or the raw window ceiling
  // stops growth; createScrollBufferFiller owns that loop.
  const bufferTargetPx = () =>
    messageListRef ? SCROLL_BUFFER_SCREENS * messageListRef.clientHeight : 0

  const canLoadOlderMessages = () =>
    !!messageListRef && !!opts.hasOlderMessages?.() && !opts.fetchingOlder?.()

  // Fire an older-history fetch (prepend). preserveBrowsingPosition keeps the
  // anchored row stationary across the prepend -- the re-pin absorbs the growth
  // ABOVE the viewport, so the buffer fills invisibly. Callers gate on position
  // (explicit top-intent) or buffer deficit (the filler); this just fires.
  const loadOlderMessages = () => {
    if (!canLoadOlderMessages())
      return
    preserveBrowsingPosition = true
    opts.onLoadOlderMessages?.()
  }

  // Wheel/key/overscroll at the very top: an explicit request to page older. Returns
  // whether it fired (the overscroll-drag tracker consumes this). Clears the
  // one-shot post-restore suppression so a deliberate reach-the-top always pages.
  const tryLoadOlderOnExplicitTopIntent = (): boolean => {
    if (!isAtTopEdge() || !canLoadOlderMessages())
      return false
    suppressAutoLoadOlderAfterRestore = false
    loadOlderMessages()
    return true
  }

  const canLoadNewerMessages = () =>
    !!messageListRef && !!opts.hasNewerMessages?.() && !opts.fetchingNewer?.()

  const loadNewerMessages = () => {
    if (!canLoadNewerMessages())
      return
    opts.onLoadNewerMessages?.()
  }

  const tryLoadNewerOnExplicitBottomIntent = (): boolean => {
    if (!isAtBottomEdge() || !canLoadNewerMessages())
      return false
    loadNewerMessages()
    return true
  }

  // ---- Stall indicators ----
  //
  // TRUE only when the view is clamped HARD against a loaded edge (within
  // EDGE_INTENT_TOLERANCE_PX -- the same 1px edge the explicit edge-intent loaders use,
  // NOT the looser 32px sticky band) AND a fetch for that direction is in flight AND
  // more history exists that way. This is the NO-SKIP stall: a scroll outran the
  // pre-fetch buffer, so the loaded-window scroll bound is holding the view at the edge
  // until the page lands -- the only time a "loading older/newer" indicator should show.
  // A BACKGROUND buffer pre-fetch fires while the view is still well inside the buffer
  // (scrollTop / distFromBottom >> the edge tolerance), so it never trips these.
  //
  // Read the DOM fresh on each eval, gated by geomTick (a position-only move or a ref
  // (un)mount bumps it) plus the reactive fetch / has-more accessors -- short-circuited
  // so the DOM is only measured while a fetch is actually in flight. Fresh reads, rather than a cached
  // at-edge boolean, mean the next fetch in a buffer-fill chain re-measures against the
  // GROWN window: a background page that lands and immediately re-fetches can't flash a
  // stale "stalled" once the view is no longer at the edge.
  const stalledOlder = createMemo(() => {
    geomTick()
    return !!opts.fetchingOlder?.() && !!opts.hasOlderMessages?.() && isAtTopEdge()
  })
  const stalledNewer = createMemo(() => {
    geomTick()
    return !!opts.fetchingNewer?.() && !!opts.hasNewerMessages?.() && isAtBottomEdge()
  })

  /**
   * Classify a scroll event AND consume the one-shot discrete-page target. Returns
   * whether the event is our own programmatic write echoing back, and whether it is
   * a genuine momentum (fling) scroll -- the only kind the synchronous re-pin defers
   * as drift and the only kind that (re)arms the fling-end settle, so the two stay in
   * lockstep behind one predicate.
   */
  const classifyScrollEvent = (forceUserScroll = false): {
    programmaticEcho: boolean
    isMomentumScroll: boolean
    discretePage: boolean
  } => {
    // A scroll event landing at the exact pixel we last wrote is our own programmatic
    // scroll echoing back, not a user gesture -- so it has no momentum to protect and
    // must not suppress the re-pin.
    const programmaticEcho = !forceUserScroll && isProgrammaticEcho()
    // Is THIS event the in-flight keyboard PageUp/PageDown's own scroll? Match by
    // position (its clamped target), not "the next scroll event", so an unrelated
    // fling interleaving before the page's native event isn't mistaken for it. A
    // discrete page is a user gesture for direction/pagination but NOT a fling.
    const discretePage = discretePageTarget !== null && !!messageListRef
      && Math.abs(readLogicalScrollTop(messageListRef) - clampScrollTop(messageListRef, discretePageTarget)) <= EDGE_INTENT_TOLERANCE_PX
    // Consume the page target on the FIRST scroll event after scrollBy, whether or
    // not it matched. clampScrollTop tracks the browser's clamp, but a
    // measurement-induced scrollHeight shift between scrollBy and its native event
    // can still land the browser's clamped scrollTop >1px from the re-clamped
    // target and miss the <=1px match above. Clearing only on a match would strand
    // the target so a LATER unrelated fling landing within 1px of it is mis-handled
    // as a discrete page (re-pinned immediately, cancelling its momentum); bounding
    // it to one event closes that. The trade -- a fling event interleaving BEFORE
    // the page's own native event clears it early, deferring the page's re-pin as
    // fling drift -- is benign and rarer than the strand it fixes.
    // Exempt a programmatic ECHO, though: pageScroll moves via scrollBy (never
    // writeScrollTopProgrammatically), so the page's OWN native event is never an
    // echo -- but an unrelated re-pin write's echo can interleave between scrollBy
    // and that native event. Consuming the target on the echo would strand the
    // page's real event as deferred fling drift; skip the clear for echoes so the
    // page's native event still matches. (A fling is not an echo, so its early-clear
    // trade above is unchanged.)
    if (discretePageTarget !== null && !programmaticEcho)
      discretePageTarget = null
    // A genuine MOMENTUM scroll: not our own programmatic echo, not a discrete page,
    // and not direct pointer/touch manipulation (a drag has no inertia to protect --
    // see isScrollInputActive).
    const isMomentumScroll = !programmaticEcho && !discretePage && !isScrollInputActive()
    return { programmaticEcho, isMomentumScroll, discretePage }
  }

  /**
   * The per-scroll-event USER-GESTURE bookkeeping plus the render-ahead lead it feeds.
   * For a genuine (non-echo) scroll this TRACKS the gesture -- resumes a paused buffer
   * fill, samples velocity, updates lastScrollDir, and clears the post-restore older
   * suppression once the viewport leaves the protected band -- then derives the lead:
   * the slice extension in the fling direction so a fast momentum scroll paints the
   * rows it is about to reach (see refreshViewport / computeRange -- mechanism #3 of
   * the NO-SKIP INVARIANT). A programmatic echo tracks nothing (its sample would
   * register as a huge instantaneous jump) and yields no lead; the direction baseline
   * is advanced for every event so the next user delta measures from the current
   * position.
   */
  const trackUserScrollAndComputeLead = (st: number, programmaticEcho: boolean): ViewportLead | undefined => {
    let lead: ViewportLead | undefined
    if (!programmaticEcho) {
      // A genuine user scroll resumes the buffer-fill pre-fetch a forced stop OR the
      // dev HMR settle window paused (see cancelPendingScroll / bufferFillSettling):
      // the user is scrolling again, so top the buffer back up. Cleared BEFORE the
      // fillScrollBuffer() at the end of handleScroll so that fill runs.
      bufferFillPaused = false
      bufferFillSettling = false
      // Sample velocity from this real (non-echo) scroll so the synchronous re-pin
      // can tell a fling from a slow scroll. Our own programmatic writes are excluded
      // -- they'd register as huge instantaneous jumps.
      scrollVelocity.sample(st)
      const dir = inferScrollDirection(lastScrollTopForDir, st)
      if (dir)
        lastScrollDir = dir
      // Clear the post-restore older-suppression once the user scrolls OUT of the
      // near-top band the restore landed in -- the same clientHeight/2 band
      // armSuppressIfNearTop arms within. Below that band (scrolled down away from the
      // top) an older prepend is absorbed by the anchor re-pin without disturbing the
      // reader, so older pre-fetch can safely resume; staying within the band keeps it
      // suppressed until an explicit scroll-to-top (tryLoadOlderOnExplicitTopIntent,
      // which clears it with its own load). Without this the older buffer stayed gated
      // after a near-top restore until scrollTop reached the very top, stalling a later
      // fling-up.
      //
      // ALSO clear it when the user actively scrolls UP and reaches the very top edge:
      // that IS the explicit scroll-to-top intent. The wheel/key/touch handlers clear it
      // via tryLoadOlderOnExplicitTopIntent, but a scrollbar-THUMB drag fires only
      // `scroll` events (no wheel/touch/key), so without this a drag up to the top while
      // suppressed would leave the older buffer gated and the reader unable to page up
      // (the filler pages older on the next deficit pass once the gate clears). Gated on
      // dir==='older' so a STATIONARY scroll event at an already-restored top (no
      // movement -> dir null) keeps the suppression -- a restore-to-top landing must not
      // auto-load older on a spurious passive scroll (suppressesPassiveOlderLoad...).
      const reachedTopScrollingUp = dir === 'older' && st <= EDGE_INTENT_TOLERANCE_PX
      if (messageListRef && (!isNearTopBand(messageListRef, st) || reachedTopScrollingUp))
        suppressAutoLoadOlderAfterRestore = false
      // Scale the render-ahead by the just-sampled speed (0 while idle / on the cold
      // Infinity seed -> no lead), capped so an extreme flick can't mount an unbounded
      // slice. Head in THIS event's resolved direction -- not the stale lastScrollDir:
      // a zero-delta event (coalesced same-tick samples at a clamped plateau during a
      // fling reversal / rubber-band bounce) keeps a nonzero sampled speed but leaves
      // `dir` null, and trusting lastScrollDir there would paint overscan on the side
      // the viewport just LEFT, flashing an unrendered gap on the side it now heads to.
      const capPx = messageListRef ? flingOverscanCapPx(messageListRef.clientHeight) : 0
      const px = dir ? Math.min(capPx, scrollVelocity.speed() * FLING_OVERSCAN_LOOKAHEAD_MS) : 0
      if (px > 0 && dir)
        lead = { dir, px }
    }
    lastScrollTopForDir = st
    return lead
  }

  const handleScroll = () => {
    // Re-arm the stall memos: this position move may have clamped the view against (or
    // freed it from) a loaded edge, which is invisible to them otherwise (scrollTop has
    // no signal). Cheap -- the memos short-circuit on the fetch flags before measuring.
    bumpGeomTick(t => t + 1)
    // Snapshot THIS event's own echo marker BEFORE handling it: if checkAtBottom
    // re-sticks below (a fresh programmatic write), it arms another marker whose echo is
    // still pending, so we name this event's marker now and consume only it below.
    const echoGen = progGuard.matchedEchoGen()
    const staleNativeScrollTranslated = staleNativeTranslator.translate()
    // Scroll mode BEFORE the capture block below can flip it: a teleport that LANDS in
    // the bottom sticky band re-engages follow inside this very handler, and Detector B
    // must classify the delta against the PRE-event mode or that teleport excuses itself
    // (see classifyUnexplainedJump's tailFollowToBottom).
    const wasFollowingBeforeEvent = isFollowing()
    // Pin the anchor to the user's CURRENT scroll position BEFORE refreshViewport
    // mounts any newly-revealed row. That mount can measure a row far taller than
    // its estimate, which shifts the offset map and synchronously fires the
    // geometry re-pin (the createEffect flushes within the setRange update). The
    // re-pin reads `anchor`, so it has to already hold where the user scrolled to;
    // resolving it AFTER the mount — against the shifted map, with a not-yet-
    // corrected scrollTop — pins a far-away row and the re-pin yanks the view to
    // it. That is the jump when a message taller than the viewport scrolls into
    // view at the top.
    //
    // Captured on EVERY scroll event (including our own programmatic writes) so a
    // fast scroll-up whose events land inside the programmatic-write window
    // doesn't leave a stale anchor for the next re-pin to yank back. At the bottom we
    // follow the tail (sticky) instead of anchoring; captureAnchor itself is edge-aware
    // and pins the top row (not the midpoint) at the very top (see captureViewportAnchor).
    if (messageListRef) {
      // Re-engage tail-follow whenever the viewport is within the 32px sticky band of
      // the live tail -- the SAME band the `atBottom` signal uses -- so `atBottom`
      // always implies following (no anchored-but-atBottom hybrid where the
      // scroll-to-bottom affordance hides yet growth won't restick). A reader sitting
      // slightly above the bottom is treated as following and auto-sticks on the next
      // append.
      //
      // Gated on the genuine LIVE tail (no more newer to load): when windowed AWAY from
      // the tail (hasNewerMessages), the loaded window's bottom is NOT the tail, so
      // following it would let a surviving sticky record drive restickIfAtBottom to
      // CHASE every newer append to the bottom -- the view runs far past where the user
      // stopped while a mostly-hidden newer run loads. Stay ANCHORED there instead;
      // newer paginates in the background and the user scrolls into it, with follow
      // re-engaging once they reach the real tail (hasNewerMessages false). That
      // guard also covers a load-older trim that drops the newest rows: it sets
      // hasNewerMessages, so the now-shorter bottom can't spuriously snap to follow.
      //
      // followTail only sets the MODE -- it does NOT snap to the bottom, so a reader
      // resting a few px above the tail keeps their position (no yank). The next
      // content GROW sticks to the bottom (shouldRestickToBottom fires within the band),
      // which is the "auto-scroll when sitting slightly above the bottom" behavior.
      // Hold an active toggle-anchor across its own re-pin's echo scroll events: a user
      // toggle pinned a SPECIFIC row's top, and re-capturing the viewport-midpoint anchor
      // here would discard it, so the resize's next phase re-pins against the midpoint and
      // jumps (see createAnchorRepin's ScrollMode.origin 'row-top').
      if (!isHoldingRowTop()) {
        if (isAtLiveTail())
          followTail()
        else
          captureAnchor()
      }
      else if (!isProgrammaticEcho()) {
        // A genuine (non-echo) scroll moved the viewport WHILE a toggle row-top hold is
        // armed. Wheel/key/touch/pointer gestures release the hold before their scroll
        // event arrives (see handlers below), so what reaches here fires only `scroll`
        // events: a scrollbar-thumb drag (no pointer events in Firefox), a momentum
        // coast, or the browser force-clamping scrollTop when the toggled row SHRANK.
        // Keep holding the SAME row but re-pin it at its NEW viewport line -- the resize
        // then keeps growing below a line the user actually sees, and the next geometry
        // commit's re-pin cannot yank the drag back to the stale toggle-time line. When
        // the held row's top has left the viewport a row-top pin is unrepresentable
        // (captureRowTopAnchor bails), so release and fall back to the normal
        // follow/midpoint capture.
        const heldId = currentAnchor()?.id
        if (heldId === undefined || !captureRowTopAnchor(heldId)) {
          releaseRowTopHold()
          if (isAtLiveTail())
            followTail()
          else
            captureAnchor()
        }
      }
    }
    const scrollTopAtStart = messageListRef ? readLogicalScrollTop(messageListRef) : undefined
    // Same-epoch geometry for Detector B: refreshViewport's row mounts can grow
    // scrollHeight in this very flush, so maxScrollTop must be read HERE, alongside
    // scrollTopAtStart, not live at classification time -- else the positions and the
    // range they're compared against belong to different coordinate epochs.
    const maxScrollTopAtStart = messageListRef ? maxScrollTopOf(messageListRef) : 0
    const lastScrollTopBeforeEvent = lastScrollTopForDir
    // Whether a fling was ALREADY in progress as of the PREVIOUS scroll event -- captured
    // before trackUserScrollAndComputeLead (below) samples THIS event's velocity. A trackpad momentum
    // coast fires scroll events with no fresh wheel input, so its input grace
    // (hasRecentMomentumInput) lapses after 750ms even while it is still moving; the velocity
    // tracker, fed by those scroll events, still knows it is flinging. Detector B uses this
    // to excuse the coast. Read PRE-sample deliberately: a genuine teleport's OWN event
    // samples a huge speed, so the post-sample isActivelyFlinging would mask the very jump we
    // want -- but a teleport from rest was NOT flinging on the prior event, so this stays false.
    const wasActivelyFlingingBeforeEvent = scrollVelocity.isActivelyFlinging()
    // Diagnostic timing for the Detector B WARN: the gap since the previous scroll event
    // (a stall vs a steady cadence) and the tracker's PRE-sample speed (0 once idle). Read
    // before trackUserScrollAndComputeLead samples this event, for the same reason as the fling flag.
    const nowMs = monotonicNow()
    const msSinceLastScrollEvent = lastScrollEventAt === undefined ? undefined : nowMs - lastScrollEventAt
    lastScrollEventAt = nowMs
    const speedBeforeEvent = scrollVelocity.speed()
    // Genuine user scrolls mark the window so the synchronous re-pin (fired while
    // refreshViewport mounts+measures rows) suppresses its scrollTop write; an echo
    // or a discrete page leaves userScrolling false so it writes immediately rather
    // than deferring ~150ms into flingSettle.
    const { programmaticEcho, isMomentumScroll, discretePage } = classifyScrollEvent(
      staleNativeScrollTranslated,
    )
    // Defer the re-pin as fling drift only for a momentum scroll.
    userScrolling = isMomentumScroll
    // Render-ahead overscan for this scroll (undefined for echoes and idle), read
    // BEFORE refreshViewport / checkAtBottom can move scrollTop.
    const viewportLead = scrollTopAtStart !== undefined
      ? trackUserScrollAndComputeLead(scrollTopAtStart, programmaticEcho)
      : undefined
    // Fling-skeleton gate: unlike the measurement deferral below (momentum-only
    // — a drag re-pins immediately by design), rows entering during ANY fast
    // user scroll mount as skeletons, scrollbar/touch drags included.
    if (!programmaticEcho && scrollVelocity.isFling()) {
      virt.setFastScrollActive(true)
      armFastScrollReset()
    }
    if (isMomentumScroll && scrollVelocity.isFling())
      virt.setVisibleMeasurementDeferral(true)
    // Time the synchronous render cascade this scroll triggers: setting the range mounts
    // newly-visible rows AND runs the premeasure computed (rendering the hidden look-ahead
    // rows) in the same flush. If that blows the frame budget, it stalls the scroll loop --
    // the batched catch-up delta a later event reports as an unexplained jump. Only a slow
    // pass logs (see warnSlowScrollPhase).
    const refreshStart = monotonicNow()
    try {
      refreshViewport(viewportLead)
    }
    finally {
      userScrolling = false
    }
    warnSlowScrollPhase('refreshViewport', monotonicNow() - refreshStart, { rows: opts.messages().length })
    // For a genuine MOMENTUM scroll, (re)arm the fling-end settle: each event
    // pushes the debounce out, so it fires once momentum stops and re-anchors to
    // the visual position the user actually reached. A programmatic echo has no
    // fling to settle (and its own write would re-arm it in a loop); a discrete
    // page and a direct pointer/touch drag both re-pinned immediately above
    // (userScrolling false), so they deferred nothing to settle.
    if (isMomentumScroll)
      flingSettle.schedule()
    // checkAtBottom owns the stale-scroll-event re-stick that keeps sticky-bottom
    // robust; it runs after refreshViewport so it sees the post-mount scrollHeight.
    // A re-stick here (stickToBottom) overrides the anchor set above within the
    // same synchronous handler, so there is no intermediate paint.
    checkAtBottom()
    // Re-engage follow within the 32px sticky band of the LIVE tail -- the same band
    // the start-of-handler gate uses, so `atBottom` always implies following. NOT a
    // windowed-away loaded bottom (hasNewerMessages): there the loaded bottom isn't the
    // tail, so following would CHASE every newer append past where the user stopped.
    // checkAtBottom's own shouldRestickToBottom handles a real grow-while-at-bottom
    // restick first; this catches the case where the user scrolled back into the band.
    if (untrack(atBottom) && isAtLiveTail()) {
      followTail()
      preserveBrowsingPosition = false
    }
    // Pagination, on the other hand, must not fire from our own scroll writes.
    // Recognize our write by position: a scroll event at the exact pixel we last
    // wrote is ours; one elsewhere is the user — even mid-write-burst, where a
    // frame-delayed flag would still be set and would swallow the gesture.
    const stillEcho = !staleNativeScrollTranslated && isProgrammaticEcho()
    // This event has been fully handled. If it matched our marker (here or at the
    // read above), consume that marker so a LATER user gesture coincidentally
    // landing within 1px of the same pixel is treated as a real scroll, not a
    // second echo. Skipped when a re-stick armed a fresher marker (gen advanced).
    if (programmaticEcho || stillEcho)
      progGuard.consumeEcho(echoGen)
    if (staleNativeScrollTranslated)
      progGuard.mark('stale-native-scroll-translate')
    // Detector B: warn on an unexplained teleport between two consecutive scroll events (see
    // classifyUnexplainedJump for the full exclusion list and why deliberate scrolling never
    // trips it).
    if (messageListRef && scrollTopAtStart !== undefined) {
      const { deltaFromLast, isUnexplained } = classifyUnexplainedJump({
        scrollTopAtStart,
        lastScrollTopBeforeEvent,
        maxScrollTopAtStart,
        programmaticEcho,
        stillEcho,
        discretePage,
        staleNative: staleNativeScrollTranslated,
        wasActivelyFlingingBeforeEvent,
        wasFollowingBeforeEvent,
        scrollInputActive: isScrollInputActive(),
        recentMomentumInput: hasRecentMomentumInput(),
        recentKeyboardScroll: hasRecentKeyboardScroll(),
      })
      if (isUnexplained) {
        // Burst suppression (sliding window): consecutive unexplained deltas within
        // UNEXPLAINED_JUMP_REWARN_MS of each other are one gesture (a scrollbar drag,
        // which fires only `scroll` events and cannot be excluded by input state) or one
        // pathological storm -- WARN once at the head with a count of what followed.
        const withinBurst = nowMs - lastUnexplainedJumpAt <= UNEXPLAINED_JUMP_REWARN_MS
        lastUnexplainedJumpAt = nowMs
        if (withinBurst) {
          unexplainedJumpsSuppressed += 1
        }
        else {
          scrollLog.warn('unexpected scroll jump (no known cause)', {
            deltaFromLast,
            scrollTop: scrollTopAtStart,
            lastScrollTop: lastScrollTopBeforeEvent,
            // Timing/velocity context to tell a momentum-after-stall (large gap + a coast the
            // input grace outlived) from a genuine teleport (small gap, no fling). measurement
            // is the last geometry commit -- a coinciding one points at a render/shift cause
            // (e.g. a focus-driven scrollIntoView) rather than pure momentum.
            msSinceLastScrollEvent,
            speedPxPerMs: speedBeforeEvent,
            wasActivelyFlinging: wasActivelyFlingingBeforeEvent,
            // Unexplained events burst-suppressed since the previous emitted WARN.
            suppressedSinceLastWarn: unexplainedJumpsSuppressed,
            measurement: virt.lastMeasurement(),
            markers: progGuard.debugMarkers(),
            dom: scrollDomDebugSnapshot(),
          })
          unexplainedJumpsSuppressed = 0
        }
      }
    }
    if (stillEcho)
      return
    // Top up the visible buffer for the new scroll position (scrolling toward an
    // edge shrinks that side's buffer). The restore-arm suppression is applied
    // INSIDE the filler (older side only, see suppressOlder) rather than skipping the
    // whole fill here -- otherwise a quiescent thread scrolled toward its loaded
    // bottom would never top up the newer side and would stall short of the tail.
    // Forward ref: the filler is created after the geometry re-pin effect so its own
    // reactive pass runs after the re-pin settles.
    fillScrollBuffer()
  }

  // Touch/pointer overscroll-at-top → load older history. Self-contained gesture
  // tracker; the drag math lives in createOverscrollDrag.
  const overscroll = createOverscrollDrag({
    atTop: isAtTopEdge,
    onDragAtTop: tryLoadOlderOnExplicitTopIntent,
  })

  // Re-pin the viewport after any geometry change — older-message prepend,
  // window trim, or a row-height measurement shifting the offset map. The
  // spacer is sized to virt.totalHeight() so scrollHeight tracks geometry, but
  // we re-resolve the anchor explicitly because a programmatic scroll write
  // doesn't emit a reliable scroll event.
  //
  // We depend on BOTH totalHeight and geometryVersion: totalHeight catches
  // list mutations (prepend/trim, which don't bump geometryVersion), while
  // geometryVersion catches measurements that shift offsets without changing
  // the total (e.g. a row above the anchor grows while one below shrinks) —
  // totalHeight alone would miss those and let the anchored row jump.
  // `defer:true` so the first render doesn't re-pin before anything has been
  // measured.
  createEffect(on([() => virt.totalHeight(), () => virt.geometryVersion()], () => {
    // Correct scrollTop synchronously (same paint as the row transforms — no
    // wiggle), but defer the slice recompute to the next frame so mounting rows
    // never re-enters the in-progress ResizeObserver delivery (no RO loop).
    // Both branches are position-only (no mount): repin keeps a scrolled-up
    // anchor stationary; restick follows the growing bottom when pinned there.
    repinToAnchor()
    // Release the toggle row-top hold once the toggled row's resize has settled -- i.e. its
    // real height has committed again (a toggle changes the row's heightKey, so it goes
    // unmeasured -> measured; hasMeasuredHeight is false through the estimate phase and true
    // once the measurement lands). The hold exists ONLY to survive that transition (see
    // captureRowTopAnchor); left armed it would linger until the next wheel/key/touch/pointer
    // gesture, and an intervening SCROLLBAR-THUMB drag -- which fires only `scroll` events, no
    // pointer event to release it -- would leave the stale row-top pin for the next geometry
    // change to yank the viewport back to. currentAnchor().id IS the held row while holding;
    // releasing clears only the re-capture guard (the anchor stays until the next capture), so
    // any later async re-measure of the same row still keeps it pinned until the user scrolls.
    if (isHoldingRowTop()) {
      const heldId = currentAnchor()?.id
      if (heldId !== undefined && virt.hasMeasuredHeight(heldId))
        releaseRowTopHold()
    }
    restickIfAtBottom()
    // Clear the post-restore older-load suppression once a trim or a measurement
    // shrink has made the content fit (no scrollable range). The same clear lives in
    // createViewportRestore's resize flush, but a window trim / row re-measure changes
    // virt.totalHeight()/geometryVersion() WITHOUT resizing the container box or
    // emitting a scroll event, so neither handleResize nor handleScroll would otherwise
    // run -- wedging older-history pre-fetch off forever (hasOlderMessages stays true but
    // the unscrollable viewport never pages up). Guarded on the flag so this fires only
    // when something is actually suppressed.
    if (messageListRef && suppressAutoLoadOlderAfterRestore && maxScrollTopOf(messageListRef) <= 0)
      suppressAutoLoadOlderAfterRestore = false
    scheduleViewportRefresh()
  }, { defer: true }))

  // Recompute the rendered slice whenever the message list changes (added,
  // removed, prepended, or trimmed) so newly-visible rows mount even when no
  // scroll event or height change fired. This is driven by store updates, not
  // ResizeObserver delivery, so mounting rows synchronously here is safe.
  createEffect(on(() => opts.messages(), () => {
    refreshViewport()
  }, { defer: true }))

  // Suppress the SPECULATIVE older-history pre-fetch while the viewport is pinned at the
  // live tail. The older buffer only earns its keep once the reader scrolls UP into
  // history; at the bottom it is pure speculation. Pre-fetching it there turned a fresh
  // mount at the bottom (a page reload or an HMR remount) into a runaway: the filler saw
  // scrollTop far below the older buffer target, paged older, prepended a page mid
  // re-measure storm, and that prepend stream fought tail-follow and dragged the view
  // up-list -- which kept scrollTop low, so the older side stayed "deficient" and it paged
  // EVERY page to the window ceiling. Suppressing it holds the view at the bottom (the
  // storm is then only the newest page's own row measurements, which tail-follow absorbs)
  // and stops the every-page fetch. The reader's first genuine scroll-up leaves the tail
  // (distFromBottom grows past the sticky band, so isAtBottom() goes false) and resumes
  // the pre-fetch via handleScroll -> fillScrollBuffer; the NO-SKIP loaded-window bound
  // turns that first page's latency into a one-time stall, never a skip.
  //
  // EXCEPTION: a viewport that can't scroll OUT of the sticky band (maxScrollTop <=
  // STICKY_BOTTOM_THRESHOLD_PX -- a hidden-heavy newest page whose few visible rows
  // barely fill the pane, or don't fill it at all) still needs the older fill to become
  // genuinely scrollable. Without it that page stays stranded half-empty until a new
  // message -- the very bug the blanket dev-mount settle once reintroduced (see
  // hmrRemount). The bound is the STICKY BAND, not 0: with 0 < maxScrollTop <= the band,
  // isAtBottom() is true at EVERY scroll position, so the "first scroll-up leaves the
  // tail and resumes the fill" escape above is mathematically unreachable and the older
  // history would be wedged off for good (a scrollbar-thumb drag to the very top fires
  // only `scroll` events, so the explicit wheel/key edge-intent loaders never run
  // either). Only once the pane can actually LEAVE the band does the suppression bite.
  const suppressOlderPrefetchAtLiveTail = (): boolean =>
    !!messageListRef
    && maxScrollTopOf(messageListRef) > STICKY_BOTTOM_THRESHOLD_PX
    && isAtLiveTail()

  // Pre-fetch a VISIBLE-content buffer beyond the viewport so scrolling stays smooth
  // in hidden-heavy stretches (see createScrollBufferFiller). Self-contained reactive
  // unit; wired here so its createEffect runs AFTER the geometry re-pin /
  // messages-refresh effects above (it reads the post-re-pin scrollTop / distFromBottom).
  const bufferFiller = createScrollBufferFiller({
    getEl: () => messageListRef,
    messages: opts.messages,
    bufferTargetPx,
    hasOlder: () => !!opts.hasOlderMessages?.(),
    hasNewer: () => !!opts.hasNewerMessages?.(),
    fetchingOlder: () => !!opts.fetchingOlder?.(),
    fetchingNewer: () => !!opts.fetchingNewer?.(),
    // The filler's own loads go through loadOlderMessages/loadNewerMessages so they
    // share preserveBrowsingPosition, not the raw store callbacks.
    onLoadOlder: loadOlderMessages,
    onLoadNewer: loadNewerMessages,
    lastScrollDir: () => lastScrollDir,
    paused: () => bufferFillPaused || bufferFillSettling,
    // Gate the older PRE-FETCH on either a near-top restore's one-shot arm OR being pinned
    // at the live tail (where the older buffer is speculative -- see
    // suppressOlderPrefetchAtLiveTail). Both leave the newer side free.
    suppressOlder: () => suppressAutoLoadOlderAfterRestore || suppressOlderPrefetchAtLiveTail(),
    atCeiling: () => !!opts.atWindowCeiling?.(),
    // Both sides' progress is measured against a stable reference row, not raw
    // scrollTop/distFromBottom, so a mid-fetch scroll in either direction can't mask a
    // productive load (see captureAnchor). anchorAt(scrollTop) is the viewport-top row;
    // scrollTopForAnchor reads its content-coordinate offset (= height ABOVE it) from the
    // offset map, and totalHeight - that is the height BELOW it. (This also makes the old
    // fling-deferred-write correction unnecessary -- the offset reflects a prepend
    // regardless of a deferred scrollTop write.)
    captureAnchor: () => messageListRef ? virt.anchorAt(readLogicalScrollTop(messageListRef)) : null,
    contentAbove: anchor => virt.scrollTopForAnchor(anchor),
    contentBelow: (anchor) => {
      const above = virt.scrollTopForAnchor(anchor)
      return above == null ? null : virt.totalHeight() - above
    },
  })
  // handleScroll tops up the buffer for the new scroll position through this ref.
  fillScrollBuffer = bufferFiller.fill

  // Expose scroll state for viewport save on tab switch. Anchored to the
  // viewport-top row (by seq) so restoration survives the spacer's estimated
  // height — a raw distance-from-bottom would resolve to the wrong place.
  const getScrollState = (): ChatScrollState | undefined => {
    if (!messageListRef || messageListRef.clientHeight === 0)
      return undefined
    const atBot = atBottom()
    const scrollTop = readLogicalScrollTop(messageListRef)
    const a = atBot ? null : virt.anchorAt(scrollTop)
    // Scrolled away from the bottom but no resolvable anchor means the virtual
    // list is empty (e.g. an all-hidden window). There is no estimated spacer
    // here (totalHeight is 0), so a clamped raw scrollTop carries no drift risk
    // -- save it as a fallback rather than losing the position to bottom-follow
    // on return. (A non-zero scrollTop comes from non-virtual content: a tall
    // streaming block or startup banner.)
    const rawScrollTop = !atBot && a === null ? scrollTop : undefined
    return {
      anchor: a ?? undefined,
      rawScrollTop,
      atBottom: atBot,
      hasMoreNewer: !!opts.hasNewerMessages?.(),
    }
  }

  const forceScrollToBottom = () => {
    cancelScrollAnimation()
    // A deliberate jump-to-bottom is the user re-taking control of position, exactly
    // like a genuine scroll: resume the pre-fetch a prior forced-stop tap paused. It
    // otherwise clears only on a non-echo scroll, and a jump emits only programmatic
    // echoes -- so without this a stop-then-jump leaves the buffer fill wedged off.
    bufferFillPaused = false
    bufferFillSettling = false
    // A jump-to-bottom also abandons any near-top restore landing, so its older-load
    // suppression no longer applies -- clear it (a window REPLACE on the hasNewer path
    // below invalidates it outright; even a same-window stick is a deliberate move off
    // the protected top). Mirrors the bufferFiller.rearm() below for per-window state.
    suppressAutoLoadOlderAfterRestore = false
    // Scrolled away from the live tail: re-fetch the latest page first, then
    // snap. jumpToLatest replaces the window; the totalHeight re-pin effect and
    // the sticky auto-scroll then land us at the true bottom.
    if (opts.hasNewerMessages?.()) {
      // The jump REPLACES the window, so drop any per-window filler state the old
      // window accumulated.
      bufferFiller.rearm()
      // Optimistically follow the tail, but remember the pre-jump anchor: no
      // scroll write happens until the jump resolves, so on failure (worker
      // disconnect / RPC error) we restore the anchor and re-sync atBottom to
      // the real scroll position rather than stranding the view anchorless at a
      // tail it never reached. The rejection is swallowed (no unhandled promise).
      const prevAnchorState = currentAnchorState()
      followTail()
      setAtBottom(true)
      void Promise.resolve(opts.onJumpToLatest?.())
        .then(() => {
          // Honor a mid-flight user scroll: if the user scrolled up while the jump was
          // in flight, handleScroll captured an anchor and cleared atBottom, so don't
          // yank them back to the live tail.
          if (!untrack(atBottom))
            return
          // The jump replaced the window with the latest page, but a live append that
          // landed DURING the in-flight jump can leave the window short of the live
          // tail again (hasNewerMessages true). Sticking to the loaded bottom would
          // then pin the view to a NON-live-tail bottom while still claiming
          // atBottom=true. Stick only when we are truly at the live tail; otherwise
          // re-sync atBottom to the real position so the scroll-to-bottom affordance
          // reappears instead of being hidden behind a stale atBottom.
          if (opts.hasNewerMessages?.())
            checkAtBottom()
          else
            stickToBottom()
        })
        .catch(() => {
          // A mid-flight user scroll already captured the user's newer anchor and cleared
          // atBottom. Do not restore the stale pre-jump anchor over it.
          if (!untrack(atBottom)) {
            checkAtBottom()
            return
          }
          // The jump failed and no scroll write landed, so we never reached the
          // tail. Restore the pre-jump anchor; if there wasn't one (we were
          // following), re-anchor to the CURRENT scroll position rather than
          // re-entering 'following' at a tail we never reached -- otherwise
          // checkAtBottom could leave atBottom true while hasMoreNewer stays set,
          // hiding the scroll-to-bottom affordance and stranding the user above
          // the live messages.
          if (prevAnchorState)
            setAnchor(prevAnchorState.anchor, undefined, prevAnchorState.viewportOffsetRatio)
          else
            setAnchor(messageListRef ? virt.anchorAt(readLogicalScrollTop(messageListRef)) : null)
          checkAtBottom()
        })
      return
    }
    stickToBottom()
  }

  // The keyboard / wheel input layer (createScrollInput). Owns no scroll state -- the
  // shared `lastScrollDir` / `discretePageTarget` stay here (handleScroll reads them)
  // and are written through the setters below; forceScrollToBottom and the load-on-edge
  // helpers stay here too (other units / the API also use them) and are passed in.
  const { handleKeyDown, handleWheel, pageScroll } = createScrollInput(scrollCtx, {
    captureAnchor,
    captureTopAnchor,
    checkAtBottom,
    forceScrollToBottom,
    cancelScrollAnimation,
    cancelPendingScroll,
    tryLoadOlderOnExplicitTopIntent,
    tryLoadNewerOnExplicitBottomIntent,
    setLastScrollDir: (dir) => { lastScrollDir = dir },
    setDiscretePageTarget: (target) => { discretePageTarget = target },
    hasOlderMessages: () => !!opts.hasOlderMessages?.(),
    // jump-to-oldest (Home) REPLACES the window with the earliest page; re-arm the
    // buffer-filler side-selection state and drop the post-restore older-suppression
    // (it was armed against the OLD window's landing). Mirrors forceScrollToBottom's
    // jump-to-latest handling.
    onJumpToOldest: opts.onJumpToOldest
      ? () => {
          bufferFiller.rearm()
          suppressAutoLoadOlderAfterRestore = false
          return opts.onJumpToOldest!()
        }
      : undefined,
  })

  // Auto-scroll the message list to the bottom when new content arrives
  // and the user is already at (or near) the bottom.
  // messageVersion covers thread merges (tool_use_result merged into an
  // existing tool_use) which don't change messages.length.
  createEffect(() => {
    const msgs = opts.messages()
    // Track the SERVER head's seq (firstServerSeq), not msgs[0].seq: a prepend is
    // the server head moving EARLIER. A local landing at index 0 (the server range
    // emptied to a pending local) would read as seq 0n -- smaller than any server seq
    // -- and spuriously trip prependedOlderMessages, suppressing the bottom auto-stick
    // when a new tail message arrives in the same update. undefined (not 0n) for an
    // all-locals window keeps that case out of the comparison below.
    const firstSeq = firstServerSeq(msgs)
    const newestSeq = lastServerSeq(msgs)
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
      opts.streamingText().length,
      opts.agentWorking?.() ?? false,
      // -1 (outside the AgentStatus enum, which starts at 0) so "no accessor" can't
      // alias UNSPECIFIED(0) -- a transition between undefined and UNSPECIFIED would
      // otherwise leave the signature unchanged and could swallow a banner scroll.
      opts.agentStatus?.() ?? -1,
      opts.hasNewerMessages?.() ?? false,
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
    // Use the atBottom signal (not a fresh DOM check) because by the time
    // this effect runs, SolidJS has already updated the DOM — scrollHeight
    // has grown but scrollTop hasn't, so a fresh measurement would wrongly
    // conclude the user is no longer at the bottom. The signal captures
    // the user's scroll position from before the content changed.
    // Windowed away from the live tail: live messages are dropped from the
    // store, the bottom of the in-memory list isn't the real bottom, and the
    // streaming/thinking UI is hidden — so never auto-stick here.
    if (opts.hasNewerMessages?.())
      return
    // Cap the in-memory window on every tail append, INDEPENDENT of scroll
    // position. While still at the live tail (hasNewerMessages false) but
    // scrolled up, live messages append to the tail and this is the ONLY window
    // cap -- the pagination trims fire only on overscroll, and the at-bottom
    // stick path below is skipped. Without this an active tab watched from a
    // scrolled-up position grows the in-memory set unbounded during a long
    // stream. Only run the raw-row cap when the message count changed: streaming text,
    // status, or message-version wake-ups can need sticky-bottom handling below, but
    // they do not increase the number of rows and must not reap the older buffer.
    const messageCountChanged = lastTrimMessageCount !== msgs.length
    lastTrimMessageCount = msgs.length
    if (messageCountChanged && messageListRef) {
      // Buffer-top anchor resolution reads the offset map (reactive) -- untrack it so
      // this auto-scroll effect doesn't subscribe to every geometry nudge. The rest of
      // the keep-newest derivation is pure; see computeBufferAwareKeepNewest.
      const keepNewest = computeBufferAwareKeepNewest(
        msgs,
        readLogicalScrollTop(messageListRef),
        messageListRef.clientHeight,
        bufferTargetPx(),
        bufTop => untrack(() => virt.anchorAt(bufTop)),
      )
      opts.onTrimOldMessages?.(keepNewest)
    }
    if (untrack(atBottom) && !preserveBrowsingPosition && messageListRef) {
      // Skip scroll when hidden (e.g. inactive tab with display:none).
      // The ResizeObserver will scroll to bottom when the tab becomes visible.
      if (messageListRef.clientHeight === 0)
        return
      // Stick only when the viewport is NOT already pinned to the real bottom --
      // streaming chunks fire this effect every frame even when the view didn't move.
      // Gating on the live position (not scrollHeight vs the last stick) is robust to
      // a trim+grow that lands back on an identical scrollHeight: the new tail still
      // pushed content below the fold, so we're no longer at the bottom and must
      // stick. Scroll synchronously (the DOM is already updated by SolidJS) so
      // deferring to rAF can't paint one frame scrolled up before snapping to bottom.
      // When we ARE at the bottom, skip the costly stick (write + slice refresh) but
      // keep the sticky record current via restickIfAtBottom -- it also SEEDS the
      // record on the first run, which a later growth's re-stick guard needs.
      if (!isAtClampedBottom())
        stickToBottom()
      else
        stickyBottom.restickIfAtBottom()
    }
  })

  // Re-check atBottom after the parent clears saved scroll state.
  // Restoration itself is handled exclusively by the ResizeObserver's
  // hidden→visible path so that we avoid a race where this effect runs
  // before the tab is actually hidden (clearing saved state too early).
  let savedScrollRecheckRaf = 0
  createEffect(on(
    () => opts.savedViewportScroll?.(),
    (saved) => {
      if (!saved && messageListRef && messageListRef.clientHeight > 0) {
        // Cancel any prior pending re-check and store the handle so onCleanup can
        // cancel it -- otherwise a tile disposed between scheduling and firing
        // would still run checkAtBottom against a detached element.
        cancelAnimationFrame(savedScrollRecheckRaf)
        savedScrollRecheckRaf = requestAnimationFrame(() => checkAtBottom())
      }
    },
  ))

  onCleanup(() => {
    cancelScrollAnimation()
    cancelAnimationFrame(viewportRefreshRafId)
    cancelAnimationFrame(savedScrollRecheckRaf)
    flingSettle.cancel()
  })

  // Saved-viewport restore + resize handling, extracted into createViewportRestore
  // (a factory like createStickyBottom / createFlingSettle). The hook keeps owning the
  // mutable state the restore writes -- suppressAutoLoadOlderAfterRestore (the buffer
  // filler reads it) and lastScrollTopForDir (trackUserScrollAndComputeLead reads it) -- and
  // passes setters; the factory owns only the resize bookkeeping (prevClientHeight,
  // the pending rAF).
  const viewportRestore = createViewportRestore(scrollCtx, {
    checkAtBottom,
    repinToAnchor,
    stickToBottom,
    forceScrollToBottom,
    clearSavedViewportScroll: () => opts.onClearSavedViewportScroll?.(),
    savedViewportScroll: () => opts.savedViewportScroll?.(),
    setSuppressOlder: (v) => { suppressAutoLoadOlderAfterRestore = v },
    setLastScrollTopForDir: (v) => { lastScrollTopForDir = v },
    onGeometrySettled: () => bumpGeomTick(t => t + 1),
  })

  // Observe ONLY the scroll container (editor/window/keyboard resize, and
  // hidden→visible). We deliberately do NOT observe the content wrapper: under
  // virtualization its height is the spacer, which the geometry re-pin keeps
  // changing — observing it would re-enter ResizeObserver delivery every time a
  // row measures and trip "ResizeObserver loop completed with undelivered
  // notifications". Content-height growth (streaming, expand/collapse, async
  // syntax highlighting) is handled instead by the virtualizer's own row
  // observer via the totalHeight effect (restickIfAtBottom) and by the
  // auto-scroll effect (streaming/thinking).
  onMount(() => {
    viewportRestore.initClientHeight()
    // Render the initial slice for the freshly-measured viewport.
    refreshViewport()
    resizeObserver = new ResizeObserver(viewportRestore.handleResize)
    if (messageListRef)
      resizeObserver.observe(messageListRef)
    // End the dev-only HMR settle window once the post-HMR-remount re-measure storm
    // has had time to quiesce (no-op unless this mount is a genuine HMR remount -- so
    // never on a cold load, in prod, or in tests). A developer scroll clears it sooner.
    let settleTimer: ReturnType<typeof setTimeout> | undefined
    if (bufferFillSettling && typeof setTimeout === 'function')
      settleTimer = setTimeout(() => { bufferFillSettling = false }, HMR_SETTLE_MS)
    onCleanup(() => {
      viewportRestore.cancelPendingResize()
      resizeObserver?.disconnect()
      resizeObserver = undefined
      if (settleTimer !== undefined)
        clearTimeout(settleTimer)
    })
  })

  const scrollToBottomAnimated = () => {
    if (!messageListRef)
      return
    cancelScrollAnimation()

    // Bound the chase: a target that grows every frame (active streaming) would
    // otherwise keep `remaining >= 1` forever and spin the rAF. Once the budget
    // is spent we hand off to sticky-bottom, which pins the current bottom and
    // follows further growth via the geometry re-stick / auto-scroll effects.
    let framesLeft = SCROLL_TO_BOTTOM_MAX_FRAMES
    const animate = () => {
      if (!messageListRef) {
        scrollAnimationId = null
        anchorRepin.resetDeferredRepin()
        return
      }
      const remaining = distFromBottom(messageListRef)
      if (remaining < 1 || framesLeft <= 0) {
        scrollAnimationId = null
        // The natural end lands at the bottom (stickToBottom), absorbing any shift the
        // re-pin deferred during the animation -- so drop the deferred flag.
        anchorRepin.resetDeferredRepin()
        stickToBottom()
        return
      }
      framesLeft--
      const step = remaining > 48 ? remaining * 0.5 : remaining * 0.4
      // Mark each frame's write as programmatic so its echoing scroll event is
      // recognized as ours -- not processed by handleScroll as a user gesture
      // (which would capture an anchor, infer a direction, and dispatch edge
      // pagination on every animation frame).
      writeScrollTopProgrammatically(readLogicalScrollTop(messageListRef) + Math.ceil(step), 'scroll-bottom-animation')
      scrollAnimationId = requestAnimationFrame(animate)
    }

    scrollAnimationId = requestAnimationFrame(animate)
  }

  const jumpToBottom = () => {
    // Cancel any in-flight animated scroll first (like forceScrollToBottom /
    // scrollToBottomAnimated do) so the next animation frame can't keep chasing the
    // bottom from a stale position after this synchronous pin.
    cancelScrollAnimation()
    stickToBottom()
  }

  // The floating scroll-to-bottom button's jump. While windowed away from the live
  // tail (hasNewerMessages) the loaded bottom isn't the real tail, so jump to the
  // latest page; otherwise the tail is in the window and a smooth animated scroll
  // suffices. Kept here, where the windowing state lives, rather than branched in the
  // view.
  const scrollToBottom = () => {
    if (opts.hasNewerMessages?.())
      forceScrollToBottom()
    else
      scrollToBottomAnimated()
  }

  // The public API's page-scroll (the shell's focus-hotkey path in TileRenderer). The
  // container's own onKeyDown wrapper releases a toggle row-top hold before paging; give
  // the direct API entry point the same semantics so the two paths can't diverge -- a
  // page jump is a user gesture taking control of position, and paging with the hold
  // armed would skip the anchor re-capture its scroll event needs.
  const pageScrollReleasingHold = (direction: -1 | 1) => {
    releaseRowTopHold()
    pageScroll(direction)
  }

  // A user toggle (expand/collapse, diff-view switch) pins the toggled row's top so its own
  // resize keeps it visually stationary; captureRowTopAnchor arms the hold across the resize's
  // re-pin echoes (see createAnchorRepin). Skip it while FOLLOWING the live tail: switching to
  // 'anchored' would freeze auto-scroll (shouldRestickToBottom / restickIfAtBottom both require
  // isFollowing()), so streamed/appended content would stop sticking until the user scrolled.
  // The toggled row is at/near the bottom there anyway, so the resize rides the tail instead.
  const anchorRowForResize = (messageId: string) => {
    if (!isFollowing())
      captureRowTopAnchor(messageId)
  }

  return {
    atBottom,
    isAtBottomFresh: isAtBottom,
    stalledOlder,
    stalledNewer,
    attachListRef: (el) => {
      messageListRef = el
      // The stall memos read messageListRef (a plain ref they can't track) and were
      // eagerly computed at hook-construction time while it was still undefined. Bump
      // the geometry tick so they re-evaluate now that the element exists (and again if
      // it detaches), instead of staying latched at their initial value.
      bumpGeomTick(t => t + 1)
    },
    scrollToBottomAnimated,
    scrollToBottom,
    jumpToBottom,
    restickIfAtBottom,
    getScrollState,
    forceScrollToBottom,
    pageScroll: pageScrollReleasingHold,
    anchorRowForResize,
    handlers: {
      onScroll: handleScroll,
      onWheel: (event) => {
        releaseRowTopHold()
        if (!event.ctrlKey && Math.abs(event.deltaY) > Math.abs(event.deltaX) && event.deltaY !== 0)
          markMomentumInput()
        handleWheel(event)
      },
      onKeyDown: (event) => {
        releaseRowTopHold()
        // Space and the arrow keys scroll NATIVELY (createScrollInput only nudges edge
        // pagination for arrows and ignores Space), and Tab can trigger the browser's
        // focus scrollIntoView when the next focusable sits in an off-screen row -- so
        // their scroll events carry no other input signal. Record the keydown so Detector
        // B attributes the resulting move to the keyboard instead of warning on every
        // Space page or focus jump (see classifyUnexplainedJump).
        if (!event.altKey && !event.ctrlKey && !event.metaKey
          && (event.key === ' ' || event.key === 'ArrowUp' || event.key === 'ArrowDown' || event.key === 'Tab')) {
          lastNativeKeyScrollAt = monotonicNow()
        }
        handleKeyDown(event)
      },
      // Touch/pointer handlers also feed the direct-manipulation tracker
      // (isScrollInputActive) so a scroll during a drag re-pins immediately
      // rather than deferring as fling drift; the overscroll unit owns the
      // drag-at-top-to-load-older gesture.
      onTouchStart: (event) => {
        releaseRowTopHold()
        // The user grabbed the surface -- stop any coasting programmatic scroll now.
        cancelPendingScroll()
        // Read the live touch list (authoritative) rather than latching a boolean,
        // so a dropped touchend self-heals on the next touch event.
        touchActive = event.touches.length > 0
        overscroll.onTouchStart(event)
      },
      onTouchMove: overscroll.onTouchMove,
      onTouchEnd: (event) => {
        // event.touches is the set of STILL-active touches after this one lifted:
        // 0 for the last finger (-> inactive), >0 while another finger remains.
        touchActive = event.touches.length > 0
        if (!touchActive)
          markMomentumInput()
        overscroll.onTouchEnd()
      },
      onTouchCancel: (event) => {
        touchActive = event.touches.length > 0
        if (!touchActive)
          clearMomentumInput()
        overscroll.onTouchEnd()
      },
      onPointerDown: (event) => {
        releaseRowTopHold()
        // A tap/press to stop a fling -- halt the deferred settle / animation now.
        cancelPendingScroll()
        // A primary pointer begins a fresh gesture with no sibling pointers down,
        // so any ids still tracked here had their up/cancel dropped -- clear them
        // before adding this one so the set can't latch active across gestures.
        if (event.isPrimary)
          activePointers.clear()
        activePointers.add(event.pointerId)
        overscroll.onPointerDown(event)
      },
      onPointerMove: overscroll.onPointerMove,
      onPointerUp: (event) => {
        activePointers.delete(event.pointerId)
        overscroll.onPointerUp()
      },
      onPointerCancel: (event) => {
        activePointers.delete(event.pointerId)
        overscroll.onPointerUp()
      },
    },
  }
}
