import { globalStyle, style } from '@vanilla-extract/css'
import { clippedText } from '~/styles/shared.css'
import { headerHeightPx, motion } from '~/styles/tokens'

export const tooltipTrigger = style({
  display: 'inline-flex',
})

export const tabBar = style({
  display: 'flex',
  alignItems: 'stretch',
  gap: '1px',
  padding: '0 var(--space-2)',
  backgroundColor: 'var(--background)',
  flexShrink: 0,
  minHeight: `${headerHeightPx - 1}px`,
})

export const tabList = style({
  position: 'relative',
  display: 'flex',
  alignItems: 'stretch',
  gap: 'var(--space-2)',
  flex: 1,
  minWidth: 0,
  overflowX: 'auto',
  scrollbarWidth: 'none',
  WebkitOverflowScrolling: 'touch',
  touchAction: 'pan-x',
  padding: 0,
  backgroundColor: 'inherit',
  borderRadius: 0,
})

globalStyle(`${tabList}::-webkit-scrollbar`, {
  display: 'none',
})

export const tab = style({
  'all': 'unset',
  'display': 'flex',
  'alignItems': 'center',
  'gap': '6px',
  'padding': 'var(--space-1) var(--space-1) var(--space-1) var(--space-2)',
  'fontSize': 'var(--text-7)',
  'color': 'var(--muted-foreground)',
  'cursor': 'pointer',
  'whiteSpace': 'nowrap',
  'maxWidth': '200px',
  'boxSizing': 'border-box',
  'borderBottom': '2px solid transparent',
  'transition': 'border-color 150ms ease',
  ':hover': {
    color: 'var(--faint-foreground)',
    backgroundColor: 'var(--lm-bg-translucent)',
  },
  'selectors': {
    '&[aria-selected="true"]': {
      color: 'var(--foreground)',
      borderBottomColor: 'var(--primary)',
    },
  },
})

export const tabClose = style({
  width: '16px',
  height: '16px',
  minWidth: '16px',
  borderRadius: '3px',
  color: 'var(--faint-foreground)',
  marginTop: '2px',
})

export const newTabWrapper = style({
  position: 'relative',
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-1)',
})

export const tabNotification = style({
  width: '6px',
  height: '6px',
  borderRadius: '50%',
  backgroundColor: 'var(--primary)',
  flexShrink: 0,
})

/** Thin task-progress bar for OSC 9;4 terminal tabs. */
export const tabProgress = style({
  'width': '24px',
  'height': '3px',
  'borderRadius': '2px',
  'backgroundColor': 'var(--border)',
  'overflow': 'hidden',
  'flexShrink': 0,
  '::after': {
    content: '""',
    display: 'block',
    height: '100%',
    backgroundColor: 'var(--primary)',
    width: 'var(--progress-percent, 0%)',
  },
})

export const tabLabel = style({
  fontSize: 'var(--text-8)',
  opacity: 0.6,
  marginRight: '2px',
})

export const tabIcon = style({
  display: 'flex',
  flexShrink: 0,
  width: '16px',
  height: '16px',
  marginTop: '2px',
})

/**
 * The tab label, clipped to one line.
 *
 * `clippedText` also supplies the `min-width: 0` this was missing, which is what
 * lets the label shrink inside the tab's 200px cap and reach the ellipsis; the
 * `nowrap` it adds restates what `tab` above already passes down.
 *
 * Composed with an EMPTY rule on purpose. The empty object keeps `tabText` its
 * own class, and the tile-size rules further down hide the label by targeting
 * it. Assigning `clippedText` here instead would point those rules at every
 * clipped label in the app.
 */
export const tabText = style([clippedText, {}])

export const tabDragging = style({
  opacity: 0.4,
})

export const tabListDropTarget = style({
  backgroundColor: 'var(--secondary)',
  outline: '2px dashed var(--primary)',
  outlineOffset: '-2px',
  borderRadius: 'var(--radius-small)',
})

export const tabEditInput = style({
  'width': '100px',
  'padding': '0 2px',
  'fontSize': 'var(--text-7)',
  'fontFamily': 'inherit',
  'color': 'var(--foreground)',
  'backgroundColor': 'var(--background)',
  'border': '1px solid var(--ring)',
  'borderRadius': 'var(--radius-small)',
  'outline': 'none',
  ':focus-visible': {
    boxShadow: '0 0 0 2px var(--ring)',
  },
})

// `providerButton`, `providerIconsRow` and `shellDefault` moved to
// `~/components/common/NewTabMenuItems.css.ts`: the Agents and Terminals
// sections are shared with the branch context menu now, and the strip's
// most-recently-used provider buttons import the same `providerButton` from
// there so one glyph cannot be two sizes.

// --- Collapsed new-tab button (visible at minimal/micro) ---
export const collapsedNewTab = style({
  display: 'none',
  alignItems: 'center',
})

// --- Collapsed overflow button with tile actions (visible at micro) ---
export const collapsedOverflow = style({
  display: 'none',
  alignItems: 'center',
})

// ======================================================================
// Responsive styles using ancestor [data-tile-size] / [data-tile-height]
// ======================================================================

// --- Narrow (360-479px): full tabs but new-tab buttons collapse into + dropdown ---
globalStyle(`[data-tile-size="narrow"] ${newTabWrapper}`, {
  display: 'none',
})

globalStyle(`[data-tile-size="narrow"] ${collapsedNewTab}`, {
  display: 'flex',
})

// --- Compact (240-359px): icon-only tabs, hide close unless hovered ---
globalStyle(`[data-tile-size="compact"] ${tabBar}`, {
  padding: '0 var(--space-1)',
  gap: '0',
})

globalStyle(`[data-tile-size="compact"] ${tab}`, {
  gap: 'var(--space-1)',
  padding: 'var(--space-1)',
  maxWidth: 'unset',
})

globalStyle(`[data-tile-size="compact"] ${tabText}`, {
  display: 'none',
})

globalStyle(`[data-tile-size="compact"] ${tabClose}`, {
  display: 'none',
})

globalStyle(`[data-tile-size="compact"] ${tab}:hover ${tabClose}`, {
  display: 'inline-flex',
})

globalStyle(`[data-tile-size="compact"] ${newTabWrapper}`, {
  display: 'none',
})

globalStyle(`[data-tile-size="compact"] ${collapsedNewTab}`, {
  display: 'flex',
})

// --- Minimal (140-239px): also icon-only tabs + collapse new-tab buttons ---
globalStyle(`[data-tile-size="minimal"] ${tabBar}`, {
  padding: '0 var(--space-1)',
  gap: '0',
})

globalStyle(`[data-tile-size="minimal"] ${tab}`, {
  gap: 'var(--space-1)',
  padding: 'var(--space-1)',
  maxWidth: 'unset',
})

globalStyle(`[data-tile-size="minimal"] ${tabText}`, {
  display: 'none',
})

globalStyle(`[data-tile-size="minimal"] ${tabClose}`, {
  display: 'none',
})

globalStyle(`[data-tile-size="minimal"] ${tab}:hover ${tabClose}`, {
  display: 'inline-flex',
})

globalStyle(`[data-tile-size="minimal"] ${newTabWrapper}`, {
  display: 'none',
})

globalStyle(`[data-tile-size="minimal"] ${collapsedNewTab}`, {
  display: 'flex',
})

// --- Micro (<140px): everything collapses into overflow ---
globalStyle(`[data-tile-size="micro"] ${tabBar}`, {
  padding: `0 2px`,
  gap: '0',
})

globalStyle(`[data-tile-size="micro"] ${tab}`, {
  gap: 'var(--space-1)',
  padding: 'var(--space-1) 2px',
  maxWidth: 'unset',
})

globalStyle(`[data-tile-size="micro"] ${tabText}`, {
  display: 'none',
})

globalStyle(`[data-tile-size="micro"] ${tabClose}`, {
  display: 'none',
})

globalStyle(`[data-tile-size="micro"] ${newTabWrapper}`, {
  display: 'none',
})

globalStyle(`[data-tile-size="micro"] ${collapsedNewTab}`, {
  display: 'none',
})

globalStyle(`[data-tile-size="micro"] ${collapsedOverflow}`, {
  display: 'flex',
})

// --- Short height (72-119px): reduced tab bar height ---
globalStyle(`[data-tile-height="short"] ${tabBar}`, {
  minHeight: '28px',
})

globalStyle(`[data-tile-height="short"] ${tab}`, {
  padding: '2px 4px',
})

// ======================================================================
// Mobile: current-tab chip + tab list panel dropping from the tab bar
// ======================================================================

// The chip that replaces the horizontal strip on phones: it shows the name
// of the active tab and opens the sheet that lists them all.
export const tabChip = style({
  'all': 'unset',
  'display': 'flex',
  'alignItems': 'center',
  'gap': 'var(--space-2)',
  'flex': 1,
  'minWidth': 0,
  'boxSizing': 'border-box',
  'padding': 'var(--space-1) var(--space-2)',
  'borderRadius': 'var(--radius-small)',
  'color': 'var(--muted-foreground)',
  'cursor': 'pointer',
  ':hover': {
    color: 'var(--faint-foreground)',
    backgroundColor: 'var(--lm-bg-translucent)',
  },
})

// Holds the chip's flex role while the chip itself is hidden (a tile with no
// tabs): the chip's `flex: 1` is what fills the bar's middle and lands the
// files toggle at the right end, so without a stand-in the trailing chrome
// collapses against the head of the bar instead.
export const mobileBarSpacer = style({
  flex: 1,
})

/**
 * Clipped single-line label for the mobile surfaces (the chip's tab name,
 * the sheet row's label). Composed with an EMPTY rule on purpose — same
 * trick as `tabText` above.
 */
export const mobileClippedLabel = style([clippedText, {}])

export const tabChipCount = style({
  display: 'inline-flex',
  alignItems: 'center',
  justifyContent: 'center',
  flexShrink: 0,
  minWidth: '18px',
  height: '18px',
  padding: '0 5px',
  borderRadius: '9px',
  backgroundColor: 'var(--secondary)',
  color: 'var(--muted-foreground)',
  fontSize: 'var(--text-8)',
})

export const tabChipChevron = style({
  flexShrink: 0,
  opacity: 0.6,
})

// The mobile "+" new-tab slot. Distinct from `collapsedNewTab`, which the
// [data-tile-size] rules keep hidden — the standalone mobile bar carries no
// such ancestor, so this variant is simply always flexed when rendered.
export const mobileNewTab = style({
  display: 'flex',
  alignItems: 'center',
})

// (The sheet's scrim lives in AppShell.css now: it anchors to the layout's
// content region below the bar, not to the tab bar.)

// The clip window the tab list drops within. It is ABSOLUTE inside the tab
// bar's own `position: relative` wrapper (AppShell.css), anchored at the
// bar's bottom edge — flush by construction, with no measured-height copy
// to keep in step with the bar. The panel slides in from
// translateY(-100%), i.e. from BEHIND the bar; `overflow: hidden` clips the
// slide to below the bar so it never paints over it (or steals its taps)
// mid-transition. `pointer-events: none` keeps the empty window from
// catching taps meant for the workspace under it — the panel turns them
// back on for itself. The bottom padding leaves room for the panel's drop
// shadow inside the clip.
export const sheetPanelClip = style({
  position: 'absolute',
  top: '100%',
  left: 0,
  right: 0,
  zIndex: 102,
  overflow: 'hidden',
  pointerEvents: 'none',
  paddingBottom: 'var(--space-4)',
})

export const sheetPanel = style({
  'display': 'flex',
  'flexDirection': 'column',
  'maxHeight': '65dvh',
  'backgroundColor': 'var(--card)',
  'borderTop': '1px solid var(--border)',
  'borderBottomLeftRadius': 'var(--radius)',
  'borderBottomRightRadius': 'var(--radius)',
  'boxShadow': '0 2px 8px rgba(0, 0, 0, 0.3)',
  'overflow': 'hidden',
  'pointerEvents': 'auto',
  'transform': 'translateY(-100%)',
  'transition': `transform ${motion.medium}ms ease`,
  'outline': 'none',
  '@media': {
    '(prefers-reduced-motion: reduce)': {
      transition: 'none',
    },
  },
})

export const sheetPanelOpen = style({
  transform: 'translateY(0)',
})

export const sheetHeader = style({
  display: 'flex',
  alignItems: 'center',
  flexShrink: 0,
  padding: 'var(--space-2) var(--space-4) var(--space-2)',
})

export const sheetTitle = style({
  fontSize: 'var(--text-7)',
  fontWeight: 'var(--font-bold)',
  color: 'var(--muted-foreground)',
})

export const sheetList = style({
  minHeight: 0,
  overflowY: 'auto',
  overscrollBehavior: 'contain',
  WebkitOverflowScrolling: 'touch',
  // Swipe = native vertical scroll. The drag grips opt out with their own
  // `touch-action: none`, which is what keeps scroll and drag from racing.
  touchAction: 'pan-y',
  // `role="tablist"` pulls in Oat's global pill styling (inline-flex, gap,
  // muted background, rounded corners) — this list is a plain full-bleed
  // column, so every one of those is overridden here.
  display: 'flex',
  flexDirection: 'column',
  alignItems: 'stretch',
  gap: 0,
  backgroundColor: 'transparent',
  borderRadius: 0,
  // Full-bleed horizontally: the rows carry their own padding, so the list
  // itself adds none on the sides.
  padding: '0 0 var(--space-2)',
})

export const sheetRow = style({
  // `all: unset` first: the Oat design system styles every [role="tab"]
  // globally (inline-flex, centered content, its own padding and font) — the
  // same conflict the strip's `tab` style settles the same way.
  all: 'unset',
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-2)',
  boxSizing: 'border-box',
  width: '100%',
  // A touch target, not a mouse row.
  minHeight: '44px',
  padding: 'var(--space-1) var(--space-2)',
  borderRadius: 'var(--radius-small)',
  color: 'var(--muted-foreground)',
  fontSize: 'var(--text-7)',
  cursor: 'pointer',
  selectors: {
    '&[aria-selected="true"]': {
      color: 'var(--foreground)',
      backgroundColor: 'var(--secondary)',
    },
  },
})

/** The sheet row's label — the shared mobile clipped label. */
export const sheetRowLabel = mobileClippedLabel

export const sheetEmpty = style({
  padding: 'var(--space-4)',
  color: 'var(--muted-foreground)',
  textAlign: 'center',
})
