import { globalStyle, keyframes, style } from '@vanilla-extract/css'
import { resizeHandleSelectors } from '~/styles/resizeHandle'
import { breakpoints, motion } from '~/styles/tokens'

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

export const container = style({
  display: 'flex',
  flexDirection: 'column',
  height: '100%',
  overflow: 'hidden',
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

export const messageListContent = style({
  display: 'flex',
  flexDirection: 'column',
  flexShrink: 0,
  gap: 'var(--space-5)',
})

export const messageList = style({
  'flex': 1,
  'overflowX': 'hidden',
  'overflowY': 'auto',
  'overflowAnchor': 'none',
  'padding': 'var(--space-4)',
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
    /**
     * Phone gutters. The rail floats over the right edge, so 16px reserved there is pure
     * lost text width; 4px still keeps the text off the glass. The left drops to 12px
     * rather than matching 4px for two reasons: the span-line stack overhangs the content
     * box by COL_OVERLAP (5px, see ./widgets/SpanLines.css.ts) and `overflowX: hidden`
     * would clip it, and 24px against 4px reads visibly lopsided on a 360px viewport.
     */
    [`(max-width: ${breakpoints.sm - 1}px)`]: {
      paddingLeft: 'var(--space-3)',
      paddingRight: 'var(--space-1)',
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
  padding: 'var(--space-1) var(--space-3) var(--space-3)',
  flexShrink: 0,
})

// disabledHint is the single-line note rendered above a disabled composer (a
// non-steerable subagent tab). Dim, small, no interaction.
export const disabledHint = style({
  fontSize: 'var(--text-7)',
  color: 'var(--muted-foreground)',
  padding: '0 0 var(--space-1)',
})

export const footerBar = style({
  display: 'flex',
  justifyContent: 'space-between',
  alignItems: 'center',
  padding: 'var(--space-1) var(--space-1) var(--space-1) var(--space-3)',
  backgroundColor: 'var(--background)',
  flexShrink: 0,
})

export const footerBarLeft = style({
  display: 'flex',
  alignItems: 'center',
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

export const settingsTrigger = style({
  all: 'unset',
  boxSizing: 'border-box',
  display: 'inline-flex',
  alignItems: 'center',
  gap: 'var(--space-1)',
  padding: '2px var(--space-1)',
  marginBottom: '3px',
  marginLeft: '-3px',
  fontSize: 'var(--text-8)',
  color: 'var(--faint-foreground)',
  cursor: 'pointer',
  borderRadius: 'var(--radius-small)',
  whiteSpace: 'nowrap',
  userSelect: 'none',
  selectors: {
    '&:hover': { color: 'var(--foreground)', backgroundColor: 'var(--card)' },
    '&[data-disabled]': { opacity: 0.5, cursor: 'default' },
  },
})

export const settingsMenu = style({
  backgroundColor: 'var(--background)',
  border: '1px solid var(--border)',
  borderRadius: 'var(--radius-medium)',
  padding: 'var(--space-4)',
  zIndex: 300,
  minWidth: '180px',
  maxHeight: 'calc(100vh - var(--space-6) * 2)',
  overflowY: 'auto',
  boxShadow: 'var(--shadow-large)',
})

export const settingsMenuWide = style({
  'minWidth': '460px',
  '@media': {
    [`(max-width: ${breakpoints.sm - 1}px)`]: {
      minWidth: 'auto',
    },
  },
})

export const settingsPanelColumns = style({
  'display': 'flex',
  'alignItems': 'flex-start',
  'gap': 'var(--space-4)',
  '@media': {
    [`(max-width: ${breakpoints.sm - 1}px)`]: {
      flexDirection: 'column',
      gap: 'var(--space-1)',
    },
  },
})

export const settingsPanelColumn = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-4)',
  flex: 1,
  minWidth: 0,
})

export const settingsPanelColumnPrimary = style({
  flex: 1.2,
})

export const settingsFieldset = style({
  position: 'relative',
  border: '1px solid var(--border)',
  borderRadius: 'var(--radius-medium)',
  padding: 'var(--space-3) var(--space-2) var(--space-2)',
  minWidth: 0,
  selectors: {
    // Read-only group (RadioGroup disabled): mute it and drop the interactive
    // cursor, following the settingsTrigger &[data-disabled] pattern.
    '&[data-disabled]': { opacity: 0.5, cursor: 'default' },
  },
})

export const settingsFieldsetFirst = style({
  marginBlockStart: 0,
})

export const settingsGroupLabel = style({
  position: 'absolute',
  top: 0,
  left: 'var(--space-3)',
  transform: 'translateY(-50%)',
  display: 'inline-block',
  padding: '0 var(--space-2)',
  fontSize: 'var(--text-8)',
  fontWeight: 'var(--font-bold)',
  color: 'var(--muted-foreground)',
  textTransform: 'uppercase',
  letterSpacing: '0.05em',
  whiteSpace: 'nowrap',
  backgroundColor: 'var(--background)',
})

export const settingsRadioItem = style({
  'all': 'unset',
  'boxSizing': 'border-box',
  'display': 'flex',
  'alignItems': 'center',
  'gap': 'var(--space-2)',
  'padding': '3px var(--space-2)',
  'fontSize': 'var(--text-8)',
  'color': 'var(--foreground)',
  'cursor': 'pointer',
  'userSelect': 'none',
  ':hover': {
    backgroundColor: 'var(--card)',
  },
  'selectors': {
    // Inside a read-only group, the items are inert: no interactive cursor, no
    // hover affordance.
    [`${settingsFieldset}[data-disabled] &`]: { cursor: 'default' },
    [`${settingsFieldset}[data-disabled] &:hover`]: { backgroundColor: 'transparent' },
  },
})

// Searchable select: current value display
export const searchableSelectCurrent = style({
  padding: '3px var(--space-2)',
  fontSize: 'var(--text-8)',
  color: 'var(--muted-foreground)',
})

// Searchable select: scrollable list
export const searchableSelectListbox = style({
  // Height fits 5 items (compact — selected value + filter input take extra space).
  // Each item is 1lh tall + 6px vertical padding → calc(1lh + 6px) per item.
  // Font-size must match items so 1lh resolves to the correct item line-height.
  fontSize: 'var(--text-8)',
  minHeight: 'calc((1lh + 6px) * 5)',
  maxHeight: 'calc((1lh + 6px) * 5)',
  overflowY: 'auto',
})

// Searchable select: item
export const searchableSelectItem = style({
  'display': 'flex',
  'alignItems': 'center',
  'justifyContent': 'space-between',
  'gap': 'var(--space-2)',
  'padding': '3px var(--space-2)',
  'fontSize': 'var(--text-8)',
  'color': 'var(--foreground)',
  'cursor': 'pointer',
  'userSelect': 'none',
  'whiteSpace': 'nowrap',
  'borderRadius': 'var(--radius-small)',
  ':hover': {
    backgroundColor: 'var(--card)',
  },
})

// Searchable select: highlighted item (keyboard navigation)
export const searchableSelectItemHighlighted = style({
  backgroundColor: 'var(--muted)',
})

// Searchable select: currently selected item
export const searchableSelectItemSelected = style({
  fontWeight: 'var(--font-bold)',
})

// Searchable select: secondary text (right-aligned, muted)
export const searchableSelectItemSecondary = style({
  fontFamily: 'var(--font-mono)',
  fontSize: 'var(--text-8)',
  color: 'var(--muted-foreground)',
  marginLeft: 'auto',
  flexShrink: 0,
})

// Searchable select: filter input container
export const searchableSelectControl = style({
  padding: '3px var(--space-2)',
  borderTop: '1px solid var(--border)',
  marginTop: 'var(--space-1)',
})

// Searchable select: filter input
export const searchableSelectInput = style({
  'all': 'unset',
  'boxSizing': 'border-box',
  'width': '100%',
  'fontSize': 'var(--text-8)',
  'color': 'var(--foreground)',
  '::placeholder': {
    color: 'var(--faint-foreground)',
  },
})

export const footerBarRight = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-1)',
})

export const infoTrigger = style({
  all: 'unset',
  boxSizing: 'border-box',
  display: 'inline-flex',
  alignItems: 'center',
  justifyContent: 'center',
  gap: 'var(--space-1)',
  padding: '2px',
  fontSize: 'var(--text-8)',
  color: 'var(--faint-foreground)',
  cursor: 'pointer',
  borderRadius: 'var(--radius-small)',
  vars: {
    '--context-grid-inactive': 'var(--border)',
    '--context-grid-warning': 'var(--warning)',
  },
  selectors: {
    '&:hover': { color: 'var(--foreground)', backgroundColor: 'var(--card)', vars: { '--context-grid-inactive': 'var(--border)', '--context-grid-warning': 'var(--warning)' } } as Record<string, unknown>,
  },
})

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

export const infoCopyButton = style({
  'all': 'unset',
  'boxSizing': 'border-box',
  'display': 'inline-flex',
  'alignItems': 'center',
  'justifyContent': 'center',
  'padding': '2px',
  'cursor': 'pointer',
  'borderRadius': 'var(--radius-small)',
  'color': 'var(--faint-foreground)',
  'flexShrink': 0,
  ':hover': {
    color: 'var(--foreground)',
    backgroundColor: 'var(--card)',
  },
})

export const infoRows = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-1)',
})

export const infoSeparator = style({
  height: '1px',
  backgroundColor: 'var(--border)',
  margin: 'var(--space-1) 0',
})

export const infoContextUsage = style({
  fontSize: 'var(--text-8)',
  color: 'var(--foreground)',
  maxHeight: '300px',
  overflowY: 'auto',
  lineHeight: '1.4',
})

export const rateLimitCountdown = style({
  fontSize: 'var(--text-8)',
  color: 'var(--muted-foreground)',
  fontFamily: 'var(--font-mono)',
  whiteSpace: 'nowrap',
})

export const messageRow = style({
  display: 'flex',
})

export const messageRowContent = style({
  flex: 1,
  minWidth: 0,
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
})

export const premeasureRow = style({
  display: 'flow-root',
  position: 'relative',
  width: '100%',
})

export const editorPanelWrapper = style({
  flexShrink: 0,
})
