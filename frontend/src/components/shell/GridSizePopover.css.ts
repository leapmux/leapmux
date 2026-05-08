import { style } from '@vanilla-extract/css'

// `display: flex` is gated on `:popover-open` so the UA stylesheet's
// `[popover]:not(:popover-open) { display: none }` rule still hides the
// element when no tile has requested it. Without the gate the singleton
// popover stays mounted and (with `position: fixed`) covers the page,
// intercepting clicks on the workspace tab tree behind it.
export const popover = style({
  position: 'fixed',
  margin: 0,
  padding: 'var(--space-3)',
  border: '1px solid var(--border)',
  borderRadius: 'var(--radius-medium)',
  background: 'var(--card)',
  boxShadow: 'var(--shadow-medium)',
  flexDirection: 'column',
  gap: 'var(--space-2)',
  zIndex: 'var(--z-dropdown)',
  selectors: {
    '&:popover-open': {
      display: 'flex',
    },
  },
})

export const sizeLabel = style({
  textAlign: 'center',
  fontSize: 'var(--text-7)',
  color: 'var(--foreground)',
})

export const grid = style({
  display: 'grid',
  gap: '2px',
  outline: 'none',
  // Center the grid within the popover; the manual-entry row below is wider
  // because of the Create button, and a left-aligned grid leaves a lopsided
  // gap on the right.
  alignSelf: 'center',
})

export const cell = style({
  width: '20px',
  height: '20px',
  border: '1px solid var(--border)',
  background: 'var(--background)',
  cursor: 'pointer',
  borderRadius: 'var(--radius-small)',
  transition: 'background var(--transition-fast), border-color var(--transition-fast)',
  selectors: {
    '&[data-highlighted="true"]': {
      background: 'var(--accent)',
      borderColor: 'var(--ring)',
    },
  },
})

export const manualEntry = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-2)',
  borderTop: '1px solid var(--border)',
  paddingTop: 'var(--space-2)',
})

// Override Oat's default input padding/font so the inputs match the height
// of the adjacent `.small` Create button. Also override its label-coupling
// margin and full-bleed width so the inputs sit inline.
export const manualInput = style({
  width: '52px',
  marginBlockStart: 0,
  padding: 'var(--space-1) var(--space-2)',
  fontSize: 'var(--text-8)',
})

export const manualSeparator = style({
  fontSize: 'var(--text-8)',
  color: 'var(--muted-foreground)',
})
