import { globalStyle, style } from '@vanilla-extract/css'

export const tabBarRow = style({
  display: 'flex',
  alignItems: 'stretch',
  borderBottom: '1px solid var(--border)',
})

export const tabBarFiller = style({
  flex: 1,
  minWidth: 0,
})

export const tile = style({
  display: 'flex',
  flexDirection: 'column',
  flex: 1,
  minHeight: 0,
  overflow: 'hidden',
  position: 'relative',
})

export const tileFocused = style({})

globalStyle(`${tileFocused} ${tabBarRow}`, {
  borderBottomColor: 'var(--primary)',
})

export const tileContent = style({
  display: 'flex',
  flexDirection: 'column',
  flex: 1,
  overflow: 'hidden',
})

export const tileContentHidden = style({
  display: 'none !important',
})

export const splitActions = style({
  display: 'flex',
  alignItems: 'center',
  gap: '2px',
  marginLeft: 'var(--space-1)',
  paddingLeft: 'var(--space-1)',
  paddingRight: 'var(--space-1)',
  borderLeft: '1px solid var(--border)',
})

// --- Responsive: hide split actions at micro width (they move to TabBar overflow) ---
globalStyle(`${tile}[data-tile-size="micro"] ${splitActions}`, {
  display: 'none',
})

// --- Responsive: reduced tab bar height at short height ---
globalStyle(`${tile}[data-tile-height="short"] ${tabBarRow}`, {
  minHeight: '28px',
})

// --- Responsive: hide tab bar at tiny height ---
globalStyle(`${tile}[data-tile-height="tiny"] ${tabBarRow}`, {
  display: 'none',
})

// Floating overlay trigger for tiny height
export const tinyOverlayTrigger = style({
  position: 'absolute',
  top: '4px',
  right: '4px',
  zIndex: 10,
})
