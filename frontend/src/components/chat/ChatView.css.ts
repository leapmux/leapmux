import { globalStyle, keyframes, style } from '@vanilla-extract/css'
import { resizeHandleSelectors } from '~/styles/resizeHandle'
import { chipBase } from '~/styles/shared.css'
import { breakpoints, motion } from '~/styles/tokens'
import { CHAT_PAD_LEFT_VAR, CHAT_PAD_RIGHT_VAR, CHAT_RAIL_WIDTH_VAR, COARSE_HIT_PX } from './chatChromeVars'
import { BAND_BORDER_PX } from './chatRowGeometry'
import { contentColumnBleed } from './messageStyles.css'

export const editorResizeHandle = style({
  height: '4px',
  flexShrink: 0,
  cursor: 'row-resize',
  position: 'relative',
  userSelect: 'none',
  margin: '-2px 0',
  zIndex: 5,
  selectors: resizeHandleSelectors('vertical'),
})

export const editorResizeHandleActive = style({
  selectors: {
    '&::before': {
      background: 'var(--primary) !important',
      height: '1px !important',
    },
  },
})

/**
 * Horizontal inset of the composer box. Deliberately NOT the list's gutter
 * (`CHAT_PAD_LEFT_VAR`, one step wider): the composer is a bordered box on the
 * panel, and the list is a column of rows that run to the panel edge, so the two
 * insets answer different questions and must not be folded into one token.
 */
const COMPOSER_INSET = 'var(--space-3)'

/**
 * The chat list's horizontal gutter, as CSS vars so the band rows can cancel it.
 * An assistant band paints edge to edge: it negates these two values and adds
 * them back as padding (see `bandRow` in ~/components/chat/messageStyles.css.ts),
 * which only works while the gutter has ONE definition. Declared on the chat
 * ROOT rather than on the list: the scroll rail sizes its column from the right
 * gutter and is the list's SIBLING, so a definition on the list itself would be
 * out of its reach. `premeasureRoot` INHERITS them from here, media override
 * included, which is what puts the hidden measurement in the same horizontal
 * geometry as the live list.
 */
const chatGutterVars = {
  [CHAT_PAD_LEFT_VAR]: 'var(--space-4)',
  [CHAT_PAD_RIGHT_VAR]: 'var(--space-4)',
} as const

/**
 * The scroll rail's column, published as a token on the chat root.
 *
 * The rail IS the gutter: it fills that strip exactly, so its track, thumb and
 * dots -- all centred in it -- run down the gutter's middle. Its box also stops
 * at the gutter, which matters because the rail overlays a row from outside that
 * row's stacking context and stays hit-testable while faded: every pointer event
 * inside the rail's box goes to the rail, and no z-index or pointer-events
 * arrangement inside the row changes that. A box that stops at the gutter cannot
 * receive an event meant for a row's floating action buttons.
 *
 * Two floors keep the column usable where the gutter alone is too thin to press:
 * the phone gutter shrinks to 4px to reclaim text width, and a coarse pointer
 * needs a whole finger. Both floors make the column WIDER than the gutter, so it
 * does overhang the message column -- which is why the row's floating actions are
 * inset by exactly that overhang (see the toolbar rules in
 * ~/components/chat/messageStyles.css.ts). The inset is computed from these same
 * two properties, so it is zero whenever the column is the bare gutter and it
 * tracks either floor automatically.
 */
const RAIL_MIN_WIDTH = '16px'
const chatRailVars = {
  [CHAT_RAIL_WIDTH_VAR]: `max(var(${CHAT_PAD_RIGHT_VAR}), ${RAIL_MIN_WIDTH})`,
} as const

export const container = style({
  'display': 'flex',
  'flexDirection': 'column',
  'height': '100%',
  'overflow': 'hidden',
  'vars': { ...chatGutterVars, ...chatRailVars },
  '@media': {
    /**
     * Phone gutters. The rail floats over the right edge, so 16px reserved there is pure
     * lost text width; 4px still separates the text from the panel edge. The left drops to
     * 12px rather than matching 4px for two reasons: the span-line stack overhangs the
     * content box by COL_OVERLAP (5px, see ./widgets/SpanLines.css.ts) and `overflowX:
     * hidden` would clip it, and 24px against 4px reads visibly uneven on a 360px viewport.
     *
     * Declared HERE, with the gutter itself, so the rail -- the list's sibling -- narrows
     * with it rather than reading a stale desktop value.
     */
    [`(max-width: ${breakpoints.sm - 1}px)`]: {
      vars: {
        [CHAT_PAD_LEFT_VAR]: 'var(--space-3)',
        [CHAT_PAD_RIGHT_VAR]: 'var(--space-1)',
      },
    },
    /**
     * A finger cannot press a 16px strip. The rail's column is the only part of the rail
     * that a track click or a thumb drag can land on, so it takes the finger size directly
     * -- the dots carry their own hit circle of the same diameter (DOT_COARSE_HIT_PX in
     * ~/components/chat/ChatScrollRail.css.ts, derived from the same constant).
     *
     * Declared as a floor rather than a fixed width, so a wide desktop-class gutter on a
     * touch screen still gives the rail the whole gutter and no more.
     */
    '(pointer: coarse)': {
      vars: {
        [CHAT_RAIL_WIDTH_VAR]: `max(var(${CHAT_PAD_RIGHT_VAR}), ${COARSE_HIT_PX}px)`,
      },
    },
  },
})

export const messageListWrapper = style({
  position: 'relative',
  flex: 1,
  overflow: 'hidden',
  display: 'flex',
  flexDirection: 'column',
})

export const messageListSpacer = style({
  flex: 1,
})

// This wrapper is the direct flex child of the scroll container. It must keep
// the virtual spacer's full height; shrinking it lets the browser clamp
// scrollTop against transient child overflow while virtual rows are swapped.
export const messageListSelectionRoot = style({
  flexShrink: 0,
  overflowAnchor: 'none',
})

/**
 * Flow gap between the virtual spacer and the in-flow tail UI below it.
 *
 * Named because two rules must agree on it: this gap, and the negative margin that
 * `bandTailMerged` uses to cancel it. It also matches the virtualizer's own
 * gapLargePx, which ChatView measures from the same token (see the
 * `measureSpaceToken('--space-5', …)` call), so a virtual row and the tail are
 * spaced alike.
 */
const MESSAGE_LIST_ROW_GAP = 'var(--space-5)'

export const messageListContent = style({
  display: 'flex',
  flexDirection: 'column',
  flexShrink: 0,
  gap: MESSAGE_LIST_ROW_GAP,
})

/**
 * Merge the streaming tail's band into the band of the row directly above it.
 *
 * The virtualizer overlaps two adjacent bands by one border width in its offset
 * map, so the pair shows one line. The tail is not in that map -- it is a flow
 * sibling of the virtual spacer -- so the same merge has to be spelled here: cancel
 * the flow gap, then one more border so the tail's top border lands on the row's
 * bottom border. Without it the reader watches a gap and two separate lines while
 * the reply streams, which close the instant the persisted row lands.
 */
export const bandTailMerged = style({
  marginTop: `calc(-1 * ${MESSAGE_LIST_ROW_GAP} - ${BAND_BORDER_PX}px)`,
})

export const messageList = style({
  'flex': 1,
  'overflowX': 'hidden',
  'overflowY': 'auto',
  'overflowAnchor': 'none',
  'padding': `var(--space-4) var(${CHAT_PAD_RIGHT_VAR}) var(--space-4) var(${CHAT_PAD_LEFT_VAR})`,
  'display': 'flex',
  'flexDirection': 'column',
  'gap': 'var(--space-3)',
  '@media': {
    /**
     * TOUCH: never a native scrollbar on the chat list, whether or not the rail owns
     * scrolling. The app-wide `::-webkit-scrollbar { width: 8px }` in the global
     * stylesheet (~/styles/global.css.ts) opts every element OUT of Chrome Android's
     * auto-hiding overlay scrollbars, so this list paints a permanent classic 8px bar that
     * eats layout width from an already-narrow text column -- most visibly in the window
     * BEFORE the marks RPC seeds the rail, which is exactly when `messageListRailActive`
     * cannot apply. Scoped to this list; the app-wide rule is left alone.
     *
     * This DELIBERATELY relaxes the "never zero scrollbars" invariant on touch: when the
     * rail is not the owner (the marks RPC failed or lags), a touch viewport now shows
     * NEITHER bar. That is the right trade on a surface where scrolling is direct
     * manipulation and there is no thumb to grab -- see resolveScrollbarOwner, which
     * records the same exception.
     *
     * Keep every `*.css.ts` reference above path-qualified (`~/...` or `./...`). A BARE
     * basename anywhere in a `.css.ts` file, a comment included, makes the vanilla-extract
     * compiler throw "Styles were unable to be assigned to a file" and point at an
     * unrelated line in this file.
     */
    '(pointer: coarse)': {
      scrollbarWidth: 'none',
    },
  },
})

// The WebKit half of the coarse-pointer rule above. `::-webkit-scrollbar` is not
// `&`-anchored, so it cannot be a `selectors` entry and needs its own globalStyle --
// the same shape as messageListRailActive's below.
globalStyle(`${messageList}::-webkit-scrollbar`, {
  '@media': {
    '(pointer: coarse)': {
      display: 'none',
    },
  },
})

/**
 * Hides the native scrollbar so ONLY the seq-space ChatScrollRail overlay shows (the
 * browser thumb reflects just the loaded window, not the whole conversation). Applied to
 * `messageList` only while the rail is actually active (marks seeded); if the marks RPC
 * fails or lags, the native scrollbar stays visible rather than leaving a long
 * conversation with no scrollbar at all. Scoped here -- the app-wide thin scrollbar stays.
 *
 * On COARSE pointers this is redundant: `messageList` hides the native bar there
 * unconditionally (see its `(pointer: coarse)` block), which is a deliberate exception to
 * the fallback described above -- on touch, a viewport with no rail simply has no
 * scrollbar, because scrolling is direct manipulation.
 */
export const messageListRailActive = style({
  scrollbarWidth: 'none',
})
globalStyle(`${messageListRailActive}::-webkit-scrollbar`, {
  display: 'none',
})

/**
 * Shared look of the "Loading older/newer messages..." indicators. Each is an absolute
 * OVERLAY pill (a sibling of the scroll container, inside the relative wrapper), NOT in
 * the scroll flow. An in-flow indicator would shift the virtualized content by its
 * height when fetching toggles -- and that shift is invisible to the anchor re-pin
 * (whose offset map covers only the virtual rows), so the view bounces by the
 * indicator's height each load cycle and a scrolled reader gets stuck re-triggering the
 * load. As an overlay it never moves the content. pointer-events: none so it can't
 * swallow a scroll/click landing on it. The top/bottom edge is set by each variant.
 */
const loadingIndicatorBase = style({
  position: 'absolute',
  left: '50%',
  transform: 'translateX(-50%)',
  zIndex: 10,
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-2)',
  padding: 'var(--space-2) var(--space-4)',
  // Match the scroll-to-bottom button's corner (the base button radius token),
  // not a pill -- both float over the same viewport so they should read as one set.
  borderRadius: 'var(--radius-medium)',
  backgroundColor: 'var(--background)',
  border: '1px solid var(--border)',
  color: 'var(--muted-foreground)',
  fontSize: 'var(--text-7)',
  pointerEvents: 'none',
  opacity: 0.95,
})

/** "Loading older messages..." overlay, pinned top-center while scrolled up against the loaded top edge. */
export const loadingOlderIndicator = style([loadingIndicatorBase, { top: 'var(--space-3)' }])

/**
 * "Loading newer messages..." overlay, pinned bottom-center. It takes the scroll-to-bottom
 * button's exact slot (same bottom-center anchor); ChatView hides that button while this is
 * shown (scroll.stalledNewer()) so the two never overlap.
 */
export const loadingNewerIndicator = style([loadingIndicatorBase, { bottom: 'var(--space-3)' }])

export const inputArea = style({
  // Bottom padding is space-1 when the status bar is shown beneath, and
  // space-2 when it's hidden (the status bar's own space-2 bottom padding
  // then provides the gap to the window edge instead).
  padding: `var(--space-1) ${COMPOSER_INSET} var(--space-1)`,
  flexShrink: 0,
})

globalStyle(`${inputArea}[data-no-status-bar]`, {
  paddingBottom: 'var(--space-2)',
})

/**
 * The composer's action cluster (Interrupt + Send). Rendered in the box's
 * top-right (collapsed) or bottom-right (expanded) overlay slot, so it's a
 * plain inline-flex with a small gap; the positioning lives in
 * the `footerSlot` style in `./markdownEditor/MarkdownEditor.css.ts`.
 */
export const actionCluster = style({
  display: 'inline-flex',
  alignItems: 'center',
  gap: 'var(--space-1)',
})

/**
 * Text label inside an action button (Interrupt/Send). Hidden below `sm` so
 * the buttons become icon-only on narrow screens, saving horizontal space.
 */
export const actionLabel = style({
  '@media': {
    [`(max-width: ${breakpoints.sm - 1}px)`]: {
      display: 'none',
    },
  },
})

export const scrollToBottomButton = style({
  'position': 'absolute',
  'bottom': 'var(--space-3)',
  'left': '50%',
  'transform': 'translateX(-50%)',
  'zIndex': 10,
  'width': '36px',
  'height': '36px',
  'backgroundColor': 'var(--background)',
  'opacity': 0.8,
  ':hover': {
    opacity: 1,
  },
})

/**
 * Inline startup indicator rendered after the last message when the
 * agent is STARTING or STARTUP_FAILED and the user has already queued
 * messages. Keeps the startup panel visible even when the outer Show's
 * fallback-centered empty state is no longer active. Aligned to match
 * the left margin of message rows.
 */
export const startupPanelInline = style({
  marginLeft: '1px',
  color: 'var(--faint-foreground)',
})

export const emptyChat = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  flex: 1,
  color: 'var(--faint-foreground)',
})

export const infoTrigger = style([chipBase, {
  justifyContent: 'center',
  gap: 'var(--space-1)',
  padding: '2px',
  fontSize: 'var(--text-8)',
  vars: {
    '--context-grid-inactive': 'var(--border)',
    '--context-grid-warning': 'var(--warning)',
  },
  selectors: {
    // Restated because chipBase's own hover rule replaces the declaration
    // block; the grid vars must survive the hover state.
    '&:hover': { vars: { '--context-grid-inactive': 'var(--border)', '--context-grid-warning': 'var(--warning)' } } as Record<string, unknown>,
  },
}])

export const infoRow = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-2)',
})

export const infoLabel = style({
  fontSize: 'var(--text-8)',
  fontWeight: 'var(--font-bold)',
  color: 'var(--muted-foreground)',
  whiteSpace: 'nowrap',
})

export const infoValue = style({
  fontSize: 'var(--text-8)',
  color: 'var(--foreground)',
  fontFamily: 'var(--font-mono)',
  wordBreak: 'break-all',
})

export const infoValueText = style({
  fontSize: 'var(--text-8)',
  color: 'var(--foreground)',
  wordBreak: 'break-all',
})

export const infoCopyButton = style([chipBase, {
  justifyContent: 'center',
  padding: '2px',
  flexShrink: 0,
}])

export const infoRows = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-1)',
  // Every row is behind a `Show`, so an agent with no session id, no branch and
  // no rate-limit row would otherwise render a card narrower than the `[+]` menu
  // it hangs off. The floor belongs to the CONTENT, not to the shared popover
  // class: the to-do and background-task cards size to their own rows.
  minWidth: '240px',
})

export const rateLimitCountdown = style({
  fontSize: 'var(--text-8)',
  color: 'var(--muted-foreground)',
  fontFamily: 'var(--font-mono)',
  whiteSpace: 'nowrap',
})

/**
 * A row's RAILED wrapper: the span-line stack beside the content column. Named
 * for the rails, NOT `messageRow`, because `~/components/chat/messageStyles.css.ts`
 * exports a different class under that name -- the bubble row that the band and
 * meta selectors target. Two exports with one name make a reader decide which is
 * meant at every read, and a `:has()` selector written against the wrong one
 * matches nothing at all.
 */
export const railedRow = style({
  display: 'flex',
})

/** The content column beside those rails. */
export const railedRowContent = style({
  flex: 1,
  minWidth: 0,
  // Widen the CLIP box to both panel edges without moving the content box, so a
  // bleeding descendant (a turn-end rule, a user bubble's right side) is not cut
  // off at the content column. `flex: 1` absorbs the negative margins -- the box
  // grows by exactly what they remove, and the equal padding gives the content
  // back its original width -- so a row without a bleeding child is unchanged.
  ...contentColumnBleed,
  // Isolate the bubble's layout/paint so an internal change (e.g. a tool card
  // expanding) doesn't invalidate the span columns beside it. The row itself
  // is also contained (see virtualRow); this inner boundary keeps bubble
  // churn from re-laying-out the sibling SpanLines flex column.
  contain: 'layout paint',
})

// Spacer sized to the whole window height; absolutely-positioned rows live
// inside it so the native scrollbar reflects the full message list.
export const virtualSpacer = style({
  position: 'relative',
  width: '100%',
  flexShrink: 0,
  // We anchor the viewport ourselves (useChatScroll); browser scroll anchoring
  // on the absolutely-positioned children would fight our re-pin math.
  overflowAnchor: 'none',
})

// A single virtualized message row, positioned by translateY. Rows own no
// out-of-box decorations — the span-line segments that cross the inter-row
// gap render in the SpanLineGapBridges overlay, a SIBLING of the rows — so
// each row can contain its layout and paint: a tool card expanding, a
// highlight landing, or any other in-row change invalidates that row alone
// instead of leaking into sibling layout. (`contain: size` must stay OFF:
// rows size to content, which is what the height measurement reads. And no
// `content-visibility`: skipping offscreen rendering would collapse the very
// heights the premeasure pipeline exists to capture.)
export const virtualRow = style({
  'position': 'absolute',
  'top': 0,
  'left': 0,
  'right': 0,
  'contain': 'layout paint',
  'transition': 'opacity var(--transition)',
  '@media': {
    '(prefers-reduced-motion: reduce)': {
      transition: 'none',
    },
  },
})

const skeletonFadeOut = keyframes({
  from: { opacity: 1 },
  to: { opacity: 0 },
})

// A skeleton on its way out: mounted fresh for the crossfade beat, so it must
// ANIMATE to transparent (a transition would need a prior styled state).
// `motion.medium` on both sides — this duration and ChatView's
// SKELETON_CROSSFADE_MS linger timer — so the fade and the unmount can't
// drift apart; `forwards` holds opacity 0 for any scheduling slack.
export const rowSkeletonClosing = style({
  'animation': `${skeletonFadeOut} ${motion.medium}ms ease forwards`,
  '@media': {
    '(prefers-reduced-motion: reduce)': {
      animation: 'none',
      opacity: 0,
    },
  },
})

// The in-row crossfade copy: when a fling skeleton upgrades to the real
// bubble, a fading-out skeleton copy sits absolutely on top of the fresh
// content for one transition beat. Inert — it must never intercept clicks on
// the content beneath.
export const rowSkeletonUpgradeOverlay = style({
  position: 'absolute',
  top: 0,
  left: 0,
  right: 0,
  pointerEvents: 'none',
})

export const premeasureRoot = style({
  position: 'fixed',
  top: 0,
  left: 0,
  visibility: 'hidden',
  pointerEvents: 'none',
  overflow: 'hidden',
  zIndex: -1,
  contain: 'layout paint',
  // Deliberately no `vars: chatGutterVars` here. This element is a descendant of
  // `container`, so it already INHERITS the gutter -- media override and all. A
  // restatement would carry the desktop values only, and below breakpoints.sm the
  // measured row would then read a 16px gutter where the live row reads 4px. That
  // costs nothing on a band, whose margins and padding cancel, but a bleeding
  // DESCENDANT (a turn-end rule) resolves its negative margins straight from these
  // two values: it would measure a whole gutter wider than it renders, wrap its
  // label at the wrong width, and commit a short height into the offset map.
})

// Sized to the premeasure root's own width, which ChatView sets to the live
// list's CONTENT width. Deliberately no `width: 100%`: with border-box sizing a
// band row's negative gutter margins plus its equal padding would leave a
// content box two gutters too narrow, so band text would wrap differently here
// than in the list and commit a wrong height. A block box with auto width
// resolves to the containing block minus its own margins, which lands on the
// live row's content width for a band and a non-band alike.
export const premeasureRow = style({
  display: 'flow-root',
  position: 'relative',
})

export const editorPanelWrapper = style({
  flexShrink: 0,
})
