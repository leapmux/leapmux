import { globalStyle, style } from '@vanilla-extract/css'
import { clippedText } from '~/styles/shared.css'
import { headerHeightPx } from '~/styles/tokens'

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

export const shellDefault = style({
  marginLeft: 'var(--space-1)',
  fontSize: 'var(--text-8)',
  color: 'var(--muted-foreground)',
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
  ':focus': {
    boxShadow: '0 0 0 2px var(--ring)',
  },
})

export const providerButton = style({
  'appearance': 'none',
  'background': 'none',
  'border': 'none',
  'display': 'inline-flex',
  'alignItems': 'center',
  'justifyContent': 'center',
  'width': '24px',
  'height': '24px',
  'minWidth': '24px',
  'padding': 0,
  'borderRadius': 'var(--radius-small)',
  'color': 'var(--muted-foreground)',
  'cursor': 'pointer',
  'flexShrink': 0,
  'lineHeight': 0,
  ':hover': {
    color: 'var(--foreground)',
    backgroundColor: 'var(--card)',
  },
})

export const providerIconsRow = style({
  display: 'flex',
  flexWrap: 'wrap',
  alignItems: 'center',
  gap: 'var(--space-1)',
  padding: 'var(--space-1) var(--space-3)',
})

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
