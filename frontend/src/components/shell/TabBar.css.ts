import { globalStyle, style } from '@vanilla-extract/css'
import { headerHeight, spacing } from '~/styles/tokens'

export const tooltipTrigger = style({
  display: 'inline-flex',
})

export const tabBar = style({
  display: 'flex',
  alignItems: 'stretch',
  gap: '1px',
  padding: `0 ${spacing.sm}`,
  backgroundColor: 'var(--background)',
  flexShrink: 0,
  minHeight: headerHeight,
})

export const tabList = style({
  position: 'relative',
  display: 'flex',
  alignItems: 'stretch',
  gap: spacing.sm,
  flex: 1,
  overflowX: 'auto',
  scrollbarWidth: 'none',
  padding: 0,
  backgroundColor: 'inherit',
  borderRadius: 0,
})

export const tab = style({
  'all': 'unset',
  'display': 'flex',
  'alignItems': 'center',
  'gap': '6px',
  'padding': `${spacing.xs} ${spacing.xs} ${spacing.xs} ${spacing.sm}`,
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
  gap: spacing.xs,
})

export const tabNotification = style({
  width: '6px',
  height: '6px',
  borderRadius: '50%',
  backgroundColor: 'var(--primary)',
  flexShrink: 0,
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

export const tabText = style({
  overflow: 'hidden',
  textOverflow: 'ellipsis',
})

export const tabDragging = style({
  opacity: 0.4,
})

export const tabDragOverlay = style({
  display: 'flex',
  alignItems: 'center',
  gap: '6px',
  padding: `${spacing.xs} 6px`,
  fontSize: 'var(--text-7)',
  color: 'var(--foreground)',
  backgroundColor: 'var(--card)',
  border: '1px solid var(--border)',
  borderRadius: '4px 4px 0 0',
  boxShadow: 'var(--shadow-large)',
  whiteSpace: 'nowrap',
  maxWidth: '180px',
  overflow: 'hidden',
  textOverflow: 'ellipsis',
})

export const tabListDropTarget = style({
  backgroundColor: 'var(--secondary)',
  outline: '2px dashed var(--primary)',
  outlineOffset: '-2px',
  borderRadius: 'var(--radius-small)',
})

export const shellDefault = style({
  marginLeft: spacing.xs,
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

// --- Compact (240-359px): icon-only tabs, hide close unless hovered ---
globalStyle(`[data-tile-size="compact"] ${tabBar}`, {
  padding: `0 ${spacing.xs}`,
  gap: '0',
})

globalStyle(`[data-tile-size="compact"] ${tab}`, {
  gap: '4px',
  padding: `${spacing.xs}`,
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

// --- Minimal (140-239px): also icon-only tabs + collapse new-tab buttons ---
globalStyle(`[data-tile-size="minimal"] ${tabBar}`, {
  padding: `0 ${spacing.xs}`,
  gap: '0',
})

globalStyle(`[data-tile-size="minimal"] ${tab}`, {
  gap: '4px',
  padding: `${spacing.xs}`,
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
  gap: '4px',
  padding: `${spacing.xs} 2px`,
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
